package daemon

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/config"
	"github.com/mehyaa/claude-telegram/internal/ipc"
)

func wrongCode(code string) string {
	if code == "123456" {
		return "654321"
	}
	return "123456"
}

func TestFirstStartPairs(t *testing.T) {
	h := newHarness(t).start()
	code := h.code()

	// Before pairing, nothing gets an answer: not a wrong code, and not
	// the /start that Telegram's Start button sends.
	h.tg.Send(stranger, wrongCode(code))
	h.tg.Send(owner, "/start")
	h.tg.Quiet(t, 300*time.Millisecond)

	h.tg.Send(owner, code)
	h.expectPrefix(owner, "Paired.")
	h.expectPrefix(owner, "ctg\n\nNo sessions.")
	if id, err := config.ReadChat(h.paths); err != nil || id != owner {
		t.Fatalf("saved chat = %d, %v; want %d", id, err, owner)
	}

	// Pairing is off now: the code works once.
	h.tg.Send(stranger, code)
	h.tg.Send(stranger, "/list")
	h.tg.Quiet(t, 300*time.Millisecond)
	h.tg.Send(owner, "/list")
	h.expectPrefix(owner, "ctg")

	audit, err := os.ReadFile(h.paths.AuditFile())
	if err != nil || !strings.Contains(string(audit), `"action":"pair"`) {
		t.Fatalf("audit log: %q, %v", audit, err)
	}
}

func TestWrongCodesTurnPairingOff(t *testing.T) {
	h := newHarness(t).start()
	code := h.code()
	for range pairMisses {
		h.tg.Send(stranger, wrongCode(code))
	}
	h.tg.Send(owner, code)
	h.tg.Quiet(t, 500*time.Millisecond)
	if id, _ := config.ReadChat(h.paths); id != 0 {
		t.Fatalf("paired chat %d after %d wrong codes", id, pairMisses)
	}
}

func TestGroupChatCantPair(t *testing.T) {
	h := newHarness(t).start()
	code := h.code()
	h.tg.SendAt(-100, "group", code, time.Now())
	h.tg.Quiet(t, 300*time.Millisecond)
	h.tg.Send(owner, code)
	h.expectPrefix(owner, "Paired.")
}

func TestCodeExpires(t *testing.T) {
	h := newHarness(t).start()
	code := h.code()
	h.advance(pairFor + time.Minute)
	h.tg.Send(owner, code)
	h.tg.Quiet(t, 300*time.Millisecond)
}

func TestRepair(t *testing.T) {
	h := newHarness(t).paired().start()
	code := h.call(ipc.Request{Op: ipc.OpPair}).Code
	if !isCode(code) {
		t.Fatalf("pair returned code %q", code)
	}

	// The old chat keeps working until another chat sends the code.
	h.tg.Send(owner, "/list")
	h.expectPrefix(owner, "ctg")

	const phone2 = int64(43)
	h.tg.Send(phone2, code)
	h.expectPrefix(owner, "ctg is now paired with another chat.")
	h.expectPrefix(phone2, "Paired.")
	h.expectPrefix(phone2, "ctg")

	h.tg.Send(owner, "/list")
	h.tg.Quiet(t, 300*time.Millisecond)
	if id, _ := config.ReadChat(h.paths); id != phone2 {
		t.Fatalf("paired chat = %d, want %d", id, phone2)
	}
}

func TestOtherChatsAreDropped(t *testing.T) {
	h := newHarness(t).paired().start()
	h.tg.Send(stranger, "/list")
	h.tg.Tap(stranger, 1, "l")
	h.tg.Quiet(t, 300*time.Millisecond)

	// The paired chat's own messages from long ago aren't acted on either.
	h.tg.SendAt(owner, "private", "/list", time.Now().Add(-staleAfter-time.Minute))
	h.tg.Quiet(t, 300*time.Millisecond)
}
