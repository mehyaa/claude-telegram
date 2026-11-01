package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/ipc"
)

func TestGlobalMute(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")

	h.tg.Send(owner, "/mute 2h")
	until := h.d.clock(h.clock().Add(2 * time.Hour))
	h.expect(fmt.Sprintf("🔕 Muted all sessions for 2h, until %s. Items still go to /pending.", until))

	// Muted: no ping, but the item is pending (Q20).
	h.add("aaaa1111", "k1", ipc.KindQuestion, "Which colour?")
	h.tg.Quiet(t, 300*time.Millisecond)
	h.tg.Send(owner, "/pending")
	h.expect("ctg · 🔕\n\n❓ 1.1 · a · Which colour? · now")

	// When the mute runs out, one line says what waited.
	h.advance(2*time.Hour + time.Minute)
	h.d.tick()
	end := h.expect("🔔 Mute ended · 1 item pending")
	if end.Buttons()["❓ Pending"] != "p:a" {
		t.Fatalf("buttons %v", end.Buttons())
	}
	h.add("aaaa1111", "k2", ipc.KindQuestion, "Which size?")
	h.expectPrefix(owner, "❓ 1.2")
}

func TestSessionMute(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.register("bbbb2222", 102, "/work/b", "%2")

	h.tg.Send(owner, "/mute 1 30m")
	h.expectPrefix(owner, "🔕 Muted 1 a for 30m, until ")
	h.add("aaaa1111", "k1", ipc.KindQuestion, "muted one")
	h.add("bbbb2222", "k2", ipc.KindQuestion, "loud one")
	h.expect("❓ 2.1 · b · loud one")
	h.tg.Quiet(t, 200*time.Millisecond)

	h.tg.Send(owner, "/unmute 1")
	h.expect("🔔 Pings are on for 1 a. 1 item pending: /pending")
	h.add("aaaa1111", "k3", ipc.KindQuestion, "loud again")
	h.expect("❓ 1.2 · a · loud again")
}

func TestUntilTurnedBackOn(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.tg.Send(owner, "/mute")
	h.expect("🔕 Muted all sessions until you turn it back on. Items still go to /pending.")
	h.advance(30 * 24 * time.Hour)
	h.d.tick()
	h.add("aaaa1111", "k1", ipc.KindQuestion, "still muted")
	h.tg.Quiet(t, 300*time.Millisecond)
	// Dated by the daemon's clock, or it would count as stale.
	h.tg.SendAt(owner, "private", "/unmute", h.clock())
	h.expect("🔔 Pings are on for all sessions. 1 item pending: /pending")
}

func TestMuteButtons(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)

	h.tg.Tap(owner, list.MessageID, list.Buttons()["⋯"])
	more := h.tg.Next(t)
	h.tg.Answered(t)
	if more.Buttons()["🔕 Mute all"] != "m:a" || more.Buttons()["❔ Help"] == "" {
		t.Fatalf("⋯ menu %v", more.Buttons())
	}
	h.tg.Tap(owner, list.MessageID, more.Buttons()["🔕 Mute all"])
	menu := h.tg.Next(t)
	h.tg.Answered(t)
	if menu.Text != "ctg\n\nMute pings for all sessions:" || menu.Buttons()["8h"] != "mu:a:8h" {
		t.Fatalf("mute menu %q %v", menu.Text, menu.Buttons())
	}
	h.tg.Tap(owner, list.MessageID, "mu:a:8h")
	after := h.tg.Next(t)
	if !strings.HasPrefix(after.Text, "ctg · 🔕\n\n") || after.Buttons()["⋯"] != "mm:a" {
		t.Fatalf("list after muting: %q %v", after.Text, after.Buttons())
	}
	if toast := h.tg.Answered(t); !strings.HasPrefix(toast, "🔕 Muted all sessions for 8h") {
		t.Fatalf("toast %q", toast)
	}

	// Per session, from the session menu.
	h.tg.Tap(owner, list.MessageID, "mu:1.aaaa1111:inf")
	h.tg.Next(t)
	h.tg.Answered(t)
	h.tg.Tap(owner, list.MessageID, "um:a")
	after = h.tg.Next(t)
	h.tg.Answered(t)
	if strings.Contains(after.Text, "🔕") {
		t.Fatalf("unmute all left a mute on:\n%s", after.Text)
	}

	audit, _ := os.ReadFile(h.paths.AuditFile())
	var actions []string
	for line := range strings.Lines(string(audit)) {
		var e struct{ Action, Session, Detail string }
		json.Unmarshal([]byte(line), &e)
		actions = append(actions, e.Action+" "+e.Session)
	}
	if got := strings.Join(actions, ","); got != "mute ,mute 1 a,unmute " {
		t.Fatalf("audit actions %q", got)
	}
}

func TestBadMuteArguments(t *testing.T) {
	h := newHarness(t).paired().start()
	h.tg.Send(owner, "/mute soon")
	h.expect(`can't read "soon" as a duration. Use e.g. 30m, 2h or 1d.`)
	h.tg.Send(owner, "/mute 9")
	h.expect("No session 9. /list shows the sessions.")
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"30m": 30 * time.Minute, "2h": 2 * time.Hour, "1d": 24 * time.Hour, "1h30m": 90 * time.Minute} {
		if got, err := parseDuration(in); err != nil || got != want {
			t.Errorf("parseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0m", "-1h", "xd", "2"} {
		if _, err := parseDuration(in); err == nil {
			t.Errorf("parseDuration(%q) should fail", in)
		}
	}
}
