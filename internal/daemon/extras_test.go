package daemon

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/drive"
	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram/fake"
)

func TestModeSteps(t *testing.T) {
	n, via, ok := modeSteps("manual", "accept")
	if n != 1 || via || !ok {
		t.Fatalf("manual→accept %d %v %v", n, via, ok)
	}
	n, via, ok = modeSteps("manual", "plan")
	if n != 2 || via || !ok {
		t.Fatalf("manual→plan %d %v %v", n, via, ok)
	}
	n, via, ok = modeSteps("plan", "manual")
	if n != 2 || !via || !ok {
		t.Fatalf("plan→manual %d %v %v", n, via, ok)
	}
	if _, _, ok := modeSteps("bypass", "manual"); ok {
		t.Fatal("bypass should not be stepped")
	}
	if _, _, ok := modeSteps("manual", "auto"); ok {
		t.Fatal("auto is not offered")
	}
}

type recTerm struct {
	mu     sync.Mutex
	screen string
	keys   []string
	text   []string
}

func (r *recTerm) Capture() (string, error) { return r.screen, nil }

func (r *recTerm) Keys(keys ...string) error {
	r.mu.Lock()
	r.keys = append(r.keys, keys...)
	r.mu.Unlock()
	return nil
}

func (r *recTerm) Literal(text string) error {
	r.mu.Lock()
	r.text = append(r.text, text)
	r.mu.Unlock()
	return nil
}

func (r *recTerm) Paste(text string) error { return r.Literal(text) }

func (r *recTerm) pressed() (keys, text []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.keys...), append([]string{}, r.text...)
}

