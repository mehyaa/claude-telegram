package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/telegram/telegramtest"
)

// openScreen selects session 1 and opens its screen.
func (h *harness) openScreen() telegramtest.Sent {
	h.t.Helper()
	h.tg.Send(owner, "/list")
	list := h.tg.Next(h.t)
	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	menu := h.tg.Next(h.t)
	h.tg.Answered(h.t)
	if menu.Buttons()["📺 Screen"] != "sc:1.aaaa1111" {
		h.t.Fatalf("session menu buttons %v", menu.Buttons())
	}
	h.tg.Send(owner, "/screen")
	return h.until("with the screen", hasPrefix("📺 1 a · "))
}

func TestRemoteKeyboard(t *testing.T) {
	h := newHarness(t).paired().start()
	st := h.stub("permission", map[string]any{"title": "Bash command", "body": []string{"rm -rf build"}, "options": []string{"Yes", "No"}})
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)

	scr := h.openScreen()
	if scr.ParseMode != "HTML" || !strings.Contains(scr.Text, "<pre>") || !strings.Contains(scr.Text, "Do you want to proceed?") {
		t.Fatalf("screen %q (mode %q)", scr.Text, scr.ParseMode)
	}
	if strings.Contains(scr.Text, strings.Repeat("─", ruleKeep+1)) {
		t.Fatal("frame lines not shortened")
	}
	b := scr.Buttons()
	for _, label := range []string{"↑", "↓", "⏎", "Esc", "Tab", "1", "9", "⌨️ Type", "🔒 Secret", "↻", "✕"} {
		if b[label] == "" {
			t.Fatalf("no %q button in %v", label, b)
		}
	}

	// One key per tap, then the screen again (Q26).
	h.tg.Tap(owner, scr.MessageID, b["↓"])
	if toast := h.tg.Answered(t); toast != "Down" {
		t.Fatalf("toast %q", toast)
	}
	after := h.until("with the screen after the key", func(m telegramtest.Sent) bool {
		return m.MessageID == scr.MessageID && m.Method == "editMessageText"
	})
	if !strings.Contains(after.Text, "❯ 2. No") {
		t.Fatalf("screen after ↓: %q", after.Text)
	}

	// A tap from the old screen, which no longer shows what is there,
	// presses nothing (Q17).
	h.tg.Tap(owner, scr.MessageID, b["⏎"])
	h.tg.Answered(t)
	stale := h.until("refusing the stale tap", func(m telegramtest.Sent) bool { return m.MessageID == scr.MessageID })
	if !strings.Contains(stale.Text, "The screen changed before your key, so nothing was pressed.") {
		t.Fatalf("stale tap: %q", stale.Text)
	}
	if k := st.keys(); len(k) != 1 || k[0] != "Down" {
		t.Fatalf("keys %q", k)
	}

	// From the screen as it is now, ⏎ answers the dialog.
	h.tg.Tap(owner, scr.MessageID, after.Buttons()["⏎"])
	h.tg.Answered(t)
	if out := st.outcome(t, 1); out["permission"] != "deny" {
		t.Fatalf("stub got %v", out)
	}
	audit, _ := os.ReadFile(h.paths.AuditFile())
	if !strings.Contains(string(audit), `"detail":"keyboard: Down"`) || !strings.Contains(string(audit), `"action":"refused"`) {
		t.Fatalf("audit:\n%s", audit)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestTypeAndSecret(t *testing.T) {
	var logs lockedBuffer
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) {
		c.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	h.start()
	st := h.stub("prompt", nil)
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	scr := h.openScreen()

	// ⌨️ Type: the text goes in without Enter.
	h.tg.Tap(owner, scr.MessageID, scr.Buttons()["⌨️ Type"])
	h.tg.Answered(t)
	h.expectPrefix(owner, "⌨️ Type the text for 1 a.")
	h.tg.Send(owner, "/model") // a slash command is text here, not a bot command
	h.until("with the new screen", func(m telegramtest.Sent) bool { return strings.Contains(m.Text, "❯\u00a0/model") })
	for range 6 {
		st.pane.Keys("BSpace") // the terminal clears it again
	}
	time.Sleep(100 * time.Millisecond)
	h.tg.Send(owner, "/screen")
	scr = h.until("with the screen", hasPrefix("📺 1 a · "))
	h.tg.Tap(owner, scr.MessageID, scr.Buttons()["⌨️ Type"])
	h.tg.Answered(t)
	h.expectPrefix(owner, "⌨️ Type the text for 1 a.")
	h.tg.Send(owner, "git status")
	h.until("moving the old screen", func(m telegramtest.Sent) bool {
		return m.MessageID == scr.MessageID && m.Text == "📺 1 a: the screen is below."
	})
	scr = h.until("with the new screen", hasPrefix("📺 1 a · "))
	if !strings.Contains(scr.Text, "❯\u00a0git status") {
		t.Fatalf("screen after typing: %q", scr.Text)
	}
	h.tg.Quiet(t, 100*time.Millisecond)
	for _, l := range st.lines() {
		if l["done"] == true {
			t.Fatalf("typing submitted the prompt: %v", l)
		}
	}
	h.tg.Tap(owner, scr.MessageID, scr.Buttons()["⏎"])
	h.tg.Answered(t)
	if out := st.outcome(t, 1); out["prompt"] != "git status" {
		t.Fatalf("stub got %v", out)
	}

	// 🔒 Secret: typed, deleted from the chat, logged by length only.
	scr = h.until("with the screen after ⏎", func(m telegramtest.Sent) bool { return m.MessageID == scr.MessageID })
	h.tg.Tap(owner, scr.MessageID, scr.Buttons()["🔒 Secret"])
	h.tg.Answered(t)
	h.expectPrefix(owner, "🔒 Type the secret for 1 a.")
	const secret = "hunter2-correct-horse"
	h.tg.Send(owner, secret)
	del := h.tg.Next(t)
	if del.Method != "deleteMessage" {
		t.Fatalf("first thing after the secret: %+v", del)
	}
	scr = h.until("with the screen after the secret", hasPrefix("📺 1 a · "))
	// The screen shows the secret where it was typed; the bot masks it.
	if !strings.Contains(scr.Text, "🔒 Typed 21 secret characters.") || !strings.Contains(scr.Text, "❯\u00a0••••") || strings.Contains(scr.Text, "hunter2") {
		t.Fatalf("screen after the secret: %q", scr.Text)
	}
	h.tg.Tap(owner, scr.MessageID, scr.Buttons()["⏎"])
	h.tg.Answered(t)
	if out := st.outcome(t, 2); out["prompt"] != secret {
		t.Fatalf("stub got %v", out)
	}
	// Sent, it shows in the screen's history; still masked.
	scr = h.until("with the screen after ⏎", func(m telegramtest.Sent) bool { return m.MessageID == scr.MessageID })
	if !strings.Contains(scr.Text, "❯ ••••") || strings.Contains(scr.Text, "hunter2") {
		t.Fatalf("screen after sending the secret: %q", scr.Text)
	}

	audit, _ := os.ReadFile(h.paths.AuditFile())
	if !strings.Contains(string(audit), `"action":"secret","session":"1 a","detail":"typed 21 secret chars"`) {
		t.Fatalf("audit lacks the secret's length:\n%s", audit)
	}
	h.stop()
	state, _ := os.ReadFile(h.paths.StateFile())
	for name, text := range map[string]string{"audit log": string(audit), "daemon log": logs.String(), "state": string(state)} {
		if strings.Contains(text, "hunter2") {
			t.Errorf("the secret is in the %s", name)
		}
	}
}

// A secret sent after a restart is still handled as one (Q27).
func TestSecretSurvivesRestart(t *testing.T) {
	h := newHarness(t).paired().start()
	st := h.stub("prompt", nil)
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	scr := h.openScreen()
	h.tg.Tap(owner, scr.MessageID, scr.Buttons()["🔒 Secret"])
	h.tg.Answered(t)
	h.expectPrefix(owner, "🔒 Type the secret for 1 a.")
	h.stop()
	h.start()
	h.tg.Send(owner, "s3cret-after-restart")
	if del := h.tg.Next(t); del.Method != "deleteMessage" {
		t.Fatalf("first thing after the secret: %+v", del)
	}
	h.until("with the screen", hasPrefix("📺 1 a · "))
	audit, _ := os.ReadFile(h.paths.AuditFile())
	if strings.Contains(string(audit), "s3cret") || !strings.Contains(string(audit), "typed 20 secret chars") {
		t.Fatalf("audit:\n%s", audit)
	}
}

// A forgotten ⌨️ Type doesn't take a message sent much later.
func TestTypeRequestLapses(t *testing.T) {
	h := newHarness(t).paired().start()
	st := h.stub("prompt", nil)
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	scr := h.openScreen()
	h.tg.Tap(owner, scr.MessageID, scr.Buttons()["⌨️ Type"])
	h.tg.Answered(t)
	h.expectPrefix(owner, "⌨️ Type the text for 1 a.")
	h.advance(awaitFor + time.Minute)
	h.tg.SendAt(owner, "private", "/pending", h.clock())
	h.expect("(The request for text had lapsed; this message was taken as usual.)")
	h.expect("ctg · → 1 a\n\nNothing pending.")
	if text, _ := st.pane.Capture(); strings.Contains(text, "/pending") {
		t.Fatalf("the lapsed request typed the message:\n%s", text)
	}
}
