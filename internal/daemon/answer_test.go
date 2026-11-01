package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram/telegramtest"
	"github.com/mehyaa/claude-telegram/internal/tmux"
)

var stubBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stub")
	if err != nil {
		panic(err)
	}
	stubBin = filepath.Join(dir, "stubtui")
	if out, err := exec.Command("go", "build", "-o", stubBin, "../drive/stubtui").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// stub runs stubtui in a private tmux server and points the harness's
// sessions at it.
type stub struct {
	pane tmux.Pane
	log  string
}

func (h *harness) stub(mode string, spec any) *stub {
	h.t.Helper()
	if err := tmux.Check(); err != nil {
		h.t.Skip(err)
	}
	dir, err := os.MkdirTemp("", "tm")
	if err != nil {
		h.t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	log := filepath.Join(dir, "stub.jsonl")
	b, _ := json.Marshal(spec)
	tm := func(args ...string) ([]byte, error) {
		cmd := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "TMUX=")
		return cmd.CombinedOutput()
	}
	if out, err := tm("new-session", "-d", "-x", "120", "-y", "45", "-s", "stub", stubBin, log, mode, string(b)); err != nil {
		h.t.Fatalf("tmux: %v %s", err, out)
	}
	h.t.Cleanup(func() {
		tm("kill-server")
		os.RemoveAll(dir)
	})
	out, err := tm("display-message", "-p", "-t", "stub", "#{pane_id}")
	if err != nil {
		h.t.Fatal(err)
	}
	h.tmuxSocket = sock
	st := &stub{pane: tmux.Pane{Socket: sock, ID: strings.TrimSpace(string(out))}, log: log}
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if text, _ := st.pane.Capture(); strings.TrimSpace(text) != "" {
			return st
		}
		if time.Now().After(deadline) {
			h.t.Fatal("stub drew nothing")
		}
	}
}

func (st *stub) lines() []map[string]any {
	f, err := os.Open(st.log)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		json.Unmarshal(sc.Bytes(), &m)
		out = append(out, m)
	}
	return out
}

// outcome waits for the n-th outcome the stub logs (1-based).
func (st *stub) outcome(t *testing.T, n int) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		seen := 0
		for _, l := range st.lines() {
			if l["done"] == true {
				if seen++; seen == n {
					return l
				}
			}
		}
	}
	t.Fatalf("no outcome %d from the stub; log %v", n, st.lines())
	return nil
}

func (st *stub) keys() []string {
	var k []string
	for _, l := range st.lines() {
		if key, ok := l["key"].(string); ok {
			k = append(k, key)
		}
	}
	return k
}

// until reads the bot's messages until one satisfies ok.
func (h *harness) until(what string, ok func(telegramtest.Sent) bool) telegramtest.Sent {
	h.t.Helper()
	var seen []string
	for range 20 {
		m := h.tg.Next(h.t)
		if ok(m) {
			return m
		}
		seen = append(seen, m.Text)
	}
	h.t.Fatalf("no message %s; saw %q", what, seen)
	return telegramtest.Sent{}
}

func hasPrefix(p string) func(telegramtest.Sent) bool {
	return func(m telegramtest.Sent) bool { return strings.HasPrefix(m.Text, p) }
}

func pizzaSpec() map[string]any {
	opt := func(l ...string) []map[string]string {
		var o []map[string]string
		for _, x := range l {
			o = append(o, map[string]string{"label": x, "description": "about " + x})
		}
		return o
	}
	return map[string]any{"questions": []map[string]any{
		{"question": "Which toppings?", "header": "Toppings", "multiSelect": true, "options": opt("Cheese", "Olives", "Basil")},
		{"question": "Which size?", "header": "Size", "multiSelect": false, "options": opt("Small", "Large")},
		{"question": "Which drink?", "header": "Drink", "multiSelect": false, "options": opt("Water", "Juice")},
	}}
}

