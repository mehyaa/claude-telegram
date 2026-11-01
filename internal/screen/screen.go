// Package screen reads a Claude Code terminal, as `tmux capture-pane -p`
// prints it, and tells which dialog is open or what the prompt box holds.
//
// The layouts are the ones captured from Claude Code 2.1.308
// (testdata/2.1.308). Only the bottom of the screen is read: dialogs and
// the prompt box are drawn there, and the scrollback above can quote anything.
package screen

import (
	"regexp"
	"strconv"
	"strings"
)

type Kind string

const (
	Question   Kind = "question"   // AskUserQuestion, on a question tab
	Review     Kind = "review"     // AskUserQuestion's "Review your answers"
	Permission Kind = "permission" // a permission prompt
	Plan       Kind = "plan"       // ExitPlanMode's approval
)

type Screen struct {
	Dialog *Dialog // nil when no dialog is open
	Prompt *Prompt // nil when the prompt box isn't drawn
	Busy   bool    // a turn is running ("esc to interrupt")
}

// Prompt is the prompt box.
type Prompt struct {
	Text   string // what is typed; empty when nothing is
	Queued bool   // messages are waiting for the turn to end
}

type Dialog struct {
	Kind    Kind
	Tabs    []Tab    // the tab bar of a question dialog; one tab for a lone question
	Heading string   // the question; a permission prompt's title ("Bash command"); "Ready to code?"
	Ask     string   // the line before the options: "Do you want to proceed?", "Ready to submit your answers?"
	Body    []string // a permission prompt's details, the plan, or the review's answers
	Options []Option
	Cursor  int  // the option under ❯; 0 when it's on the Next/Submit row
	OnNext  bool // the cursor is on a multi-select's Next/Submit row
	Hint    string
	Preview bool // options with a preview: side-by-side Notes pane (Q63)
	lines   []string
}

type Option struct {
	Num     int
	Label   string
	Box     bool // a multi-select checkbox
	Checked bool
	Desc    string
}

type Tab struct {
	Label string
	Done  bool // ☒: answered
}

// Text is everything the dialog shows, one line per row.
func (d *Dialog) Text() string { return strings.Join(d.lines, "\n") }

// Option returns the option numbered n, or nil.
func (d *Dialog) Option(n int) *Option {
	for i := range d.Options {
		if d.Options[i].Num == n {
			return &d.Options[i]
		}
	}
	return nil
}

var (
	optionRE = regexp.MustCompile(`^\s*(❯)?\s*(\d+)\. (.*)$`)
	boxRE    = regexp.MustCompile(`^\[(.)\] (.*)$`)
	nextRE   = regexp.MustCompile(`^\s*(❯)?\s*(Next|Submit)$`)
)

const (
	nbsp = "\u00a0"
	// promptMark starts the prompt box's first line. Prompts echoed in the
	// scrollback use a plain space instead.
	promptMark = "❯" + nbsp
	// lookBack bounds how far above the bottom a dialog may start.
	lookBack = 80
)

// Parse reads a screen.
func Parse(text string) Screen {
	raw := strings.Split(strings.TrimRight(text, "\n"), "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		raw[i] = strings.TrimRight(l, " ")
		lines[i] = strings.TrimRight(strings.ReplaceAll(l, nbsp, " "), " ")
	}
	last := len(lines) - 1
	for last >= 0 && lines[last] == "" {
		last--
	}
	if last < 0 {
		return Screen{}
	}
	lines, raw = lines[:last+1], raw[:last+1]
	floor := max(0, len(lines)-lookBack)

	var s Screen
	switch {
	case hasNear(lines, "Ready to submit your answers?", 6):
		s.Dialog = parseReview(lines, floor)
	case hasNear(lines, "Enter to select ·", 2):
		s.Dialog = parseQuestion(lines, floor)
	case hasNear(lines, "ctrl+g to edit in nano ·", 1) && hasNear(lines, "Would you like to proceed?", 8):
		s.Dialog = parsePlan(lines, floor)
	case hasNear(lines, "Esc to cancel", 2):
		s.Dialog = parsePermission(lines, floor)
	default:
		s.Prompt, s.Busy = parsePrompt(lines, raw, floor)
	}
	return s
}

