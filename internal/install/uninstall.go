package install

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func (r *runner) undoShell() error {
	r.step("Stop sourcing the tmux wrapper from " + r.p.Bashrc)
	mark := `claude-tmux.bash"  # ctg`
	if b, err := os.ReadFile(r.p.Bashrc); err == nil && strings.Contains(string(b), mark) {
		var kept []string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, mark) {
				continue
			}
			kept = append(kept, line)
		}
		// Split drops a trailing newline's empty last element; write the
		// kept lines joined, and restore a trailing newline if the file had one.
		out := strings.Join(kept, "\n")
		if strings.HasSuffix(string(b), "\n") && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if err := writeAtomic(r.p.Bashrc, []byte(out), fileMode(r.p.Bashrc)); err != nil {
			return err
		}
		r.info("removed from " + r.p.Bashrc + "; open shells keep the wrapper until they exit")
	} else {
		r.info("not in " + r.p.Bashrc)
	}
	os.Remove(r.p.WrapperFile())
	return nil
}

func (r *runner) undoMCP() error {
	r.step("Remove the ctg MCP server")
	if _, err := exec.LookPath("claude"); err == nil && exec.Command("claude", "mcp", "get", "ctg").Run() == nil {
		if out, err := exec.Command("claude", "mcp", "remove", "--scope", "user", "ctg").CombinedOutput(); err != nil {
			return fmt.Errorf("claude mcp remove: %v\n%s", err, out)
		}
		r.info("removed")
		return nil
	}
	r.info("not there")
	return nil
}

func (r *runner) undoHooks() error {
	r.step("Remove ctg's hooks from " + r.p.Settings)
	b, err := os.ReadFile(r.p.Settings)
	if err != nil {
		r.info("no settings file")
		return nil
	}
	backup := r.p.Settings + ".ctg-backup-" + time.Now().Format("20060102-150405")
	mode := fileMode(r.p.Settings)
	if err := copyFile(r.p.Settings, backup, mode); err != nil {
		return err
	}
	r.info("backup: " + backup)
	stripped, err := removeHooks(b)
	if err != nil {
		return fmt.Errorf("%s: %w", r.p.Settings, err)
	}
	if err := writeAtomic(r.p.Settings, stripped, mode); err != nil {
		return err
	}
	r.info("removed")
	return nil
}

func (r *runner) undoService() error {
	r.step("Stop and remove the ctg service")
	if _, err := exec.LookPath("systemctl"); err == nil {
		_ = exec.Command("systemctl", "--user", "disable", "--now", "ctg").Run()
		os.Remove(r.p.UnitFile())
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		r.info("stopped and removed")
	}
	return nil
}

func (r *runner) undoConfig() error {
	r.step("Delete the token, the paired chat and ctg's state")
	if !r.confirm("Delete " + r.p.ConfigDir + " and " + r.p.StateDir + " (the audit log too)?") {
		r.info("kept")
		return nil
	}
	os.RemoveAll(r.p.ConfigDir)
	os.RemoveAll(r.p.StateDir)
	r.info("deleted")
	return nil
}

func (r *runner) undoBinary() error {
	r.step("Delete " + r.p.Bin)
	os.Remove(r.p.Bin)
	r.info("deleted")
	return nil
}
