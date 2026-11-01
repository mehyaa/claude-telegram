package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram/fake"
)

const twoQuestions = "Two things.\n\n" +
	"❓ **Q16** - **Storage**: SQLite or Postgres?\n➡️ SQLite: one file.\n\n" +
	"❓ **Q17** - **Name**: what should the binary be called?\n➡️ ctg.\n"

func (h *harness) turnEnds(reply string) {
	h.t.Helper()
	h.call(ipc.Request{Op: ipc.OpUpdate, SessionID: "aaaa1111", State: ipc.StateIdle, Reply: reply})
}

func TestQuestionBlocks(t *testing.T) {
	h := newHarness(t).paired().start()
	// The sent prompt stays on screen a while, so UserPromptSubmit arrives
	// before the driver has seen it go, as in the live probe.
	st := h.stub("slowprompt", nil)
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)

	h.turnEnds(twoQuestions)
	p16 := h.expect("❓ 1.1 · a · Q16 Storage: SQLite or Postgres?")
	p17 := h.expect("❓ 1.2 · a · Q17 Name: what should the binary be called?")
	if p16.Buttons()["✅ Accept"] != "ba:1.aaaa1111:1.1" || p16.Buttons()["✍️ Reply…"] != "br:1.aaaa1111:1.1" {
		t.Fatalf("ping buttons %v", p16.Buttons())
	}

	// Accept on the ping.
	h.tg.Tap(owner, p16.MessageID, p16.Buttons()["✅ Accept"])
	if toast := h.tg.Answered(t); toast != "Accepted Q16" {
		t.Fatalf("toast %q", toast)
	}
	status := h.expect("📝 1 a · answers to send\nQ16: As you recommended.\nQ17: (none yet)")
	if status.Buttons()["📤 Send 1 answer"] != "bs:1.aaaa1111" {
		t.Fatalf("status buttons %v", status.Buttons())
	}

	// Reply with text; the status comes again below it.
	h.tg.Tap(owner, p17.MessageID, p17.Buttons()["✍️ Reply…"])
	h.tg.Answered(t)
	h.expect("✍️ Type your answer to Q17. /cancel to stop.")
	h.tg.Send(owner, "call it ctg,\nshort and plain")
	old := h.tg.Next(t)
	if old.MessageID != status.MessageID || old.Keyboard != nil {
		t.Fatalf("the old status should lose its button: %+v", old)
	}
	status = h.expect("📝 1 a · answers to send\nQ16: As you recommended.\nQ17: call it ctg, short and plain")

	// Send types them as one message.
	h.tg.Tap(owner, status.MessageID, status.Buttons()["📤 Send 2 answers"])
	h.tg.Answered(t)
	if out := st.outcome(t, 1); out["prompt"] != "Q16: As you recommended.\nQ17: call it ctg, short and plain" {
		t.Fatalf("typed %v", out)
	}
	// UserPromptSubmit closes both, as live, while the driver may still be
	// checking the screen.
	h.call(ipc.Request{Op: ipc.OpUpdate, SessionID: "aaaa1111", State: ipc.StateBusy, Prompt: "Q16: As you recommended.\nQ17: call it ctg, short and plain"})
	var sentNote bool
	for !sentNote {
		m := h.tg.Next(t)
		switch {
		// An edit of the status, or a new message if Send came before the
		// status message's ID was in.
		case strings.HasPrefix(m.Text, "📤 2 answers sent to 1 a:"):
			if m.Keyboard != nil {
				t.Fatalf("status keeps buttons: %+v", m)
			}
			sentNote = true
		case m.MessageID == p16.MessageID && m.Text != "✅ 1.1 · a · answered from Telegram: As you recommended.",
			m.MessageID == p17.MessageID && m.Text != "✅ 1.2 · a · answered from Telegram: call it ctg, short and plain":
			t.Fatalf("ping closed as %q", m.Text)
		}
	}
}

