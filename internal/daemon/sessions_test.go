package daemon

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/ipc"
)

func TestSessionNumbers(t *testing.T) {
	h := newHarness(t).paired().start()
	for i, id := range []string{"aaaa1111", "bbbb2222", "cccc3333"} {
		if n := h.register(id, 101+i, "/work/p"+id, "%1"); n != i+1 {
			t.Fatalf("session %s got number %d, want %d", id, n, i+1)
		}
	}

	// A number is reused only once its session has ended (Q11).
	h.call(ipc.Request{Op: ipc.OpEnd, SessionID: "bbbb2222"})
	if n := h.register("dddd4444", 104, "/work/d", "%4"); n != 2 {
		t.Fatalf("new session got %d, want the freed 2", n)
	}
	// Registering again keeps the number.
	if n := h.register("aaaa1111", 101, "/work/a", "%1"); n != 1 {
		t.Fatalf("re-registered session got %d, want 1", n)
	}
	// /clear: same process, new session ID, same number.
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{ID: "eeee5555", PID: 101, Cwd: "/work/a", Pane: "%1"}})
	h.d.mu.Lock()
	cleared, old := h.d.byID("eeee5555"), h.d.byID("aaaa1111")
	h.d.mu.Unlock()
	if cleared == nil || cleared.Num != 1 || old != nil {
		t.Fatalf("after /clear: new id %+v, old id %+v; want the new id on number 1", cleared, old)
	}

	// A session whose process is gone is pruned, even with no SessionEnd.
	h.kill(103)
	h.d.tick()
	h.d.mu.Lock()
	gone := h.d.byNum(3)
	h.d.mu.Unlock()
	if gone != nil {
		t.Fatalf("session 3 survived its process: %+v", gone)
	}
}

func TestPIDReuseIsNotTheSameSession(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.spawn(101) // same PID, new start time: a different process
	h.d.tick()
	h.d.mu.Lock()
	n := len(h.d.st.Sessions)
	h.d.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d sessions after the PID was reused, want 0", n)
	}
}

func TestListView(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/myapp", "%1")
	h.call(ipc.Request{Op: ipc.OpUpdate, SessionID: "aaaa1111", Name: ptr("myapp-build")})
	h.register("bbbb2222", 102, "/work/claude-telegram", "%2")
	h.call(ipc.Request{Op: ipc.OpUpdate, SessionID: "bbbb2222", State: ipc.StateBusy})
	h.register("cccc3333", 103, "/work/notes", "") // not in tmux
	h.register("dddd4444", 104, "/work/a-folder-with-a-really-long-name", "%4")

	h.add("aaaa1111", "toolu_1", ipc.KindPermission, "Bash: make test")
	h.expectPrefix(owner, "🔐 1.1")
	h.tg.Send(owner, "/mute 2")
	h.expectPrefix(owner, "🔕 Muted 2 claude-telegram/master until you turn it back on.")

	h.tg.Send(owner, "/list")
	list := h.expect(`ctg · 🔕

1 · myapp-build · ❓ waiting · 1❓
2 · claude-telegram/master · ⚙️ working · 🔕
3 · notes · 💤 idle · 👁 read-only
4 · a-folder-with-a-really-… · 💤 idle`)
	btn := list.Buttons()
	if btn["1 · myapp-build ❓"] != "s:1.aaaa1111" || btn["❓ Pending (1)"] != "p:a" || btn["⋯"] != "mm:a" {
		t.Fatalf("list buttons: %v", btn)
	}

	// Tapping a session selects it: the header shows it (Q14).
	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	menu := h.tg.Next(t)
	if menu.Method != "editMessageText" || menu.MessageID != list.MessageID {
		t.Fatalf("tap should edit the list in place, got %+v", menu)
	}
	if want := "ctg · → 1 myapp-build · 🔕\n\n1 · myapp-build · ❓ waiting · 1❓\n/work/myapp\nClaude Code 2.1.308 · tmux pane %1"; menu.Text != want {
		t.Fatalf("session menu:\n%s\n\nwant:\n%s", menu.Text, want)
	}
	h.tg.Answered(t)

	// The session's branch line, and read-only sessions, read right.
	h.tg.Tap(owner, list.MessageID, "s:3.cccc3333")
	if got := h.tg.Next(t).Text; !strings.HasSuffix(got, "Claude Code 2.1.308 · not in tmux: read-only") {
		t.Fatalf("read-only session menu:\n%s", got)
	}
	h.tg.Answered(t)

	// A button from before the session ended doesn't reach a newcomer that
	// took its number.
	h.call(ipc.Request{Op: ipc.OpEnd, SessionID: "cccc3333"})
	h.register("ffff6666", 106, "/work/new", "%6")
	h.tg.Tap(owner, list.MessageID, "s:3.cccc3333")
	if got := h.tg.Next(t).Text; !strings.Contains(got, "3 · new · 💤 idle") {
		t.Fatalf("stale tap should show the list, got:\n%s", got)
	}
	if toast := h.tg.Answered(t); toast != "That session has ended." {
		t.Fatalf("toast %q", toast)
	}
}

