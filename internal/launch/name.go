// Package launch turns a name and a folder into a tmux session, and reads
// Claude Code's past sessions the way /resume does.
package launch

import (
	"regexp"
	"strings"
	"unicode"
)

// sessionNameRE is ^[a-z][a-z0-9]*([-_][a-z0-9]+)*$ (Q37).
var sessionNameRE = regexp.MustCompile(`^[a-z][a-z0-9]*([-_][a-z0-9]+)*$`)

// SessionName converts a word the way the shell wrapper does (Q37, Q44).
// A word that already matches is kept. Anything else is split on camelCase,
// lowercased, and squeezed. A result that still doesn't match is refused.
// A leading "-" is never a name.
func SessionName(word string) (string, bool) {
	word = strings.TrimSpace(word)
	if word == "" || strings.HasPrefix(word, "-") {
		return "", false
	}
	if sessionNameRE.MatchString(word) && len(word) <= 40 {
		return word, true
	}
	for _, r := range word {
		if !nameRune(r) {
			return "", false
		}
	}
	word = strings.ToLower(squeeze(splitCamel(word)))
	if sessionNameRE.MatchString(word) && len(word) <= 40 {
		return word, true
	}
	return "", false
}

func nameRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_'
}

// splitCamel mirrors the wrapper's two sed substitutions, in the same order.
func splitCamel(s string) string {
	r := []rune(s)
	var b []rune
	for i, c := range r {
		if i > 0 && lowerOrDigit(r[i-1]) && unicode.IsUpper(c) {
			b = append(b, '-')
		}
		b = append(b, c)
	}
	r = b
	b = nil
	for i := 0; i < len(r); i++ {
		if i > 0 && i+1 < len(r) && unicode.IsUpper(r[i-1]) && unicode.IsUpper(r[i]) && unicode.IsLower(r[i+1]) {
			b = append(b, '-')
		}
		b = append(b, r[i])
	}
	return string(b)
}

func lowerOrDigit(r rune) bool {
	return unicode.IsLower(r) || unicode.IsDigit(r)
}

// squeeze keeps the first of a run of separators and trims the ends,
// matching s/([-_])[-_]+/\1/g and s/^[-_]+|[-_]+$//g.
func squeeze(s string) string {
	var b strings.Builder
	prev := rune(0)
	for _, r := range s {
		sep := r == '-' || r == '_'
		if sep && (prev == '-' || prev == '_') {
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	return strings.Trim(b.String(), "-_")
}
