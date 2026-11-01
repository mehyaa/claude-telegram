package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/install"
	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/update"
)

// openStatus draws 🩺 Status. The release check runs off the lock.
func (d *Daemon) openStatus(msg int64) {
	rel := d.cfg.Release
	if rel == nil {
		d.paintStatus(msg, "", false, nil)
		return
	}
	go func() {
		tag, err := rel()
		d.mu.Lock()
		defer d.mu.Unlock()
		d.paintStatus(msg, tag, true, err)
	}()
}

func (d *Daemon) paintStatus(msg int64, tag string, checked bool, relErr error) {
	v := d.statusView(tag, checked, relErr)
	if msg == 0 {
		d.sendView(v)
		return
	}
	d.edit(msg, v)
}

func (d *Daemon) statusView(tag string, checked bool, relErr error) view {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n🩺 Status\n", d.header())
	ver := d.cfg.Version
	if ver == "" {
		ver = "dev"
	}
	up := "just started"
	if !d.booted.IsZero() {
		up = upFor(d.now().Sub(d.booted))
	}
	fmt.Fprintf(&b, "Up %s · ctg %s\n", up, ver)
	var updateBtn []telegram.InlineKeyboardButton
	if checked {
		switch {
		case relErr != nil:
			b.WriteString("Release check failed: " + relErr.Error() + "\n")
		case newerRelease(tag, ver):
			b.WriteString("Release " + tag + " is newer.\n")
			updateBtn = row(button("⬆️ Update", "up:a"))
		default:
			b.WriteString("This is the latest release.\n")
		}
	}
	b.WriteString("\nClaude Code\n")
	if len(d.st.Sessions) == 0 {
		b.WriteString("No sessions.\n")
	}
	for _, s := range d.st.Sessions {
		line := s.ref()
		if s.Version != "" {
			line += " · " + s.Version
			if !d.isVerified(s.Version) {
				line += " · answer in the terminal"
			}
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nInstalled\n")
	b.WriteString(d.installLines())
	errText := d.lastProblem()
	if errText == "" {
		errText = "none"
	}
	b.WriteString("\nLast error: " + errText)
	rows := [][]telegram.InlineKeyboardButton{}
	if len(updateBtn) > 0 {
		rows = append(rows, updateBtn)
	}
	rows = append(rows, row(button("« Sessions", "l")))
	return view{b.String(), keyboard(rows...)}
}

func (d *Daemon) installLines() string {
	home := d.home()
	hooks, service, shell := install.StepState(filepath.Join(d.cfg.Paths.StateDir, "installed.json"))
	var b strings.Builder
	fmt.Fprintf(&b, "hooks: %s\n", piece(hooks, fileHas(filepath.Join(home, ".claude", "settings.json"), "ctg hook")))
	fmt.Fprintf(&b, "mcp: %s\n", mcpState(home))
	fmt.Fprintf(&b, "service: %s\n", piece(service, fileExists(filepath.Join(home, ".config", "systemd", "user", "ctg.service"))))
	shellFile := filepath.Join(home, ".local", "share", "claude-telegram", "claude-tmux.bash")
	sourced := fileHas(filepath.Join(home, ".bashrc"), "claude-tmux.bash")
	fmt.Fprintf(&b, "shell: %s\n", piece(shell, fileExists(shellFile) && sourced))
	return b.String()
}

func piece(hash string, present bool) string {
	switch {
	case hash == "current" && present:
		return "current"
	case hash == "stale":
		return "out of date"
	case present:
		return "installed"
	default:
		return "not installed"
	}
}

func mcpState(home string) string {
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return "not installed"
	}
	if strings.Contains(string(b), `"ctg"`) {
		return "installed"
	}
	return "not installed"
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func fileHas(path, needle string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), needle)
}

func (d *Daemon) runUpdate() {
	fn := d.cfg.Update
	if fn == nil {
		fn = func() (string, error) {
			var buf strings.Builder
			err := update.Run(&buf, &buf)
			return buf.String(), err
		}
	}
	d.reply("⬆️ Updating. The service restarts when it finishes.", nil)
	go func() {
		out, err := fn()
		d.mu.Lock()
		defer d.mu.Unlock()
		if err != nil {
			d.noteProblem("update: " + err.Error())
			msg := "Update failed: " + err.Error() + "."
			if strings.TrimSpace(out) != "" {
				msg += "\n" + strings.TrimSpace(out)
			}
			d.reply(msg, nil)
			return
		}
		d.reply("⬆️ Update finished.\n"+strings.TrimSpace(out), nil)
	}()
}

func upFor(d time.Duration) string {
	if d < time.Minute {
		return "just started"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func newerRelease(latest, current string) bool {
	l, lok := parseVer(latest)
	c, cok := parseVer(current)
	if !lok || !cok {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseVer(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var v [3]int
	n, err := fmt.Sscanf(s, "%d.%d.%d", &v[0], &v[1], &v[2])
	return v, err == nil && n == 3
}
