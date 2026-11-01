package transcript

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
)

// Context is the context use of the last main-conversation assistant
// message, as a percentage of the model's window (Q83). ok is false when
// the transcript has no usage yet.
func Context(path string, pid int) (pct int, ok bool) {
	used, model, ok := lastUsage(path)
	if !ok || model == "" {
		return 0, false
	}
	window := contextWindow(model)
	if n := procEnv(pid, "CLAUDE_CODE_MAX_CONTEXT_TOKENS"); n != "" {
		if v, err := strconv.Atoi(n); err == nil && v > 0 {
			window = v
		}
	}
	if window <= 0 {
		return 0, false
	}
	pct = int(math.Round(float64(used) / float64(window) * 100))
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct, true
}

// Mode is the session's current permission mode: manual, accept, plan,
// auto or bypass. Empty when the transcript doesn't say.
func Mode(path string) string {
	raw := tailText(path, 256<<10)
	mode := ""
	for _, line := range strings.Split(raw, "\n") {
		if v := jsonString(line, "permissionMode"); v != "" {
			mode = v
		}
		if v := jsonString(line, "permission_mode"); v != "" {
			mode = v
		}
	}
	return canonMode(mode)
}

func canonMode(v string) string {
	switch v {
	case "default", "manual":
		return "manual"
	case "acceptEdits", "accept":
		return "accept"
	case "plan":
		return "plan"
	case "auto":
		return "auto"
	case "bypassPermissions", "bypass":
		return "bypass"
	default:
		return ""
	}
}

func lastUsage(path string) (used int, model string, ok bool) {
	raw := tailText(path, 512<<10)
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		if !strings.Contains(line, `"usage"`) {
			continue
		}
		var row struct {
			Type        string `json:"type"`
			IsSidechain bool   `json:"isSidechain"`
			AgentID     string `json:"agentId"`
			Message     struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					Input         int `json:"input_tokens"`
					CacheCreation int `json:"cache_creation_input_tokens"`
					CacheRead     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.Type != "assistant" {
			continue
		}
		if row.IsSidechain || row.AgentID != "" {
			continue
		}
		id := row.Message.ID
		if id != "" && seen[id] {
			continue
		}
		if id != "" {
			seen[id] = true
		}
		u := row.Message.Usage
		used = u.Input + u.CacheCreation + u.CacheRead
		if row.Message.Model != "" {
			model = row.Message.Model
		}
		ok = true
	}
	return used, model, ok
}

// contextWindow is the model's window from the table built into ctg (Q83).
// [1m] in the model name is the 1M variant of a 200k model.
func contextWindow(model string) int {
	m := strings.ToLower(model)
	if strings.Contains(m, "[1m]") {
		return 1_000_000
	}
	switch {
	case strings.Contains(m, "haiku-4-5"), strings.Contains(m, "haiku-4.5"),
		strings.Contains(m, "opus-4-0"), strings.Contains(m, "opus-4.0"),
		strings.Contains(m, "opus-4-1"), strings.Contains(m, "opus-4.1"),
		strings.Contains(m, "opus-4-5"), strings.Contains(m, "opus-4.5"),
		strings.Contains(m, "opus-4-6"), strings.Contains(m, "opus-4.6"),
		strings.Contains(m, "sonnet-4"):
		return 200_000
	case strings.Contains(m, "opus-4-7"), strings.Contains(m, "opus-4.7"),
		strings.Contains(m, "opus-4-8"), strings.Contains(m, "opus-4.8"),
		strings.Contains(m, "opus-5"),
		strings.Contains(m, "sonnet-5"),
		strings.Contains(m, "haiku-5"),
		strings.Contains(m, "fable"):
		return 1_000_000
	default:
		// An unknown model uses 200k, so the percentage isn't too small.
		return 200_000
	}
}

func tailText(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	off := int64(0)
	if fi.Size() > n {
		off = fi.Size() - n
	}
	b := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(b, off); err != nil {
		return ""
	}
	if i := bytes.IndexByte(b, '\n'); off > 0 && i >= 0 {
		b = b[i+1:]
	}
	return string(b)
}

func procEnv(pid int, key string) string {
	if pid <= 0 {
		return ""
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, e := range bytes.Split(b, []byte{0}) {
		if strings.HasPrefix(string(e), prefix) {
			return string(e)[len(prefix):]
		}
	}
	return ""
}

func jsonString(line, field string) string {
	key := `"` + field + `":"`
	i := strings.Index(line, key)
	if i < 0 {
		key = `"` + field + `": "`
		i = strings.Index(line, key)
		if i < 0 {
			return ""
		}
	}
	rest := line[i+len(key):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}
