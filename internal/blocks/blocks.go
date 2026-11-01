// Package blocks finds the questions a model asks in its reply (Q22), in
// the form ctg mcp's instructions teach (Q23):
//
//	❓ **Q16** - **Storage**: Use SQLite or Postgres?
//	➡️ SQLite: one file, no server.
//
// It is lenient about the rest: bold or not, a dash or a colon, no number,
// no title, a question or recommendation over several lines.
package blocks

import (
	"regexp"
	"strings"
)

// Block is one question.
type Block struct {
	Label          string // "Q16"; empty when the block has no number
	Title          string
	Question       string
	Recommendation string
}

var labelRE = regexp.MustCompile(`^(Q\d+)\b\s*[-–—:.]?\s*(.*)$`)

// clean drops markdown emphasis and surrounding space.
func clean(s string) string {
	s = strings.NewReplacer("**", "", "__", "").Replace(s)
	return strings.TrimSpace(s)
}

// cutMark reports whether line starts with one of the marks, and returns the
// rest.
func cutMark(line string, marks ...string) (string, bool) {
	t := strings.TrimSpace(line)
	t = strings.TrimLeft(t, "-*> ") // list bullets and quotes
	for _, m := range marks {
		if rest, ok := strings.CutPrefix(t, m); ok {
			return strings.TrimSpace(strings.TrimPrefix(rest, "\ufe0f")), true
		}
	}
	return "", false
}

func question(line string) (string, bool) { return cutMark(line, "❓", "❔") }

func recommendation(line string) (string, bool) {
	rest, ok := cutMark(line, "➡️", "➡")
	if !ok {
		return "", false
	}
	// "➡️ **Recommendation:** …"
	if r := clean(rest); strings.HasPrefix(strings.ToLower(r), "recommendation:") {
		return strings.TrimSpace(r[len("recommendation:"):]), true
	}
	return clean(rest), true
}

// Parse finds the question blocks in text. ❓ inside code blocks doesn't
// count.
func Parse(text string) []Block {
	var out []Block
	var cur *Block
	inRec := false
	inCode := false
	blank := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			cur = nil
			continue
		}
		if inCode {
			continue
		}
		if rest, ok := question(line); ok {
			out = append(out, header(rest))
			cur, inRec, blank = &out[len(out)-1], false, false
			continue
		}
		if cur == nil {
			continue
		}
		if rest, ok := recommendation(line); ok {
			cur.Recommendation = rest
			inRec, blank = true, false
			continue
		}
		t := clean(line)
		switch {
		case t == "":
			blank = true
		case blank:
			// A blank line ends the block, unless the recommendation
			// follows it; that line was handled above.
			cur = nil
		case inRec:
			cur.Recommendation += " " + t
		default:
			cur.Question += " " + t
		}
	}
	for i := range out {
		out[i].Question = strings.TrimSpace(out[i].Question)
		out[i].Recommendation = strings.TrimSpace(out[i].Recommendation)
	}
	return out
}

// header reads the first line of a block, after the ❓.
func header(rest string) Block {
	var b Block
	rest = clean(rest)
	if m := labelRE.FindStringSubmatch(rest); m != nil {
		b.Label, rest = m[1], m[2]
	}
	if title, q, ok := strings.Cut(rest, ": "); ok && len([]rune(title)) <= 60 && !strings.ContainsAny(title, "?.") {
		b.Title, b.Question = strings.TrimSpace(title), q
	} else {
		b.Question = rest
	}
	return b
}
