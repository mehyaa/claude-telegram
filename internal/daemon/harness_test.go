package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/config"
	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram/telegramtest"
)

const (
	owner    = int64(42)
	stranger = int64(777)
)

// harness runs a daemon against the fake Bot API, with a fake clock, process
// table and git.
type harness struct {
	t     *testing.T
	tg    *telegramtest.Server
	paths config.Paths
	d     *Daemon
	out   syncBuffer

	mu    sync.Mutex
	now   time.Time
	procs map[int]string // pid → start time

	// tmuxSocket is what registered sessions name as their tmux server;
	// a private server when a test runs the stub, else a path nothing
	// listens on.
	tmuxSocket string
	cfgEdit    func(*Config) // adjusts the config before start

	cancel context.CancelFunc
	done   chan error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// A short directory: Unix socket paths are limited to 108 bytes.
	dir, err := os.MkdirTemp("", "ctg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return &harness{
		t:  t,
		tg: telegramtest.New(t),
		paths: config.Paths{
			ConfigDir: filepath.Join(dir, "config"),
			StateDir:  filepath.Join(dir, "state"),
			Socket:    filepath.Join(dir, "d.sock"),
		},
		now:        time.Now(),
		procs:      map[int]string{},
		tmuxSocket: filepath.Join(dir, "no-tmux.sock"),
	}
}

// paired writes the chat file, as if the owner had paired before.
func (h *harness) paired() *harness {
	h.t.Helper()
	if err := config.WriteChat(h.paths, owner); err != nil {
		h.t.Fatal(err)
	}
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func (h *harness) spawn(pid int) {
	h.mu.Lock()
	h.procs[pid] = "start-" + time.Now().Format(time.RFC3339Nano)
	h.mu.Unlock()
}

func (h *harness) kill(pid int) {
	h.mu.Lock()
	delete(h.procs, pid)
	h.mu.Unlock()
}

func (h *harness) config() Config {
	c := Config{
		Paths: h.paths,
		Token: telegramtest.Token,
		API:   h.tg.URL,
		Out:   &h.out,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:   h.clock,
		ProcStart: func(pid int) (string, bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			s, ok := h.procs[pid]
			return s, ok
		},
		Branch: func(dir string) string {
			if strings.HasSuffix(dir, "/claude-telegram") {
				return "master"
			}
			return ""
		},
		PollTimeout:  1,
		ClientActive: func(string, string, time.Time) bool { return false },
		Home:         filepath.Dir(h.paths.Socket),
		Tmux: func(args ...string) ([]byte, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("no tmux in this test")
			}
			return nil, fmt.Errorf("tmux %s: no tmux in this test", args[0])
		},
	}
	if h.cfgEdit != nil {
		h.cfgEdit(&c)
	}
	return c
}

// start runs the daemon until the test ends, and waits for its socket.
func (h *harness) start() *harness {
	h.t.Helper()
	h.d = New(h.config())
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.d.Run(ctx) }()
	h.t.Cleanup(h.stop)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(h.paths.Socket); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, err := ipc.Call(ctx, h.paths.Socket, ipc.Request{Op: "ping?"})
			cancel()
			if err != nil && strings.Contains(err.Error(), "unknown op") {
				return h
			}
		}
		select {
		case err := <-h.done:
			h.t.Fatalf("daemon exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			h.t.Fatal("daemon socket not up within 3s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	h.cancel = nil
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("daemon: %v", err)
		}
	case <-time.After(5 * time.Second):
		h.t.Error("daemon didn't stop within 5s")
	}
}

func (h *harness) call(req ipc.Request) ipc.Response {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := ipc.Call(ctx, h.paths.Socket, req)
	if err != nil {
		h.t.Fatalf("%s: %v", req.Op, err)
	}
	return resp
}

// register starts a fake process and registers a session for it.
func (h *harness) register(id string, pid int, cwd, pane string) int {
	h.t.Helper()
	h.spawn(pid)
	sock := ""
	if pane != "" {
		sock = h.tmuxSocket
	}
	return h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{
		ID: id, PID: pid, Cwd: cwd, Pane: pane, TmuxSocket: sock, Version: "2.1.308",
	}}).Number
}

func (h *harness) add(session, key, kind, summary string) string {
	h.t.Helper()
	return h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: session, Item: &ipc.Item{Key: key, Kind: kind, Summary: summary}}).Item
}

var codeRE = regexp.MustCompile(`\b\d{6}\b`)

// code returns the pairing code the daemon printed.
func (h *harness) code() string {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c := codeRE.FindString(h.out.String()); c != "" {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("no pairing code printed; output: %q", h.out.String())
	return ""
}

// expect waits for the next message and checks that its text is want.
func (h *harness) expect(want string) telegramtest.Sent {
	h.t.Helper()
	got := h.tg.Next(h.t)
	if got.Text != want {
		h.t.Fatalf("bot sent:\n%s\n\nwant:\n%s", got.Text, want)
	}
	return got
}

// expectPrefix waits for the next message and checks how it starts.
func (h *harness) expectPrefix(chat int64, prefix string) telegramtest.Sent {
	h.t.Helper()
	got := h.tg.Next(h.t)
	if got.ChatID != chat || !strings.HasPrefix(got.Text, prefix) {
		h.t.Fatalf("bot sent to %d:\n%s\n\nwant to %d, starting %q", got.ChatID, got.Text, chat, prefix)
	}
	return got
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