func promptScreen(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../screen/testdata/2.1.308/prompt-typed.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFilesAndEndAndMode(t *testing.T) {
	term := &recTerm{screen: promptScreen(t)}
	var mu sync.Mutex
	var tmuxCalls [][]string
	windows := "claude-api 1"
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) {
		c.Terminal = func(string, string) drive.Terminal { return term }
		c.Tmux = func(args ...string) ([]byte, error) {
			mu.Lock()
			tmuxCalls = append(tmuxCalls, append([]string{}, args...))
			line := windows
			mu.Unlock()
			if strings.Contains(strings.Join(args, " "), "display-message") {
				return []byte(line + "\n"), nil
			}
			return []byte("ok\n"), nil
		}
	}
	h.start()
	home := filepath.Dir(h.paths.Socket)
	dir := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(home, "mb.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	lines := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(c)
			if sc.Scan() {
				lines <- sc.Text()
			}
			c.Close()
		}
	}()

	h.spawn(101)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{
		ID: "aaaa1111", PID: 101, Cwd: dir, Mailbox: sock, Version: "2.1.308",
	}})
	h.tg.Send(owner, "/list")
	list := h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "s:1.aaaa1111")
	h.tg.Next(t)
	h.tg.Answered(t)

	h.tg.File(owner, "f1", "notes.txt", []byte("hello"), "")
	saved := h.expectPrefix(owner, "📎 Saved")
	if !strings.Contains(saved.Text, ".ctg-uploads/") || !strings.Contains(saved.Text, "notes.txt") {
		t.Fatalf("saved: %s", saved.Text)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".ctg-uploads", "*notes.txt"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("uploads %v %v", matches, err)
	}
	if b, err := os.ReadFile(matches[0]); err != nil || string(b) != "hello" {
		t.Fatalf("file %q %v", b, err)
	}
	ex, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil || !strings.Contains(string(ex), ".ctg-uploads/") {
		t.Fatalf("exclude %q %v", ex, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err == nil {
		t.Fatal(".gitignore should not be written")
	}

	h.tg.Send(owner, "/list")
	if got := h.tg.Next(t); !strings.Contains(got.Text, "📎 1 file waiting") {
		t.Fatalf("header: %s", got.Text)
	}
	h.tg.Send(owner, "ship it")
	select {
	case msg := <-lines:
		if !strings.Contains(msg, "ship it") || !strings.Contains(msg, "[attached: .ctg-uploads/") {
			t.Fatalf("mailbox %s", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mailbox got nothing")
	}
	h.expectPrefix(owner, "📨 Sent")

	h.tg.File(owner, "f2", "pic.txt", []byte("x"), "look")
	select {
	case msg := <-lines:
		if !strings.Contains(msg, "look") || !strings.Contains(msg, "[attached: .ctg-uploads/") || strings.Contains(msg, "ship it") {
			t.Fatalf("caption %s", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("caption was not sent")
	}
	h.expectPrefix(owner, "📨 Sent")

	h.tg.FileSized(owner, "big", "big.bin", 21<<20, nil, "")
	h.expectPrefix(owner, "That file is over 20 MB")

	h.tg.Send(owner, "/end")
	if got := h.tg.Next(t); !strings.Contains(got.Text, "Remove 1 proj from the list?") || got.Buttons()["🗑 Remove it"] == "" {
		t.Fatalf("read-only end: %s %v", got.Text, got.Buttons())
	}
	h.tg.Send(owner, "/list")
	list = h.tg.Next(t)
	h.tg.Tap(owner, list.MessageID, "mm:1.aaaa1111")
	more := h.tg.Next(t)
	h.tg.Answered(t)
	if more.Buttons()["🛑 End"] != "" || more.Buttons()["🔀 Mode"] != "" || more.Buttons()["🗑 Remove"] == "" {
		t.Fatalf("read-only menu %v", more.Buttons())
	}
	if more.Buttons()["💤 Pings: on"] == "" || more.Buttons()["🔎 Search"] == "" {
		t.Fatalf("session menu %v", more.Buttons())
	}

	// A tmux session can be ended, and its mode can be switched.
	trans := filepath.Join(dir, "t.jsonl")
	body := `{"type":"permission-mode","permissionMode":"default"}` + "\n" +
		`{"type":"assistant","message":{"id":"m1","model":"claude-opus-4-6","content":[{"type":"tool_use","id":"toolu_1","name":"Edit","input":{"file_path":"main.go","old_string":"old\n","new_string":"new\n"}}],"usage":{"input_tokens":1000,"cache_creation_input_tokens":1000,"cache_read_input_tokens":80000}}}` + "\n"
	if err := os.WriteFile(trans, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h.spawn(102)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{
		ID: "bbbb2222", PID: 102, Cwd: dir, Pane: "%1", TmuxSocket: h.tmuxSocket,
		Version: "2.1.308", Transcript: trans,
	}})
	tasks := []ipc.Task{{ID: "b1", Type: "shell"}}
	h.setState("bbbb2222", ipc.StateBusy, &tasks, nil)

	h.tg.Send(owner, "/end 2")
	ask := h.tg.Next(t)
	if !strings.Contains(ask.Text, "Esc is pressed first") || !strings.Contains(ask.Text, "Background work is still running") {
		t.Fatalf("confirm: %s", ask.Text)
	}
	if ask.Buttons()["🛑 End it"] == "" {
		t.Fatalf("confirm buttons %v", ask.Buttons())
	}
	h.tg.Tap(owner, ask.MessageID, ask.Buttons()["🛑 End it"])
	ended := h.until("ended", hasPrefix("🛑 Ended"))
	h.tg.Answered(t)
	if !strings.Contains(ended.Text, "claude-api") {
		t.Fatalf("ended: %s", ended.Text)
	}
	keys, typed := term.pressed()
	if !contains(keys, "Escape") || !contains(keys, "Enter") || !contains(typed, "/exit") {
		t.Fatalf("keys %v typed %v", keys, typed)
	}
	mu.Lock()
	gotCalls := append([][]string{}, tmuxCalls...)
	mu.Unlock()
	if !saw(gotCalls, "-S", h.tmuxSocket, "kill-session", "claude-api") {
		t.Fatalf("tmux calls %v", gotCalls)
	}

	// plan → manual passes through auto, so a running turn is refused.
	os.WriteFile(trans, []byte(`{"type":"permission-mode","permissionMode":"plan"}`+"\n"), 0o644)
	h.tg.Send(owner, "/mode 2")
	mode := h.tg.Next(t)
	if mode.Buttons()["Manual"] == "" || mode.Buttons()["Auto"] != "" || mode.Buttons()["bypass"] != "" {
		t.Fatalf("mode buttons %v", mode.Buttons())
	}
	before, _ := term.pressed()
	h.tg.Tap(owner, mode.MessageID, mode.Buttons()["Manual"])
	h.until("refused", func(m fake.Sent) bool {
		return strings.Contains(m.Text, "passes through auto")
	})
	h.tg.Answered(t)
	after, _ := term.pressed()
	if len(after) != len(before) {
		t.Fatalf("pressed while busy: %v", after)
	}

	h.setState("bbbb2222", ipc.StateIdle, nil, nil)
	// The idle ping is its own message. Drain it if it arrives before the mode reply.
	os.WriteFile(trans, []byte(`{"type":"permission-mode","permissionMode":"default"}`+"\n"), 0o644)
	h.tg.Send(owner, "/mode 2")
	mode = h.until("mode", func(m fake.Sent) bool { return strings.Contains(m.Text, "🔀 Mode") })
	h.tg.Tap(owner, mode.MessageID, mode.Buttons()["Accept edits"])
	switched := h.until("switched", hasPrefix("🔀 "))
	h.tg.Answered(t)
	if !strings.Contains(switched.Text, "Accept edits") {
		t.Fatalf("switch: %s", switched.Text)
	}
	keys, _ = term.pressed()
	if count(keys, "BTab") < 1 {
		t.Fatalf("keys %v", keys)
	}

	if err := os.WriteFile(trans, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h.tg.Send(owner, "/list")
	list = h.until("list", func(m fake.Sent) bool { return m.Buttons()["⋯"] == "mm:a" })
	if strings.Contains(list.Text, "ctx ") {
		t.Fatalf("ctx on the list: %s", list.Text)
	}
	h.tg.Tap(owner, list.MessageID, "s:2.bbbb2222")
	view := h.tg.Next(t)
	h.tg.Answered(t)
	if !strings.Contains(view.Text, "ctx 41%") {
		t.Fatalf("session view: %s", view.Text)
	}

	h.tg.Send(owner, "/last 5 full")
	last := h.until("last", func(m fake.Sent) bool { return strings.Contains(m.Text, "main.go") })
	data := ""
	for label, cb := range last.Buttons() {
		if strings.HasPrefix(label, "🧾") {
			data = cb
		}
	}
	if data == "" {
		t.Fatalf("no diff button: %v\n%s", last.Buttons(), last.Text)
	}
	h.tg.Tap(owner, last.MessageID, data)
	diffMsg := h.until("diff", func(m fake.Sent) bool { return strings.Contains(m.Text, "language-diff") })
	h.tg.Answered(t)
	if !strings.Contains(diffMsg.Text, "-old") || !strings.Contains(diffMsg.Text, "+new") {
		t.Fatalf("diff: %s", diffMsg.Text)
	}
}

func TestGrep(t *testing.T) {
	h := newHarness(t).paired().start()
	home := filepath.Dir(h.paths.Socket)
	dir := filepath.Join(home, "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, "live.jsonl")
	writeLines(t, live,
		grepLine("nothing to see", "2026-10-28T10:00:00Z"),
		grepLine("only-here-token", "2026-10-28T11:00:00Z"),
	)
	proj := filepath.Join(home, ".claude", "projects", "slug")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	past := filepath.Join(proj, "abc.jsonl")
	writeLines(t, past,
		grepLine("before-one", "2026-10-28T12:00:00Z"),
		grepLine("before-two", "2026-10-28T12:01:00Z"),
		grepLine("banana split", "2026-10-28T12:02:00Z"),
		grepLine("after-one", "2026-10-28T12:03:00Z"),
		grepLine("after-two", "2026-10-28T12:04:00Z"),
	)
	old := filepath.Join(proj, "old.jsonl")
	writeLines(t, old, grepLine("ancient-token", "2026-01-01T00:00:00Z"))
	ago := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(old, ago, ago); err != nil {
		t.Fatal(err)
	}
	h.spawn(101)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{
		ID: "aaaa1111", PID: 101, Cwd: dir, Pane: "%1", TmuxSocket: h.tmuxSocket,
		Version: "2.1.308", Transcript: live,
	}})

	h.tg.Send(owner, "/grep 1 only-here-token")
	hit := h.until("live hit", func(m fake.Sent) bool { return strings.Contains(m.Text, "only-here-token") })
	if strings.Contains(hit.Text, "banana") {
		t.Fatalf("session search saw other sessions: %s", hit.Text)
	}

	h.tg.Send(owner, "/grep banana")
	hits := h.until("banana", func(m fake.Sent) bool { return strings.Contains(m.Text, "banana") })
	if hits.Buttons()["📜 Around"] == "" {
		t.Fatalf("buttons %v", hits.Buttons())
	}
	h.tg.Tap(owner, hits.MessageID, hits.Buttons()["📜 Around"])
	around := h.until("around", func(m fake.Sent) bool { return strings.Contains(m.Text, "before-one") })
	h.tg.Answered(t)
	for _, s := range []string{"before-two", "banana split", "after-one", "after-two"} {
		if !strings.Contains(around.Text, s) {
			t.Fatalf("around missing %s: %s", s, around.Text)
		}
	}

	h.tg.Send(owner, "/grep ancient-token")
	if got := h.until("none", func(m fake.Sent) bool {
		return strings.Contains(m.Text, "ancient-token") || strings.Contains(m.Text, "No hits")
	}); !strings.Contains(got.Text, "No hits") {
		t.Fatalf("old file: %s", got.Text)
	}
}

func TestStatus(t *testing.T) {
	var updated bool
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) {
		c.Version = "0.3.0"
		c.Release = func() (string, error) { return "v9.9.9", nil }
		c.Update = func() (string, error) {
			updated = true
			return "binary replaced", nil
		}
	}
	h.start()
	home := filepath.Dir(h.paths.Socket)
	if err := os.MkdirAll(h.paths.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.paths.StateDir, "installed.json"), []byte(`{"hooks":"nope"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"ctg":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.register("aaaa1111", 101, "/work/a", "%1")
	// Re-register the version the status should call unverified. register()
	// stamps 2.1.308, so say so again over the socket.
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{
		ID: "aaaa1111", PID: 101, Cwd: "/work/a", Pane: "%1", TmuxSocket: h.tmuxSocket, Version: "9.9.9",
	}})

	h.tg.Send(owner, "/status")
	got := h.until("status", func(m fake.Sent) bool { return strings.Contains(m.Text, "🩺 Status") })
	for _, s := range []string{"ctg 0.3.0", "v9.9.9", "hooks: out of date", "mcp: installed", "service: not installed", "9.9.9", "answer in the terminal", "Last error: none"} {
		if !strings.Contains(got.Text, s) {
			t.Errorf("status missing %q:\n%s", s, got.Text)
		}
	}
	if got.Buttons()["⬆️ Update"] == "" {
		t.Fatalf("buttons %v", got.Buttons())
	}
	h.tg.Tap(owner, got.MessageID, got.Buttons()["⬆️ Update"])
	h.until("updating", hasPrefix("⬆️ Updating"))
	done := h.until("updated", func(m fake.Sent) bool { return strings.Contains(m.Text, "binary replaced") })
	h.tg.Answered(t)
	if !updated || !strings.Contains(done.Text, "binary replaced") {
		t.Fatalf("update %v %s", updated, done.Text)
	}

	h.tg.Send(owner, "/list")
	list := h.until("list", func(m fake.Sent) bool { return m.Buttons()["⋯"] == "mm:a" })
	h.tg.Tap(owner, list.MessageID, "mm:a")
	more := h.tg.Next(t)
	h.tg.Answered(t)
	if more.Buttons()["🔎 Search"] == "" || more.Buttons()["🩺 Status"] == "" || more.Buttons()["❔ Help"] == "" || more.Buttons()["🔕 Mute all"] == "" {
		t.Fatalf("list menu %v", more.Buttons())
	}
}

func grepLine(text, ts string) string {
	return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":"` + text + `"}}`
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiffOnAPermission(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	detail := []byte(`{"file_path":"main.go","old_string":"old\n","new_string":"new\n"}`)
	h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{
		Key: "k1", Kind: ipc.KindPermission, Tool: "Edit", Summary: "Edit main.go", Detail: detail,
	}})
	ping := h.until("ping", func(m fake.Sent) bool { return strings.Contains(m.Text, "1.1") })
	h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
	card := h.until("card", func(m fake.Sent) bool { return m.Buttons()["🧾 Diff"] != "" })
	h.tg.Answered(t)
	h.tg.Tap(owner, card.MessageID, card.Buttons()["🧾 Diff"])
	got := h.until("diff", func(m fake.Sent) bool { return strings.Contains(m.Text, "language-diff") })
	h.tg.Answered(t)
	if !strings.Contains(got.Text, "-old") || !strings.Contains(got.Text, "+new") {
		t.Fatalf("diff: %s", got.Text)
	}
}

func TestLongDiffIsAFile(t *testing.T) {
	h := newHarness(t).paired().start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	var body strings.Builder
	for i := 0; i < 800; i++ {
		body.WriteString("line\n")
	}
	detail := []byte(`{"file_path":"big.txt","content":"` + strings.ReplaceAll(body.String(), "\n", `\n`) + `"}`)
	h.call(ipc.Request{Op: ipc.OpItemAdd, SessionID: "aaaa1111", Item: &ipc.Item{
		Key: "k1", Kind: ipc.KindPermission, Tool: "Write", Summary: "Write big.txt", Detail: detail,
	}})
	ping := h.until("ping", func(m fake.Sent) bool { return m.Buttons()["Answer"] != "" })
	h.tg.Tap(owner, ping.MessageID, ping.Buttons()["Answer"])
	card := h.until("card", func(m fake.Sent) bool { return m.Buttons()["🧾 Diff"] != "" })
	h.tg.Answered(t)
	h.tg.Tap(owner, card.MessageID, card.Buttons()["🧾 Diff"])
	got := h.until("file", func(m fake.Sent) bool { return m.Method == "sendDocument" })
	h.tg.Answered(t)
	if !strings.HasSuffix(got.File, ".diff") || !strings.Contains(got.Text, "Write") {
		t.Fatalf("document %+v", got)
	}
}

func TestEndLeavesALiveProcess(t *testing.T) {
	old := exitWait
	exitWait = 0
	t.Cleanup(func() { exitWait = old })
	term := &recTerm{screen: promptScreen(t)}
	var mu sync.Mutex
	var calls [][]string
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) {
		c.Terminal = func(string, string) drive.Terminal { return term }
		c.Tmux = func(args ...string) ([]byte, error) {
			mu.Lock()
			calls = append(calls, append([]string{}, args...))
			mu.Unlock()
			return []byte("claude-api 1\n"), nil
		}
	}
	h.start()
	pid := os.Getpid()
	h.spawn(pid)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{
		ID: "aaaa1111", PID: pid, Cwd: "/work/a", Pane: "%1", TmuxSocket: h.tmuxSocket, Version: "2.1.308",
	}})
	h.tg.Send(owner, "/end 1")
	ask := h.tg.Next(t)
	h.tg.Tap(owner, ask.MessageID, ask.Buttons()["🛑 End it"])
	got := h.until("left", func(m fake.Sent) bool { return strings.Contains(m.Text, "still running") })
	h.tg.Answered(t)
	if !strings.Contains(got.Text, "still running") {
		t.Fatalf("%s", got.Text)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range calls {
		for _, a := range c {
			if a == "kill-session" || a == "kill-pane" {
				t.Fatalf("tmux was closed: %v", calls)
			}
		}
	}
}

func TestEndClosesOnlyThePane(t *testing.T) {
	term := &recTerm{screen: promptScreen(t)}
	var mu sync.Mutex
	var calls [][]string
	h := newHarness(t).paired()
	h.cfgEdit = func(c *Config) {
		c.Terminal = func(string, string) drive.Terminal { return term }
		c.Tmux = func(args ...string) ([]byte, error) {
			mu.Lock()
			calls = append(calls, append([]string{}, args...))
			mu.Unlock()
			if strings.Contains(strings.Join(args, " "), "display-message") {
				return []byte("dev 2\n"), nil
			}
			return []byte("ok\n"), nil
		}
	}
	h.start()
	h.register("aaaa1111", 101, "/work/a", "%1")
	h.tg.Send(owner, "/end 1")
	ask := h.tg.Next(t)
	h.tg.Tap(owner, ask.MessageID, ask.Buttons()["🛑 End it"])
	got := h.until("pane", hasPrefix("🛑 Ended"))
	h.tg.Answered(t)
	if !strings.Contains(got.Text, "closed its pane") {
		t.Fatalf("%s", got.Text)
	}
	mu.Lock()
	defer mu.Unlock()
	if !saw(calls, "kill-pane", "%1") {
		t.Fatalf("calls %v", calls)
	}
	for _, c := range calls {
		for _, a := range c {
			if a == "kill-session" {
				t.Fatalf("closed the whole session: %v", calls)
			}
		}
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func count(ss []string, want string) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}

func saw(calls [][]string, parts ...string) bool {
	for _, c := range calls {
		ok := true
		blob := " " + strings.Join(c, " ") + " "
		for _, p := range parts {
			if !strings.Contains(blob, " "+p+" ") && !strings.Contains(strings.Join(c, " "), p) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestGhostDotSessionRejectedAndReadOnlyRemoved(t *testing.T) {
	h := newHarness(t).paired().start()

	// Empty or "." Cwd registrations are rejected.
	if resp, err := ipc.Call(context.Background(), h.paths.Socket, ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{ID: "ghost001", Cwd: ""}}); err == nil || resp.OK {
		t.Fatalf("empty cwd should be rejected: resp=%+v err=%v", resp, err)
	}
	if resp, err := ipc.Call(context.Background(), h.paths.Socket, ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{ID: "ghost002", Cwd: "."}}); err == nil || resp.OK {
		t.Fatalf("dot cwd should be rejected: resp=%+v err=%v", resp, err)
	}
	h.tg.Send(owner, "/list")
	if got := h.tg.Next(t); !strings.Contains(got.Text, "No sessions.") {
		t.Fatalf("ghost sessions should be rejected: %s", got.Text)
	}

	// A read-only session with a valid Cwd and mailbox can be removed via 🗑 Remove.
	h.spawn(101)
	h.call(ipc.Request{Op: ipc.OpRegister, Session: &ipc.Session{
		ID: "aaaa1111", PID: 101, Cwd: "/work/ro", Mailbox: "/tmp/mb.sock", Version: "2.1.308",
	}})
	h.tg.Send(owner, "/end 1")
	confirm := h.tg.Next(t)
	if confirm.Buttons()["🗑 Remove it"] != "ey:1.aaaa1111" {
		t.Fatalf("confirm buttons: %v", confirm.Buttons())
	}
	h.tg.Tap(owner, confirm.MessageID, "ey:1.aaaa1111")
	h.tg.Answered(t)
	h.expect("🗑 Removed 1 ro.")
	h.tg.Send(owner, "/list")
	if got := h.tg.Next(t); !strings.Contains(got.Text, "No sessions.") {
		t.Fatalf("after remove: %s", got.Text)
	}
}

func TestLegendAndMessageLogging(t *testing.T) {
	h := newHarness(t).paired().start()
	h.transcriptSession(userLine("hello from terminal"), assistantLine("hi there"))

	// /legend shows the icon legend.
	h.tg.Send(owner, "/legend")
	leg := h.tg.Next(t)
	for _, want := range []string{"ℹ️ Icon Legend", "📡 monitor", "🖥 Terminal", "✖ dismissed"} {
		if !strings.Contains(leg.Text, want) {
			t.Fatalf("legend missing %q:\n%s", want, leg.Text)
		}
	}

	// /help has the ℹ️ Legend button.
	h.tg.Send(owner, "/help")
	help := h.tg.Next(t)
	if help.Buttons()["ℹ️ Legend"] != "lg:a" {
		t.Fatalf("help buttons: %v", help.Buttons())
	}

	// /last logs the message and raw transcript sources in messages.jsonl.
	h.tg.Send(owner, "/last 2")
	h.tg.Next(t)
	// Send one more command so the outbox goroutine finishes logging the previous send.
	h.tg.Send(owner, "/list")
	h.tg.Next(t)

	b, err := os.ReadFile(h.paths.MessagesFile())
	if err != nil {
		t.Fatalf("ReadFile(messages.jsonl): %v", err)
	}
	content := string(b)
	if !strings.Contains(content, `"kind":"last"`) || !strings.Contains(content, "hello from terminal") || !strings.Contains(content, `"sources":[`) {
		t.Fatalf("messages.jsonl missing expected fields:\n%s", content)
	}
}
