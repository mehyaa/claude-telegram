package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mehyaa/claude-telegram/contrib"
)

var tokenRe = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
var pairCodeRe = regexp.MustCompile(`[0-9]{6}`)

func (r *runner) stepBinary() error {
	r.step("Copy this ctg to " + r.p.Bin)
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		src = resolved
	}
	if err := os.MkdirAll(r.p.BinDir, 0o755); err != nil {
		return err
	}
	tmp := r.p.Bin + ".new"
	if err := copyFile(src, tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.p.Bin); err != nil {
		os.Remove(tmp)
		return err
	}
	cmd := exec.Command(r.p.Bin, "help")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("the installed %s doesn't run: %v\n%s", r.p.Bin, err, out)
	}
	r.info("installed " + r.p.Bin)
	if !pathOnPATH(r.p.BinDir) {
		r.warn(r.p.BinDir + " isn't on your PATH; the hooks use the full path, so that's fine for them")
	}
	return nil
}

func (r *runner) stepToken() error {
	r.step("Store the bot token in " + r.p.TokenFile())
	token, fromEnv := envValue(r.p.EnvFile, "TELEGRAM_BOT_TOKEN")
	if fromEnv {
		r.info("taking TELEGRAM_BOT_TOKEN from " + r.p.EnvFile)
	} else {
		fmt.Fprint(r.out, "  Paste the bot token from @BotFather (it isn't shown): ")
		if isTTY(r.inRaw) {
			restore := hideEcho(r.inRaw)
			defer restore()
		}
		line, err := r.in.ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("no token")
		}
		fmt.Fprintln(r.out)
		token = strings.TrimSpace(line)
	}
	if !tokenRe.MatchString(token) {
		return fmt.Errorf("that doesn't look like a bot token (digits:secret)")
	}
	if err := os.MkdirAll(r.p.ConfigDir, 0o700); err != nil {
		return err
	}
	if err := writeAtomic(r.p.TokenFile(), []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	r.info("stored, mode 0600")
	if fi, err := os.Stat(r.p.EnvFile); err == nil && fi.Mode().Perm() != 0o600 {
		if r.confirm(r.p.EnvFile + " is readable by others; make it 0600?") {
			if err := os.Chmod(r.p.EnvFile, 0o600); err != nil {
				return err
			}
			r.info(r.p.EnvFile + " is now 0600")
		}
	}
	return nil
}

