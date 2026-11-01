package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram/telegramtest"
)

// transcriptSession registers a session whose transcript is a file the test
// writes to, and selects it.
func (h *harness) transcriptSession(lines ...any) (path string, write func(...any)) {
	h.t.Helper()
	path = filepath.Join(filepath.Dir(h.paths.Socket), "t.jsonl")
	write = func(vs ...any) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			h.t.Fatal(err)
		}
		defer f.Close()
		for _, v := range vs {
			b, _ := json.Marshal(v)
			f.Write(append(b, '\n'))
		}
	}
	write(lines...)
	h.spawn(101)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{ID: "aaaa1111", PID: 101, Cwd: "/work/a", Pane: "%1", TmuxSocket: h.tmuxSocket, Transcript: path, Version: "2.1.308"}})
	h.tg.Send(owner, "/list")
	list := h.tg.Next(h.t)
	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	h.tg.Next(h.t)
	h.tg.Answered(h.t)
	return path, write
}

func userLine(text string) any {
	return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}
}

func assistantLine(text string) any {
	return map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}}
}

func toolUseLine(id, cmd string) any {
	return map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": cmd}}}}}
}

func toolResultLine(id string, isError bool) any {
	return map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isError, "content": "x"}}}}
}

func TestLast(t *testing.T) {
	h := newHarness(t).paired().start()
	h.transcriptSession(
		userLine("old prompt"), assistantLine("old reply"),
		userLine("run the tests"), toolUseLine("t1", "make test-unit"), toolResultLine("t1", false),
		toolUseLine("t2", "make lint"), toolResultLine("t2", true),
		assistantLine("Tests pass; lint fails."),
	)
	h.tg.Send(owner, "/last 2")
	h.expect("👤 <b>#1 a</b> · You (terminal) · 1/2\nrun the tests\n\n🤖 <b>#1 a</b> · Claude · 2/2\nTests pass; lint fails.")
	h.tg.Send(owner, "/last 2 full")
	h.expect("👤 <b>#1 a</b> · You (terminal) · 1/4\nrun the tests\n\n🔧 <b>#1 a</b> · <b>Bash</b> · make test-unit · 2/4\n<b>Command:</b>\n<pre><code class=\"language-bash\">make test-unit</code></pre>\n<b>Result:</b> ✓\n<blockquote expandable>x</blockquote>\n\n🔧 <b>#1 a</b> · <b>Bash</b> · make lint · 3/4\n<b>Command:</b>\n<pre><code class=\"language-bash\">make lint</code></pre>\n<b>Result:</b> ✗\n<blockquote expandable>x</blockquote>\n\n🤖 <b>#1 a</b> · Claude · 4/4\nTests pass; lint fails.")

	// 📜 Last opens the picker; Chat sends the default 10.
	h.tg.Tap(owner, 1, "lt:1.aaaa1111")
	picker := h.tg.Next(t)
	h.tg.Answered(t)
	if picker.Buttons()["[10✓]"] == "" || picker.Buttons()["💬 Chat"] == "" || picker.Buttons()["✍️"] == "" {
		t.Fatalf("picker buttons %v", picker.Buttons())
	}
	h.tg.Tap(owner, picker.MessageID, picker.Buttons()["20"])
	moved := h.tg.Next(t)
	h.tg.Answered(t)
	if moved.Buttons()["[20✓]"] == "" || moved.Buttons()["10"] == "" {
		t.Fatalf("number should only move the mark: %v", moved.Buttons())
	}
	h.tg.Tap(owner, picker.MessageID, moved.Buttons()["🤖 Agent"])
	h.tg.Answered(t)
	if got := h.tg.Next(t).Text; !strings.Contains(got, "🤖 <b>#1 a</b> · Claude") || strings.Contains(got, "You (terminal)") {
		t.Fatalf("agent mode: %q", got)
	}
	h.tg.Tap(owner, picker.MessageID, picker.Buttons()["✍️"])
	h.tg.Answered(t)
	ask := h.tg.Next(t)
	if !ask.ForceReply || ask.Placeholder != "1–50" {
		t.Fatalf("number prompt: %+v", ask)
	}
	h.tg.Send(owner, "7")
	if got := h.tg.Next(t); got.MessageID != picker.MessageID || got.Buttons()["[7✓]"] == "" {
		t.Fatalf("custom count: %q %v", got.Text, got.Buttons())
	}
}

