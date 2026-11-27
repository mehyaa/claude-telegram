package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/ipc"
)

func TestSendBareHTML(t *testing.T) {
	h := newHarness(t).paired().start()
	h.call(ipc.Request{Op: ipc.OpSend, Text: "Hello **world**"})
	got := h.tg.Next(t)
	if got.ChatID != owner || got.ParseMode != "HTML" || got.Text != "Hello <b>world</b>" {
		t.Fatalf("sent %+v", got)
	}
	if got.Keyboard != nil {
		t.Fatalf("unexpected keyboard: %+v", got.Keyboard)
	}
}

func TestSendWithSessionTag(t *testing.T) {
	h := newHarness(t).paired().start()
	n := h.register("aaaa1111", 101, "/work/myapp", "%1")
	resp := h.call(ipc.Request{Op: ipc.OpSend, Text: "ping", SessionNum: n})
	wantTag := fmt.Sprintf("#%d myapp", n)
	if resp.Tag != wantTag {
		t.Fatalf("tag %q, want %q", resp.Tag, wantTag)
	}
	got := h.tg.Next(t)
	want := "<b>" + wantTag + "</b>\n\nping"
	if got.ParseMode != "HTML" || got.Text != want {
		t.Fatalf("sent %q, want %q", got.Text, want)
	}
}

func TestSendByPID(t *testing.T) {
	h := newHarness(t).paired().start()
	n := h.register("aaaa1111", 101, "/work/myapp", "%1")
	h.call(ipc.Request{Op: ipc.OpSend, Text: "from mcp", PID: 101})
	got := h.tg.Next(t)
	want := fmt.Sprintf("<b>#%d myapp</b>\n\nfrom mcp", n)
	if got.Text != want {
		t.Fatalf("sent %q, want %q", got.Text, want)
	}
}

func TestSendUnknownPIDIsBare(t *testing.T) {
	h := newHarness(t).paired().start()
	resp := h.call(ipc.Request{Op: ipc.OpSend, Text: "orphan", PID: 99999})
	if resp.Tag != "" {
		t.Fatalf("tag %q", resp.Tag)
	}
	got := h.tg.Next(t)
	if got.Text != "orphan" {
		t.Fatalf("sent %q", got.Text)
	}
}

func TestSendUnknownSession(t *testing.T) {
	h := newHarness(t).paired().start()
	_, err := ipcCall(h, ipc.Request{Op: ipc.OpSend, Text: "x", SessionNum: 99})
	if err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("err %v", err)
	}
}

func TestSendUnpaired(t *testing.T) {
	h := newHarness(t).start()
	_, err := ipcCall(h, ipc.Request{Op: ipc.OpSend, Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("err %v", err)
	}
}

func TestSendEmpty(t *testing.T) {
	h := newHarness(t).paired().start()
	_, err := ipcCall(h, ipc.Request{Op: ipc.OpSend, Text: "  "})
	if err == nil || !strings.Contains(err.Error(), "needs text") {
		t.Fatalf("err %v", err)
	}
}

func TestSendPages(t *testing.T) {
	h := newHarness(t).paired().start()
	body := strings.Repeat("line\n", 400)
	h.call(ipc.Request{Op: ipc.OpSend, Text: body})
	got := h.tg.Next(t)
	if got.ParseMode != "HTML" || !strings.Contains(got.Text, "(1/") {
		t.Fatalf("first page: %q", got.Text)
	}
	var more string
	for label, data := range got.Buttons() {
		if strings.HasPrefix(label, "⏬ More") {
			more = data
			break
		}
	}
	if more == "" {
		t.Fatalf("no More button: %v", got.Buttons())
	}
	h.tg.Tap(owner, got.MessageID, more)
	page2 := h.tg.Next(t)
	if !strings.Contains(page2.Text, "(2/") {
		t.Fatalf("second page: %q", page2.Text)
	}
}

func TestSendIgnoresMute(t *testing.T) {
	h := newHarness(t).paired().start()
	h.tg.Send(owner, "/mute")
	h.expect("🔕 Muted all sessions until you turn it back on. Items still go to /pending.")
	h.call(ipc.Request{Op: ipc.OpSend, Text: "still goes"})
	got := h.tg.Next(t)
	if got.Text != "still goes" {
		t.Fatalf("muted send: %q", got.Text)
	}
}

func ipcCall(h *harness, req ipc.Request) (ipc.Response, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return ipc.Call(ctx, h.paths.Socket, req)
}
