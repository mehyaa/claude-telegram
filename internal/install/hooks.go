package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

// ctgHook matches a command that runs `ctg hook`, quoted or not, so an old
// install at another path is still recognised as ours.
var ctgHook = regexp.MustCompile(`ctg"? hook `)

func mergeHooks(current, ours []byte, bin string) ([]byte, error) {
	ours = bytes.ReplaceAll(ours, []byte("$HOME/.local/bin/ctg"), []byte(bin))
	cur, err := decodeObject(current)
	if err != nil {
		return nil, err
	}
	var ourFile struct {
		Hooks map[string][]any `json:"hooks"`
	}
	if err := json.Unmarshal(ours, &ourFile); err != nil {
		return nil, fmt.Errorf("embedded hooks.json: %w", err)
	}
	hooks, _ := cur["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for ev, ourEntries := range ourFile.Hooks {
		var kept []any
		if existing, ok := hooks[ev].([]any); ok {
			for _, e := range existing {
				if !isCTGEntry(e) {
					kept = append(kept, e)
				}
			}
		}
		for _, e := range ourEntries {
			kept = append(kept, e)
		}
		hooks[ev] = kept
	}
	cur["hooks"] = hooks
	return marshalJSON(cur)
}

func removeHooks(current []byte) ([]byte, error) {
	cur, err := decodeObject(current)
	if err != nil {
		return nil, err
	}
	hooks, ok := cur["hooks"].(map[string]any)
	if !ok {
		return marshalJSON(cur)
	}
	for ev, v := range hooks {
		entries, _ := v.([]any)
		var kept []any
		for _, e := range entries {
			if !isCTGEntry(e) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	if len(hooks) == 0 {
		delete(cur, "hooks")
	} else {
		cur["hooks"] = hooks
	}
	return marshalJSON(cur)
}

func isCTGEntry(e any) bool {
	m, ok := e.(map[string]any)
	if !ok {
		return false
	}
	hooks, _ := m["hooks"].([]any)
	for _, h := range hooks {
		hm, _ := h.(map[string]any)
		cmd, _ := hm["command"].(string)
		if ctgHook.MatchString(cmd) {
			return true
		}
	}
	return false
}

func decodeObject(b []byte) (map[string]any, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var cur map[string]any
	if err := json.Unmarshal(b, &cur); err != nil {
		return nil, err
	}
	if cur == nil {
		cur = map[string]any{}
	}
	return cur, nil
}

func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
