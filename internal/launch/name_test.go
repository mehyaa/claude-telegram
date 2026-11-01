package launch

import (
	"strings"
	"testing"
)

func TestSessionName(t *testing.T) {
	ok := []struct{ in, want string }{
		{"my-app", "my-app"},
		{"my_app", "my_app"},
		{"update", "update"},
		{"MyApp", "my-app"},
		{"HTTPServer", "http-server"},
		{"My_App", "my_app"},
		{"my--app", "my-app"},
		{"XMLParser", "xml-parser"},
	}
	for _, tc := range ok {
		got, yes := SessionName(tc.in)
		if !yes || got != tc.want {
			t.Errorf("SessionName(%q) = %q, %v; want %q", tc.in, got, yes, tc.want)
		}
	}
	for _, in := range []string{"", "-rf", "2fa", "fix the bug", strings.Repeat("a", 41)} {
		if _, yes := SessionName(in); yes {
			t.Errorf("SessionName(%q) should be refused", in)
		}
	}
}

func TestScriptQuotes(t *testing.T) {
	if got := Script("", "my-app", "fix the 'bug'"); got != `exec claude --name 'my-app' 'fix the '\''bug'\'''` {
		t.Fatalf("script: %s", got)
	}
	if got := Script("11111111-1111-4111-8111-111111111111", "my-app", "nope"); got != `exec claude --resume '11111111-1111-4111-8111-111111111111'` {
		t.Fatalf("resume script: %s", got)
	}
	args := Command("/bin/bash", "claude-my-app", "my-app: ~/work", "/home/user/work", Script("", "my-app", ""))
	if args[0] != "new-session" || args[1] != "-d" || args[3] != "claude-my-app" || args[7] != "/home/user/work" {
		t.Fatalf("args %q", args)
	}
	if args[len(args)-1] != "exec claude --name 'my-app'" {
		t.Fatalf("command %q", args[len(args)-1])
	}
}
