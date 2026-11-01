// Package page cuts long messages into pages for Telegram (Q6): about
// 1,500 characters each, at line breaks, without splitting a code block.
package page

import "strings"

const (
	// Size is the length a page aims for.
	Size = 1500
	// codeMax is how long a code block may be and still get a page of its
	// own, whole; Telegram's limit is 4096.
	codeMax = 3500
)

func runes(s string) int { return len([]rune(s)) }

type block struct {
	lines []string
	code  bool
	fence string // the opening fence line of a code block
}

// split separates prose from ``` code blocks. An unclosed block runs to the
// end.
func split(text string) []block {
	var out []block
	var cur *block
	for _, l := range strings.Split(text, "\n") {
		isFence := strings.HasPrefix(strings.TrimSpace(l), "```")
		switch {
		case cur != nil && cur.code:
			cur.lines = append(cur.lines, l)
			if isFence && len(cur.lines) > 1 {
				cur = nil
			}
		case isFence:
			out = append(out, block{lines: []string{l}, code: true, fence: strings.TrimSpace(l)})
			cur = &out[len(out)-1]
		default:
			if cur == nil || cur.code {
				out = append(out, block{})
				cur = &out[len(out)-1]
			}
			cur.lines = append(cur.lines, l)
		}
	}
	return out
}

// Pages cuts text into pages of about Size characters. A text that fits is
// one page.
func Pages(text string) []string {
	text = strings.TrimSpace(text)
	if runes(text) <= Size {
		return []string{text}
	}
	var pages []string
	var cur []string
	n := 0
	flush := func() {
		if p := strings.TrimSpace(strings.Join(cur, "\n")); p != "" {
			pages = append(pages, p)
		}
		cur, n = nil, 0
	}
	add := func(s string) {
		cur = append(cur, s)
		n += runes(s) + 1
	}
	for _, b := range split(text) {
		if !b.code {
			for _, l := range b.lines {
				for _, piece := range hardWrap(l, Size) {
					if n+runes(piece) > Size {
						flush()
					}
					add(piece)
				}
			}
			continue
		}
		whole := strings.Join(b.lines, "\n")
		switch {
		case n+runes(whole) <= Size:
			add(whole)
		case runes(whole) <= codeMax:
			flush()
			add(whole)
			flush()
		default:
			flush()
			for _, part := range splitCode(b) {
				add(part)
				flush()
			}
		}
	}
	flush()
	return pages
}

// splitCode cuts a code block too long for one page at line breaks, closing
// and reopening the fence on each part.
func splitCode(b block) []string {
	body := b.lines[1:]
	closed := len(body) > 0 && strings.HasPrefix(strings.TrimSpace(body[len(body)-1]), "```")
	if closed {
		body = body[:len(body)-1]
	}
	var parts []string
	var cur []string
	n := 0
	flush := func() {
		if len(cur) > 0 {
			parts = append(parts, b.fence+"\n"+strings.Join(cur, "\n")+"\n```")
		}
		cur, n = nil, 0
	}
	room := codeMax - runes(b.fence) - 8
	for _, l := range body {
		for _, piece := range hardWrap(l, room) {
			if n+runes(piece) > room {
				flush()
			}
			cur = append(cur, piece)
			n += runes(piece) + 1
		}
	}
	flush()
	return parts
}

// hardWrap cuts a line longer than n, at a space where it can.
func hardWrap(l string, n int) []string {
	r := []rune(l)
	var out []string
	for len(r) > n {
		cut := n
		for i := n; i > n/2; i-- {
			if r[i] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = []rune(strings.TrimLeft(string(r[cut:]), " "))
	}
	return append(out, string(r))
}
