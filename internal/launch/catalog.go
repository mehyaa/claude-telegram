package launch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const scanN = 64 << 10

// Past is one resumable Claude Code session (Q81).
type Past struct {
	ID         string
	Label      string
	Custom     string // the session's own name, if it has one
	AI         string
	Dir        string
	Branch     string
	When       time.Time // last user or assistant timestamp, else the file mtime
	Mod        time.Time
	Size       int64
	Background bool
}

// TmuxName is the session's own name converted by Q44, else its AI title
// converted the same way (Q50).
func (p Past) TmuxName() (string, bool) {
	if p.Custom != "" {
		if n, ok := SessionName(p.Custom); ok {
			return n, true
		}
	}
	if p.AI != "" {
		if n, ok := SessionName(p.AI); ok {
			return n, true
		}
	}
	return "", false
}

// Line is the picker row: label · ~/folder · branch · age · size.
// bg is included for a background session.
func (p Past) Line(home string, now time.Time) string {
	parts := []string{clip(p.Label, 60), Short(home, p.Dir)}
	if p.Branch != "" {
		parts = append(parts, p.Branch)
	}
	if p.Background {
		parts = append(parts, "bg")
	}
	when := p.When
	if when.IsZero() {
		when = p.Mod
	}
	parts = append(parts, age(now.Sub(when)), sizeLabel(p.Size))
	return strings.Join(parts, " · ")
}

// List reads every resumable session under projects, newest file first.
// projects is ~/.claude/projects, or a stand-in in tests.
func List(projects string) ([]Past, error) {
	known := projectIDs(projects)
	var all []Past
	roots, err := os.ReadDir(projects)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, root := range roots {
		if !root.IsDir() {
			continue
		}
		dir := filepath.Join(projects, root.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(f.Name(), ".jsonl")
			if !uuidRE.MatchString(id) {
				continue
			}
			p, ok := readPast(filepath.Join(dir, f.Name()), dir, id, known)
			if !ok {
				continue
			}
			all = append(all, p)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].Mod.Equal(all[j].Mod) {
			return all[i].Mod.After(all[j].Mod)
		}
		return all[i].ID > all[j].ID
	})
	return all, nil
}

// Load reads one session by id. ok is false when it is missing or hidden.
func Load(projects, id string) (Past, bool) {
	if !uuidRE.MatchString(id) {
		return Past{}, false
	}
	known := projectIDs(projects)
	roots, err := os.ReadDir(projects)
	if err != nil {
		return Past{}, false
	}
	name := id + ".jsonl"
	for _, root := range roots {
		if !root.IsDir() {
			continue
		}
		dir := filepath.Join(projects, root.Name())
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		return readPast(path, dir, id, known)
	}
	return Past{}, false
}

// Filter keeps sessions whose label, branch, or folder contains q.
func Filter(all []Past, q, home string) []Past {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return all
	}
	var out []Past
	for _, p := range all {
		if strings.Contains(strings.ToLower(p.Label), q) ||
			strings.Contains(strings.ToLower(p.Branch), q) ||
			strings.Contains(strings.ToLower(p.Dir), q) ||
			strings.Contains(strings.ToLower(Short(home, p.Dir)), q) {
			out = append(out, p)
		}
	}
	return out
}

// Page returns a 10-row page. page is clamped. pages is the count of pages.
func Page(all []Past, page int) (int, int, []Past) {
	const n = 10
	if len(all) == 0 {
		return 0, 0, nil
	}
	pages := (len(all) + n - 1) / n
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	i := page * n
	j := i + n
	if j > len(all) {
		j = len(all)
	}
	return page, pages, all[i:j]
}

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var fieldRE = regexp.MustCompile(`"([A-Za-z0-9_]+)"\s*:\s*"((?:\\.|[^"\\])*)"`)

