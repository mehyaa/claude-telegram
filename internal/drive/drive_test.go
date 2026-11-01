package drive

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/tmux"
)

var stubBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stub")
	if err != nil {
		panic(err)
	}
	stubBin = filepath.Join(dir, "stubtui")
	if out, err := exec.Command("go", "build", "-o", stubBin, "./stubtui").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type stub struct {
	t    *testing.T
	pane tmux.Pane
	log  string
}

// startStub runs stubtui in a tmux server of its own, so the test never
// touches any other server.
func startStub(t *testing.T, mode string, spec any) *stub {
	t.Helper()
	if err := tmux.Check(); err != nil {
		t.Skip(err)
	}
	dir, err := os.MkdirTemp("", "drv")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "tmux.sock")
	log := filepath.Join(dir, "stub.jsonl")
	b, _ := json.Marshal(spec)
	tm := func(args ...string) ([]byte, error) {
		cmd := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "TMUX=")
		return cmd.CombinedOutput()
	}
	if out, err := tm("new-session", "-d", "-x", "120", "-y", "45", "-s", "stub", stubBin, log, mode, string(b)); err != nil {
		t.Fatalf("tmux new-session: %v %s", err, out)
	}
	t.Cleanup(func() {
		tm("kill-server")
		os.RemoveAll(dir)
	})
	out, err := tm("display-message", "-p", "-t", "stub", "#{pane_id}")
	if err != nil {
		t.Fatalf("tmux display-message: %v %s", err, out)
	}
	s := &stub{t: t, pane: tmux.Pane{Socket: sock, ID: strings.TrimSpace(string(out))}, log: log}
	// Wait for the first frame.
	deadline := time.Now().Add(3 * time.Second)
	for {
		text, _ := s.pane.Capture()
		if strings.TrimSpace(text) != "" {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("stub drew nothing")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *stub) driver() *Driver {
	return &Driver{T: s.pane, Wait: 2 * time.Second, Poll: 30 * time.Millisecond}
}

func (s *stub) lines() []map[string]any {
	f, err := os.Open(s.log)
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

// outcome waits for the stub to log how its dialog ended.
func (s *stub) outcome() map[string]any {
	s.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, l := range s.lines() {
			if l["done"] == true {
				return l
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatalf("the stub's dialog didn't end; log: %v", s.lines())
	return nil
}

// keys lists the keys the stub received.
func (s *stub) keys() []string {
	var k []string
	for _, l := range s.lines() {
		if key, ok := l["key"].(string); ok {
			k = append(k, key)
		}
	}
	return k
}

func opts(labels ...string) []map[string]string {
	var o []map[string]string
	for _, l := range labels {
		o = append(o, map[string]string{"label": l, "description": "about " + l})
	}
	return o
}

type spec struct {
	Questions []map[string]any `json:"questions"`
}

func q(question, header string, multi bool, labels ...string) map[string]any {
	return map[string]any{"question": question, "header": header, "multiSelect": multi, "options": opts(labels...)}
}

func questions(sp spec) []Question {
	var qs []Question
	for _, m := range sp.Questions {
		qq := Question{Question: m["question"].(string), Header: m["header"].(string), MultiSelect: m["multiSelect"].(bool)}
		for _, o := range m["options"].([]map[string]string) {
			qq.Options = append(qq.Options, o["label"])
		}
		qs = append(qs, qq)
	}
	return qs
}

func answers(t *testing.T, out map[string]any) map[string]string {
	t.Helper()
	raw, ok := out["answers"].(map[string]any)
	if !ok {
		t.Fatalf("no answers: %v", out)
	}
	m := map[string]string{}
	for k, v := range raw {
		m[k] = v.(string)
	}
	return m
}

func TestAnswerQuestions(t *testing.T) {
	colours := spec{[]map[string]any{q("Pick a colour", "Colour", false, "Red", "Green", "Blue")}}
	pizza := spec{[]map[string]any{
		q("Which toppings?", "Toppings", true, "Cheese", "Olives", "Basil"),
		q("Which size?", "Size", false, "Small", "Large"),
		q("Which drink?", "Drink", false, "Water", "Juice"),
	}}
	pets := spec{[]map[string]any{
		q("Which pets?", "Pets", true, "Cat", "Dog"),
		q("Pet name?", "Name", false, "Rex", "Tom"),
	}}
	days := spec{[]map[string]any{q("Which days?", "Days", true, "Mon", "Tue", "Wed")}}

	for _, tc := range []struct {
		name  string
		spec  spec
		picks []Pick
		want  map[string]string
	}{
		{"single choice", colours, []Pick{{Options: []int{2}}}, map[string]string{"Pick a colour": "Green"}},
		{"single Other with digits", colours, []Pick{{Other: "2 pears or 1 plum"}}, map[string]string{"Pick a colour": "2 pears or 1 plum"}},
		{"three questions", pizza, []Pick{{Options: []int{1, 3}}, {Options: []int{2}}, {Other: "Lemonade, cold"}},
			map[string]string{"Which toppings?": "Cheese, Basil", "Which size?": "Large", "Which drink?": "Lemonade, cold"}},
		{"multi-select with Other", pets, []Pick{{Options: []int{2}, Other: "Hamster"}, {Options: []int{2}}},
			map[string]string{"Which pets?": "Dog, Hamster", "Pet name?": "Tom"}},
		{"multi-select alone", days, []Pick{{Options: []int{1, 3}}}, map[string]string{"Which days?": "Mon, Wed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startStub(t, "question", tc.spec)
			if err := s.driver().Answer(questions(tc.spec), tc.picks); err != nil {
				t.Fatalf("Answer: %v (keys %q)", err, s.keys())
			}
			if got := answers(t, s.outcome()); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("answers %v, want %v (keys %q)", got, tc.want, s.keys())
			}
		})
	}
}

func TestQuestionStartedInTerminal(t *testing.T) {
	sp := spec{[]map[string]any{q("Which size?", "Size", false, "Small", "Large"), q("Which drink?", "Drink", false, "Water", "Juice")}}
	s := startStub(t, "question", sp)
	s.pane.Keys("Down") // someone at the terminal moved the cursor
	time.Sleep(100 * time.Millisecond)
	err := s.driver().Answer(questions(sp), []Pick{{Options: []int{1}}, {Options: []int{1}}})
	if !errors.Is(err, ErrStarted) {
		t.Fatalf("got %v, want ErrStarted", err)
	}
	if k := s.keys(); !reflect.DeepEqual(k, []string{"Down"}) {
		t.Fatalf("the driver pressed keys anyway: %q", k)
	}
}

func TestSameLabel(t *testing.T) {
	for _, tc := range []struct {
		on, want string
		ok       bool
	}{
		{"Green", "Green", true},
		{"foo…", "foo bar baz", true},
		{"Ship the compact status line now and revisit the layout after the cutover window closes, keeping every field",
			"Ship the compact status line now and revisit the layout after the cutover window closes, keeping every field abbreviated so it fits on one row of the console", true},
		{"Red", "Reddish", false},
		{"Green", "Blue", false},
		{"", "Green", false},
	} {
		if got := sameLabel(tc.on, tc.want); got != tc.ok {
			t.Errorf("sameLabel(%q, %q) = %v, want %v", tc.on, tc.want, got, tc.ok)
		}
	}
	long := strings.Repeat("a", 2000) + " more"
	if !sameHeading(strings.Repeat("a", 2000)+"…", long) {
		t.Error("sameHeading should accept a 2000-character cut")
	}
	if sameHeading("Pick", "Pick a colour") {
		t.Error("sameHeading should not treat a short prefix as a cut")
	}
}

const longQ = "Phase 22's console needs a dedicated status line so operators can see which shard is draining, how many replicas still hold the old schema, and whether the cutover window is still open, without scrolling. Which layout should we ship?"

func TestAnswerLongAndWrappedQuestions(t *testing.T) {
	para := "The cutover has two remaining risks.\n\nThe replica lag on shard B is still above the threshold we set last week. Which action should we take?"
	wrap := "Ship the compact status line now and revisit the layout after the cutover window closes, keeping every field abbreviated so it fits on one row of the console"
	for _, tc := range []struct {
		name  string
		spec  spec
		picks []Pick
		want  map[string]string
	}{
		{"long", spec{[]map[string]any{q(longQ, "Layout", false, "Compact", "Full", "Split")}},
			[]Pick{{Options: []int{1}}}, map[string]string{longQ: "Compact"}},
		{"paragraphs", spec{[]map[string]any{q(para, "Action", false, "Wait", "Abort", "Proceed")}},
			[]Pick{{Options: []int{2}}}, map[string]string{para: "Abort"}},
		{"wrapped label", spec{[]map[string]any{q("Which label wraps?", "Wrap", false, wrap, "Short")}},
			[]Pick{{Options: []int{1}}}, map[string]string{"Which label wraps?": wrap}},
		{"short then long", spec{[]map[string]any{
			q("Pick a colour", "Colour", false, "Red", "Green", "Blue"),
			q(longQ, "Layout", false, "Compact", "Full", "Split"),
		}}, []Pick{{Options: []int{1}}, {Options: []int{3}}},
			map[string]string{"Pick a colour": "Red", longQ: "Split"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startStub(t, "question", tc.spec)
			if err := s.driver().Answer(questions(tc.spec), tc.picks); err != nil {
				t.Fatalf("Answer: %v (keys %q)", err, s.keys())
			}
			if got := answers(t, s.outcome()); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("answers %v, want %v (keys %q)", got, tc.want, s.keys())
			}
		})
	}
}

func TestTruncatedQuestion(t *testing.T) {
	full := strings.Repeat("word ", 500) + "end?"
	sp := spec{[]map[string]any{q(full, "Cut", false, "Yes", "No")}}
	s := startStub(t, "question", sp)
	if err := s.driver().Answer(questions(sp), []Pick{{Options: []int{1}}}); err != nil {
		t.Fatalf("Answer: %v (keys %q)", err, s.keys())
	}
	if got := answers(t, s.outcome()); got[full] != "Yes" {
		t.Fatalf("answers %v (keys %q)", got, s.keys())
	}
}

func TestPreviewRefusesWithoutKeys(t *testing.T) {
	sp := spec{[]map[string]any{{
		"question": "Which mockup?", "header": "Mockup", "multiSelect": false,
		"options": []map[string]string{
			{"label": "Alpha", "description": "A", "preview": "```\nalpha\n```"},
			{"label": "Beta", "description": "B", "preview": "```\nbeta\n```"},
		},
	}}}
	s := startStub(t, "question", sp)
	dr := s.driver()
	dr.Wait = 300 * time.Millisecond
	err := dr.Answer(questions(sp), []Pick{{Options: []int{1}}})
	var me *MismatchError
	if !errors.As(err, &me) || me.Sent || !strings.Contains(err.Error(), "preview pane") {
		t.Fatalf("got %v", err)
	}
	if k := s.keys(); len(k) != 0 {
		t.Fatalf("keys pressed on a preview: %q", k)
	}
}

func TestQuestionMismatchAfterFirstAnswer(t *testing.T) {
	sp := spec{[]map[string]any{
		q("Pick a colour", "Colour", false, "Red", "Green"),
		q("Which size?", "Size", false, "Small", "Large"),
	}}
	s := startStub(t, "question", sp)
	expected := questions(sp)
	expected[1].Question = "Which drink?"
	dr := s.driver()
	dr.Wait = 300 * time.Millisecond
	err := dr.Answer(expected, []Pick{{Options: []int{1}}, {Options: []int{1}}})
	var me *MismatchError
	if !errors.As(err, &me) || !me.Sent || !reflect.DeepEqual(me.Done, []string{"Colour"}) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "another question") {
		t.Fatalf("got %v", err)
	}
}

func TestQuestionMismatchPressesNothing(t *testing.T) {
	sp := spec{[]map[string]any{q("Pick a colour", "Colour", false, "Red", "Green")}}
	s := startStub(t, "question", sp)
	expected := questions(sp)
	expected[0].Question = "Pick a color"
	dr := s.driver()
	dr.Wait = 300 * time.Millisecond
	err := dr.Answer(expected, []Pick{{Options: []int{1}}})
	var me *MismatchError
	if !errors.As(err, &me) || me.Sent || !strings.Contains(err.Error(), `another question ("Pick a colour")`) {
		t.Fatalf("got %v", err)
	}
	if k := s.keys(); len(k) != 0 {
		t.Fatalf("keys pressed on a mismatch: %q", k)
	}
}

var bashPrompt = map[string]any{
	"title":   "Bash command",
	"body":    []string{"touch probe-file.txt"},
	"options": []string{"Yes", "Yes, and always allow access to /work from this project", "Yes, and switch to auto mode", "No"},
}

func TestPermission(t *testing.T) {
	t.Run("allow", func(t *testing.T) {
		s := startStub(t, "permission", bashPrompt)
		if err := s.driver().Allow(Permission{Needle: "touch probe-file.txt"}); err != nil {
			t.Fatal(err)
		}
		if out := s.outcome(); out["permission"] != "allow" {
			t.Fatalf("outcome %v", out)
		}
	})
	t.Run("deny", func(t *testing.T) {
		s := startStub(t, "permission", bashPrompt)
		if err := s.driver().Deny(Permission{Needle: "touch"}, ""); err != nil {
			t.Fatal(err)
		}
		if out := s.outcome(); out["permission"] != "deny" || out["reason"] != nil {
			t.Fatalf("outcome %v", out)
		}
	})
	t.Run("deny with reason", func(t *testing.T) {
		s := startStub(t, "permission", map[string]any{"title": "Create file", "body": []string{"notes.txt"}, "options": []string{"Yes", "Yes, and switch to accept edits", "No"}})
		if err := s.driver().Deny(Permission{Needle: "notes.txt"}, "not now, use /tmp"); err != nil {
			t.Fatalf("%v (keys %q)", err, s.keys())
		}
		if out := s.outcome(); out["permission"] != "deny" || out["reason"] != "not now, use /tmp" {
			t.Fatalf("outcome %v", out)
		}
	})
	t.Run("another command", func(t *testing.T) {
		s := startStub(t, "permission", bashPrompt)
		dr := s.driver()
		dr.Wait = 300 * time.Millisecond
		if err := dr.Allow(Permission{Needle: "rm -rf"}); err == nil || !strings.Contains(err.Error(), "for something else") {
			t.Fatalf("got %v", err)
		}
		if k := s.keys(); len(k) != 0 {
			t.Fatalf("keys pressed: %q", k)
		}
	})
}

func TestPlan(t *testing.T) {
	plan := map[string]any{"plan": "Plan: create plan.txt\n1. Write it\n2. Say so"}
	t.Run("approve", func(t *testing.T) {
		s := startStub(t, "plan", plan)
		if err := s.driver().Approve(); err != nil {
			t.Fatal(err)
		}
		// Q33: approve keeps manual approval of edits.
		if out := s.outcome(); out["plan"] != "manual" {
			t.Fatalf("outcome %v", out)
		}
	})
	t.Run("change", func(t *testing.T) {
		s := startStub(t, "plan", plan)
		if err := s.driver().Change("Use two steps, not three"); err != nil {
			t.Fatalf("%v (keys %q)", err, s.keys())
		}
		if out := s.outcome(); out["plan"] != "change" || out["text"] != "Use two steps, not three" {
			t.Fatalf("outcome %v", out)
		}
	})
}

func TestType(t *testing.T) {
	t.Run("one line", func(t *testing.T) {
		s := startStub(t, "prompt", nil)
		if err := s.driver().Type("[answer to 2.3] go with b"); err != nil {
			t.Fatal(err)
		}
		if out := s.outcome(); out["prompt"] != "[answer to 2.3] go with b" {
			t.Fatalf("outcome %v", out)
		}
	})
	t.Run("several lines", func(t *testing.T) {
		s := startStub(t, "prompt", nil)
		if err := s.driver().Type("first line\nsecond line"); err != nil {
			t.Fatal(err)
		}
		if out := s.outcome(); out["prompt"] != "first line\nsecond line" {
			t.Fatalf("outcome %v", out)
		}
	})
	t.Run("someone is typing", func(t *testing.T) {
		s := startStub(t, "prompt", nil)
		s.pane.Literal("half a thought")
		dr := s.driver()
		dr.Wait = 300 * time.Millisecond
		err := dr.Type("hello")
		if err == nil || !strings.Contains(err.Error(), "text being typed in the prompt") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a dialog is open", func(t *testing.T) {
		s := startStub(t, "permission", bashPrompt)
		dr := s.driver()
		dr.Wait = 300 * time.Millisecond
		if err := dr.Type("hello"); err == nil || !strings.Contains(err.Error(), "a permission dialog") {
			t.Fatalf("got %v", err)
		}
		if k := s.keys(); len(k) != 0 {
			t.Fatalf("keys pressed: %q", k)
		}
	})
}

func TestInterruptThenType(t *testing.T) {
	s := startStub(t, "busy", nil)
	dr := s.driver()
	if err := dr.Interrupt(); err != nil {
		t.Fatal(err)
	}
	// Typed straight after Esc, the first letter would turn into Alt+N.
	if err := dr.Type("Never mind, say hi"); err != nil {
		t.Fatalf("%v (keys %q)", err, s.keys())
	}
	if out := s.outcome(); out["interrupted"] != true {
		t.Fatalf("first outcome %v", out)
	}
	var prompts []any
	for _, l := range s.lines() {
		if p, ok := l["prompt"]; ok {
			prompts = append(prompts, p)
		}
	}
	if len(prompts) != 1 || prompts[0] != "Never mind, say hi" {
		t.Fatalf("prompts %v (keys %q)", prompts, s.keys())
	}
}
