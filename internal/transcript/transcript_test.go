package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The fixture is the step-1 probe's transcript, without the API payloads.
func fixture(t *testing.T) []byte {
	b, err := os.ReadFile("testdata/probe-2.1.308.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProbeTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, fixture(t), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := NewTail(path, false).Poll()
	if err != nil {
		t.Fatal(err)
	}
	uses := map[string]string{} // tool ID → name
	var interrupts, turns int
	var pizza map[string]string
	var petsRejected bool
	for _, e := range events {
		switch e.Kind {
		case ToolUse:
			uses[e.ToolID] = e.ToolName
		case Interrupted:
			interrupts++
		case TurnEnd:
			turns++
		case ToolResult:
			if _, ok := e.Answers["Which toppings?"]; ok {
				pizza = e.Answers
			}
			if uses[e.ToolID] == "AskUserQuestion" && e.Rejected && e.IsError && !petsRejected {
				petsRejected = true
			}
		}
	}
	// Step 1 interrupted four turns: the cancelled Pets dialog, a deny by
	// digit, Esc during a running tool, and Esc on a question.
	if interrupts != 4 {
		t.Errorf("%d interrupts, want 4", interrupts)
	}
	if turns == 0 {
		t.Error("no turn ends")
	}
	if want := map[string]string{"Which toppings?": "Cheese, Basil", "Which size?": "Large", "Which drink?": "Lemonade, cold"}; !reflect.DeepEqual(pizza, want) {
		t.Errorf("pizza answers %v", pizza)
	}
	if !petsRejected {
		t.Error("the cancelled question wasn't reported as rejected")
	}
	var names []string
	for _, n := range uses {
		names = append(names, n)
	}
	for _, want := range []string{"AskUserQuestion", "Bash", "Write", "ExitPlanMode"} {
		found := false
		for _, n := range names {
			found = found || n == want
		}
		if !found {
			t.Errorf("no %s tool use among %v", want, names)
		}
	}
}

func TestTailFollowsGrowth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	old := `{"type":"system","subtype":"turn_duration"}` + "\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	tail := NewTail(path, true) // skip what is there
	use, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "ls"}},
	}}})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Half a line is held back until the rest arrives.
	f.Write(use[:10])
	if ev, err := tail.Poll(); err != nil || len(ev) != 0 {
		t.Fatalf("half a line: %v %v", ev, err)
	}
	f.Write(append(use[10:], '\n'))
	ev, err := tail.Poll()
	if err != nil || len(ev) != 1 || ev[0].Kind != ToolUse || ev[0].ToolID != "toolu_1" || string(ev[0].Input) != `{"command":"ls"}` {
		t.Fatalf("after the rest: %+v %v", ev, err)
	}
	if ev, _ := tail.Poll(); len(ev) != 0 {
		t.Fatalf("polled the same lines twice: %+v", ev)
	}

	// A reader resuming at Offset sees what comes next, a half line too.
	f.Write([]byte(`{"type":"system","sub`))
	tail.Poll()
	resumed := NewTailAt(path, tail.Offset())
	f.Write([]byte(`type":"turn_duration"}` + "\n"))
	if ev, _ := resumed.Poll(); len(ev) != 1 || ev[0].Kind != TurnEnd {
		t.Fatalf("resumed: %+v", ev)
	}
}

func TestMissingFileAppearsLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	tail := NewTail(path, true)
	if ev, err := tail.Poll(); err != nil || ev != nil {
		t.Fatalf("missing file: %v %v", ev, err)
	}
	os.WriteFile(path, []byte(`{"type":"system","subtype":"turn_duration"}`+"\n"), 0o600)
	if ev, _ := tail.Poll(); len(ev) != 1 || ev[0].Kind != TurnEnd {
		t.Fatalf("new file read from its start: %+v", ev)
	}
}

func TestBackgroundTaskEvents(t *testing.T) {
	shell := Parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]},"toolUseResult":{"backgroundTaskId":"b123"}}`))
	if len(shell) != 1 || shell[0].StartID != "b123" || shell[0].StartKind != "shell" {
		t.Fatalf("shell: %+v", shell)
	}
	mon := Parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1m"}]},"toolUseResult":{"monitorId":"m123"}}`))
	if len(mon) != 1 || mon[0].StartID != "m123" || mon[0].StartKind != "monitor" {
		t.Fatalf("monitor: %+v", mon)
	}
	agent := Parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2"}]},"toolUseResult":{"status":"async_launched","agentId":"ag1"}}`))
	if len(agent) != 1 || agent[0].StartID != "ag1" || agent[0].StartKind != "agent" {
		t.Fatalf("agent: %+v", agent)
	}
	end := Parse([]byte(`{"type":"user","message":{"content":"<task-notification>\n<task-id>b123</task-id>\n</task-notification>"}}`))
	if len(end) != 1 || end[0].Kind != TaskEnd || end[0].EndedID != "b123" {
		t.Fatalf("notification: %+v", end)
	}
	stop := Parse([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"TaskStop","input":{"task_id":"b123"}}]}}`))
	if len(stop) != 1 || stop[0].StopID != "b123" {
		t.Fatalf("stop: %+v", stop)
	}
}
