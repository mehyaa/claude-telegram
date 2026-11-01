package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/ipc"
)

func (h *harness) setState(id, state string, tasks *[]ipc.Task, crons *int) {
	h.t.Helper()
	h.call(ipc.Request{Op: ipc.OpUpdate, SessionID: id, State: state, Tasks: tasks, Crons: crons})
}

func TestIdlePing(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/api", "%1")
	h.setState("aaaa1111", ipc.StateBusy, nil, nil)
	h.setState("aaaa1111", ipc.StateIdle, nil, nil)
	ping := h.expect("💤 1 · api")
	if ping.Buttons()["Open 1"] != "o:1.aaaa1111" {
		t.Fatalf("buttons %v", ping.Buttons())
	}
	// One ping per idle stretch.
	h.setState("aaaa1111", ipc.StateIdle, nil, nil)
	h.tg.Quiet(t, 200*time.Millisecond)
}

func TestBackgroundWorkDelaysThePing(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	shells := []ipc.Task{{ID: "b123", Type: "shell"}, {ID: "ag1", Type: "subagent"}}
	crons := 0
	h.setState("aaaa1111", ipc.StateIdle, &shells, &crons)
	h.tg.Quiet(t, 200*time.Millisecond)

	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)
	if !strings.Contains(list.Text, "1 · a · 🐚🤖") || list.Buttons()["1 · a 🐚🤖"] != "s:1.aaaa1111" {
		t.Fatalf("list:\n%s\n%v", list.Text, list.Buttons())
	}

	h.setState("aaaa1111", ipc.StateBusy, nil, nil)
	h.tg.Send(owner, "/list")
	if got := h.tg.Next(t).Text; !strings.Contains(got, "⚙️🐚🤖 working") {
		t.Fatalf("during the turn:\n%s", got)
	}

	none := []ipc.Task{}
	h.setState("aaaa1111", ipc.StateIdle, &none, &crons)
	h.expect("💤 1 · a")
}

func TestScheduledWakeCountsAsIdle(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	none := []ipc.Task{}
	crons := 1
	h.setState("aaaa1111", ipc.StateIdle, &none, &crons)
	h.expect("💤⏰ 1 · a")

	h.tg.Send(owner, "/settings")
	menu := h.tg.Next(t)
	h.tg.Tap(owner, menu.MessageID, menu.Buttons()["Reminders: off"])
	h.tg.Answered(t)
	if got := h.tg.Next(t).Text; !strings.Contains(got, "Reminders: on") {
		t.Fatalf("settings:\n%s", got)
	}
	h.advance(time.Hour)
	h.d.tick()
	h.tg.Quiet(t, 200*time.Millisecond)
}

func TestTerminalSkipsTheIdlePing(t *testing.T) {
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) {
		c.ClientActive = func(string, string, time.Time) bool { return true }
	}
	h.start()
	st := h.stub("prompt", nil)
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	h.setState("aaaa1111", ipc.StateBusy, nil, nil)
	h.setState("aaaa1111", ipc.StateIdle, nil, nil)
	h.tg.Quiet(t, 200*time.Millisecond)

	// A turn started from the phone still pings, even if the terminal is open.
	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	h.tg.Next(t)
	h.tg.Answered(t)
	h.tg.Send(owner, "from the phone")
	h.expectPrefix(owner, "⌨️ Typed into 1 a")
	h.call(ipc.Request{Op: ipc.OpUpdate, SessionID: "aaaa1111", State: ipc.StateBusy, Prompt: "from the phone"})
	none := []ipc.Task{}
	crons := 0
	h.setState("aaaa1111", ipc.StateIdle, &none, &crons)
	h.expect("💤 1 · a")
}

func TestRemindersAndOpening(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.setState("aaaa1111", ipc.StateBusy, nil, nil)
	h.setState("aaaa1111", ipc.StateIdle, nil, nil)
	h.expect("💤 1 · a")

	h.advance(5 * time.Minute)
	h.d.tick()
	h.tg.Quiet(t, 200*time.Millisecond) // reminders are off

	h.tg.Send(owner, "/settings")
	menu := h.tg.Next(t)
	h.tg.Tap(owner, menu.MessageID, "sg:a:r")
	h.tg.Answered(t)
	h.tg.Next(t)
	h.d.tick()
	h.expect("💤 1 · a · 5m")

	h.advance(5 * time.Minute)
	h.d.tick()
	h.expect("💤 1 · a · 10m")

	// Opening the session starts the schedule over. The message is dated on
	// the daemon's clock: wall time is ten minutes behind it.
	h.tg.SendAt(owner, "private", "/list", h.clock())
	list := h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	h.tg.Next(t)
	h.tg.Answered(t)
	h.advance(4 * time.Minute)
	h.d.tick()
	h.tg.Quiet(t, 200*time.Millisecond)
	h.advance(time.Minute)
	h.d.tick()
	h.expect("💤 1 · a · 5m")
}

