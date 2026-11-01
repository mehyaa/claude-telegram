// Package install is `ctg install` and `ctg uninstall` (Q60): the same steps
// and questions as the old bash scripts, with the contrib files embedded.
package install

import (
	"fmt"
	"io"
	"strings"

	"github.com/mehyaa/claude-telegram/contrib"
)

const InstallUsage = `usage: ctg install [--yes] [step...]

With no steps, runs them all in order, asking before each. Steps:

  binary   copy this executable to ~/.local/bin/ctg
  token    store the bot token (from .env's TELEGRAM_BOT_TOKEN, or typed)
  pair     pair your Telegram chat with the bot
  service  run the daemon as a systemd user service
  hooks    add ctg's hooks to Claude Code's user settings (with a backup)
  mcp      add the post_question MCP server to Claude Code
  shell    make interactive claude start in tmux (~/.bashrc)

Every step can be run again; ctg uninstall undoes them.
`

const UninstallUsage = `usage: ctg uninstall [--yes] [step...]

With no steps, undoes them all, last first, asking before each. Steps:

  shell    stop sourcing the tmux wrapper from ~/.bashrc
  mcp      remove the ctg MCP server from Claude Code
  hooks    remove ctg's hooks from Claude Code's settings (with a backup)
  service  stop and remove the systemd user service
  config   delete the bot token, the paired chat and ctg's state
  binary   delete ~/.local/bin/ctg
`

var installOrder = []string{"binary", "token", "pair", "service", "hooks", "mcp", "shell"}
var uninstallOrder = []string{"shell", "mcp", "hooks", "service", "config", "binary"}

// Install runs the named steps, or every step in order. --yes / -y answers
// the questions yes.
func Install(args []string, in io.Reader, out, errw io.Writer) error {
	yes, steps, help, err := parseArgs(args)
	if err != nil {
		return err
	}
	if help {
		fmt.Fprint(out, InstallUsage)
		return nil
	}
	r, err := newRunner(in, out, errw, yes)
	if err != nil {
		return err
	}
	ask := false
	if len(steps) == 0 {
		steps = append([]string{}, installOrder...)
		ask = true
		r.all = true
	}
	for _, s := range steps {
		if !known(installOrder, s) {
			return fmt.Errorf("no step %s (steps: %s)", s, strings.Join(installOrder, " "))
		}
	}
	for _, s := range steps {
		if ask && !r.confirm("Run the "+s+" step?") {
			r.info("skipped " + s)
			continue
		}
		if err := r.runInstall(s); err != nil {
			return err
		}
	}
	fmt.Fprint(out, "\nDone.\n")
	return nil
}

// Uninstall undoes the named steps, or every step last first.
func Uninstall(args []string, in io.Reader, out, errw io.Writer) error {
	yes, steps, help, err := parseArgs(args)
	if err != nil {
		return err
	}
	if help {
		fmt.Fprint(out, UninstallUsage)
		return nil
	}
	r, err := newRunner(in, out, errw, yes)
	if err != nil {
		return err
	}
	ask := false
	if len(steps) == 0 {
		steps = append([]string{}, uninstallOrder...)
		ask = true
	}
	for _, s := range steps {
		if !known(uninstallOrder, s) {
			return fmt.Errorf("no step %s (steps: %s)", s, strings.Join(uninstallOrder, " "))
		}
	}
	for _, s := range steps {
		if ask && !r.confirm("Undo the "+s+" step?") {
			r.info("skipped " + s)
			continue
		}
		if err := r.runUninstall(s); err != nil {
			return err
		}
	}
	fmt.Fprint(out, "\nDone.\n")
	return nil
}

// ApplyChanged re-runs hooks, service and shell when their embedded files
// no longer match what was last installed, then restarts the service (Q79).
func ApplyChanged(out, errw io.Writer) error {
	r, err := newRunner(nil, out, errw, true)
	if err != nil {
		return err
	}
	rec := r.loadInstalled()
	if rec.Hooks != sumEmbed(contrib.Hooks) {
		r.info("hooks embed changed; re-running the hooks step")
		if err := r.stepHooks(); err != nil {
			return err
		}
	}
	if rec.Service != sumEmbed(contrib.ServiceUnit) {
		r.info("service embed changed; re-running the service step")
		if err := r.stepService(); err != nil {
			return err
		}
	}
	if rec.Shell != sumEmbed(contrib.ClaudeTmux) {
		r.info("shell embed changed; re-running the shell step")
		if err := r.stepShell(); err != nil {
			return err
		}
	}
	return r.restartService()
}

func (r *runner) runInstall(step string) error {
	switch step {
	case "binary":
		return r.stepBinary()
	case "token":
		return r.stepToken()
	case "pair":
		return r.stepPair()
	case "service":
		return r.stepService()
	case "hooks":
		return r.stepHooks()
	case "mcp":
		return r.stepMCP()
	case "shell":
		return r.stepShell()
	default:
		return fmt.Errorf("no step %s", step)
	}
}

func (r *runner) runUninstall(step string) error {
	switch step {
	case "shell":
		return r.undoShell()
	case "mcp":
		return r.undoMCP()
	case "hooks":
		return r.undoHooks()
	case "service":
		return r.undoService()
	case "config":
		return r.undoConfig()
	case "binary":
		return r.undoBinary()
	default:
		return fmt.Errorf("no step %s", step)
	}
}

func parseArgs(args []string) (yes bool, steps []string, help bool, err error) {
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			yes = true
		case "-h", "--help":
			help = true
		case "--apply":
			return false, nil, false, fmt.Errorf("unknown option %s", a)
		default:
			if strings.HasPrefix(a, "-") {
				return false, nil, false, fmt.Errorf("unknown option %s", a)
			}
			steps = append(steps, a)
		}
	}
	return yes, steps, help, nil
}

func known(all []string, s string) bool {
	for _, x := range all {
		if x == s {
			return true
		}
	}
	return false
}
