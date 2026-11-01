package install

import (
	"os"
	"strings"
)

// envValue returns KEY's value from a .env file without sourcing it, so
// nothing in the file runs. A quoted value is taken as it is; an unquoted one
// loses a trailing " # comment" and surrounding space. The last matching line
// wins.
func envValue(path, key string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var found string
	ok := false
	prefix := key + "="
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "export ") {
			trimmed = strings.TrimSpace(trimmed[len("export "):])
		}
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		val := trimmed[len(prefix):]
		switch {
		case strings.HasPrefix(val, `"`):
			if i := strings.Index(val[1:], `"`); i >= 0 {
				val = val[1 : 1+i]
			}
		case strings.HasPrefix(val, `'`):
			if i := strings.Index(val[1:], `'`); i >= 0 {
				val = val[1 : 1+i]
			}
		default:
			if i := strings.Index(val, " #"); i >= 0 {
				val = val[:i]
			} else if i := strings.Index(val, "\t#"); i >= 0 {
				val = val[:i]
			}
			val = strings.TrimSpace(val)
		}
		if val != "" {
			found = val
			ok = true
		}
	}
	return found, ok
}
