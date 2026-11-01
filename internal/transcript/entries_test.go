package transcript

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureEntries(t *testing.T, name string) []Entry {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return Entries(bytes.Split(b, []byte("\n")))
}

func find(entries []Entry, kind EntryKind, sub string) *Entry {
	for i := range entries {
		if entries[i].Kind == kind && strings.Contains(entries[i].Text, sub) {
			return &entries[i]
		}
	}
	return nil
}

func TestEntriesFromTheProbe(t *testing.T) {
	es := fixtureEntries(t, "probe-2.1.308.jsonl")
	for _, c := range []struct {
		kind EntryKind
		sub  string
	}{
		{User, "Call the AskUserQuestion tool once with exactly one single-choice question"},
		{Assistant, "Green"},
		{User, "1QUEUED: also say banana"}, // typed mid-turn: a queued_command attachment
		{Peer, "SOCKET-A: external, no auth."},
		{Note, "interrupted"},
		{Tool, "ran touch probe-file.txt"},
		{Tool, "asked: Pick a colour"},
		{Tool, "wrote notes.txt"},
		{Tool, "presented a plan"},
	} {
		if find(es, c.kind, c.sub) == nil {
			t.Errorf("no entry of kind %d with %q", c.kind, c.sub)
		}
	}
	// The peer wrapper is gone, and verifiedPeerPid is preserved.
	if p := find(es, Peer, "SOCKET-A"); p == nil || strings.Contains(p.Text, "This came from another Claude session") || p.PeerPID != 10002 {
		t.Errorf("peer entry SOCKET-A: %+v", p)
	}
	// A peer message queued mid-turn (attachment queued_command with origin.kind="peer")
	// is also classified as Peer with its verifiedPeerPid.
	if p := find(es, Peer, "SOCKET-C"); p == nil || p.PeerPID != 10004 {
		t.Errorf("peer entry SOCKET-C: %+v", p)
	}
	// Results are applied to their calls: the first touch ran, the fourth
	// was denied.
	if e := find(es, Tool, "ran touch probe-file.txt"); e == nil || e.Status != OK {
		t.Errorf("touch probe-file.txt: %+v", e)
	}
	if e := find(es, Tool, "ran touch probe-file-4.txt"); e == nil || e.Status != Failed {
		t.Errorf("touch probe-file-4.txt: %+v", e)
	}
	for _, e := range es {
		if e.Kind == User && (strings.Contains(e.Text, "<system-reminder>") || strings.HasPrefix(e.Text, "<")) {
			t.Errorf("meta text as a user entry: %q", e.Text)
		}
	}
}

func TestSlashCommands(t *testing.T) {
	es := fixtureEntries(t, "commands-2.1.308.jsonl")
	var users []string
	for _, e := range es {
		if e.Kind == User {
			users = append(users, e.Text)
		}
	}
	got := strings.Join(users, " | ")
	if !strings.Contains(got, "/clear") || strings.Contains(got, "local-command") || strings.Contains(got, "<command-name>") {
		t.Fatalf("user entries: %q", got)
	}
	if e := find(es, User, "/rename"); e == nil || !e.Cmd || e.Result != "Session renamed to: renamed-probe" {
		t.Fatalf("rename: %+v", e)
	}
}

