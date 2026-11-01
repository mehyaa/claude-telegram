package launch

import "strings"

// Script is the login-shell command tmux runs (Q72). A new session uses
// --name and never --resume. A resumed session uses --resume and never --name.
func Script(resumeID, name, prompt string) string {
	if resumeID != "" {
		return "exec claude --resume " + quote(resumeID)
	}
	s := "exec claude --name " + quote(name)
	if prompt != "" {
		s += " " + quote(prompt)
	}
	return s
}

// Command is `tmux new-session` on the default server (Q72).
func Command(shell, session, window, dir, script string) []string {
	if shell == "" {
		shell = "/bin/bash"
	}
	window = strings.ReplaceAll(window, "\n", " ")
	return []string{"new-session", "-d", "-s", session, "-n", window, "-c", dir, "--", shell, "-lic", script}
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