func TestQuestionsAskedAgainOrDropped(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.turnEnds(twoQuestions)
	p16 := h.expectPrefix(owner, "❓ 1.1")
	h.expectPrefix(owner, "❓ 1.2")

	// The next reply asks Q17 again, reworded, and not Q16.
	h.turnEnds("Still open:\n❓ **Q17** - **Name**: ctg or cctg?\n➡️ ctg.")
	h.until("dropping Q16", func(m fake.Sent) bool {
		return m.MessageID == p16.MessageID && m.Text == "✅ 1.1 · a · no longer asked"
	})
	h.tg.Quiet(t, 200*time.Millisecond) // no second ping for Q17
	h.tg.Send(owner, "/pending")
	h.expect("ctg\n\n❓ 1.2 · a · Q17 Name: ctg or cctg? · now")

	// Answered in the terminal, by its label.
	h.call(ipc.Request{Op: ipc.OpUpdate, SessionID: "aaaa1111", State: ipc.StateBusy, Prompt: "Q17: cctg after all"})
	h.until("closing Q17", hasPrefix("✅ 1.2 · a · answered in terminal: cctg after all"))

	// A reply without blocks leaves nothing pending.
	h.turnEnds("Q? blocks:\n❓ Should I squash?\n➡️ No.")
	h.expectPrefix(owner, "❓ 1.3 · a · Should I squash?")
	h.turnEnds("Done, nothing to ask.")
	h.until("dropping 1.3", hasPrefix("✅ 1.3 · a · no longer asked"))
}

func TestDismissQuestionAndPostQuestionLifecycle(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")

	h.turnEnds(twoQuestions)
	p1 := h.expectPrefix(owner, "❓ 1.1 · a · Q16")
	p2 := h.expectPrefix(owner, "❓ 1.2 · a · Q17")
	if p1.Buttons()["✖ Dismiss"] != "dm:1.aaaa1111:1.1" || p2.Buttons()["✖ Dismiss"] != "dm:1.aaaa1111:1.2" {
		t.Fatalf("missing dismiss button on ping: %v", p1.Buttons())
	}

	// Dismiss 1.1 from the ping button.
	h.tg.Tap(owner, p1.MessageID, "dm:1.aaaa1111:1.1")
	if toast := h.tg.Answered(t); toast != "Dismissed 1.1" {
		t.Fatalf("toast %q", toast)
	}
	edited := h.until("dismissed 1.1", func(m fake.Sent) bool {
		return m.MessageID == p1.MessageID && m.Text == "✖ 1.1 · a · dismissed"
	})
	if edited.Keyboard != nil {
		t.Fatalf("dismissed ping should clear buttons: %+v", edited)
	}

	// /pending shows 1.2 and has ✖ Dismiss all.
	h.tg.Send(owner, "/pending")
	pend := h.tg.Next(t)
	if strings.Contains(pend.Text, "1.1") || !strings.Contains(pend.Text, "1.2") || pend.Buttons()["✖ Dismiss all"] != "dma:a" {
		t.Fatalf("pending: %q %v", pend.Text, pend.Buttons())
	}

	// Tap ✖ Dismiss all to clear remaining items; session returns to 💤 idle.
	h.tg.Tap(owner, pend.MessageID, "dma:a")
	h.tg.Answered(t)
	h.until("dismissed 1.2", func(m fake.Sent) bool {
		return m.MessageID == p2.MessageID && m.Text == "✖ 1.2 · a · dismissed"
	})
	h.tg.Send(owner, "/list")
	list := h.until("list idle", func(m fake.Sent) bool {
		return strings.Contains(m.Text, "1 · a · 💤 idle")
	})
	if strings.Contains(list.Text, "❓") {
		t.Fatalf("session should not have ❓ after dismissing all: %s", list.Text)
	}

	// A post_question (closing question) auto-closes when a subsequent turn finishes.
	h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{
		Key: "post:test", Kind: ipc.KindPost, Summary: "Want me to commit this?", Detail: []byte(`{"question":"Want me to commit this?"}`),
	}})
	postPing := h.until("post ping", func(m fake.Sent) bool { return strings.Contains(m.Text, "1.3") })
	// The turn that added the post_question ends first: it stays open.
	h.turnEnds("Want me to commit this?")
	h.tg.Send(owner, "/pending")
	if got := h.tg.Next(t).Text; !strings.Contains(got, "1.3") {
		t.Fatalf("post_question should survive its own turn end: %s", got)
	}
	// The next turn ends without asking it again: it closes automatically.
	h.turnEnds("Committed and pushed.")
	h.until("closing post_question", func(m fake.Sent) bool {
		return m.MessageID == postPing.MessageID && m.Text == "✅ 1.3 · a · no longer asked"
	})
}
