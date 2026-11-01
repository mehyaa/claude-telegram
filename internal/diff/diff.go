// Package diff builds the unified diffs shown for Edit and Write (Q84).
package diff

import (
	"encoding/json"
	"fmt"
	"strings"
)

const contextLines = 3

// Unified is a unified diff of oldText and newText with three lines of
// context. An empty oldText is a new file.
func Unified(path, oldText, newText string) string {
	if path == "" {
		path = "file"
	}
	a, b := splitLines(oldText), splitLines(newText)
	oldName, newName := "a/"+path, "b/"+path
	if oldText == "" {
		oldName = "/dev/null"
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "--- %s\n+++ %s\n", oldName, newName)
	for _, h := range hunks(a, b) {
		fmt.Fprintf(&bld, "@@ -%d,%d +%d,%d @@\n", h.aLine, h.aCount, h.bLine, h.bCount)
		for _, l := range h.lines {
			bld.WriteString(l)
			bld.WriteByte('\n')
		}
	}
	return bld.String()
}

// OfTool is the diff for an Edit, MultiEdit or Write tool input.
func OfTool(name string, raw []byte) (string, bool) {
	var in struct {
		FilePath  string `json:"file_path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
		Content   string `json:"content"`
		Edits     []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", false
	}
	path := in.FilePath
	switch name {
	case "Write":
		return Unified(path, "", in.Content), true
	case "Edit":
		return Unified(path, in.OldString, in.NewString), true
	case "MultiEdit":
		var b strings.Builder
		for _, e := range in.Edits {
			b.WriteString(Unified(path, e.OldString, e.NewString))
		}
		if b.Len() == 0 {
			return Unified(path, in.OldString, in.NewString), true
		}
		return b.String(), true
	default:
		return "", false
	}
}

type patchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// OfResult builds a unified diff from a tool_result's toolUseResult payload
// when it carries bashEditDiff (from Bash file edits) or structuredPatch.
func OfResult(raw []byte) (body, firstPath string, plus, minus int, ok bool) {
	if len(raw) == 0 {
		return "", "", 0, 0, false
	}
	var res struct {
		FilePath        string      `json:"filePath"`
		StructuredPatch []patchHunk `json:"structuredPatch"`
		BashEditDiff    struct {
			Files []struct {
				FilePath string      `json:"filePath"`
				Created  bool        `json:"created"`
				Hunks    []patchHunk `json:"hunks"`
			} `json:"files"`
		} `json:"bashEditDiff"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return "", "", 0, 0, false
	}
	var bld strings.Builder
	writeFileHunks := func(path string, created bool, hs []patchHunk) {
		if len(hs) == 0 {
			return
		}
		if path == "" {
			path = "file"
		}
		if firstPath == "" {
			firstPath = path
		}
		oldName, newName := "a/"+path, "b/"+path
		if created {
			oldName = "/dev/null"
		}
		if bld.Len() > 0 && !strings.HasSuffix(bld.String(), "\n") {
			bld.WriteByte('\n')
		}
		fmt.Fprintf(&bld, "--- %s\n+++ %s\n", oldName, newName)
		for _, h := range hs {
			aCount, bCount := h.OldLines, h.NewLines
			if aCount == 0 && bCount == 0 {
				for _, l := range h.Lines {
					switch {
					case strings.HasPrefix(l, "+"):
						bCount++
					case strings.HasPrefix(l, "-"):
						aCount++
					default:
						aCount++
						bCount++
					}
				}
			}
			aStart, bStart := h.OldStart, h.NewStart
			if aStart == 0 && aCount > 0 {
				aStart = 1
			}
			if bStart == 0 && bCount > 0 {
				bStart = 1
			}
			fmt.Fprintf(&bld, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
			for _, l := range h.Lines {
				bld.WriteString(l)
				bld.WriteByte('\n')
				switch {
				case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
					plus++
				case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
					minus++
				}
			}
		}
	}
	if len(res.BashEditDiff.Files) > 0 {
		for _, f := range res.BashEditDiff.Files {
			writeFileHunks(f.FilePath, f.Created, f.Hunks)
		}
	} else if len(res.StructuredPatch) > 0 {
		writeFileHunks(res.FilePath, false, res.StructuredPatch)
	}
	if bld.Len() == 0 {
		return "", "", 0, 0, false
	}
	return bld.String(), firstPath, plus, minus, true
}

// Stats counts the added and removed lines in a unified diff string.
func Stats(unified string) (plus, minus int) {
	for _, l := range strings.Split(unified, "\n") {
		switch {
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			plus++
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			minus++
		}
	}
	return plus, minus
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

type hunk struct {
	aLine, aCount int
	bLine, bCount int
	lines         []string
}

func hunks(a, b []string) []hunk {
	ops := lcs(a, b)
	// ops[i] is ' ' equal, '-' delete, '+' insert, in order.
	type row struct {
		mark byte
		text string
		ai   int
		bi   int
	}
	var rows []row
	ai, bi := 0, 0
	for _, op := range ops {
		switch op {
		case ' ':
			rows = append(rows, row{' ', a[ai], ai, bi})
			ai++
			bi++
		case '-':
			rows = append(rows, row{'-', a[ai], ai, bi})
			ai++
		case '+':
			rows = append(rows, row{'+', b[bi], ai, bi})
			bi++
		}
	}
	var hs []hunk
	i := 0
	for i < len(rows) {
		if rows[i].mark == ' ' {
			i++
			continue
		}
		start := i - contextLines
		if start < 0 {
			start = 0
		}
		end := i + 1
		for end < len(rows) {
			if rows[end].mark != ' ' {
				end++
				continue
			}
			// Look ahead for another change within 2*context.
			gap := 0
			j := end
			for j < len(rows) && rows[j].mark == ' ' && gap < contextLines*2 {
				j++
				gap++
			}
			if j < len(rows) && rows[j].mark != ' ' && gap <= contextLines*2 {
				end = j + 1
				continue
			}
			break
		}
		tail := end + contextLines
		if tail > len(rows) {
			tail = len(rows)
		}
		// Don't start a hunk in the middle of a run that the previous hunk took.
		h := hunk{}
		if rows[start].mark == '-' || (rows[start].mark == ' ' && rows[start].ai >= 0) {
			h.aLine = rows[start].ai + 1
		} else {
			h.aLine = rows[start].ai + 1
		}
		h.bLine = rows[start].bi + 1
		if rows[start].mark == '+' && start == 0 && len(a) == 0 {
			h.aLine = 0
		}
		for k := start; k < tail; k++ {
			h.lines = append(h.lines, string(rows[k].mark)+rows[k].text)
			switch rows[k].mark {
			case ' ':
				h.aCount++
				h.bCount++
			case '-':
				h.aCount++
			case '+':
				h.bCount++
			}
		}
		if h.aCount == 0 {
			h.aLine = 0
		}
		hs = append(hs, h)
		i = tail
	}
	return hs
}

// lcs returns an edit script of ' ', '-' and '+'.
func lcs(a, b []string) []byte {
	n, m := len(a), len(b)
	if n*m > 400*400 {
		var ops []byte
		for range a {
			ops = append(ops, '-')
		}
		for range b {
			ops = append(ops, '+')
		}
		return ops
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []byte
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, ' ')
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, '-')
			i++
		default:
			ops = append(ops, '+')
			j++
		}
	}
	for i < n {
		ops = append(ops, '-')
		i++
	}
	for j < m {
		ops = append(ops, '+')
		j++
	}
	return ops
}