func TestAnswerQuestionsFromTelegram(t *testing.T) {
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) { c.DriveWait = 2 * time.Second }
	h.start()
	spec := pizzaSpec()
	st := h.stub("question", spec)
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	detail, _ := json.Marshal(spec)
	h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{Key: "toolu_1", Kind: ipc.KindQuestion, Tool: "AskUserQuestion", Summary: "Which toppings? (+2 more)", Detail: detail}})
	ping := h.expect("❓ 1.1 · a · Which toppings? (+2 more)")

	h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
	v := h.until("with the item", hasPrefix("ctg\n\n<b>❓ 1.1 · a</b>\nQ1 · Toppings: Which toppings? (pick any)"))
	h.tg.Answered(t)
	tap := func(label string) telegramtest.Sent {
		t.Helper()
		data, ok := v.Buttons()[label]
		if !ok {
			t.Fatalf("no button %q in %v", label, v.Buttons())
		}
		h.tg.Tap(owner, v.MessageID, data)
		h.tg.Answered(t)
		v = h.tg.Next(t)
		return v
	}
	tap("Q1 Cheese")
	tap("Q1 Basil")
	if _, ok := v.Buttons()["📤 Send"]; ok {
		t.Fatal("Send offered before every question has an answer")
	}
	tap("Q2 Large")
	// Q3 gets its own text.
	h.tg.Tap(owner, v.MessageID, v.Buttons()["✍️ Q3 Other…"])
	h.tg.Answered(t)
	h.expect("✍️ Type your own answer for Q3. /cancel to stop.")
	h.tg.Send(owner, "Lemonade, cold")
	v = h.until("with Q3's text", func(m telegramtest.Sent) bool { return strings.Contains(m.Text, "✓ ✍️ Lemonade, cold") })
	h.tg.Tap(owner, v.MessageID, v.Buttons()["📤 Send"])
	h.tg.Answered(t)

	h.until("saying it was sent", hasPrefix("📤 1.1 · a · sent from Telegram (answered: Which toppings? → Cheese, Basil; Which size? → Large; Which drink? → Lemonade, cold). Waiting for Claude Code."))
	out := st.outcome(t, 1)
	want := map[string]any{"Which toppings?": "Cheese, Basil", "Which size?": "Large", "Which drink?": "Lemonade, cold"}
	if got, _ := out["answers"].(map[string]any); len(got) != 3 || got["Which toppings?"] != want["Which toppings?"] || got["Which size?"] != want["Which size?"] || got["Which drink?"] != want["Which drink?"] {
		t.Fatalf("the stub got %v", out)
	}

	// PostToolUse then closes the item; the ping says Telegram answered.
	h.call(ipc.Request{Op: ipc.OpItemDone, SessionID: "aaaa1111", Key: "toolu_1", Note: "answered: Which size? → Large"})
	h.until("closing the ping", func(m telegramtest.Sent) bool {
		return m.MessageID == ping.MessageID && m.Text == "✅ 1.1 · a · answered from Telegram: Which size? → Large"
	})
}

func TestAllowAndDenyFromTelegram(t *testing.T) {
	prompt := map[string]any{"title": "Bash command", "body": []string{"touch x.txt"}, "options": []string{"Yes", "Yes, and always allow", "No"}}
	for _, tc := range []struct {
		button string
		reason string
		want   map[string]any
	}{
		{"✅ Allow once", "", map[string]any{"permission": "allow"}},
		{"❌ Deny", "", map[string]any{"permission": "deny"}},
		{"💬 Deny with a reason…", "use y.txt instead", map[string]any{"permission": "deny", "reason": "use y.txt instead"}},
	} {
		t.Run(tc.button, func(t *testing.T) {
			h := newHarness(t).paired().start()
			st := h.stub("permission", prompt)
			h.register("aaaa1111", 101, "/work/a", st.pane.ID)
			h.add("aaaa1111", "perm:1", ipc.KindPermission, "Bash: touch x.txt")
			h.d.mu.Lock()
			h.d.byID("aaaa1111").Items[0].Needle = "touch x.txt"
			h.d.mu.Unlock()
			ping := h.tg.Next(t)
			h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
			v := h.tg.Next(t)
			h.tg.Answered(t)
			h.tg.Tap(owner, v.MessageID, v.Buttons()[tc.button])
			h.tg.Answered(t)
			if tc.reason != "" {
				h.expect("💬 Type the reason for denying 1.1. /cancel to stop.")
				h.tg.Send(owner, tc.reason)
			}
			out := st.outcome(t, 1)
			for k, w := range tc.want {
				if out[k] != w {
					t.Fatalf("stub outcome %v, want %v", out, tc.want)
				}
			}
		})
	}
}

