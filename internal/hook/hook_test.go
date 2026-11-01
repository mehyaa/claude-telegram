package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/config"
	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/transcript"
)

// The fixtures are real hook inputs from the step-1 probe.
func input(t *testing.T, name string) Input {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var in Input
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func env(m map[string]string) Env { return func(k string) string { return m[k] } }

func one(t *testing.T, reqs []ipc.Request) ipc.Request {
	t.Helper()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1: %+v", len(reqs), reqs)
	}
	return reqs[0]
}

func TestSessionStart(t *testing.T) {
	r := one(t, Requests("SessionStart", input(t, "SessionStart"), env(map[string]string{
		"CLAUDE_PID":                   "0",
		"TMUX":                         "/tmp/tmux-1000/default,1234,0",
		"TMUX_PANE":                    "%7",
		"CLAUDE_CODE_MESSAGING_SOCKET": "/run/user/1000/cc-socks/1.sock",
	})))
	s := r.Session
	if r.Op != ipc.OpRegister || s.ID != "677a85c1-f7da-4a4c-af58-cba3bf36004e" || s.Pane != "%7" ||
		s.TmuxSocket != "/tmp/tmux-1000/default" || s.Mailbox != "/run/user/1000/cc-socks/1.sock" ||
		!strings.HasSuffix(s.Transcript, ".jsonl") || !strings.HasSuffix(s.Cwd, "/.scratch/main") {
		t.Fatalf("register: %+v", s)
	}
	for _, bad := range []string{"", "   ", "."} {
		in := input(t, "SessionStart")
		in.Cwd = bad
		if reqs := Requests("SessionStart", in, env(nil)); len(reqs) != 0 {
			t.Fatalf("SessionStart with cwd %q registered: %+v", bad, reqs)
		}
	}
	if comm, ppid := procParent(os.Getpid()); comm == "" || ppid <= 0 {
		t.Fatalf("procParent(self) = %q, %d", comm, ppid)
	}
}

func TestUnattendedSessionsDontJoin(t *testing.T) {
	// claude -p, as checked before step 3 was finished.
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		if reqs := Requests(ev, input(t, ev), env(map[string]string{"CLAUDE_CODE_SESSION_ATTENDED": "0"})); len(reqs) != 0 {
			t.Errorf("%s from claude -p: %+v", ev, reqs)
		}
	}
	if reqs := Requests("SessionStart", input(t, "SessionStart"), env(map[string]string{"CLAUDE_CODE_SESSION_ATTENDED": "1"})); len(reqs) != 1 {
		t.Errorf("attended session didn't register: %+v", reqs)
	}
}

func TestVersionFromExecutable(t *testing.T) {
	// This test's own process isn't Claude Code, so there is no version.
	if v := version(os.Getpid()); v != "" {
		t.Fatalf("version of the test binary = %q", v)
	}
	if !versionRE.MatchString("2.1.308") || versionRE.MatchString("go") {
		t.Fatal("versionRE")
	}
}

func TestSessionEndSkipsClear(t *testing.T) {
	in := input(t, "SessionEnd")
	if r := one(t, Requests("SessionEnd", in, env(nil))); r.Op != ipc.OpEnd || r.SessionID != in.SessionID {
		t.Fatalf("%+v", r)
	}
	in.Reason = "clear"
	if reqs := Requests("SessionEnd", in, env(nil)); len(reqs) != 0 {
		t.Fatalf("/clear ended the session: %+v", reqs)
	}
}

func TestPromptAndStop(t *testing.T) {
	r := one(t, Requests("UserPromptSubmit", input(t, "UserPromptSubmit"), env(nil)))
	if r.Op != ipc.OpUpdate || r.State != ipc.StateBusy || !strings.HasPrefix(r.Prompt, "Call the AskUserQuestion tool") || len([]rune(r.Prompt)) > promptKept {
		t.Fatalf("%+v", r)
	}
	in := input(t, "UserPromptSubmit")
	in.SessionTitle = "myapp-build"
	if r := one(t, Requests("UserPromptSubmit", in, env(nil))); r.Name == nil || *r.Name != "myapp-build" {
		t.Fatalf("name not passed on: %+v", r)
	}
	r = one(t, Requests("Stop", input(t, "Stop"), env(nil)))
	if r.State != ipc.StateIdle || r.Reply != "Green" || r.Tasks == nil || len(*r.Tasks) != 0 || r.Crons == nil || *r.Crons != 0 {
		t.Fatalf("%+v", r)
	}
	in = input(t, "Stop")
	in.Background = []ipc.Task{{ID: "b1234567", Type: "shell"}, {ID: "ag1", Type: "subagent"}}
	in.Crons = []json.RawMessage{[]byte(`{"id":"c1"}`)}
	r = one(t, Requests("Stop", in, env(nil)))
	if r.Tasks == nil || len(*r.Tasks) != 2 || (*r.Tasks)[0].Type != "shell" || r.Crons == nil || *r.Crons != 1 {
		t.Fatalf("stop tasks: %+v crons %v", r.Tasks, r.Crons)
	}
}