// hasNear reports whether one of the last n non-empty lines contains sub.
func hasNear(lines []string, sub string, n int) bool {
	for i := len(lines) - 1; i >= 0 && n > 0; i-- {
		if lines[i] == "" {
			continue
		}
		if strings.Contains(lines[i], sub) {
			return true
		}
		n--
	}
	return false
}

func isRule(l string) bool {
	return strings.HasPrefix(strings.TrimSpace(l), "──────────")
}

// isTopRule is a rule drawn from the first column, as the frames of dialogs
// and the prompt box are.
func isTopRule(l string) bool { return strings.HasPrefix(l, "──────────") }

func lastIndex(lines []string, floor int, match func(string) bool) int {
	for i := len(lines) - 1; i >= floor; i-- {
		if match(lines[i]) {
			return i
		}
	}
	return -1
}

func isTabBar(l string) bool {
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "← ") || strings.HasPrefix(t, "☐ ") || strings.HasPrefix(t, "☒ ")
}

// stripGutter removes the dim left border Claude Code draws on a question
// longer than 80 columns or containing a newline (Q63). A blank paragraph
// is a bare "│".
func stripGutter(l string) string {
	t := strings.TrimSpace(l)
	t = strings.TrimPrefix(t, "│ ")
	t = strings.TrimPrefix(t, "│")
	return strings.TrimSpace(t)
}

func parseTabs(l string) []Tab {
	t := strings.TrimSpace(l)
	t = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "←"), "→"))
	var tabs []Tab
	for _, f := range regexp.MustCompile(`\s{2,}`).Split(t, -1) {
		mark, label, ok := strings.Cut(f, " ")
		if !ok || mark == "✔" {
			continue // "✔ Submit" is the submit tab, not a question
		}
		tabs = append(tabs, Tab{Label: label, Done: mark == "☒"})
	}
	return tabs
}

// parseOptions reads numbered options, their descriptions and the
// Next/Submit row from lines.
func parseOptions(d *Dialog, lines []string) {
	for _, l := range lines {
		if m := optionRE.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[2])
			o := Option{Num: n, Label: m[3]}
			if b := boxRE.FindStringSubmatch(o.Label); b != nil {
				o.Box, o.Checked, o.Label = true, b[1] != " ", b[2]
			}
			if m[1] != "" {
				d.Cursor = n
			}
			d.Options = append(d.Options, o)
			continue
		}
		if m := nextRE.FindStringSubmatch(l); m != nil {
			if m[1] != "" {
				d.OnNext = true
			}
			continue
		}
		if t := strings.TrimSpace(l); t != "" && !isRule(l) && len(d.Options) > 0 {
			o := &d.Options[len(d.Options)-1]
			if o.Desc == "" {
				o.Desc = t
			} else {
				o.Desc += " " + t
			}
		}
	}
}

func parseQuestion(lines []string, floor int) *Dialog {
	top := lastIndex(lines, floor, isTabBar)
	if top < 0 {
		return nil
	}
	region := lines[top:]
	d := &Dialog{Kind: Question, Tabs: parseTabs(region[0]), lines: region}
	d.Hint = strings.TrimSpace(region[len(region)-1])
	d.Preview = strings.Contains(d.Hint, "n to add notes")
	// The question runs from the line after the tab bar to the first option.
	var q []string
	i := 1
	for ; i < len(region) && !optionRE.MatchString(region[i]); i++ {
		if t := stripGutter(region[i]); t != "" {
			q = append(q, t)
		}
	}
	d.Heading = strings.Join(q, " ")
	parseOptions(d, region[i:len(region)-1])
	for _, l := range region {
		if strings.Contains(l, "Notes:") {
			d.Preview = true
			break
		}
	}
	return d
}