func TestMismatchMeansAnswerInTerminal(t *testing.T) {
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) { c.DriveWait = 300 * time.Millisecond }
	h.start()
	st := h.stub("permission", map[string]any{"title": "Bash command", "body": []string{"touch x.txt"}, "options": []string{"Yes", "No"}})
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	h.add("aaaa1111", "perm:1", ipc.KindPermission, "Bash: rm -rf build")
	h.d.mu.Lock()
	h.d.byID("aaaa1111").Items[0].Needle = "rm -rf build"
	h.d.mu.Unlock()
	ping := h.tg.Next(t)
	h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
	v := h.tg.Next(t)
	h.tg.Answered(t)
	h.tg.Tap(owner, v.MessageID, v.Buttons()["✅ Allow once"])
	h.tg.Answered(t)
	h.until("saying to answer in the terminal", func(m telegramtest.Sent) bool {
		return strings.Contains(m.Text, "⚠️ Answer in the terminal:") && strings.Contains(m.Text, `permission prompt for something else ("Bash command")`)
	})
	if k := st.keys(); len(k) != 0 {
		t.Fatalf("keys pressed on a mismatch: %q", k)
	}
	h.tg.Tap(owner, v.MessageID, "i:"+"1.aaaa1111"+":1.1")
	got := h.tg.Next(t)
	h.tg.Answered(t)
	if !strings.Contains(got.Text, "⚠️ Answer in the terminal:") {
		t.Fatalf("blocked item view: %q", got.Text)
	}
	b := got.Buttons()
	if b["🔁 Retry"] == "" || b["📺 Screen"] == "" {
		t.Fatalf("blocked item buttons %v", b)
	}
}

func TestUncheckedVersion(t *testing.T) {
	h := newHarness(t).paired().start()
	h.spawn(101)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{ID: "aaaa1111", PID: 101, Cwd: "/work/a", Pane: "%1", TmuxSocket: h.tmuxSocket, Version: "9.9.9"}})
	h.add("aaaa1111", "perm:1", ipc.KindPermission, "Bash: ls")
	ping := h.tg.Next(t)
	h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
	v := h.tg.Next(t)
	h.tg.Answered(t)
	if !strings.HasSuffix(v.Text, "⚠️ Answer in the terminal: Claude Code 9.9.9 hasn't been checked with ctg yet (re-run the step-1 probe on it).") {
		t.Fatalf("%q %v", v.Text, v.Buttons())
	}
	if _, ok := v.Buttons()["✅ Allow once"]; ok {
		t.Fatalf("unverified version still offered Allow: %v", v.Buttons())
	}
	// A version listed in verified-versions is accepted.
	os.WriteFile(filepath.Join(h.paths.ConfigDir, "verified-versions"), []byte("9.9.9\n"), 0o600)
	h.tg.Tap(owner, v.MessageID, "i:1.aaaa1111:1.1")
	v = h.tg.Next(t)
	h.tg.Answered(t)
	if _, ok := v.Buttons()["✅ Allow once"]; !ok {
		t.Fatalf("after listing 9.9.9: %q %v", v.Text, v.Buttons())
	}
}

func TestPlainTextAndInterrupt(t *testing.T) {
	h := newHarness(t).paired().start()
	st := h.stub("prompt", nil)
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)

	h.tg.Send(owner, "hello there")
	h.expect("Pick a session first: /list, then tap one.")

	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	h.tg.Next(t)
	h.tg.Answered(t)
	h.tg.Send(owner, "hello there")
	h.expect("⌨️ Typed into 1 a.")
	if out := st.outcome(t, 1); out["prompt"] != "hello there" {
		t.Fatalf("stub got %v", out)
	}

	h.tg.Send(owner, "/interrupt 1 stop and say hi")
	h.expect("⏹ Esc, then ⌨️ typed into 1 a.")
	if out := st.outcome(t, 2); out["escape"] != true {
		t.Fatalf("stub got %v", out)
	}
	if out := st.outcome(t, 3); out["prompt"] != "stop and say hi" {
		t.Fatalf("stub got %v", out)
	}

	audit, _ := os.ReadFile(h.paths.AuditFile())
	for _, want := range []string{`"action":"type","session":"1 a","detail":"hello there"`, `"action":"interrupt"`, `"action":"keys"`} {
		if !strings.Contains(string(audit), want) {
			t.Errorf("audit log lacks %s:\n%s", want, audit)
		}
	}
}