func TestLastReadsFromTheEnd(t *testing.T) {
	b, err := os.ReadFile("testdata/probe-2.1.308.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Pad the front past one read chunk, so Last has to read more than once.
	path := filepath.Join(t.TempDir(), "t.jsonl")
	pad := bytes.Repeat([]byte(`{"type":"api-request-blob","x":"`+strings.Repeat("x", 1000)+`"}`+"\n"), 1500)
	os.WriteFile(path, append(pad, b...), 0o600)

	all := fixtureEntries(t, "probe-2.1.308.jsonl")
	for _, n := range []int{1, 3, 10, 1000} {
		es, err := Last(path, n)
		if err != nil {
			t.Fatal(err)
		}
		want := min(n, countMessages(all))
		if got := countMessages(es); got != want {
			t.Errorf("Last(%d): %d messages, want %d", n, got, want)
		}
		if !es[0].IsMessage() {
			t.Errorf("Last(%d) starts with %+v", n, es[0])
		}
	}
	last, _ := Last(path, 1)
	if want := all[len(all)-1]; last[len(last)-1].Text != want.Text {
		t.Errorf("Last(1) ends with %q, want %q", last[len(last)-1].Text, want.Text)
	}
}

func TestToolSummary(t *testing.T) {
	for _, c := range []struct{ name, input, want string }{
		{"Bash", `{"command":"make test-unit\nmake lint"}`, "ran make test-unit"},
		{"Edit", `{"file_path":"/a/b/main.go"}`, "edited main.go"},
		{"mcp__ctg__post_question", `{}`, "used ctg · post_question"},
		{"Frobnicate", `{}`, "used Frobnicate"},
	} {
		if got := ToolSummary(c.name, []byte(c.input)); got != c.want {
			t.Errorf("ToolSummary(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStreamHoldsToolsUntilTheirResult(t *testing.T) {
	b, err := os.ReadFile("testdata/probe-2.1.308.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var s Stream
	var got []Entry
	// One line at a time, as a follower would see them.
	for _, line := range bytes.Split(b, []byte("\n")) {
		got = append(got, s.Feed([][]byte{line})...)
	}
	all := fixtureEntries(t, "probe-2.1.308.jsonl")
	if countMessages(got) != countMessages(all) {
		t.Fatalf("%d messages streamed, %d in the file", countMessages(got), countMessages(all))
	}
	for _, e := range got {
		if e.Kind == Tool && e.Status == Running {
			t.Fatalf("a tool streamed before its result: %+v", e)
		}
	}
	if e := find(got, Tool, "ran touch probe-file-4.txt"); e == nil || e.Status != Failed {
		t.Fatalf("denied call: %+v", e)
	}
}

func TestStreamSlashCommandOutput(t *testing.T) {
	b, err := os.ReadFile("testdata/commands-2.1.308.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var s Stream
	got := s.Feed(bytes.Split(b, []byte("\n")))
	if e := find(got, User, "/rename"); e == nil || !e.Cmd || e.Result != "Session renamed to: renamed-probe" {
		t.Fatalf("streamed rename: %+v", e)
	}
}

func TestXMLWrappersBecomeCmdNotes(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"user","message":{"content":"<task-notification>\n<summary>build finished</summary>\n</task-notification>"}}`),
		[]byte(`{"type":"attachment","attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"<task-notification><summary>mid-turn task done</summary></task-notification>"}}`),
	}
	es := Entries(lines)
	if len(es) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(es), es)
	}
	if es[0].Kind != Note || !es[0].Cmd || es[0].Text != "build finished" {
		t.Fatalf("entry 0: %+v", es[0])
	}
	if es[1].Kind != Note || !es[1].Cmd || es[1].Text != "mid-turn task done" {
		t.Fatalf("entry 1: %+v", es[1])
	}
}

func TestBashAndCommandAndInternalXMLTags(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"user","message":{"content":"<bash-input>git status -s</bash-input>"}}`),
		[]byte(`{"type":"user","message":{"content":"<bash-stdout>M internal/daemon/bot.go</bash-stdout><bash-stderr></bash-stderr>"}}`),
		[]byte(`{"type":"user","message":{"content":"<command-message>running skill</command-message>\n<command-name>/grilling</command-name>\n<command-args>test args</command-args>"}}`),
		[]byte(`{"type":"user","message":{"content":"<local-command-stdout>skill output</local-command-stdout>"}}`),
		[]byte(`{"type":"user","message":{"content":"<ide_opened_file>foo.go</ide_opened_file><user-prompt-submit-hook>ok</user-prompt-submit-hook>"}}`),
	}
	es := Entries(lines)
	if len(es) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(es), es)
	}
	if !es[0].Term || es[0].Text != "git status -s" || es[0].Result != "M internal/daemon/bot.go" {
		t.Fatalf("bash entry: %+v", es[0])
	}
	if !es[1].Cmd || es[1].Text != "/grilling test args" || es[1].Result != "skill output" {
		t.Fatalf("command entry: %+v", es[1])
	}
}

func TestPeerSubagentAndCrossSessionXMLCleanup(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"user","isMeta":true,"origin":{"kind":"peer","from":"uds:/run/user/1000/cc-socks/1417052.sock","name":"ag-release-action","verifiedPeerPid":1417052},"message":{"content":"Another Claude session sent a message:\n<cross-session-message from=\"uds:/run/user/1000/cc-socks/1417052.sock\" from-name=\"ag-release-action\" from-mode=\"prompting\">\nShip v1.2.3 now\n</cross-session-message>\n\nThis came from another Claude session — not typed by your user"}}`),
		[]byte(`{"type":"user","isMeta":true,"origin":{"kind":"peer","from":"a31230851a90b9b17","name":"general-purpose","handback":true},"message":{"content":"Another Claude session sent a message:\n<agent-message from=\"a31230851a90b9b17\">\n[Subagent hand-back] The text below is the final report of a subagent this session delegated to. The report follows:\n  ## Summary\n  All tests passed.\n</agent-message>\n\nThat \"other Claude session\" is an agent working inside this same session"}}`),
		[]byte(`{"type":"attachment","attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"<task-notification>\n<task-id>a31230851a90b9b17</task-id>\n<tool-use-id>toolu_01Djbob2tVGyvAKg8BZWMdVM</tool-use-id>\n<output-file>/tmp/claude-1000/a31230851a90b9b17.output</output-file>\n<status>completed</status>\n<summary>Agent \"Build part 1\" finished</summary>\n<note>A task-notification fires each time this agent stops — that does NOT mean the agent is completely done</note>\n<result>## Summary\nAll tests passed.</result>\n<usage><subagent_tokens>263790</subagent_tokens><tool_uses>131</tool_uses><duration_ms>2580002</duration_ms></usage>\n</task-notification>"}}`),
		[]byte(`{"type":"user","origin":{"kind":"task-notification"},"message":{"content":"<task-notification>\n<task-id>a31230851a90b9b17</task-id>\n<tool-use-id>toolu_01Djbob2tVGyvAKg8BZWMdVM</tool-use-id>\n<output-file>/tmp/claude-1000/a31230851a90b9b17.output</output-file>\n<status>completed</status>\n<summary>Agent \"Build part 1\" finished</summary>\n<note>A task-notification fires each time this agent stops — that does NOT mean the agent is completely done</note>\n<result>## Summary\nAll tests passed.</result>\n<usage><subagent_tokens>263790</subagent_tokens><tool_uses>131</tool_uses><duration_ms>2580002</duration_ms></usage>\n</task-notification>"}}`),
	}
	es := Entries(lines)
	if len(es) != 3 {
		t.Fatalf("got %d entries, want 3 (deduplicated task-notification): %+v", len(es), es)
	}
	if es[0].Kind != Peer || es[0].Subagent || es[0].Text != "Ship v1.2.3 now" || es[0].PeerName != "ag-release-action" {
		t.Fatalf("cross-session peer: %+v", es[0])
	}
	if es[1].Kind != Peer || !es[1].Subagent || es[1].Text != "## Summary\nAll tests passed." || es[1].PeerName != "general-purpose" || es[1].PeerFrom != "a31230851a90b9b17" {
		t.Fatalf("subagent peer: %+v", es[1])
	}
	wantNote := `Agent "Build part 1" finished (✓ completed · 263.8k tok · 131 tools · 43m)`
	if es[2].Kind != Note || !es[2].Cmd || es[2].Text != wantNote || es[2].Result != "" {
		t.Fatalf("task notification: got Text=%q Result=%q, want Text=%q empty Result", es[2].Text, es[2].Result, wantNote)
	}
}