func TestPingAndClose(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/claude-telegram", "%1")

	if n := h.add("aaaa1111", "toolu_1", ipc.KindPermission, "Bash: make\n  test"); n != "1.1" {
		t.Fatalf("item number %q, want 1.1", n)
	}
	ping := h.expect("🔐 1.1 · claude-telegram/master · Bash: make test")
	if ping.Buttons()["Open 1"] != "o:1.aaaa1111" {
		t.Fatalf("ping buttons %v", ping.Buttons())
	}
	// The same key again is the same item, with no second ping.
	if n := h.add("aaaa1111", "toolu_1", ipc.KindPermission, "Bash: make test"); n != "1.1" {
		t.Fatalf("repeated item got %q", n)
	}
	h.tg.Quiet(t, 200*time.Millisecond)

	h.call(ipc.Request{Op: ipc.OpItemDone, SessionID: "aaaa1111", Key: "toolu_1", Note: "answered: Yes"})
	done := h.tg.Next(t)
	if done.Method != "editMessageText" || done.MessageID != ping.MessageID ||
		done.Text != "✅ 1.1 · claude-telegram/master · answered in terminal: Yes" || done.Keyboard != nil {
		t.Fatalf("closing should edit the ping and drop its buttons, got %+v", done)
	}

	// Open from a ping sends the menu as a new message and keeps the ping.
	h.add("aaaa1111", "toolu_2", ipc.KindQuestion, "Pick a colour")
	ping2 := h.expect("❓ 1.2 · claude-telegram/master · Pick a colour")
	h.tg.Tap(owner, ping2.MessageID, "o:1.aaaa1111")
	menu := h.tg.Next(t)
	if menu.Method != "sendMessage" || !strings.HasPrefix(menu.Text, "ctg · → 1 claude-telegram/master") {
		t.Fatalf("open from ping: %+v", menu)
	}
	h.tg.Answered(t)

	// When the session's process goes, its ping says so.
	h.kill(101)
	h.d.tick()
	if got := h.tg.Next(t); got.MessageID != ping2.MessageID || got.Text != "⏹ 1.2 · claude-telegram/master · session ended" {
		t.Fatalf("ping after the session ended: %+v", got)
	}
}

func TestItemForUnknownSession(t *testing.T) {
	h := newHarness(t).paired().start()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := ipc.Call(ctx, h.paths.Socket, ipc.Request{Op: ipc.OpItemAdd, SessionID: "nope", Item: &ipc.Item{Key: "k"}})
	if err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("got %v, want unknown session", err)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.register("bbbb2222", 102, "/work/b", "%2")
	h.call(ipc.Request{Op: ipc.OpEnd, SessionID: "aaaa1111"})
	h.add("bbbb2222", "k1", ipc.KindPlan, "Plan: refactor")
	h.expectPrefix(owner, "📋 2.1")
	h.stop()

	h.start()
	h.tg.Send(owner, "/list")
	h.expect("ctg\n\n2 · b · ❓ waiting · 1❓")
	// The item counter carries on too.
	if n := h.add("bbbb2222", "k2", ipc.KindPlan, "Plan: again"); n != "2.2" {
		t.Fatalf("item after restart is %q, want 2.2", n)
	}
}

func TestSecondDaemonRefused(t *testing.T) {
	h := newHarness(t).paired().start()
	err := New(h.config()).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon: %v", err)
	}
}

func TestProcStart(t *testing.T) {
	if s, ok := procStart(os.Getpid()); !ok || s == "" {
		t.Fatalf("procStart(self) = %q, %v", s, ok)
	}
	if _, ok := procStart(1 << 30); ok {
		t.Fatal("procStart of a missing PID should fail")
	}
}

func ptr[T any](v T) *T { return &v }
