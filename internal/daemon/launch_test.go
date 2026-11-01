package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/launch"
)

type recTmux struct {
	mu     sync.Mutex
	calls  [][]string
	has    map[string]bool
	panes  map[string]string
	socket string
}

func (r *recTmux) run(args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{}, args...))
	if len(args) == 0 {
		return nil, fmt.Errorf("empty")
	}
	switch args[0] {
	case "has-session":
		if r.has[strings.TrimPrefix(flagArg(args, "-t"), "=")] {
			return nil, nil
		}
		return nil, fmt.Errorf("no session")
	case "display-message":
		return []byte(r.socket + "\n"), nil
	case "list-panes":
		return []byte(r.panes[strings.TrimPrefix(flagArg(args, "-t"), "=")]), nil
	case "new-session":
		if r.has == nil {
			r.has = map[string]bool{}
		}
		r.has[flagArg(args, "-s")] = true
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected %v", args)
	}
}

func (r *recTmux) command() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "new-session" {
			b.WriteString(strings.Join(c, " "))
		}
	}
	return b.String()
}

func flagArg(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func writePast(t *testing.T, home, cwd, id, title string, mkdir bool) {
	t.Helper()
	if mkdir {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(home, ".claude", "projects", launch.Slug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("{\"cwd\":%q,\"customTitle\":%q,\"gitBranch\":\"main\"}\n", cwd, title)
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewAndResume(t *testing.T) {
	h := newHarness(t).paired()
	home := filepath.Dir(h.paths.Socket)
	proj := filepath.Join(home, "Projects", "claude-telegram")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "Projects", "aaa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "Projects", "aaab"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recTmux{}
	h.cfgEdit = func(c *Config) { c.Tmux = rec.run }
	h.start()

	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)
	if list.Buttons()["🆕 New"] != "nw" || list.Buttons()["⏪ Resume"] != "rs" {
		t.Fatalf("list buttons %v", list.Buttons())
	}

	h.tg.Send(owner, "/new MyApp proj/claude-tel -- fix the bug")
	h.expectPrefix(owner, "🆕 Starting claude-my-app in ~/Projects/claude-telegram.")
	cmd := rec.command()
	if !strings.Contains(cmd, "-d") || !strings.Contains(cmd, "-s claude-my-app") || !strings.Contains(cmd, "-c "+proj) {
		t.Fatalf("new-session: %s", cmd)
	}
	if !strings.Contains(cmd, "-lic exec claude --name 'my-app' 'fix the bug'") {
		t.Fatalf("script: %s", cmd)
	}
	if strings.Contains(cmd, "--resume") {
		t.Fatalf("a new session must not resume: %s", cmd)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	if !strings.Contains(cmd, shell+" -lic") {
		t.Fatalf("login shell: %s", cmd)
	}

	h.tg.Send(owner, "/new update "+proj)
	h.expectPrefix(owner, "🆕 Starting claude-update in ~/Projects/claude-telegram.")

	h.tg.Send(owner, "/new demo proj/aa")
	offer := h.tg.Next(t)
	if offer.Buttons()["~/Projects/aaa"] == "" || offer.Buttons()["~/Projects/aaab"] == "" {
		t.Fatalf("folders %v", offer.Buttons())
	}
	h.tg.Tap(owner, offer.MessageID, offer.Buttons()["~/Projects/aaa"])
	h.expectPrefix(owner, "🆕 Starting claude-demo in ~/Projects/aaa.")

	h.tg.Send(owner, "/new nope foo/../../../etc")
	h.expect("New sessions stay under your home folder.")
	if ask := h.tg.Next(t); !ask.ForceReply {
		t.Fatalf("expected another folder prompt, got %q", ask.Text)
	}

	h.tg.Send(owner, "/new fresh brand/new")
	create := h.tg.Next(t)
	if create.Buttons()["📁 Create ~/brand/new"] != "nc:a" {
		t.Fatalf("create %q %v", create.Text, create.Buttons())
	}
	if _, err := os.Stat(filepath.Join(home, "brand")); !os.IsNotExist(err) {
		t.Fatal("created before the tap")
	}
	h.tg.Tap(owner, create.MessageID, "nc:a")
	h.expectPrefix(owner, "🆕 Starting claude-fresh in ~/brand/new.")
	if _, err := os.Stat(filepath.Join(home, "brand", "new")); err != nil {
		t.Fatal(err)
	}
}

func TestResume(t *testing.T) {
	h := newHarness(t).paired()
	home := filepath.Dir(h.paths.Socket)
	cwd := filepath.Join(home, "work", "api")
	id := "11111111-1111-4111-8111-111111111111"
	other := "22222222-2222-4222-8222-222222222222"
	writePast(t, home, cwd, id, "api-work", true)
	writePast(t, home, filepath.Join(home, "work", "web"), other, "web", true)
	rec := &recTmux{socket: "/tmp/ctg-fake.sock"}
	h.cfgEdit = func(c *Config) { c.Tmux = rec.run }
	h.start()

	h.tg.Send(owner, "/resume api")
	list := h.tg.Next(t)
	if !strings.Contains(list.Text, "api-work") || strings.Contains(list.Text, "web") {
		t.Fatalf("filter:\n%s", list.Text)
	}
	if list.Buttons()["1 api-work"] != "rr:a:"+id {
		t.Fatalf("buttons %v", list.Buttons())
	}
	h.tg.Tap(owner, list.MessageID, "rr:a:"+id)
	h.expectPrefix(owner, "⏪ Resuming claude-api-work in ~/work/api.")
	cmd := rec.command()
	if !strings.Contains(cmd, "exec claude --resume '"+id+"'") || strings.Contains(cmd, "--name") {
		t.Fatalf("resume command: %s", cmd)
	}
	h.register(id, 101, cwd, "")
	h.tg.Quiet(t, 200*time.Millisecond)

	// Already running: select, don't start a second copy.
	h.tg.Send(owner, "/resume")
	list = h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "rr:a:"+id)
	h.expectPrefix(owner, "That session is already running.")
	h.tg.Next(t) // the session menu

	gone := filepath.Join(home, "work", "missing")
	goneID := "33333333-3333-4333-8333-333333333333"
	writePast(t, home, gone, goneID, "gone", false)
	os.RemoveAll(gone)
	h.tg.Send(owner, "/resume gone")
	list = h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "rr:a:"+goneID)
	h.expect("That session's folder is gone, so it can't be resumed.")

	// The name's tmux session is a different conversation.
	rec.mu.Lock()
	rec.has = map[string]bool{"claude-web": true}
	rec.panes = map[string]string{"claude-web": "%9\n"}
	rec.mu.Unlock()
	h.tmuxSocket = rec.socket
	h.register("44444444-4444-4444-8444-444444444444", 102, cwd, "%9")
	h.tg.Send(owner, "/resume web")
	list = h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "rr:a:"+other)
	ask := h.tg.Next(t)
	if !ask.ForceReply || !strings.Contains(ask.Text, "different session") {
		t.Fatalf("clash: %q", ask.Text)
	}
	h.tg.Send(owner, "other")
	h.expectPrefix(owner, "⏪ Resuming claude-other in ~/work/web.")
}

func TestResumeRename(t *testing.T) {
	h := newHarness(t).paired()
	home := filepath.Dir(h.paths.Socket)
	cwd := filepath.Join(home, "work", "api")
	id := "11111111-1111-4111-8111-111111111111"
	writePast(t, home, cwd, id, "2fa", true)
	rec := &recTmux{}
	h.cfgEdit = func(c *Config) { c.Tmux = rec.run }
	h.start()
	st := h.stub("prompt", nil)

	h.tg.Send(owner, "/resume")
	list := h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "rr:a:"+id)
	if ask := h.tg.Next(t); !ask.ForceReply {
		t.Fatalf("wanted a name, got %q", ask.Text)
	}
	h.tg.Send(owner, "api")
	h.expectPrefix(owner, "⏪ Resuming claude-api in ~/work/api.")
	if cmd := rec.command(); strings.Contains(cmd, "--name") || !strings.Contains(cmd, "--resume") {
		t.Fatalf("command %s", cmd)
	}
	h.register(id, 101, cwd, st.pane.ID)
	h.expectPrefix(owner, "⌨️ Typed into")
	if out := st.outcome(t, 1); out["prompt"] != "/rename api" {
		t.Fatalf("stub %v", out)
	}
}