func TestMailboxForSessionsOutsideTmux(t *testing.T) {
	h := newHarness(t).paired().start()
	sock := filepath.Join(filepath.Dir(h.paths.Socket), "mb.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
	}()
	h.spawn(101)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{ID: "aaaa1111", PID: 101, Cwd: "/work/a", Mailbox: sock, Version: "2.1.308"}})
	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	h.tg.Next(t)
	h.tg.Answered(t)
	h.tg.Send(owner, "status?")
	h.expect("📨 Sent to 1 a through the mailbox. It arrives as a message from another session, without your authority.")
	select {
	case line := <-got:
		if line != `{"message":{"content":"status?","role":"user"},"type":"user"}`+"\n" {
			t.Fatalf("mailbox got %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing reached the mailbox")
	}
}

func TestPostQuestion(t *testing.T) {
	h := newHarness(t).paired().start()
	st := h.stub("prompt", nil)
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	detail, _ := json.Marshal(map[string]any{"question": "Use SQLite?", "options": []string{"yes", "no"}, "recommendation": "yes: one file"})
	// ctg mcp names the session by its process.
	resp := h.call(ipc.Request{Op: ipc.OpItemAdd, PID: 101, Item: &ipc.Item{Key: "post:1", Kind: ipc.KindPost, Summary: "Use SQLite?", Detail: detail}})
	if resp.Item != "1.1" {
		t.Fatalf("item %q", resp.Item)
	}
	ping := h.expect("❓ 1.1 · a · Use SQLite?")
	h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
	v := h.tg.Next(t)
	h.tg.Answered(t)
	if v.Text != "ctg\n\n<b>❓ 1.1 · a</b>\nUse SQLite?\n1. yes\n2. no\n➡️ yes: one file" {
		t.Fatalf("view %q", v.Text)
	}
	h.tg.Tap(owner, v.MessageID, v.Buttons()["yes"])
	h.tg.Answered(t)
	if out := st.outcome(t, 1); out["prompt"] != "[answer to 1.1] yes" {
		t.Fatalf("stub got %v", out)
	}
	// The prompt reaches UserPromptSubmit, which closes the item.
	h.call(ipc.Request{Op: ipc.OpUpdate, SessionID: "aaaa1111", State: ipc.StateBusy, Prompt: "[answer to 1.1] yes"})
	h.until("closing 1.1", func(m telegramtest.Sent) bool {
		return m.MessageID == ping.MessageID && m.Text == "✅ 1.1 · a · answered from Telegram: yes"
	})

	// Answered in the terminal instead: "1.2 no thanks".
	h.call(ipc.Request{Op: ipc.OpItemAdd, PID: 101, Item: &ipc.Item{Key: "post:2", Kind: ipc.KindPost, Summary: "Rename it?", Detail: detail}})
	// Closing 1.1 edited its open view as well as its ping.
	ping2 := h.until("pinging 1.2", hasPrefix("❓ 1.2"))
	h.call(ipc.Request{Op: ipc.OpUpdate, SessionID: "aaaa1111", State: ipc.StateBusy, Prompt: "1.2 no thanks"})
	h.until("closing 1.2", func(m telegramtest.Sent) bool {
		return m.MessageID == ping2.MessageID && m.Text == "✅ 1.2 · a · answered in terminal: no thanks"
	})
}