func TestRemindersStopAfter16Hours(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/api", "%1")
	h.setState("aaaa1111", ipc.StateBusy, nil, nil)
	h.setState("aaaa1111", ipc.StateIdle, nil, nil)
	h.expect("💤 1 · api")
	h.tg.SendAt(owner, "private", "/settings", h.clock())
	menu := h.tg.Next(t)
	h.tg.Tap(owner, menu.MessageID, "sg:a:r")
	h.tg.Answered(t)
	h.tg.Next(t)

	// The gaps between 5m, 10m, 15m, 30m, 1h, 2h, 4h, 8h, 12h and 16h.
	for _, gap := range []time.Duration{
		5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 15 * time.Minute,
		30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 4 * time.Hour, 4 * time.Hour,
	} {
		h.advance(gap)
		h.d.tick()
		h.tg.Next(t)
	}
	h.d.tick()
	h.expect("💤 1 · api: no more reminders")
	h.d.tick()
	h.tg.Quiet(t, 200*time.Millisecond)
}

func TestSessionCanTurnIdlePingsOff(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	h.tg.Next(t)
	h.tg.Answered(t)
	h.tg.Tap(owner, list.MessageID, "mm:1.aaaa1111")
	more := h.tg.Next(t)
	h.tg.Answered(t)
	if more.Buttons()["💤 Pings: on"] == "" {
		t.Fatalf("session menu %v", more.Buttons())
	}
	h.tg.Tap(owner, list.MessageID, more.Buttons()["💤 Pings: on"])
	h.tg.Answered(t)
	if got := h.tg.Next(t); got.Buttons()["💤 Pings: off · session"] == "" {
		t.Fatalf("after the tap: %v", got.Buttons())
	}
	h.setState("aaaa1111", ipc.StateBusy, nil, nil)
	h.setState("aaaa1111", ipc.StateIdle, nil, nil)
	h.tg.Quiet(t, 200*time.Millisecond)
}

func TestMuteSkipsTheIdlePing(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.tg.Send(owner, "/mute")
	h.expectPrefix(owner, "🔕 Muted all sessions")
	h.setState("aaaa1111", ipc.StateBusy, nil, nil)
	h.setState("aaaa1111", ipc.StateIdle, nil, nil)
	h.tg.Quiet(t, 200*time.Millisecond)
}

func TestMonitorStatusAndSummary(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	mons := []ipc.Task{{ID: "m1", Type: "monitor"}}
	crons := 0
	h.setState("aaaa1111", ipc.StateIdle, &mons, &crons)

	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)
	if !strings.Contains(list.Text, "1 · a · 📡 monitor") || list.Buttons()["1 · a 📡"] != "s:1.aaaa1111" {
		t.Fatalf("monitor only list:\n%s\n%v", list.Text, list.Buttons())
	}

	both := []ipc.Task{{ID: "b1", Type: "shell"}, {ID: "m1", Type: "monitor"}, {ID: "m2", Type: "monitor"}}
	h.setState("aaaa1111", ipc.StateIdle, &both, &crons)
	h.tg.Send(owner, "/list")
	list = h.tg.Next(t)
	if !strings.Contains(list.Text, "1 · a · 🐚📡") || list.Buttons()["1 · a 🐚📡"] != "s:1.aaaa1111" {
		t.Fatalf("shell+monitor list:\n%s\n%v", list.Text, list.Buttons())
	}

	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	sv := h.tg.Next(t)
	h.tg.Answered(t)
	if !strings.Contains(sv.Text, "🐚 1 shell · 📡 2 monitors") {
		t.Fatalf("sessionView missing task summary:\n%s", sv.Text)
	}

	h.setState("aaaa1111", ipc.StateBusy, nil, nil)
	h.tg.Send(owner, "/list")
	if got := h.tg.Next(t).Text; !strings.Contains(got, "⚙️🐚📡 working") {
		t.Fatalf("busy with shell+monitor:\n%s", got)
	}
}