func TestResumePages(t *testing.T) {
	h := newHarness(t).paired()
	home := filepath.Dir(h.paths.Socket)
	cwd := filepath.Join(home, "work", "api")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "projects", launch.Slug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 11; i++ {
		id := fmt.Sprintf("12121212-1212-4121-8121-%012d", i+1)
		body := fmt.Sprintf("{\"cwd\":%q,\"customTitle\":%q}\n", cwd, fmt.Sprintf("s%02d", i+1))
		path := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		when := now.Add(-time.Duration(i) * time.Minute)
		os.Chtimes(path, when, when)
	}
	h.cfgEdit = func(c *Config) { c.Tmux = (&recTmux{}).run }
	h.start()
	h.tg.Send(owner, "/resume")
	page := h.tg.Next(t)
	if page.Buttons()["▶"] != "rp:a:1" {
		t.Fatalf("page %v\n%s", page.Buttons(), page.Text)
	}
	if strings.Contains(page.Text, "s11") {
		t.Fatalf("first page includes s11:\n%s", page.Text)
	}
	h.tg.Tap(owner, page.MessageID, "rp:a:1")
	next := h.tg.Next(t)
	if !strings.Contains(next.Text, "s11") {
		t.Fatalf("second page:\n%s", next.Text)
	}
}