func TestQuestionAndPlanItems(t *testing.T) {
	in := input(t, "PreToolUse-ask")
	r := one(t, Requests("PreToolUse", in, env(nil)))
	if r.Op != ipc.OpItemAdd || r.Item.Key != in.ToolUseID || r.Item.Kind != ipc.KindQuestion ||
		r.Item.Summary != "Which toppings? (+2 more)" || len(r.Item.Detail) == 0 {
		t.Fatalf("%+v", r.Item)
	}
	r = one(t, Requests("PreToolUse", input(t, "PreToolUse-plan"), env(nil)))
	if r.Item.Kind != ipc.KindPlan || r.Item.Summary != "Plan: create plan.txt" {
		t.Fatalf("%+v", r.Item)
	}
	// Their PermissionRequest is left to PreToolUse.
	in.ToolUseID = ""
	if reqs := Requests("PermissionRequest", in, env(nil)); len(reqs) != 0 {
		t.Fatalf("%+v", reqs)
	}
	done := one(t, Requests("PostToolUse", input(t, "PostToolUse-ask"), env(nil)))
	if done.Op != ipc.OpItemDone || done.Note != "answered: Which toppings? → Cheese, Basil; Which size? → Large; Which drink? → Lemonade, cold" {
		t.Fatalf("%+v", done)
	}
}

func TestPermissionItems(t *testing.T) {
	r := one(t, Requests("PermissionRequest", input(t, "PermissionRequest-bash"), env(nil)))
	if r.Item.Kind != ipc.KindPermission || r.Item.Summary != "Bash: touch probe-file.txt" || r.Item.Needle != "touch probe-file.txt" || r.Item.Tool != "Bash" {
		t.Fatalf("%+v", r.Item)
	}
	// A long command's needle is a plain prefix: the screen shows no "…"
	// (found in the step-3 live probe).
	long := input(t, "PermissionRequest-bash")
	long.ToolInput = json.RawMessage(`{"command":"printf 'planned\\n' > plan.txt && grep -qx planned plan.txt && cat plan.txt"}`)
	if n := one(t, Requests("PermissionRequest", long, env(nil))).Item.Needle; n != `printf 'planned\n' > plan.txt && grep -q` {
		t.Fatalf("needle %q", n)
	}
	w := one(t, Requests("PermissionRequest", input(t, "PermissionRequest-write"), env(nil)))
	if w.Item.Summary != "Write: notes.txt" || w.Item.Needle != "notes.txt" {
		t.Fatalf("%+v", w.Item)
	}

	// The key must come out the same from the transcript's tool_use line,
	// which is how a denied prompt gets closed.
	b, err := os.ReadFile("../transcript/testdata/probe-2.1.308.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		for _, e := range transcript.Parse([]byte(line)) {
			if e.Kind == transcript.ToolUse {
				found[ipc.PermKey(e.ToolName, e.Input)] = true
			}
		}
	}
	for _, it := range []*ipc.Item{r.Item, w.Item} {
		if !found[it.Key] {
			t.Errorf("%s: key %s doesn't match any tool_use in the transcript", it.Summary, it.Key)
		}
	}
}

func TestSpoolWhenDaemonIsAway(t *testing.T) {
	dir, err := os.MkdirTemp("", "hook")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p := config.Paths{StateDir: filepath.Join(dir, "state"), Socket: filepath.Join(dir, "none.sock")}
	b, _ := os.ReadFile("testdata/PermissionRequest-bash.json")
	start := time.Now()
	Run("PermissionRequest", strings.NewReader(string(b)), env(nil), p)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v with no daemon", d)
	}
	spooled, err := os.ReadFile(p.SpoolFile())
	if err != nil {
		t.Fatal(err)
	}
	var r ipc.Request
	if err := json.Unmarshal(spooled, &r); err != nil || r.Op != ipc.OpItemAdd || r.Time.IsZero() {
		t.Fatalf("spooled %s (%v)", spooled, err)
	}
	fi, _ := os.Stat(p.SpoolFile())
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("spool mode %04o", fi.Mode().Perm())
	}
}
