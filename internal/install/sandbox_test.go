package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/contrib"
	"github.com/mehyaa/claude-telegram/internal/telegram/telegramtest"
)

func TestSandbox(t *testing.T) {
	built := buildCTG(t)
	root := sandboxRoot(t)
	home := filepath.Join(root, "h")
	fakeDir := filepath.Join(root, "f")
	runDir := filepath.Join(root, "r")
	for _, dir := range []string{home, fakeDir, runDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settings, []byte(otherSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(root, "env")
	if err := os.WriteFile(envFile, []byte(
		"TELEGRAM_BOT_NAME=test_bot   # a comment\n"+
			"TELEGRAM_BOT_TOKEN=\"123456:fake-token\"\n"+
			"export TELEGRAM_USER_ID=42\r\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("# a bashrc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := filepath.Join(root, "calls.log")
	mcpFlag := filepath.Join(root, "mcp-added")
	writeFakes(t, fakeDir, calls, mcpFlag)

	tg := telegramtest.New(t)
	env := sandboxEnv(home, runDir, fakeDir, envFile, tg.URL)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(built, args...)
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("install", "--yes", "binary", "token")
	if err != nil {
		t.Fatalf("binary/token: %v\n%s", err, out)
	}

	pair := exec.Command(built, "install", "--yes", "pair")
	pair.Env = env
	pair.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var pairBuf syncBuf
	pair.Stdout, pair.Stderr = &pairBuf, &pairBuf
	if err := pair.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if pair.Process != nil {
			_ = syscall.Kill(-pair.Process.Pid, syscall.SIGKILL)
		}
	})
	re := regexp.MustCompile(`(?m)^ {6}(\d{6})$`)
	var code string
	for i := 0; i < 200; i++ {
		if m := re.FindStringSubmatch(pairBuf.String()); m != nil {
			code = m[1]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code == "" {
		logb, _ := os.ReadFile(filepath.Join(home, ".local", "state", "claude-telegram", "pair.log"))
		t.Fatalf("no pairing code shown\n%s\n--- pair.log ---\n%s", pairBuf.String(), logb)
	}
	tg.Send(42, code)
	done := make(chan error, 1)
	go func() { done <- pair.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("pair step: %v\n%s", err, pairBuf.String())
		}
	case <-time.After(15 * time.Second):
		_ = syscall.Kill(-pair.Process.Pid, syscall.SIGKILL)
		t.Fatalf("pair step hung\n%s", pairBuf.String())
	}
	if !strings.Contains(pairBuf.String(), "that is your TELEGRAM_USER_ID") {
		t.Fatalf("user ID check missing\n%s", pairBuf.String())
	}

	out, err = run("install", "--yes", "service", "hooks", "mcp", "shell")
	if err != nil {
		t.Fatalf("later steps: %v\n%s", err, out)
	}

	bin := filepath.Join(home, ".local", "bin", "ctg")
	if fi, err := os.Stat(bin); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("no executable %s: %v", bin, err)
	}
	tokenPath := filepath.Join(home, ".config", "claude-telegram", "token")
	if fi, err := os.Stat(tokenPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token mode: %v", err)
	}
	if b, _ := os.ReadFile(tokenPath); strings.TrimSpace(string(b)) != "123456:fake-token" {
		t.Fatalf("token content %q", b)
	}
	if fi, err := os.Stat(envFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".env not made 0600: %v", err)
	}
	chatPath := filepath.Join(home, ".config", "claude-telegram", "chat_id")
	if b, _ := os.ReadFile(chatPath); strings.TrimSpace(string(b)) != "42" {
		t.Fatalf("chat_id %q", b)
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "ctg.service"))
	if err != nil || !bytes.Contains(unit, []byte("ExecStart="+bin+" daemon")) {
		t.Fatalf("unit ExecStart: %s", unit)
	}
	logged, _ := os.ReadFile(calls)
	if !bytes.Contains(logged, []byte("systemctl --user restart ctg")) {
		t.Fatalf("service not started:\n%s", logged)
	}
	if !bytes.Contains(logged, []byte("claude mcp add --scope user ctg -- "+bin+" mcp")) {
		t.Fatalf("mcp add:\n%s", logged)
	}
	bashrc, _ := os.ReadFile(filepath.Join(home, ".bashrc"))
	wantLine := `source "` + filepath.Join(home, ".local", "share", "claude-telegram", "claude-tmux.bash") + `"  # ctg: claude in tmux`
	if !bytes.Contains(bashrc, []byte(wantLine)) {
		t.Fatalf("bashrc line:\n%s", bashrc)
	}

	s, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := decodeObject(s)
	if err != nil {
		t.Fatal(err)
	}
	if cur["theme"] != "dark" {
		t.Fatalf("settings lost a key: %s", s)
	}
	hooks, _ := cur["hooks"].(map[string]any)
	stop, _ := hooks["Stop"].([]any)
	if !hasCommand(stop, "notify-send done") {
		t.Fatal("someone else's hook lost")
	}
	if hasCommand(stop, "/old/path") {
		t.Fatal("old ctg hook kept")
	}
	pre, _ := hooks["PreToolUse"].([]any)
	if !hasCommand(pre, `"`+bin+`" hook PreToolUse`) {
		t.Fatalf("hook command: %s", s)
	}

	if out, err := run("install", "--yes", "hooks", "shell"); err != nil {
		t.Fatalf("install again: %v\n%s", err, out)
	}
	s, _ = os.ReadFile(settings)
	cur, _ = decodeObject(s)
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
		t.Fatalf("%d ctg hook entries after installing twice, want 7", n)
	}
	bashrc, _ = os.ReadFile(filepath.Join(home, ".bashrc"))
	if c := bytes.Count(bashrc, []byte("# ctg: claude in tmux")); c != 1 {
		t.Fatalf("bashrc line doubled: %d", c)
	}

	installed := filepath.Join(home, ".local", "state", "claude-telegram", "installed.json")
	out, err = run("update", "--apply")
	if err != nil {
		t.Fatalf("update --apply: %v\n%s", err, out)
	}
	if strings.Contains(out, "embed changed") {
		t.Fatalf("matching embeds still re-ran a step:\n%s", out)
	}
	if err := os.WriteFile(installed, []byte(`{"hooks":"0","service":"0","shell":"0"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = run("update", "--apply")
	if err != nil {
		t.Fatalf("update --apply after embed change: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hooks embed changed") {
		t.Fatalf("changed embeds did not re-run hooks:\n%s", out)
	}
	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	var rec recorded
	if err := json.Unmarshal(got, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Hooks != sumEmbed(contrib.Hooks) || rec.Service != sumEmbed(contrib.ServiceUnit) || rec.Shell != sumEmbed(contrib.ClaudeTmux) {
		t.Fatalf("installed.json not updated after apply: %s", got)
	}

	if out, err := run("uninstall", "--yes"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatal("binary left")
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "claude-telegram")); !os.IsNotExist(err) {
		t.Fatal("config left")
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "ctg.service")); !os.IsNotExist(err) {
		t.Fatal("unit left")
	}
	bashrc, _ = os.ReadFile(filepath.Join(home, ".bashrc"))
	if bytes.Contains(bashrc, []byte("# ctg")) {
		t.Fatalf("bashrc line left:\n%s", bashrc)
	}
	s, _ = os.ReadFile(settings)
	cur, _ = decodeObject(s)
	hooksJSON, _ := json.Marshal(cur["hooks"])
	wantHooks := `{"Stop":[{"hooks":[{"command":"notify-send done","type":"command"}]}]}`
	var a, b any
	_ = json.Unmarshal(hooksJSON, &a)
	_ = json.Unmarshal([]byte(wantHooks), &b)
	if !jsonEqual(a, b) {
		t.Fatalf("hooks after uninstall: %s", hooksJSON)
	}
	logged, _ = os.ReadFile(calls)
	if !bytes.Contains(logged, []byte("claude mcp remove --scope user ctg")) {
		t.Fatalf("mcp remove:\n%s", logged)
	}

	if _, err := run("update"); err == nil {
		t.Fatal("ctg update without a compiled key should fail")
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// sandboxRoot is a short directory under .scratch. t.TempDir() with
// TMPDIR=.scratch/tmp makes the daemon socket longer than sockaddr_un
// allows (~108 bytes), so pairing never comes up.
func sandboxRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
	parent := filepath.Join(dir, ".scratch")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(parent, "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

func buildCTG(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ctg")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/mehyaa/claude-telegram/cmd/ctg")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func sandboxEnv(home, runDir, fakeDir, envFile, tgURL string) []string {
	path := fakeDir + string(os.PathListSeparator) + os.Getenv("PATH")
	return []string{
		"PATH=" + path,
		"HOME=" + home,
		"XDG_RUNTIME_DIR=" + runDir,
		"TERM=dumb",
		"CTG_ENV_FILE=" + envFile,
		"CTG_TELEGRAM_API=" + tgURL,
	}
}

func writeFakes(t *testing.T, dir, logPath, mcpFlag string) {
	t.Helper()
	for _, cmd := range []string{"systemctl", "journalctl"} {
		body := fmt.Sprintf("#!/usr/bin/env bash\necho %s \"$@\" >> %q\n", cmd, logPath)
		path := filepath.Join(dir, cmd)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	claude := fmt.Sprintf("#!/usr/bin/env bash\n"+
		"echo \"claude $*\" >> %q\n"+
		"if [[ $1 == mcp && $2 == get ]]; then\n"+
		"  [[ -f %q ]]\n"+
		"elif [[ $1 == mcp && $2 == add ]]; then\n"+
		"  touch %q\n"+
		"elif [[ $1 == mcp && $2 == remove ]]; then\n"+
		"  rm -f %q\n"+
		"fi\n", logPath, mcpFlag, mcpFlag, mcpFlag)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(claude), 0o755); err != nil {
		t.Fatal(err)
	}
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
