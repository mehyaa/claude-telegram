package install

import (
	"os"
	"path/filepath"
)

// Paths is where install writes, matching the old scripts/lib.sh layout.
type Paths struct {
	Home      string
	BinDir    string
	Bin       string
	ConfigDir string
	StateDir  string
	ShareDir  string
	UnitDir   string
	ClaudeDir string
	Settings  string
	Bashrc    string
	EnvFile   string
}

// Resolve returns the install paths for the current user. CTG_BIN_DIR,
// CTG_BASHRC, CTG_ENV_FILE and CLAUDE_CONFIG_DIR override the defaults, for
// tests and for an unusual layout.
func Resolve() (Paths, error) {
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
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	binDir := os.Getenv("CTG_BIN_DIR")
	if binDir == "" {
		binDir = filepath.Join(home, ".local", "bin")
	}
	claudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeDir == "" {
		claudeDir = filepath.Join(home, ".claude")
	}
	bashrc := os.Getenv("CTG_BASHRC")
	if bashrc == "" {
		bashrc = filepath.Join(home, ".bashrc")
	}
	env := os.Getenv("CTG_ENV_FILE")
	if env == "" {
		env = ".env"
	}
	return Paths{
		Home:      home,
		BinDir:    binDir,
		Bin:       filepath.Join(binDir, "ctg"),
		ConfigDir: filepath.Join(cfg, "claude-telegram"),
		StateDir:  filepath.Join(state, "claude-telegram"),
		ShareDir:  filepath.Join(data, "claude-telegram"),
		UnitDir:   filepath.Join(cfg, "systemd", "user"),
		ClaudeDir: claudeDir,
		Settings:  filepath.Join(claudeDir, "settings.json"),
		Bashrc:    bashrc,
		EnvFile:   env,
	}, nil
}

func (p Paths) TokenFile() string     { return filepath.Join(p.ConfigDir, "token") }
func (p Paths) ChatFile() string      { return filepath.Join(p.ConfigDir, "chat_id") }
func (p Paths) InstalledFile() string { return filepath.Join(p.StateDir, "installed.json") }
func (p Paths) UnitFile() string      { return filepath.Join(p.UnitDir, "ctg.service") }
func (p Paths) WrapperFile() string   { return filepath.Join(p.ShareDir, "claude-tmux.bash") }
func (p Paths) PairLog() string       { return filepath.Join(p.StateDir, "pair.log") }
