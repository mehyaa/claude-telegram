package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/telegram/telegramtest"
)

// TestBinary runs the built ctg: first start prints a code, the code pairs,
// ctg pair reaches the daemon, and SIGTERM stops it cleanly.
func TestBinary(t *testing.T) {
	dir := scratchDir(t)
	bin := filepath.Join(dir, "ctg")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	fake := telegramtest.New(t)
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(dir, "c"),
		"XDG_STATE_HOME="+filepath.Join(dir, "s"),
		"XDG_RUNTIME_DIR="+filepath.Join(dir, "r"),
		"CTG_TELEGRAM_API="+fake.URL,
	)
	cfgDir := filepath.Join(dir, "c", "claude-telegram")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(cfgDir, "token")
	if err := os.WriteFile(tokenFile, []byte(telegramtest.Token+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A token file others can read is refused.
	cmd := exec.Command(bin, "daemon")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "chmod 600") || strings.Contains(string(out), "fake-token") {
		t.Fatalf("daemon with a 0644 token: %v\n%s", err, out)
	}
	if err := os.Chmod(tokenFile, 0o600); err != nil {
		t.Fatal(err)
	}

	daemon := exec.Command(bin, "daemon")
	daemon.Env = env
	stderr, err := daemon.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { daemon.Process.Kill() })

	codes := make(chan string, 1)
	scanned := make(chan struct{})
	var logged strings.Builder // written by the scanner until scanned closes
	go func() {
		defer close(scanned)
		re := regexp.MustCompile(`\b\d{6}\b`)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			logged.WriteString(sc.Text() + "\n")
			if c := re.FindString(sc.Text()); c != "" && strings.Contains(sc.Text(), "Send this code") {
				codes <- c
			}
		}
	}()
	var code string
	select {
	case code = <-codes:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon printed no pairing code")
	}

	fake.Send(42, code)
	if got := fake.Next(t); got.ChatID != 42 || !strings.HasPrefix(got.Text, "Paired.") {
		t.Fatalf("after sending the code: %+v", got)
	}
	fake.Next(t) // the session list

	pair := exec.Command(bin, "pair")
	pair.Env = env
	pout, err := pair.CombinedOutput()
	if err != nil || !regexp.MustCompile(`code to the bot from your Telegram account: \d{6}`).Match(pout) {
		t.Fatalf("ctg pair: %v\n%s", err, pout)
	}

	if err := daemon.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scanned: // stderr closed: the daemon is exiting
	case <-time.After(5 * time.Second):
		t.Fatal("daemon didn't stop on SIGTERM")
	}
	if err := daemon.Wait(); err != nil {
		t.Fatalf("daemon exit: %v\n%s", err, logged.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "s", "claude-telegram", "heartbeat")); err != nil {
		t.Fatalf("no heartbeat after shutdown: %v", err)
	}
	if strings.Contains(logged.String(), "fake-token") {
		t.Fatalf("daemon logged the token:\n%s", logged.String())
	}

	// With the daemon gone, ctg pair says so.
	pair = exec.Command(bin, "pair")
	pair.Env = env
	if pout, err := pair.CombinedOutput(); err == nil || !strings.Contains(string(pout), "is `ctg daemon` running?") {
		t.Fatalf("ctg pair without a daemon: %v\n%s", err, pout)
	}
}

// scratchDir is short enough that the daemon socket fits in sockaddr_un
// when TMPDIR is the repo's .scratch/tmp.
func scratchDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(wd, "..", "..", ".scratch")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(parent, "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