func TestMultiLineBashAndBashEditDiffAndAgentArgs(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t_bash","name":"Bash","input":{"command":"cd /work\npython3 - <<'EOF'\nprint(1)\nEOF","description":"Run script","timeout":300000,"run_in_background":true}}]}}`),
		[]byte(`{"type":"user","toolUseResult":{"stdout":"1\n","stderr":"","bashEditDiff":{"files":[{"filePath":"/work/a.py","created":false,"hunks":[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-old","+new"]}]}]}},"message":{"content":[{"type":"tool_result","tool_use_id":"t_bash","content":"1\n"}]}}`),
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t_agent","name":"Agent","input":{"description":"Audit repo","subagent_type":"general-purpose","model":"opus","run_in_background":true,"prompt":"Check everything\ncarefully"}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t_agent","content":"Async agent launched successfully.\nagentId: a12345 (This tool result is internal metadata — never quote or paste)" }]}}`),
	}
	es := Entries(lines)
	if len(es) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(es), es)
	}
	bash := es[0]
	if !strings.Contains(bash.Command, "python3 - <<'EOF'\nprint(1)\nEOF") {
		t.Fatalf("multi-line bash command truncated: %q", bash.Command)
	}
	if bash.Path != "/work/a.py" || bash.Plus != 1 || bash.Minus != 1 || !strings.Contains(bash.Diff, "+new") {
		t.Fatalf("bashEditDiff not extracted: path=%q +%d -%d diff=%q", bash.Path, bash.Plus, bash.Minus, bash.Diff)
	}
	if len(bash.Args) != 2 || bash.Args[0].Name != "Timeout" || bash.Args[0].Value != "5m" || bash.Args[1].Name != "Background" {
		t.Fatalf("bash args: %+v", bash.Args)
	}
	agent := es[1]
	if agent.Output != "" {
		t.Fatalf("agent boilerplate should be suppressed, got %q", agent.Output)
	}
	if len(agent.Args) != 4 || agent.Args[0].Value != "general-purpose" || !agent.Args[3].Block {
		t.Fatalf("agent args: %+v", agent.Args)
	}
}