func (r *runner) stepPair() error {
	r.step("Pair your Telegram chat")
	if _, err := os.Stat(r.p.Bin); err != nil {
		return fmt.Errorf("run the binary step first")
	}
	if _, err := os.Stat(r.p.TokenFile()); err != nil {
		return fmt.Errorf("run the token step first")
	}
	if _, err := os.Stat(r.p.ChatFile()); err == nil && r.all {
		chat, _ := os.ReadFile(r.p.ChatFile())
		if !r.confirmNo("Already paired with chat " + strings.TrimSpace(string(chat)) + "; pair again?") {
			r.info("kept the paired chat")
			return nil
		}
	}
	var before int64
	if t, ok := mtime(r.p.ChatFile()); ok {
		before = t
	}
	var daemon *exec.Cmd
	defer func() {
		if daemon != nil && daemon.Process != nil {
			_ = daemon.Process.Kill()
			_ = daemon.Wait()
		}
	}()
	if !r.daemonUp() {
		if err := os.MkdirAll(r.p.StateDir, 0o700); err != nil {
			return err
		}
		r.info("starting a daemon for pairing; it stops when this step ends")
		logf, err := os.OpenFile(r.p.PairLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer logf.Close()
		daemon = exec.Command(r.p.Bin, "daemon")
		daemon.Stdout, daemon.Stderr = logf, logf
		daemon.Env = os.Environ()
		if err := daemon.Start(); err != nil {
			return err
		}
		ok := false
		for i := 0; i < 50; i++ {
			if r.daemonUp() {
				ok = true
				break
			}
			if daemon.Process != nil {
				if err := daemon.Process.Signal(syscall.Signal(0)); err != nil {
					return fmt.Errorf("the daemon stopped; see %s", r.p.PairLog())
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !ok {
			return fmt.Errorf("the daemon didn't come up; see %s", r.p.PairLog())
		}
	}
	out, err := r.runBin("pair")
	if err != nil {
		return fmt.Errorf("ctg pair: %w", err)
	}
	code := pairCodeRe.FindString(out)
	if code == "" {
		return fmt.Errorf("ctg pair printed no code:\n%s", out)
	}
	bot, ok := envValue(r.p.EnvFile, "TELEGRAM_BOT_NAME")
	if !ok {
		bot = "your bot"
	}
	if !strings.HasPrefix(bot, "@") && bot != "your bot" {
		bot = "@" + bot
	}
	r.info("In Telegram, open " + bot + ", tap Start, and send it this code:")
	fmt.Fprintf(r.out, "\n      %s\n\n", code)
	r.info("waiting for it (15 minutes at most; Ctrl-C to stop)…")
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if t, ok := mtime(r.p.ChatFile()); ok && t != before {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if t, ok := mtime(r.p.ChatFile()); !ok || t == before {
		return fmt.Errorf("no pairing within 15 minutes")
	}
	chat, err := os.ReadFile(r.p.ChatFile())
	if err != nil {
		return err
	}
	chatID := strings.TrimSpace(string(chat))
	r.info("paired with chat " + chatID)
	if user, ok := envValue(r.p.EnvFile, "TELEGRAM_USER_ID"); ok {
		if chatID == user {
			r.info("that is your TELEGRAM_USER_ID, as it should be")
		} else {
			r.warn("that isn't your TELEGRAM_USER_ID (" + user + "). If someone else sent the code, run this step again.")
		}
	}
	if daemon != nil && daemon.Process != nil {
		_ = daemon.Process.Kill()
		_ = daemon.Wait()
		daemon = nil
	}
	return nil
}

func (r *runner) daemonUp() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.p.Bin, "pair")
	cmd.Env = os.Environ()
	return cmd.Run() == nil
}

func (r *runner) runBin(args ...string) (string, error) {
	cmd := exec.Command(r.p.Bin, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *runner) stepService() error {
	r.step("Run the daemon as the systemd user service ctg")
	if err := need("systemctl"); err != nil {
		return err
	}
	if _, err := os.Stat(r.p.Bin); err != nil {
		return fmt.Errorf("run the binary step first")
	}
	if _, err := os.Stat(r.p.ChatFile()); err != nil {
		r.warn("not paired yet; the service will print a code to its journal (or run the pair step)")
	}
	re := regexp.MustCompile(`(?m)^ExecStart=.*`)
	unit := re.ReplaceAllLiteralString(string(contrib.ServiceUnit), "ExecStart="+r.p.Bin+" daemon")
	if err := os.MkdirAll(r.p.UnitDir, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(r.p.UnitFile(), []byte(unit), 0o644); err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	_ = exec.Command("systemctl", "--user", "enable", "ctg").Run()
	if out, err := exec.Command("systemctl", "--user", "restart", "ctg").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart: %v\n%s", err, out)
	}
	time.Sleep(time.Second)
	if err := exec.Command("systemctl", "--user", "is-active", "--quiet", "ctg").Run(); err != nil {
		return fmt.Errorf("ctg didn't start; see journalctl --user -u ctg")
	}
	r.info("ctg is running; its log: journalctl --user -u ctg")
	return r.remember("service")
}

func (r *runner) stepHooks() error {
	r.step("Add ctg's hooks to " + r.p.Settings)
	if _, err := os.Stat(r.p.Bin); err != nil {
		return fmt.Errorf("run the binary step first")
	}
	if err := os.MkdirAll(r.p.ClaudeDir, 0o700); err != nil {
		return err
	}
	current := []byte("{}")
	mode := os.FileMode(0o600)
	if b, err := os.ReadFile(r.p.Settings); err == nil {
		mode = fileMode(r.p.Settings)
		if _, err := decodeObject(b); err != nil {
			return fmt.Errorf("%s isn't valid JSON; fix it first", r.p.Settings)
		}
		current = b
		backup := r.p.Settings + ".ctg-backup-" + time.Now().Format("20060102-150405")
		if err := copyFile(r.p.Settings, backup, mode); err != nil {
			return err
		}
		r.info("backup: " + backup)
	}
	merged, err := mergeHooks(current, contrib.Hooks, r.p.Bin)
	if err != nil {
		return err
	}
	if err := writeAtomic(r.p.Settings, merged, mode); err != nil {
		return err
	}
	cur, err := decodeObject(merged)
	if err != nil {
		return err
	}
	var names []string
	if hooks, ok := cur["hooks"].(map[string]any); ok {
		for ev := range hooks {
			names = append(names, ev)
		}
	}
	sort.Strings(names)
	r.info("hooks for: " + strings.Join(names, ", "))
	r.info("sessions started from now on join; running ones join when restarted")
	return r.remember("hooks")
}

func (r *runner) stepMCP() error {
	r.step("Add the post_question MCP server (ctg) to Claude Code")
	if err := need("claude"); err != nil {
		return err
	}
	if _, err := os.Stat(r.p.Bin); err != nil {
		return fmt.Errorf("run the binary step first")
	}
	if exec.Command("claude", "mcp", "get", "ctg").Run() == nil {
		_ = exec.Command("claude", "mcp", "remove", "--scope", "user", "ctg").Run()
	}
	cmd := exec.Command("claude", "mcp", "add", "--scope", "user", "ctg", "--", r.p.Bin, "mcp")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("claude mcp add: %v\n%s", err, out)
	}
	r.info("added for every project (user scope)")
	return nil
}

func (r *runner) stepShell() error {
	r.step("Start interactive claude in tmux, from " + r.p.Bashrc)
	if err := need("tmux"); err != nil {
		return err
	}
	if err := os.MkdirAll(r.p.ShareDir, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(r.p.WrapperFile(), contrib.ClaudeTmux, 0o644); err != nil {
		return err
	}
	line := `source "` + r.p.ShareDir + `/claude-tmux.bash"  # ctg: claude in tmux`
	mark := `claude-tmux.bash"  # ctg`
	b, err := os.ReadFile(r.p.Bashrc)
	if err == nil && strings.Contains(string(b), mark) {
		r.info(r.p.Bashrc + " already sources it")
		return r.remember("shell")
	}
	f, err := os.OpenFile(r.p.Bashrc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "\n%s\n", line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	r.info("added to " + r.p.Bashrc + "; new shells pick it up (or run: source " + r.p.Bashrc + ")")
	return r.remember("shell")
}

func (r *runner) restartService() error {
	if _, err := os.Stat(r.p.UnitFile()); err != nil {
		return nil
	}
	if err := need("systemctl"); err != nil {
		r.warn("systemctl not found; start ctg yourself")
		return nil
	}
	if out, err := exec.Command("systemctl", "--user", "restart", "ctg").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart: %v\n%s", err, out)
	}
	r.info("restarted the ctg service")
	return nil
}

func hideEcho(in io.Reader) func() {
	f, ok := in.(*os.File)
	if !ok {
		return func() {}
	}
	off := exec.Command("stty", "-echo")
	off.Stdin = f
	if err := off.Run(); err != nil {
		return func() {}
	}
	return func() {
		on := exec.Command("stty", "echo")
		on.Stdin = f
		_ = on.Run()
	}
}
