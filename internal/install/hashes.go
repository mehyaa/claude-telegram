package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"

	"github.com/mehyaa/claude-telegram/contrib"
)

// recorded is the sha256 of each embedded file as of the last install of
// that step. ctg update re-runs a step when the embed no longer matches (Q79).
type recorded struct {
	Hooks   string `json:"hooks,omitempty"`
	Service string `json:"service,omitempty"`
	Shell   string `json:"shell,omitempty"`
}

// StepState says whether a recorded install hash matches the embedded file.
// It is "current", "stale", or "missing".
func StepState(installedFile string) (hooks, service, shell string) {
	b, err := os.ReadFile(installedFile)
	if err != nil {
		return "missing", "missing", "missing"
	}
	var rec recorded
	if json.Unmarshal(b, &rec) != nil {
		return "missing", "missing", "missing"
	}
	return oneState(rec.Hooks, sumEmbed(contrib.Hooks)), oneState(rec.Service, sumEmbed(contrib.ServiceUnit)), oneState(rec.Shell, sumEmbed(contrib.ClaudeTmux))
}

func oneState(got, want string) string {
	switch {
	case got == "":
		return "missing"
	case got == want:
		return "current"
	default:
		return "stale"
	}
}

func sumEmbed(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (r *runner) loadInstalled() recorded {
	b, err := os.ReadFile(r.p.InstalledFile())
	if err != nil {
		return recorded{}
	}
	var rec recorded
	_ = json.Unmarshal(b, &rec)
	return rec
}

func (r *runner) remember(which string) error {
	if err := os.MkdirAll(r.p.StateDir, 0o700); err != nil {
		return err
	}
	rec := r.loadInstalled()
	switch which {
	case "hooks":
		rec.Hooks = sumEmbed(contrib.Hooks)
	case "service":
		rec.Service = sumEmbed(contrib.ServiceUnit)
	case "shell":
		rec.Shell = sumEmbed(contrib.ClaudeTmux)
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(r.p.InstalledFile(), append(b, '\n'), 0o600)
}
