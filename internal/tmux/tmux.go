// Package tmux reads and types into one tmux pane.
//
// A Pane names its server's socket explicitly, taken from the session's own
// $TMUX, so ctg never acts on a server or pane it wasn't told about.
package tmux

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Pane is one pane on one tmux server.
type Pane struct {
	Socket string // the server's socket path, the first field of $TMUX
	ID     string // the pane ID, $TMUX_PANE, e.g. "%12"
}

// SocketFromEnv returns the socket path in a $TMUX value
// ("/tmp/tmux-1000/default,1234,0").
func SocketFromEnv(tmuxEnv string) string {
	sock, _, _ := strings.Cut(tmuxEnv, ",")
	return sock
}

const timeout = 2 * time.Second

func (p Pane) run(stdin []byte, args ...string) ([]byte, error) {
	if p.Socket == "" || !strings.HasPrefix(p.ID, "%") {
		return nil, fmt.Errorf("tmux: no socket or pane ID (%q, %q)", p.Socket, p.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", p.Socket}, args...)...)
	// Outside tmux's own environment, so nothing is inherited from the
	// pane ctg itself might run in.
	cmd.Env = withoutTMUX(os.Environ())
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func withoutTMUX(env []string) []string {
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(e, "TMUX=") && !strings.HasPrefix(e, "TMUX_PANE=") {
			out = append(out, e)
		}
	}
	return out
}

// Capture returns the visible screen as plain text.
func (p Pane) Capture() (string, error) {
	out, err := p.run(nil, "capture-pane", "-p", "-t", p.ID)
	return string(out), err
}

// Keys presses named keys ("Enter", "Down", "Escape", "1").
func (p Pane) Keys(keys ...string) error {
	_, err := p.run(nil, append([]string{"send-keys", "-t", p.ID}, keys...)...)
	return err
}

// Literal types text as it is, without reading key names.
func (p Pane) Literal(text string) error {
	if text == "" {
		return nil
	}
	_, err := p.run(nil, "send-keys", "-l", "-t", p.ID, "--", text)
	return err
}

// Paste puts text in the pane as a bracketed paste, so newlines stay inside
// one prompt instead of submitting it.
func (p Pane) Paste(text string) error {
	b := make([]byte, 6)
	rand.Read(b)
	buf := "ctg-" + hex.EncodeToString(b)
	if _, err := p.run([]byte(text), "load-buffer", "-b", buf, "-"); err != nil {
		return err
	}
	_, err := p.run(nil, "paste-buffer", "-p", "-d", "-b", buf, "-t", p.ID)
	return err
}

// ClientRecent reports whether a client of the pane's tmux session was
// active within window (Q58). No client, or a tmux error, is not active.
func ClientRecent(socket, pane string, window time.Duration, now time.Time) bool {
	if socket == "" || !strings.HasPrefix(pane, "%") {
		return false
	}
	p := Pane{Socket: socket, ID: pane}
	out, err := p.run(nil, "list-panes", "-t", pane, "-F", "#{session_name}")
	if err != nil {
		return false
	}
	session, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if session == "" {
		return false
	}
	out, err = p.run(nil, "list-clients", "-t", session, "-F", "#{client_activity}")
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		sec, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
		if err != nil {
			continue
		}
		if now.Sub(time.Unix(sec, 0)) <= window {
			return true
		}
	}
	return false
}

// Alive reports whether the pane still exists.
func (p Pane) Alive() bool {
	out, err := p.run(nil, "display-message", "-p", "-t", p.ID, "#{pane_id}")
	return err == nil && strings.TrimSpace(string(out)) == p.ID
}

var ErrNoTmux = errors.New("tmux isn't installed")

// Check reports whether the tmux binary is there.
func Check() error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return ErrNoTmux
	}
	return nil
}

// Default runs tmux on the default server. TMUX and TMUX_PANE are removed
// so a server ctg itself sits in is not the one that gets the command.
// The bot starts sessions this way (Q72).
func Default(args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("tmux: no command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.Env = withoutTMUX(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
