// Package config finds ctg's files and reads the ones the owner provides.
//
// The bot token and the paired chat ID live under the XDG config dir
// (~/.config/claude-telegram, Q30), state and the audit log under the XDG
// state dir (~/.local/state/claude-telegram, Q24), and the daemon's socket
// under the runtime dir.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Paths says where ctg keeps its files.
type Paths struct {
	ConfigDir string
	StateDir  string
	Socket    string
}

// Default returns the paths for the current user, following XDG_CONFIG_HOME,
// XDG_STATE_HOME and XDG_RUNTIME_DIR. CTG_HOME, when set, puts everything
// under one directory instead, for tests and probes.
func Default() (Paths, error) {
	if dir := os.Getenv("CTG_HOME"); dir != "" {
		return Paths{
			ConfigDir: filepath.Join(dir, "config"),
			StateDir:  filepath.Join(dir, "state"),
			Socket:    filepath.Join(dir, "daemon.sock"),
		}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	p := Paths{
		ConfigDir: filepath.Join(cfg, "claude-telegram"),
		StateDir:  filepath.Join(state, "claude-telegram"),
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		p.Socket = filepath.Join(rt, "claude-telegram", "daemon.sock")
	} else {
		p.Socket = filepath.Join(p.StateDir, "daemon.sock")
	}
	return p, nil
}

func (p Paths) TokenFile() string     { return filepath.Join(p.ConfigDir, "token") }
func (p Paths) ChatFile() string      { return filepath.Join(p.ConfigDir, "chat_id") }
func (p Paths) StateFile() string     { return filepath.Join(p.StateDir, "state.json") }
func (p Paths) HeartbeatFile() string { return filepath.Join(p.StateDir, "heartbeat") }
func (p Paths) SpoolFile() string     { return filepath.Join(p.StateDir, "spool.jsonl") }
func (p Paths) AuditFile() string     { return filepath.Join(p.StateDir, "audit.jsonl") }
func (p Paths) MessagesFile() string  { return filepath.Join(p.StateDir, "messages.jsonl") }

// ReadToken reads the bot token. It refuses a token file that anyone but the
// owner can read.
func ReadToken(p Paths) (string, error) {
	path := p.TokenFile()
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no bot token: put it in %s with mode 0600", path)
	}
	if err != nil {
		return "", err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is mode %04o; run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return token, nil
}

// ReadChat returns the paired chat ID, or 0 when no chat is paired.
func ReadChat(p Paths) (int64, error) {
	b, err := os.ReadFile(p.ChatFile())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", p.ChatFile(), err)
	}
	return id, nil
}

// WriteChat saves the paired chat ID.
func WriteChat(p Paths, id int64) error {
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		return err
	}
	return WriteFileAtomic(p.ChatFile(), []byte(strconv.FormatInt(id, 10)+"\n"))
}

// WriteFileAtomic writes data to path with mode 0600, through a temporary
// file in the same directory, so readers never see half a file.
func WriteFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