func TestPreviewGoesStraightToTerminal(t *testing.T) {
	h := newHarness(t).paired().start()
	st := h.stub("question", map[string]any{"questions": []map[string]any{{
		"question": "Which mockup?", "header": "Mockup", "multiSelect": false,
		"options": []map[string]string{
			{"label": "Alpha", "description": "A", "preview": "```\nalpha\n```"},
			{"label": "Beta", "description": "B", "preview": "```\nbeta\n```"},
		},
	}}})
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	detail, _ := json.Marshal(map[string]any{"questions": []map[string]any{{
		"question": "Which mockup?", "header": "Mockup", "multiSelect": false,
		"options": []map[string]any{
			{"label": "Alpha", "description": "A", "preview": "```\nalpha\n```"},
			{"label": "Beta", "description": "B", "preview": "```\nbeta\n```"},
		},
	}}})
	h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{Key: "toolu_p", Kind: ipc.KindQuestion, Tool: "AskUserQuestion", Summary: "Which mockup?", Detail: detail}})
	ping := h.expect("❓ 1.1 · a · Which mockup?")
	h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
	v := h.tg.Next(t)
	h.tg.Answered(t)
	if !strings.Contains(v.Text, "this dialog has a preview pane") {
		t.Fatalf("view %q", v.Text)
	}
	if v.Buttons()["📺 Screen"] == "" || v.Buttons()["🔁 Retry"] == "" {
		t.Fatalf("buttons %v", v.Buttons())
	}
	if _, ok := v.Buttons()["Alpha"]; ok {
		t.Fatalf("preview item still offered options: %v", v.Buttons())
	}
	if k := st.keys(); len(k) != 0 {
		t.Fatalf("keys pressed: %q", k)
	}
}

func TestRetryAfterRefusal(t *testing.T) {
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) { c.DriveWait = 300 * time.Millisecond }
	h.start()
	st := h.stub("permission", map[string]any{"title": "Bash command", "body": []string{"touch x.txt"}, "options": []string{"Yes", "No"}})
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	h.add("aaaa1111", "perm:1", ipc.KindPermission, "Bash: rm -rf build")
	h.d.mu.Lock()
	h.d.byID("aaaa1111").Items[0].Needle = "rm -rf build"
	h.d.mu.Unlock()
	ping := h.tg.Next(t)
	h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
	v := h.tg.Next(t)
	h.tg.Answered(t)
	h.tg.Tap(owner, v.MessageID, v.Buttons()["✅ Allow once"])
	h.tg.Answered(t)
	blocked := h.until("refusal", func(m telegramtest.Sent) bool {
		return strings.Contains(m.Text, "⚠️ Answer in the terminal:")
	})
	if blocked.Buttons()["🔁 Retry"] == "" {
		t.Fatalf("no Retry in %v", blocked.Buttons())
	}
	h.tg.Tap(owner, blocked.MessageID, blocked.Buttons()["🔁 Retry"])
	h.tg.Answered(t)
	again := h.until("refusal after retry", func(m telegramtest.Sent) bool {
		return m.MessageID == blocked.MessageID && strings.Contains(m.Text, "⚠️ Answer in the terminal:")
	})
	if again.Buttons()["🔁 Retry"] == "" {
		t.Fatalf("Retry gone after retry: %v", again.Buttons())
	}
	if k := st.keys(); len(k) != 0 {
		t.Fatalf("keys pressed: %q", k)
	}
}

