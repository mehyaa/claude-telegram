package install

import (
	"strings"
	"testing"

	"github.com/mehyaa/claude-telegram/contrib"
)

const otherSettings = `{"theme": "dark",
 "hooks": {"Stop": [
   {"hooks": [{"type": "command", "command": "notify-send done"}]},
   {"hooks": [{"type": "command", "command": "/old/path/ctg hook Stop"}]}]}}`

func TestMergeAndRemoveHooks(t *testing.T) {
	bin := "/home/user/.local/bin/ctg"
	merged, err := mergeHooks([]byte(otherSettings), contrib.Hooks, bin)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := decodeObject(merged)
	if err != nil {
		t.Fatal(err)
	}
	if cur["theme"] != "dark" {
		t.Fatalf("theme = %v; want dark", cur["theme"])
	}
	hooks, _ := cur["hooks"].(map[string]any)
	stop, _ := hooks["Stop"].([]any)
	if !hasCommand(stop, "notify-send done") {
		t.Fatal("someone else's hook lost")
	}
	if hasCommand(stop, "/old/path") {
		t.Fatal("old ctg hook kept")
	}
	want := `"` + bin + `" hook PreToolUse`
	pre, _ := hooks["PreToolUse"].([]any)
	if !hasCommand(pre, want) {
		t.Fatalf("PreToolUse command missing %q in %#v", want, pre)
	}

	again, err := mergeHooks(merged, contrib.Hooks, bin)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ = decodeObject(again)
	hooks, _ = cur["hooks"].(map[string]any)
	n := 0
	for _, v := range hooks {
		entries, _ := v.([]any)
		for _, e := range entries {
			if isCTGEntry(e) {
				n++
			}
		}
	}
	if n != 7 {
		t.Fatalf("%d ctg hook entries after merging twice, want 7", n)
	}

	stripped, err := removeHooks(again)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ = decodeObject(stripped)
	hooks, _ = cur["hooks"].(map[string]any)
	stop, _ = hooks["Stop"].([]any)
	if len(hooks) != 1 || len(stop) != 1 || !hasCommand(stop, "notify-send done") {
		t.Fatalf("hooks after remove: %s", stripped)
	}
	if strings.Contains(string(stripped), "ctg hook") {
		t.Fatalf("ctg hook left: %s", stripped)
	}
}

func hasCommand(entries []any, substr string) bool {
	for _, e := range entries {
		m, _ := e.(map[string]any)
		hooks, _ := m["hooks"].([]any)
		for _, h := range hooks {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, substr) {
				return true
			}
		}
	}
	return false
}

func TestMergeHooksInvalidJSON(t *testing.T) {
	if _, err := mergeHooks([]byte("{"), contrib.Hooks, "/bin/ctg"); err == nil {
		t.Fatal("wanted an error")
	}
}