func parseReview(lines []string, floor int) *Dialog {
	top := lastIndex(lines, floor, isTabBar)
	if top < 0 {
		return nil
	}
	region := lines[top:]
	d := &Dialog{Kind: Review, Tabs: parseTabs(region[0]), Heading: "Review your answers", lines: region}
	ask := -1
	for i, l := range region {
		if strings.TrimSpace(l) == "Ready to submit your answers?" {
			ask = i
			break
		}
		if t := strings.TrimSpace(l); i > 0 && t != "" && t != d.Heading {
			d.Body = append(d.Body, t)
		}
	}
	if ask < 0 {
		return nil
	}
	d.Ask = "Ready to submit your answers?"
	parseOptions(d, cutAtRule(region[ask+1:]))
	return d
}

// cutAtRule drops whatever follows the first rule, such as the frame of the
// prompt box below a dialog.
func cutAtRule(lines []string) []string {
	for i, l := range lines {
		if isRule(l) {
			return lines[:i]
		}
	}
	return lines
}

func parsePermission(lines []string, floor int) *Dialog {
	top := lastIndex(lines, floor, isTopRule)
	if top < 0 || top == len(lines)-1 {
		return nil
	}
	region := lines[top+1:]
	d := &Dialog{Kind: Permission, lines: region}
	d.Hint = strings.TrimSpace(region[len(region)-1])
	ask := -1
	for i, l := range region {
		t := strings.TrimSpace(l)
		switch {
		case t == "" || strings.HasPrefix(t, "╌"):
		case d.Heading == "":
			d.Heading = t
		case strings.HasPrefix(t, "Do you want to"):
			d.Ask = t
			ask = i
		}
		if ask >= 0 {
			break
		}
		if t != "" && !strings.HasPrefix(t, "╌") && t != d.Heading {
			d.Body = append(d.Body, t)
		}
	}
	if ask < 0 {
		return nil
	}
	parseOptions(d, region[ask+1:len(region)-1])
	return d
}

func parsePlan(lines []string, floor int) *Dialog {
	top := lastIndex(lines, floor, func(l string) bool { return strings.TrimSpace(l) == "Ready to code?" })
	if top < 0 {
		return nil
	}
	region := lines[top:]
	d := &Dialog{Kind: Plan, Heading: "Ready to code?", lines: region}
	d.Hint = strings.TrimSpace(region[len(region)-1])
	ask := -1
	inPlan := false
	for i, l := range region {
		t := strings.TrimSpace(l)
		if strings.Contains(t, "Would you like to proceed?") {
			d.Ask, ask = t, i
			break
		}
		switch {
		case t == "Here is Claude's plan:":
			inPlan = true
		case strings.HasPrefix(t, "╌") || isRule(l):
		case inPlan && t != "":
			d.Body = append(d.Body, t)
		}
	}
	if ask < 0 {
		return nil
	}
	parseOptions(d, region[ask+1:len(region)-1])
	return d
}

// placeholders are drawn in the empty prompt box; nothing is typed.
var placeholders = map[string]bool{"Press up to edit queued messages": true}

func parsePrompt(lines, raw []string, floor int) (*Prompt, bool) {
	bottom := lastIndex(lines, floor, isTopRule)
	if bottom < 1 {
		return nil, false
	}
	busy := false
	for _, l := range lines[bottom+1:] {
		if strings.Contains(l, "esc to interrupt") {
			busy = true
		}
	}
	top := lastIndex(lines[:bottom], floor, isTopRule)
	if top < 0 || !strings.HasPrefix(raw[top+1], promptMark) {
		return nil, busy
	}
	var b strings.Builder
	for i, l := range lines[top+1 : bottom] {
		if i == 0 {
			l = strings.TrimPrefix(strings.TrimPrefix(l, "❯"), " ")
		} else {
			b.WriteString("\n")
			l = strings.TrimPrefix(l, "  ")
		}
		b.WriteString(l)
	}
	p := &Prompt{Text: strings.TrimSpace(b.String())}
	if placeholders[p.Text] {
		p.Queued, p.Text = true, ""
	}
	return p, busy
}