func TestPartialAnswerShowsScreen(t *testing.T) {
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) { c.DriveWait = 300 * time.Millisecond }
	h.start()
	stubSpec := map[string]any{"questions": []map[string]any{
		{"question": "Pick a colour", "header": "Colour", "multiSelect": false, "options": []map[string]string{{"label": "Red", "description": "r"}, {"label": "Green", "description": "g"}}},
		{"question": "Which size?", "header": "Size", "multiSelect": false, "options": []map[string]string{{"label": "Small", "description": "s"}, {"label": "Large", "description": "l"}}},
	}}
	st := h.stub("question", stubSpec)
	h.register("aaaa1111", 101, "/work/a", st.pane.ID)
	detail, _ := json.Marshal(map[string]any{"questions": []map[string]any{
		{"question": "Pick a colour", "header": "Colour", "multiSelect": false, "options": []map[string]string{{"label": "Red", "description": "r"}, {"label": "Green", "description": "g"}}},
		{"question": "Which drink?", "header": "Drink", "multiSelect": false, "options": []map[string]string{{"label": "Small", "description": "s"}, {"label": "Large", "description": "l"}}},
	}})
	h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{Key: "toolu_2", Kind: ipc.KindQuestion, Tool: "AskUserQuestion", Summary: "Pick a colour (+1 more)", Detail: detail}})
	ping := h.expect("❓ 1.1 · a · Pick a colour (+1 more)")
	h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
	v := h.until("with the item", hasPrefix("ctg\n\n<b>❓ 1.1 · a</b>\nQ1 · Colour: Pick a colour"))
	h.tg.Answered(t)
	h.tg.Tap(owner, v.MessageID, v.Buttons()["Q1 Red"])
	h.tg.Answered(t)
	v = h.tg.Next(t)
	h.tg.Tap(owner, v.MessageID, v.Buttons()["Q2 Small"])
	h.tg.Answered(t)
	v = h.tg.Next(t)
	h.tg.Tap(owner, v.MessageID, v.Buttons()["📤 Send"])
	h.tg.Answered(t)

	scr := h.until("with the screen after a partial answer", func(m telegramtest.Sent) bool {
		return m.ParseMode == "HTML" && strings.Contains(m.Text, "already answered Colour") && strings.Contains(m.Text, "<pre>")
	})
	if scr.Buttons()["↑"] == "" || scr.Buttons()["⏎"] == "" {
		t.Fatalf("screen keyboard %v", scr.Buttons())
	}
	if !strings.Contains(strings.Join(st.keys(), " "), "1") {
		t.Fatalf("expected Q1's key, got %q", st.keys())
	}
}

func TestTranscriptClosesItems(t *testing.T) {
	h := newHarness(t).paired().start()
	path := filepath.Join(filepath.Dir(h.paths.Socket), "t.jsonl")
	os.WriteFile(path, []byte(`{"type":"system","subtype":"turn_duration"}`+"\n"), 0o600)
	h.spawn(101)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{ID: "aaaa1111", PID: 101, Cwd: "/work/a", Pane: "%1", TmuxSocket: h.tmuxSocket, Transcript: path, Version: "2.1.308"}})
	h.d.watch() // starts following at the end of the file

	input := json.RawMessage(`{"command":"touch x.txt","description":"make x"}`)
	h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{Key: ipc.PermKey("Bash", input), Kind: ipc.KindPermission, Tool: "Bash", Summary: "Bash: touch x.txt"}})
	ping := h.expectPrefix(owner, "🔐 1.1")
	h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{Key: "toolu_q", Kind: ipc.KindQuestion, Summary: "Pick one"}})
	ping2 := h.expectPrefix(owner, "❓ 1.2")

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	write := func(v any) {
		b, _ := json.Marshal(v)
		f.Write(append(b, '\n'))
	}
	write(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "toolu_b", "name": "Bash", "input": input}}}})
	write(map[string]any{"type": "user", "toolDenialKind": "user-rejected", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "toolu_b", "is_error": true, "content": "rejected"}}}})
	write(map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "[Request interrupted by user for tool use]"}}}})
	f.Close()
	h.d.watch()

	h.until("closing the denied prompt", func(m telegramtest.Sent) bool {
		return m.MessageID == ping.MessageID && m.Text == "✅ 1.1 · a · denied in terminal"
	})
	h.until("closing the interrupted question", func(m telegramtest.Sent) bool {
		return m.MessageID == ping2.MessageID && m.Text == "✅ 1.2 · a · interrupted in terminal"
	})

	// What happens while the daemon is restarting isn't lost.
	h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{Key: "toolu_r", Kind: ipc.KindQuestion, Summary: "Again?"}})
	ping3 := h.until("pinging 1.3", hasPrefix("❓ 1.3"))
	h.stop()
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	write(map[string]any{"type": "user", "toolDenialKind": "user-rejected", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "toolu_r", "is_error": true, "content": "rejected"}}}})
	f.Close()
	h.start()
	h.d.watch()
	h.until("closing 1.3 after the restart", func(m telegramtest.Sent) bool {
		return m.MessageID == ping3.MessageID && m.Text == "✅ 1.3 · a · cancelled in terminal"
	})
}
