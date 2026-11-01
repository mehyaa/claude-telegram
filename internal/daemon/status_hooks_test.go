package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mehyaa/claude-telegram/contrib"
	"github.com/mehyaa/claude-telegram/internal/config"
)

// The installer writes a quoted binary path. On disk the quote is escaped,
// so the settings text contains ctg\" hook rather than ctg hook.
func TestInstallLinesQuotedHook(t *testing.T) {
	settings, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"PermissionRequest": []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": `"/home/user/.local/bin/ctg" hook PermissionRequest`,
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := installLinesFor(t, settings, true)
	if !strings.Contains(got, "hooks: current\n") {
		t.Fatalf("install lines:\n%s", got)
	}
}

func TestInstallLinesUnquotedHook(t *testing.T) {
	settings := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/old/path/ctg hook Stop"}]}]}}`)
	got := installLinesFor(t, settings, true)
	if !strings.Contains(got, "hooks: current\n") {
		t.Fatalf("install lines:\n%s", got)
	}
}

func TestInstallLinesNoHooks(t *testing.T) {
	got := installLinesFor(t, []byte("{}\n"), true)
	if !strings.Contains(got, "hooks: not installed\n") {
		t.Fatalf("install lines:\n%s", got)
	}
}

func installLinesFor(t *testing.T, settings []byte, recordHash bool) string {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), settings, 0o644); err != nil {
		t.Fatal(err)
	}
	if recordHash {
		sum := sha256.Sum256(contrib.Hooks)
		rec := "{\"hooks\":\"" + hex.EncodeToString(sum[:]) + "\"}\n"
		if err := os.WriteFile(filepath.Join(state, "installed.json"), []byte(rec), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{cfg: Config{
		Home:  home,
		Paths: config.Paths{StateDir: state},
	}}
	return d.installLines()
}
