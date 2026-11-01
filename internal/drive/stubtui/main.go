// Command stubtui draws Claude Code's dialogs the way version 2.1.308 does,
// and takes keys the way Claude Code 2.1.308 does, so the dialog
// driver can be tested in a real tmux without a model.
//
//	stubtui <log> question|permission|plan|prompt [spec-json]
//
// slowprompt keeps a sent prompt on screen for 500 ms before clearing it.
// busy is the prompt with a turn running, which Esc stops. Like Claude Code,
// the stub reads Esc and a character arriving together as Alt+character.
// question takes AskUserQuestion's input; permission takes
// {"title": …, "body": […], "options": […]}; plan takes {"plan": …}.
// Every key and every outcome is appended to <log> as a JSON line. After a
// dialog ends the stub shows an empty prompt box.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	width       = 120
	questionMax = 2000 // Claude Code cuts question text at pG=2000 with "…"
)

var rule = strings.Repeat("─", width)

func wrapWords(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	var cur string
	for _, w := range strings.Fields(s) {
		if cur == "" {
			cur = w
			continue
		}
		if len([]rune(cur))+1+len([]rune(w)) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func questionLines(q string) []string {
	t := q
	if r := []rune(t); len(r) > questionMax {
		t = string(r[:questionMax]) + "…"
	}
	if !strings.Contains(t, "\n") && len([]rune(t)) <= 80 {
		return []string{t}
	}
	inner := width - 2
	var out []string
	for _, para := range strings.Split(t, "\n") {
		if para == "" {
			out = append(out, "│")
			continue
		}
		for _, line := range wrapWords(para, inner) {
			out = append(out, "│ "+line)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func wrapLabeled(prefix, label, desc string) []string {
	rest := width - len([]rune(prefix))
	if rest < 8 {
		rest = 8
	}
	wrapped := wrapWords(label, rest)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	lines := []string{prefix + wrapped[0]}
	indent := strings.Repeat(" ", 5)
	for _, w := range wrapped[1:] {
		lines = append(lines, indent+w)
	}
	if desc != "" {
		lines = append(lines, indent+desc)
	}
	return lines
}

func pad(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

type question struct {
	Question    string `json:"question"`
	Header      string `json:"header"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
		Preview     string `json:"preview"`
	} `json:"options"`
}

type app struct {
	log  *os.File
	mode string

	// question
	qs     []question
	tab    int
	cursor int
	single []int          // per tab: the picked option, 0 for none
	multi  []map[int]bool // per tab: ticked rows, Other included
	other  []string       // per tab: the Other text
	review bool

	// permission
	title   string
	body    []string
	options []string
	amend   bool
	reason  string

	// plan
	plan   string
	change string

	// prompt
	prompt  string
	history []string
	busy    bool
	slow    bool
	clearAt time.Time // slowprompt: when the sent prompt leaves the screen
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: stubtui <log> <mode> [spec]")
		os.Exit(2)
	}
	log, err := os.OpenFile(os.Args[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	a := &app{log: log, mode: os.Args[2], cursor: 1}
	switch a.mode {
	case "busy":
		a.mode, a.busy = "prompt", true
	case "slowprompt":
		a.mode, a.slow = "prompt", true
	}
	spec := []byte("{}")
	if len(os.Args) > 3 {
		spec = []byte(os.Args[3])
	}
	switch a.mode {
	case "question":
		var in struct{ Questions []question }
		must(json.Unmarshal(spec, &in))
		a.qs = in.Questions
		a.single = make([]int, len(a.qs))
		a.multi = make([]map[int]bool, len(a.qs))
		for i := range a.multi {
			a.multi[i] = map[int]bool{}
		}
		a.other = make([]string, len(a.qs))
	case "permission":
		var in struct {
			Title   string
			Body    []string
			Options []string
		}
		must(json.Unmarshal(spec, &in))
		a.title, a.body, a.options = in.Title, in.Body, in.Options
	case "plan":
		var in struct{ Plan string }
		must(json.Unmarshal(spec, &in))
		a.plan = in.Plan
	}

	restore := makeRaw(0)
	defer restore()
	os.Stdout.WriteString("\x1b[?2004h") // ask for bracketed paste
	a.draw()
	chunks := make(chan string)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				close(chunks)
				return
			}
			chunks <- string(buf[:n])
		}
	}()
	var pasting bool
	var pasted strings.Builder
	for {
		var tick <-chan time.Time
		if !a.clearAt.IsZero() {
			tick = time.After(time.Until(a.clearAt))
		}
		var in string
		var ok bool
		select {
		case in, ok = <-chunks:
			if !ok {
				return
			}
		case <-tick:
			a.clearAt, a.prompt = time.Time{}, ""
			a.draw()
			continue
		}
		// A lone Esc may start Alt+key: like a terminal app, wait a moment
		// for the rest.
		for strings.HasSuffix(in, "\x1b") && !pasting {
			select {
			case more, ok := <-chunks:
				if !ok {
					return
				}
				in += more
				continue
			case <-time.After(100 * time.Millisecond):
			}
			break
		}
		for in != "" {
			if pasting {
				end := strings.Index(in, "\x1b[201~")
				if end < 0 {
					pasted.WriteString(in)
					in = ""
					continue
				}
				pasted.WriteString(in[:end])
				in = in[end+len("\x1b[201~"):]
				pasting = false
				text := strings.ReplaceAll(pasted.String(), "\r", "\n")
				a.record(map[string]any{"paste": text})
				a.typed(text)
				pasted.Reset()
				continue
			}
			key, rest := nextKey(in)
			in = rest
			if key == "paste-start" {
				pasting = true
				continue
			}
			a.record(map[string]any{"key": key})
			a.press(key)
		}
		a.draw()
	}
}

// nextKey splits the first key off the input.
func nextKey(in string) (string, string) {
	for seq, name := range map[string]string{
		"\x1b[200~": "paste-start", "\x1b[A": "Up", "\x1b[B": "Down",
		"\x1b[C": "Right", "\x1b[D": "Left", "\x1b[Z": "BTab",
	} {
		if strings.HasPrefix(in, seq) {
			return name, in[len(seq):]
		}
	}
	switch in[0] {
	case 0x1b:
		if len(in) > 1 && in[1] >= ' ' && in[1] != '[' {
			return "Alt+" + in[1:2], in[2:]
		}
		return "Escape", in[1:]
	case '\r', '\n':
		return "Enter", in[1:]
	case '\t':
		return "Tab", in[1:]
	case 0x7f, 0x08:
		return "BSpace", in[1:]
	}
	r := []rune(in)
	return string(r[0]), string(r[1:])
}

func (a *app) record(v map[string]any) {
	b, _ := json.Marshal(v)
	a.log.Write(append(b, '\n'))
}

func (a *app) done(v map[string]any) {
	v["done"] = true
	a.record(v)
	a.mode = "prompt"
}

func (a *app) press(key string) {
	switch a.mode {
	case "question":
		a.pressQuestion(key)
	case "permission":
		a.pressPermission(key)
	case "plan":
		a.pressPlan(key)
	case "prompt":
		a.pressPrompt(key)
	}
}

func digit(key string) int {
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return int(key[0] - '0')
	}
	return 0
}

// printable is a key that types a character; digits too.
func printable(key string) bool {
	r := []rune(key)
	return len(r) == 1 && r[0] >= ' ' && r[0] != 0x7f
}

// question

func (a *app) severalTabs() bool { return len(a.qs) > 1 || a.qs[0].MultiSelect }

func (a *app) answer(i int) string {
	q := a.qs[i]
	n := len(q.Options)
	if !q.MultiSelect {
		if a.single[i] > 0 {
			return q.Options[a.single[i]-1].Label
		}
		return a.other[i]
	}
	var parts []string
	for k := 1; k <= n; k++ {
		if a.multi[i][k] {
			parts = append(parts, q.Options[k-1].Label)
		}
	}
	if a.multi[i][n+1] && a.other[i] != "" {
		parts = append(parts, a.other[i])
	}
	return strings.Join(parts, ", ")
}

func (a *app) answered(i int) bool { return a.answer(i) != "" }

func (a *app) advance() {
	if a.tab < len(a.qs)-1 {
		a.tab++
		a.cursor = 1
		return
	}
	if a.severalTabs() {
		a.review, a.cursor = true, 1
		return
	}
	a.finish()
}

func (a *app) finish() {
	answers := map[string]string{}
	for i, q := range a.qs {
		answers[q.Question] = a.answer(i)
	}
	a.done(map[string]any{"answers": answers})
}

func (a *app) pressQuestion(key string) {
	if key == "Escape" {
		a.done(map[string]any{"declined": true})
		return
	}
	if a.review {
		switch {
		case key == "1", key == "Enter" && a.cursor == 1:
			a.finish()
		case key == "2", key == "Enter" && a.cursor == 2:
			a.done(map[string]any{"declined": true})
		case key == "Down":
			a.cursor = 2
		case key == "Up":
			a.cursor = 1
		}
		return
	}
	q := a.qs[a.tab]
	n := len(q.Options)
	onOther := a.cursor == n+1
	if onOther && printable(key) {
		// With the cursor in the Other field, keys are text, digits too
		// (step 1).
		a.other[a.tab] += key
		if q.MultiSelect {
			a.multi[a.tab][n+1] = true
		}
		return
	}
	if onOther && key == "BSpace" {
		r := []rune(a.other[a.tab])
		if len(r) > 0 {
			a.other[a.tab] = string(r[:len(r)-1])
		}
		return
	}
	last := n + 2 // Chat about this, or a multi-select's Next row
	switch {
	case key == "Down":
		a.cursor = min(a.cursor+1, last)
	case key == "Up":
		a.cursor = max(a.cursor-1, 1)
	case key == "Tab":
		a.advance()
	case !q.MultiSelect && digit(key) >= 1 && digit(key) <= n:
		a.single[a.tab] = digit(key)
		a.other[a.tab] = ""
		a.advance()
	case !q.MultiSelect && digit(key) == n+1:
		a.cursor = n + 1
	case !q.MultiSelect && key == "Enter" && a.cursor <= n:
		a.single[a.tab] = a.cursor
		a.advance()
	case !q.MultiSelect && key == "Enter" && a.cursor == n+1 && a.other[a.tab] != "":
		a.single[a.tab] = 0
		a.advance()
	case q.MultiSelect && digit(key) >= 1 && digit(key) <= n+1:
		a.multi[a.tab][digit(key)] = !a.multi[a.tab][digit(key)]
	case q.MultiSelect && key == "Enter" && a.cursor <= n+1:
		a.multi[a.tab][a.cursor] = !a.multi[a.tab][a.cursor]
	case q.MultiSelect && key == "Enter" && a.cursor == last:
		a.advance()
	}
}

func (a *app) tabBar() string {
	if !a.severalTabs() {
		return " ☐ " + a.qs[0].Header
	}
	parts := []string{}
	for i, q := range a.qs {
		mark := "☐"
		if a.answered(i) {
			mark = "☒"
		}
		parts = append(parts, mark+" "+q.Header)
	}
	return "←  " + strings.Join(parts, "  ") + "  ✔ Submit  →"
}

func cur(on bool) string {
	if on {
		return "❯ "
	}
	return "  "
}

func (a *app) drawPreviewQuestion() []string {
	q := a.qs[a.tab]
	preview := ""
	for k, o := range q.Options {
		if a.cursor == k+1 {
			preview = strings.ReplaceAll(o.Preview, "\n", " ")
			break
		}
	}
	if preview == "" {
		preview = "No preview available"
	}
	l := []string{rule, a.tabBar(), ""}
	l = append(l, questionLines(q.Question)...)
	l = append(l, "")
	for k, o := range q.Options {
		line := pad(fmt.Sprintf("%s%d. %s", cur(a.cursor == k+1), k+1, o.Label), 30)
		if k == 0 {
			line += preview
		}
		l = append(l, line)
	}
	l = append(l, "", strings.Repeat(" ", 34)+"Notes: press n to add notes", rule,
		"  Chat about this", "",
		"Enter to select · ↑/↓ to navigate · n to add notes · Esc to cancel")
	return l
}

func (a *app) hasPreview() bool {
	if a.tab < 0 || a.tab >= len(a.qs) {
		return false
	}
	for _, o := range a.qs[a.tab].Options {
		if o.Preview != "" {
			return true
		}
	}
	return false
}

func (a *app) drawQuestion() []string {
	if a.review {
		l := []string{rule, a.tabBar(), "", "Review your answers", ""}
		for i, q := range a.qs {
			l = append(l, " ● "+q.Question, "   → "+a.answer(i))
		}
		return append(l, "", "Ready to submit your answers?", "",
			cur(a.cursor == 1)+"1. Submit answers", cur(a.cursor == 2)+"2. Cancel")
	}
	if a.hasPreview() {
		return a.drawPreviewQuestion()
	}
	q := a.qs[a.tab]
	n := len(q.Options)
	l := []string{rule, a.tabBar(), ""}
	l = append(l, questionLines(q.Question)...)
	l = append(l, "")
	for k, o := range q.Options {
		if q.MultiSelect {
			box := "[ ]"
			if a.multi[a.tab][k+1] {
				box = "[✔]"
			}
			l = append(l, wrapLabeled(fmt.Sprintf("%s%d. %s ", cur(a.cursor == k+1), k+1, box), o.Label, o.Description)...)
		} else {
			l = append(l, wrapLabeled(fmt.Sprintf("%s%d. ", cur(a.cursor == k+1), k+1), o.Label, o.Description)...)
		}
	}
	if q.MultiSelect {
		box, label := "[ ]", "Type something"
		if a.multi[a.tab][n+1] {
			box = "[✔]"
		}
		if a.other[a.tab] != "" {
			label = a.other[a.tab]
		}
		next := "Next"
		if a.tab == len(a.qs)-1 && len(a.qs) == 1 {
			next = "Submit"
		}
		l = append(l, fmt.Sprintf("%s%d. %s %s", cur(a.cursor == n+1), n+1, box, label))
		if a.cursor == n+2 {
			l = append(l, "❯    "+next)
		} else {
			l = append(l, "     "+next)
		}
	} else {
		label := "Type something."
		if a.other[a.tab] != "" {
			label = a.other[a.tab]
		}
		l = append(l, fmt.Sprintf("%s%d. %s", cur(a.cursor == n+1), n+1, label))
	}
	l = append(l, rule)
	if !q.MultiSelect {
		l = append(l, fmt.Sprintf("%s%d. Chat about this", cur(a.cursor == n+2), n+2))
	} else {
		l = append(l, fmt.Sprintf("  %d. Chat about this", n+2))
	}
	nav := "↑/↓ to navigate"
	if len(a.qs) > 1 {
		nav = "Tab/Arrow keys to navigate"
	}
	hint := "Enter to select · " + nav
	if a.cursor == n+1 {
		hint += " · ctrl+g to edit in nano"
	}
	return append(l, "", hint+" · Esc to cancel")
}

// permission

func (a *app) pressPermission(key string) {
	last := len(a.options)
	if a.amend && printable(key) {
		a.reason += key
		return
	}
	if a.amend && key == "BSpace" {
		if r := []rune(a.reason); len(r) > 0 {
			a.reason = string(r[:len(r)-1])
		}
		return
	}
	switch {
	case key == "Escape":
		a.done(map[string]any{"permission": "deny"})
	case key == "Down":
		a.cursor = min(a.cursor+1, last)
	case key == "Up":
		a.cursor = max(a.cursor-1, 1)
	case key == "Tab" && a.cursor == last:
		a.amend = true
	case key == "Enter" && a.amend:
		a.done(map[string]any{"permission": "deny", "reason": a.reason})
	case key == "Enter":
		a.choose(a.cursor)
	case digit(key) >= 1 && digit(key) <= last:
		a.choose(digit(key))
	}
}

func (a *app) choose(k int) {
	switch k {
	case 1:
		a.done(map[string]any{"permission": "allow"})
	case len(a.options):
		a.done(map[string]any{"permission": "deny"})
	default:
		a.done(map[string]any{"permission": "option", "option": k})
	}
}

func (a *app) drawPermission() []string {
	l := []string{rule, " " + a.title}
	dash := strings.Repeat("╌", width)
	l = append(l, dash)
	for _, b := range a.body {
		l = append(l, " "+b)
	}
	l = append(l, dash, " Do you want to proceed?")
	for k, o := range a.options {
		if k == len(a.options)-1 && a.amend {
			o = "No, and tell Claude what to do differently"
			if a.reason != "" {
				o = "No, " + a.reason
			}
		}
		l = append(l, fmt.Sprintf(" %s%d. %s", cur(a.cursor == k+1), k+1, o))
	}
	hint := " Esc to cancel · Tab to amend"
	if a.amend {
		hint = " Esc to cancel"
	}
	return append(l, "", hint)
}

// plan

func (a *app) pressPlan(key string) {
	if a.cursor == 3 && printable(key) {
		a.change += key
		return
	}
	switch {
	case key == "Escape":
		a.done(map[string]any{"plan": "rejected"})
	case key == "Down":
		a.cursor = min(a.cursor+1, 3)
	case key == "Up":
		a.cursor = max(a.cursor-1, 1)
	case key == "1", key == "Enter" && a.cursor == 1:
		a.done(map[string]any{"plan": "auto"})
	case key == "2", key == "Enter" && a.cursor == 2:
		a.done(map[string]any{"plan": "manual"})
	case key == "3":
		a.cursor = 3
	case key == "Enter" && a.cursor == 3 && a.change != "":
		a.done(map[string]any{"plan": "change", "text": a.change})
	}
}

func (a *app) drawPlan() []string {
	dash := "  " + strings.Repeat("╌", width-4)
	inner := "  " + strings.Repeat("─", width-4)
	l := []string{strings.Repeat("▔", width), inner, "   Ready to code?", "   Here is Claude's plan:", dash}
	for _, p := range strings.Split(a.plan, "\n") {
		l = append(l, "   "+p)
	}
	change := "Tell Claude what to change"
	if a.change != "" {
		change = a.change
	}
	return append(l, dash, inner,
		"   Claude has written up a plan and is ready to execute. Would you like to proceed?",
		"   "+cur(a.cursor == 1)+"1. Yes, and use auto mode",
		"   "+cur(a.cursor == 2)+"2. Yes, manually approve edits",
		"   "+cur(a.cursor == 3)+"3. "+change,
		"        shift+tab to approve with this feedback",
		"   ctrl+g to edit in nano · ~/.claude/plans/stub-plan.md")
}

// prompt

func (a *app) typed(text string) {
	if a.mode == "prompt" {
		a.prompt += text
	}
}

func (a *app) pressPrompt(key string) {
	switch {
	case key == "Enter":
		a.done(map[string]any{"prompt": a.prompt})
		a.history = append(a.history, "❯ "+strings.ReplaceAll(a.prompt, "\n", "\n  "), "● ok", "")
		if a.slow {
			a.clearAt = time.Now().Add(500 * time.Millisecond)
		} else {
			a.prompt = ""
		}
	case key == "Escape" && a.busy:
		a.busy = false
		a.done(map[string]any{"interrupted": true})
	case key == "Escape":
		a.done(map[string]any{"escape": true})
	case key == "BSpace":
		if r := []rune(a.prompt); len(r) > 0 {
			a.prompt = string(r[:len(r)-1])
		}
	case printable(key):
		a.prompt += key
	}
}

func (a *app) drawPrompt() []string {
	l := []string{" ▐▛███▜▌   Stub Claude Code v2.1.308", ""}
	l = append(l, a.history...)
	status := "  ⏸ manual mode on · ? for shortcuts"
	if a.busy {
		l = append(l, "✢ Working… (12s · ↓ 80 tokens)")
		status = "  ⏸ manual mode on · esc to interrupt"
	}
	lines := strings.Split(a.prompt, "\n")
	// The live prompt box has a no-break space after ❯; echoes don't.
	l = append(l, rule, "❯\u00a0"+lines[0])
	for _, c := range lines[1:] {
		l = append(l, "  "+c)
	}
	return append(l, rule, status)
}

func (a *app) draw() {
	var l []string
	switch a.mode {
	case "question":
		l = a.drawQuestion()
	case "permission":
		l = a.drawPermission()
	case "plan":
		l = a.drawPlan()
	default:
		l = a.drawPrompt()
	}
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	for i, line := range l {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(line)
	}
	os.Stdout.WriteString(b.String())
}

func makeRaw(fd int) func() {
	var t syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&t))); e != 0 {
		panic(e)
	}
	old := t
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&t))); e != 0 {
		panic(e)
	}
	return func() {
		syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&old)))
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