func TestLongMessagesPage(t *testing.T) {
	h := newHarness(t).paired().start()
	long := strings.TrimSpace(strings.Repeat(strings.Repeat("word ", 59)+"end\n", 14)) // about 4,200 characters
	h.transcriptSession(userLine("explain"), assistantLine(long), userLine("thanks"))
	h.tg.Send(owner, "/last 3")
	first := h.tg.Next(t)
	if !strings.HasPrefix(first.Text, "👤 <b>#1 a</b> · You (terminal) · 1/3\nexplain\n\n🤖 <b>#1 a</b> · Claude · 2/3\nword") || !strings.HasSuffix(first.Text, "end\n(1/3)") {
		t.Fatalf("first message: %q", first.Text)
	}
	more := first.Buttons()["⏬ More (2/3)"]
	if more == "" {
		t.Fatalf("buttons %v", first.Buttons())
	}
	// The messages after the cut one come in the next Telegram message.
	if got := h.tg.Next(t).Text; got != "👤 <b>#1 a</b> · You (terminal) · 3/3\nthanks" {
		t.Fatalf("after the cut message: %q", got)
	}
	h.tg.Tap(owner, first.MessageID, more)
	h.tg.Answered(t)
	p2 := h.tg.Next(t)
	if !strings.HasPrefix(p2.Text, "🤖 <b>#1 a</b> · Claude\nword") || !strings.HasSuffix(p2.Text, "(2/3)") || p2.Buttons()["⏬ More (3/3)"] == "" {
		t.Fatalf("page 2: %q %v", p2.Text, p2.Buttons())
	}
	h.tg.Send(owner, "/more")
	if p3 := h.tg.Next(t); !strings.HasSuffix(p3.Text, "(3/3)") || len(p3.Buttons()) != 0 {
		t.Fatalf("page 3: %q %v", p3.Text, p3.Buttons())
	}
	h.tg.Send(owner, "/more")
	h.expect("That was the last page.")

	// A second paged message gets pagedSeq=2 (no session #2 exists); tapping
	// ⏬ More must still send page 2 instead of treating "2" as a session scope.
	h.tg.Send(owner, "/last 3")
	second1 := h.tg.Next(t)
	more2 := second1.Buttons()["⏬ More (2/3)"]
	if more2 != "mr:2:1" {
		t.Fatalf("second more button = %q, want mr:2:1", more2)
	}
	_ = h.tg.Next(t) // trailing "thanks" message
	h.tg.Tap(owner, second1.MessageID, more2)
	h.tg.Answered(t)
	if got := h.tg.Next(t); !strings.HasPrefix(got.Text, "🤖 <b>#1 a</b> · Claude\nword") || !strings.HasSuffix(got.Text, "(2/3)") {
		t.Fatalf("second paged message page 2: msg=%d %q", got.MessageID, got.Text)
	}
}

func TestFollow(t *testing.T) {
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) { c.FollowEvery = time.Millisecond }
	h.start()
	_, write := h.transcriptSession(userLine("before following"))
	h.d.watch()

	h.tg.Send(owner, "/follow full")
	start := h.expect("👁 Following 1 a (🧰 Full): new messages arrive as they come.")
	if start.Buttons()["⏹ Unfollow"] != "uf:1.aaaa1111" || start.Buttons()["Open 1"] != "o:1.aaaa1111" {
		t.Fatalf("buttons %v", start.Buttons())
	}
	write(userLine("run it"), toolUseLine("t1", "make"), assistantLine("Running make."))
	h.d.watch()
	// The tool call waits for its result.
	got := h.tg.Next(t)
	if got.Text != "👤 <b>#1 a</b> · You (terminal)\nrun it\n\n🤖 <b>#1 a</b> · Claude\nRunning make." || got.Buttons()["⏹ Unfollow"] != "uf:1.aaaa1111" || got.Buttons()["Open 1"] != "o:1.aaaa1111" {
		t.Fatalf("follow: %q %v", got.Text, got.Buttons())
	}
	write(toolResultLine("t1", false), assistantLine("Done."))
	h.advance(time.Second) // past FollowEvery since the last batch
	h.d.watch()
	h.expect("🔧 <b>#1 a</b> · <b>Bash</b> · make\n<b>Command:</b>\n<pre><code class=\"language-bash\">make</code></pre>\n<b>Result:</b> ✓\n<blockquote expandable>x</blockquote>\n\n🤖 <b>#1 a</b> · Claude\nDone.")

	h.tg.Tap(owner, got.MessageID, "uf:1.aaaa1111")
	h.tg.Answered(t)
	h.expect("Stopped following 1 a.")
	write(assistantLine("unseen"))
	h.d.watch()
	h.tg.Quiet(t, 200*time.Millisecond)

	// Following stops when the session ends.
	h.tg.Send(owner, "/follow 1")
	h.until("following again", hasPrefix("👁 Following 1 a"))
	h.kill(101)
	h.d.tick()
	h.until("saying it stopped", func(m telegramtest.Sent) bool { return m.Text == "⏹ 1 a ended; stopped following it." })
}