// Slug is Claude Code's project-directory name for a path.
func Slug(path string) string {
	var b strings.Builder
	for _, r := range path {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func projectIDs(projects string) map[string]bool {
	ids := map[string]bool{}
	roots, err := os.ReadDir(projects)
	if err != nil {
		return ids
	}
	for _, root := range roots {
		if !root.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(projects, root.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			id := strings.TrimSuffix(f.Name(), ".jsonl")
			if !f.IsDir() && uuidRE.MatchString(id) {
				ids[strings.ToLower(id)] = true
			}
		}
	}
	return ids
}

func readPast(path, dir, id string, known map[string]bool) (Past, bool) {
	head, tail, size, mod, err := headTail(path)
	if err != nil {
		return Past{}, false
	}
	if hidden(head, tail, known) {
		return Past{}, false
	}
	custom := lastField(tail, "customTitle")
	if custom == "" {
		custom = sidecarTitle(dir, id)
	}
	if custom == "" {
		custom = firstField(head, "customTitle")
	}
	ai := lastField(tail, "aiTitle")
	if ai == "" {
		ai = firstField(head, "aiTitle")
	}
	cwd := firstField(head, "cwd")
	if cwd == "" {
		cwd = firstField(tail, "cwd")
	}
	// A session filed under a project directory that is not the slug of its
	// cwd belongs to a different folder. Two different paths that slug the
	// same way share a directory and both stay listed: the transcript does
	// not say which path the directory was created for.
	if cwd != "" && Slug(cwd) != filepath.Base(dir) {
		return Past{}, false
	}
	branch := lastField(tail, "gitBranch")
	if branch == "" {
		branch = firstField(head, "gitBranch")
	}
	label := custom
	if label == "" {
		label = ai
	}
	if label == "" {
		label = lastSummary(tail)
	}
	if label == "" {
		label = lastSummary(head)
	}
	if label == "" {
		label = lastPrompt(tail)
	}
	if label == "" {
		label = firstPrompt(head)
	}
	if label == "" {
		label = "(session)"
	}
	label = clip(label, 200)
	when := lastTimestamp(tail)
	return Past{
		ID:     id,
		Label:  label,
		Custom: custom,
		AI:     ai,
		Dir:    cwd,
		Branch: branch,
		When:   when,
		Mod:    mod,
		Size:   size,
		Background: strings.Contains(head, `"isBackground":true`) || strings.Contains(head, `"isBackground": true`) ||
			strings.Contains(tail, `"isBackground":true`) || strings.Contains(tail, `"isBackground": true`),
	}, true
}

func hidden(head, tail string, known map[string]bool) bool {
	if strings.Contains(head, `"isSidechain":true`) || strings.Contains(head, `"isSidechain": true`) {
		return true
	}
	if team(head) || team(tail) {
		return true
	}
	if daemonKind(head) || daemonKind(tail) {
		return true
	}
	entry := firstField(head, "entrypoint")
	if entry == "" {
		entry = lastField(tail, "entrypoint")
	}
	switch entry {
	case "sdk-cli", "sdk-ts", "sdk-py":
		return true
	}
	if strings.Contains(head, "<command-name>/loop</command-name>") {
		return true
	}
	if target := continued(head + "\n" + tail); target != "" && known[strings.ToLower(target)] {
		return true
	}
	return false
}

func team(raw string) bool {
	for _, v := range allFields(raw, "teamName") {
		if v != "" {
			return true
		}
	}
	return false
}

func daemonKind(raw string) bool {
	for _, v := range allFields(raw, "sessionKind") {
		if v == "daemon" || v == "daemon-worker" {
			return true
		}
	}
	return false
}

var continuedRE = regexp.MustCompile(`continued-in[\s\S]{0,400}?([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)

func continued(raw string) string {
	if !strings.Contains(raw, "continued-in") {
		return ""
	}
	m := continuedRE.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return m[1]
}

func sidecarTitle(dir, id string) string {
	b, err := os.ReadFile(filepath.Join(dir, id, "custom-title.json"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if v := lastField(s, "customTitle"); v != "" {
		return v
	}
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return ""
}

func lastSummary(raw string) string {
	return lastTyped(raw, "summary", "summary")
}

func lastPrompt(raw string) string {
	if v := lastTyped(raw, "last-prompt", "prompt"); v != "" {
		return clip(v, 200)
	}
	if v := lastTyped(raw, "last-prompt", "lastPrompt"); v != "" {
		return clip(v, 200)
	}
	return ""
}

func firstPrompt(head string) string {
	for _, line := range strings.Split(head, "\n") {
		if !strings.Contains(line, `"type":"user"`) && !strings.Contains(line, `"type": "user"`) {
			continue
		}
		if strings.Contains(line, `"isMeta":true`) || strings.Contains(line, `"isMeta": true`) ||
			strings.Contains(line, `"isCompactSummary":true`) || strings.Contains(line, `"isCompactSummary": true`) ||
			strings.Contains(line, `"tool_result"`) {
			continue
		}
		if v := firstField(line, "content"); v != "" {
			return clip(slashCommand(v), 200)
		}
	}
	return ""
}

var commandNameRE = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)

func slashCommand(s string) string {
	m := commandNameRE.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	rest := strings.TrimSpace(commandNameRE.ReplaceAllString(s, ""))
	if rest == "" {
		return "/" + m[1]
	}
	return "/" + m[1] + " " + rest
}

func lastTyped(raw, typ, field string) string {
	var got string
	for _, line := range strings.Split(raw, "\n") {
		if !strings.Contains(line, `"type":"`+typ+`"`) && !strings.Contains(line, `"type": "`+typ+`"`) {
			continue
		}
		if v := lastField(line, field); v != "" {
			got = v
		}
	}
	return got
}

func lastTimestamp(raw string) time.Time {
	var got time.Time
	for _, v := range allFields(raw, "timestamp") {
		if t, ok := parseTime(v); ok {
			got = t
		}
	}
	return got
}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func firstField(raw, name string) string {
	a := allFields(raw, name)
	if len(a) == 0 {
		return ""
	}
	return a[0]
}

func lastField(raw, name string) string {
	a := allFields(raw, name)
	if len(a) == 0 {
		return ""
	}
	return a[len(a)-1]
}

func allFields(raw, name string) []string {
	var out []string
	for _, m := range fieldRE.FindAllStringSubmatch(raw, -1) {
		if m[1] != name {
			continue
		}
		s, err := strconv.Unquote(`"` + m[2] + `"`)
		if err != nil {
			s = m[2]
		}
		out = append(out, s)
	}
	return out
}

func headTail(path string) (head, tail string, size int64, mod time.Time, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", "", 0, time.Time{}, err
	}
	size, mod = fi.Size(), fi.ModTime()
	if size == 0 {
		return "", "", size, mod, nil
	}
	n := int(size)
	if int64(n) > scanN {
		n = scanN
	}
	hb := make([]byte, n)
	if _, err = io.ReadFull(f, hb); err != nil {
		return "", "", 0, time.Time{}, err
	}
	head = string(hb)
	if size <= scanN {
		return head, head, size, mod, nil
	}
	tb := make([]byte, scanN)
	if _, err = f.ReadAt(tb, size-scanN); err != nil {
		return "", "", 0, time.Time{}, err
	}
	return head, string(tb), size, mod, nil
}

func age(d time.Duration) string {
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func sizeLabel(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
