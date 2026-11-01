package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/config"
	"github.com/mehyaa/claude-telegram/internal/ipc"
)

func writeSpool(t *testing.T, p config.Paths, reqs ...ipc.Request) {
	t.Helper()
	if err := os.MkdirAll(p.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range reqs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteString("not json\n")
	if err := os.WriteFile(p.SpoolFile(), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGapReport(t *testing.T) {
	h := newHarness(t).paired()
	down := h.clock().Add(-20 * time.Minute)
	if err := os.MkdirAll(h.paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.paths.HeartbeatFile(), []byte(down.UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.spawn(101)
	h.spawn(102)
	writeSpool(t, h.paths,
		ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{ID: "aaaa1111", PID: 101, Cwd: "/work/a", Pane: "%1"}},
		ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{Key: "k1", Kind: ipc.KindPermission, Summary: "Bash: ls"}},
		ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{Key: "k1", Kind: ipc.KindPermission, Summary: "Bash: ls"}},
		ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{Key: "k2", Kind: ipc.KindQuestion, Summary: "answered while down"}},
		ipc.Request{Op: ipc.OpItemDone, SessionID: "aaaa1111", Key: "k2", Note: "answered in terminal"},
		// A session that ended while the daemon was down: its item didn't wait.
		ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{ID: "bbbb2222", PID: 999, Cwd: "/work/b", Pane: "%2"}},
		ipc.Request{Op: ipc.OpItemAdd, SessionID: "bbbb2222", Item: &ipc.Item{Key: "k3", Kind: ipc.KindQuestion, Summary: "gone"}},
	)
	h.start()

	msg := h.expect(fmt.Sprintf("bot was down %s–%s; 1 item waited", h.d.clock(down), h.d.clock(h.clock())))
	if msg.Buttons()["❓ Pending"] != "p:a" {
		t.Fatalf("buttons %v", msg.Buttons())
	}
	// Spooled items get no pings of their own.
	h.tg.Quiet(t, 300*time.Millisecond)
	h.tg.Send(owner, "/pending")
	h.expect("ctg\n\n🔐 1.1 · a · Bash: ls · now")
	if _, err := os.Stat(h.paths.SpoolFile()); !os.IsNotExist(err) {
		t.Fatalf("spool still there after replay: %v", err)
	}
}

func TestQuickRestartReportsNothing(t *testing.T) {
	h := newHarness(t).paired().start()
	h.stop()
	h.advance(30 * time.Second)
	h.start()
	h.tg.Quiet(t, 300*time.Millisecond)
}