func TestMultiSessionFollowAndManager(t *testing.T) {
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) { c.FollowEvery = time.Millisecond }
	h.start()

	_, write1 := h.transcriptSession(userLine("init 1"))
	p2 := filepath.Join(filepath.Dir(h.paths.Socket), "t2.jsonl")
	write2 := func(vs ...any) {
		f, err := os.OpenFile(p2, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, v := range vs {
			b, _ := json.Marshal(v)
			f.Write(append(b, '\n'))
		}
	}
	write2(userLine("init 2"))
	h.spawn(202)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{
		ID: "bbbb2222", PID: 202, Cwd: "/work/b", Pane: "%2", TmuxSocket: h.tmuxSocket, Transcript: p2, Version: "2.1.308",
	}})
	h.d.watch()

	h.tg.Send(owner, "/follow 1 chat")
	h.expectPrefix(owner, "👁 Following 1 a (💬 Chat)")
	h.tg.Send(owner, "/follow 2 agent")
	h.expectPrefix(owner, "👁 Following 2 b (🤖 Agent)")

	// Both sessions show their follow mode in /list, plus the 👁 Following (2) button.
	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)
	if !strings.Contains(list.Text, "1 · a · 💤 idle · 👁 chat") || !strings.Contains(list.Text, "2 · b · 💤 idle · 👁 agent") {
		t.Fatalf("list text:\n%s", list.Text)
	}
	if list.Buttons()["👁 Following (2)"] != "fm:a" {
		t.Fatalf("list buttons: %v", list.Buttons())
	}

	// Both sessions stream simultaneously.
	write1(assistantLine("from session 1"))
	write2(assistantLine("from session 2"))
	h.advance(time.Second)
	h.d.watch()
	m1 := h.tg.Next(t)
	m2 := h.tg.Next(t)
	if !strings.Contains(m1.Text, "🤖 <b>#1 a</b> · Claude\nfrom session 1") {
		t.Fatalf("stream 1: %q", m1.Text)
	}
	if !strings.Contains(m2.Text, "🤖 <b>#2 b</b> · Claude\nfrom session 2") {
		t.Fatalf("stream 2: %q", m2.Text)
	}

	// Plain /unfollow with >1 followed session opens the manager.
	h.tg.Send(owner, "/unfollow")
	mgr := h.tg.Next(t)
	if !strings.Contains(mgr.Text, "👁 Following") || mgr.Buttons()["⏹ #1"] != "uf:1.aaaa1111" || mgr.Buttons()["⏹ #2"] != "uf:2.bbbb2222" || mgr.Buttons()["⏹ Unfollow all"] != "ufa:a" {
		t.Fatalf("manager: %q %v", mgr.Text, mgr.Buttons())
	}

	// Unfollow session 1; session 2 keeps streaming.
	h.tg.Send(owner, "/unfollow 1")
	h.expect("Stopped following 1 a.")
	write1(assistantLine("ignored 1"))
	write2(assistantLine("still 2"))
	h.advance(time.Second)
	h.d.watch()
	got := h.tg.Next(t)
	if !strings.Contains(got.Text, "🤖 <b>#2 b</b> · Claude\nstill 2") {
		t.Fatalf("after unfollow 1: %q", got.Text)
	}

	// Unfollow all stops the remaining session.
	h.tg.Send(owner, "/unfollow all")
	h.expect("Stopped following 2 b.")
}

func TestBashEditDiffInFullModeAndDiffButton(t *testing.T) {
	h := newHarness(t).paired().start()
	h.transcriptSession(
		userLine("patch config"),
		map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []any{
					map[string]any{
						"type": "tool_use",
						"id":   "t_bash_diff",
						"name": "Bash",
						"input": map[string]any{
							"command":     "sed -i 's/old/new/' config.yaml",
							"description": "Update config.yaml",
						},
					},
				},
			},
		},
		map[string]any{
			"type": "user",
			"message": map[string]any{
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "t_bash_diff",
						"content":     "",
					},
				},
			},
			"toolUseResult": map[string]any{
				"stdout": "",
				"stderr": "",
				"bashEditDiff": map[string]any{
					"files": []any{
						map[string]any{
							"filePath": "config.yaml",
							"hunks": []any{
								map[string]any{
									"oldStart": 1,
									"oldLines": 1,
									"newStart": 1,
									"newLines": 1,
									"lines":    []string{"-mode: old", "+mode: new"},
								},
							},
						},
					},
				},
			},
		},
	)

	h.tg.Send(owner, "/last 2 full")
	msg := h.tg.Next(t)
	if !strings.Contains(msg.Text, "<b>Path:</b> <code>config.yaml</code>") || !strings.Contains(msg.Text, "<b>Change:</b> +1 −1") || !strings.Contains(msg.Text, "<pre><code class=\"language-diff\">") {
		t.Fatalf("expected inline bashEditDiff in full mode:\n%s", msg.Text)
	}
	diffBtn := msg.Buttons()["🧾 config.yaml"]
	if diffBtn == "" {
		t.Fatalf("expected 🧾 config.yaml button on Bash edit entry, got: %v", msg.Buttons())
	}
	h.tg.Tap(owner, msg.MessageID, diffBtn)
	h.tg.Answered(t)
	diffReply := h.tg.Next(t)
	if !strings.Contains(diffReply.Text, "-mode: old") || !strings.Contains(diffReply.Text, "+mode: new") {
		t.Fatalf("unexpected diff reply:\n%s", diffReply.Text)
	}
}
