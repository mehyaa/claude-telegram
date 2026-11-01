package screen

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The fixtures are real captures from Claude Code 2.1.308.
func load(t *testing.T, name string) Screen {
	t.Helper()
	b, err := os.ReadFile("testdata/2.1.308/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return Parse(string(b))
}

func labels(d *Dialog) []string {
	var l []string
	for _, o := range d.Options {
		l = append(l, o.Label)
	}
	return l
}

func dialog(t *testing.T, name string, kind Kind) *Dialog {
	t.Helper()
	s := load(t, name)
	if s.Dialog == nil || s.Dialog.Kind != kind {
		t.Fatalf("%s: dialog %+v, want a %s dialog", name, s.Dialog, kind)
	}
	if s.Prompt != nil {
		t.Fatalf("%s: a prompt box under a dialog: %+v", name, s.Prompt)
	}
	return s.Dialog
}

func TestSingleQuestion(t *testing.T) {
	d := dialog(t, "q1-dialog", Question)
	if d.Heading != "Pick a colour" || d.Cursor != 1 || d.OnNext {
		t.Fatalf("heading %q cursor %d onNext %v", d.Heading, d.Cursor, d.OnNext)
	}
	if want := []Tab{{Label: "Colour"}}; !reflect.DeepEqual(d.Tabs, want) {
		t.Fatalf("tabs %+v", d.Tabs)
	}
	if want := []string{"Red", "Green", "Blue", "Type something.", "Chat about this"}; !reflect.DeepEqual(labels(d), want) {
		t.Fatalf("options %q", labels(d))
	}
	if d.Options[0].Desc != "Warm red" || d.Options[0].Box {
		t.Fatalf("option 1: %+v", d.Options[0])
	}
}

func TestSeveralQuestions(t *testing.T) {
	d := dialog(t, "q2-tab1-toggled", Question)
	if want := []Tab{{"Toppings", true}, {"Size", false}, {"Drink", false}}; !reflect.DeepEqual(d.Tabs, want) {
		t.Fatalf("tabs %+v", d.Tabs)
	}
	if d.Heading != "Which toppings?" || d.Cursor != 1 {
		t.Fatalf("heading %q cursor %d", d.Heading, d.Cursor)
	}
	var checked []bool
	for _, o := range d.Options {
		checked = append(checked, o.Checked)
	}
	if want := []bool{true, false, true, false, false}; !reflect.DeepEqual(checked, want) {
		t.Fatalf("checked %v (options %q)", checked, labels(d))
	}
	if !d.Options[0].Box || d.Options[4].Box || d.Options[3].Label != "Type something" {
		t.Fatalf("options %+v", d.Options)
	}

	d = dialog(t, "q2-tab2", Question)
	if d.Heading != "Which size?" || !reflect.DeepEqual(labels(d), []string{"Small", "Large", "Type something.", "Chat about this"}) {
		t.Fatalf("tab 2: %q %q", d.Heading, labels(d))
	}
}

func TestOtherText(t *testing.T) {
	d := dialog(t, "q2-other", Question)
	if d.Cursor != 3 || d.Options[2].Label != "Type something." || !strings.Contains(d.Hint, "ctrl+g") {
		t.Fatalf("cursor %d option 3 %q hint %q", d.Cursor, d.Options[2].Label, d.Hint)
	}
	d = dialog(t, "q2-other-typed", Question)
	if d.Cursor != 3 || d.Options[2].Label != "Lemonade, cold" {
		t.Fatalf("cursor %d option 3 %q", d.Cursor, d.Options[2].Label)
	}
	d = dialog(t, "q4-other-typed", Question)
	if d.Heading != "Which fruit?" || d.Options[2].Label != "Mango" || len(d.Tabs) != 1 {
		t.Fatalf("%q %q %+v", d.Heading, labels(d), d.Tabs)
	}
}

func TestMultiSelectNextRow(t *testing.T) {
	d := dialog(t, "q3-ms-cursor", Question)
	if d.Cursor != 3 || d.OnNext || d.Options[2].Label != "Type something" || !d.Options[2].Checked {
		t.Fatalf("cursor %d onNext %v option 3 %+v", d.Cursor, d.OnNext, d.Options[2])
	}
	d = dialog(t, "q3-ms-next", Question)
	if d.Cursor != 0 || !d.OnNext || d.Options[2].Label != "Hamster" {
		t.Fatalf("cursor %d onNext %v option 3 %+v", d.Cursor, d.OnNext, d.Options[2])
	}
	d = dialog(t, "q6-single-ms", Question)
	if want := []Tab{{Label: "Days"}}; !reflect.DeepEqual(d.Tabs, want) || d.Heading != "Which days?" {
		t.Fatalf("tabs %+v heading %q", d.Tabs, d.Heading)
	}
}

func TestReview(t *testing.T) {
	d := dialog(t, "q2-submit", Review)
	if want := []string{"● Which toppings?", "→ Cheese, Basil", "● Which size?", "→ Large", "● Which drink?", "→ Lemonade, cold"}; !reflect.DeepEqual(d.Body, want) {
		t.Fatalf("body %q", d.Body)
	}
	if !reflect.DeepEqual(labels(d), []string{"Submit answers", "Cancel"}) || d.Cursor != 1 {
		t.Fatalf("options %q cursor %d", labels(d), d.Cursor)
	}
	// Under this one the prompt box's frame is drawn; it isn't an option.
	d = dialog(t, "q6-after-tab", Review)
	if !reflect.DeepEqual(labels(d), []string{"Submit answers", "Cancel"}) || !d.Tabs[0].Done {
		t.Fatalf("options %q tabs %+v", labels(d), d.Tabs)
	}
}

func TestPermission(t *testing.T) {
	d := dialog(t, "perm-during-hook", Permission)
	if d.Heading != "Bash command" || d.Ask != "Do you want to proceed?" || d.Cursor != 1 {
		t.Fatalf("heading %q ask %q cursor %d", d.Heading, d.Ask, d.Cursor)
	}
	if !strings.Contains(strings.Join(d.Body, "\n"), "touch probe-file.txt") {
		t.Fatalf("body %q", d.Body)
	}
	if l := labels(d); len(l) != 4 || l[0] != "Yes" || l[3] != "No" {
		t.Fatalf("options %q", l)
	}

	d = dialog(t, "write-dialog", Permission)
	if d.Heading != "Create file" || d.Ask != "Do you want to create notes.txt?" || len(d.Options) != 3 || d.Options[2].Label != "No" {
		t.Fatalf("heading %q ask %q options %q", d.Heading, d.Ask, labels(d))
	}

	d = dialog(t, "perm5-tab-on-no", Permission)
	if d.Cursor != 4 || d.Options[3].Label != "No, and tell Claude what to do differently" {
		t.Fatalf("cursor %d option 4 %q", d.Cursor, d.Options[3].Label)
	}
	d = dialog(t, "perm5-reason-typed", Permission)
	if d.Options[3].Label != "No, not now, probe says no" {
		t.Fatalf("option 4 %q", d.Options[3].Label)
	}
}

// An MCP tool's prompt, first seen in the step-3 live probe.
func TestMCPToolPermission(t *testing.T) {
	d := dialog(t, "perm-mcp-tool", Permission)
	if d.Heading != "Tool use" || d.Cursor != 1 || len(d.Options) != 3 || d.Options[0].Label != "Yes" || d.Options[2].Label != "No" {
		t.Fatalf("heading %q cursor %d options %q", d.Heading, d.Cursor, labels(d))
	}
	if !strings.Contains(d.Text(), `question: "Use SQLite or Postgres for the cache?"`) {
		t.Fatalf("body %q", d.Body)
	}
}

func TestPlan(t *testing.T) {
	d := dialog(t, "plan-dialog3", Plan)
	if want := []string{"Yes, and use auto mode", "Yes, manually approve edits", "Tell Claude what to change"}; !reflect.DeepEqual(labels(d), want) {
		t.Fatalf("options %q", labels(d))
	}
	if d.Cursor != 1 || len(d.Body) == 0 || d.Body[0] != "Plan: create plan.txt" {
		t.Fatalf("cursor %d body %q", d.Cursor, d.Body)
	}
	d = dialog(t, "plan-3-typed", Plan)
	if d.Cursor != 3 || d.Options[2].Label != "Use two steps, not three" {
		t.Fatalf("cursor %d option 3 %q", d.Cursor, d.Options[2].Label)
	}
}

const longQuestion = "Phase 22's console needs a dedicated status line so operators can see which shard is draining, how many replicas still hold the old schema, and whether the cutover window is still open, without scrolling. Which layout should we ship?"

func TestLongQuestion(t *testing.T) {
	d := dialog(t, "q-long", Question)
	if d.Heading != longQuestion {
		t.Fatalf("heading %q", d.Heading)
	}
	if want := []string{"Compact", "Full", "Split", "Type something.", "Chat about this"}; !reflect.DeepEqual(labels(d), want) {
		t.Fatalf("options %q", labels(d))
	}
}

func TestParagraphQuestion(t *testing.T) {
	d := dialog(t, "q-paragraphs", Question)
	want := "The cutover has two remaining risks. The replica lag on shard B is still above the threshold we set last week. Which action should we take?"
	if d.Heading != want {
		t.Fatalf("heading %q", d.Heading)
	}
}

func TestWrappedLabels(t *testing.T) {
	d := dialog(t, "q-wrap", Question)
	if d.Heading != "Which label wraps?" {
		t.Fatalf("heading %q", d.Heading)
	}
	if !strings.HasPrefix(d.Options[0].Label, "Ship the compact status line") {
		t.Fatalf("option 1 %q", d.Options[0].Label)
	}
	if strings.Contains(d.Options[0].Label, "│") {
		t.Fatalf("gutter leaked into a label: %q", d.Options[0].Label)
	}
}

func TestPreviewPane(t *testing.T) {
	d := dialog(t, "q-preview", Question)
	if !d.Preview || d.Heading != "Which mockup?" {
		t.Fatalf("preview %v heading %q", d.Preview, d.Heading)
	}
}

func TestShortThenLongSecond(t *testing.T) {
	d := dialog(t, "q-short-then-long-q2", Question)
	if len(d.Tabs) != 2 || !d.Tabs[0].Done || d.Tabs[1].Done {
		t.Fatalf("tabs %+v", d.Tabs)
	}
	if d.Heading != longQuestion {
		t.Fatalf("heading %q", d.Heading)
	}
}

func TestPromptBox(t *testing.T) {
	for name, want := range map[string]Prompt{
		"q1-done":      {},
		"q5-queued":    {Queued: true},
		"prompt-typed": {Text: "Reply with just the two words you see on the next two lines, joined by a space.\nalpha\nbravo"},
	} {
		s := load(t, name)
		if s.Dialog != nil || s.Prompt == nil || *s.Prompt != want {
			t.Errorf("%s: dialog %+v prompt %+v, want prompt %+v", name, s.Dialog, s.Prompt, want)
		}
	}
	if s := load(t, "q5-queued"); !s.Busy {
		t.Error("q5-queued: a running turn should read as busy")
	}
	if s := load(t, "q1-done"); s.Busy {
		t.Error("q1-done: an idle session read as busy")
	}
	// While PreToolUse runs there's no dialog yet, only the prompt box.
	if s := load(t, "q1-wait"); s.Dialog != nil || s.Prompt == nil || !s.Busy {
		t.Errorf("q1-wait: %+v", s)
	}
}
