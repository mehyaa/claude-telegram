// Package drive answers Claude Code's dialogs, and types prompts, by
// pressing keys in its terminal.
//
// Before every key it reads the screen and checks that it shows what the
// next key expects, retrying for a few seconds while the terminal
// redraws. When the screen doesn't match it stops without pressing anything
// more and says why, and when the terminal has already started on a dialog
// it doesn't touch it at all
// (docs/adr/0001-tmux-keystrokes-are-owner-authority.md).
// The key sequences are the ones captured from Claude Code 2.1.308.
package drive

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/screen"
)

// Terminal is the pane being driven. tmux.Pane is one.
type Terminal interface {
	Capture() (string, error)
	Keys(keys ...string) error
	Literal(text string) error
	Paste(text string) error
}

type Driver struct {
	T    Terminal
	Wait time.Duration // how long a check may retry; 5 s by default
	Poll time.Duration // how often it looks; 150 ms by default
	// Pressed, if set, hears every key or text sent, for the audit log.
	Pressed func(what string)
	sent    bool
}

// ErrStarted means the terminal has already started on the dialog, so
// Telegram leaves it alone (Q18).
var ErrStarted = errors.New("the terminal has already started on this dialog")

// MismatchError means the screen didn't show what the next key needed.
type MismatchError struct {
	Want string   // what was expected
	Saw  string   // what the screen showed instead
	Sent bool     // whether any key had been pressed already
	Done []string // headers of questions already answered (Q64)
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("the screen shows %s, not %s", e.Saw, e.Want)
}

func (dr *Driver) wait() time.Duration {
	if dr.Wait > 0 {
		return dr.Wait
	}
	return 5 * time.Second
}

func (dr *Driver) poll() time.Duration {
	if dr.Poll > 0 {
		return dr.Poll
	}
	return 150 * time.Millisecond
}

// await reads the screen until check accepts it, for up to Wait. check
// returns "" when the screen is right, or else what it shows instead.
func (dr *Driver) await(want string, check func(screen.Screen) string) (screen.Screen, error) {
	deadline := time.Now().Add(dr.wait())
	for {
		text, err := dr.T.Capture()
		if err != nil {
			return screen.Screen{}, err
		}
		s := screen.Parse(text)
		saw := check(s)
		if saw == "" {
			return s, nil
		}
		if time.Now().After(deadline) {
			return s, &MismatchError{Want: want, Saw: saw, Sent: dr.sent}
		}
		time.Sleep(dr.poll())
	}
}

func (dr *Driver) keys(keys ...string) error {
	dr.sent = true
	if dr.Pressed != nil {
		dr.Pressed("keys " + strings.Join(keys, " "))
	}
	return dr.T.Keys(keys...)
}

func (dr *Driver) literal(text string) error {
	dr.sent = true
	if dr.Pressed != nil {
		dr.Pressed(fmt.Sprintf("text %q", text))
	}
	return dr.T.Literal(text)
}

func (dr *Driver) paste(text string) error {
	dr.sent = true
	if dr.Pressed != nil {
		dr.Pressed(fmt.Sprintf("paste %d chars", len(text)))
	}
	return dr.T.Paste(text)
}

func describe(s screen.Screen) string {
	switch {
	case s.Dialog != nil && s.Dialog.Heading != "":
		return fmt.Sprintf("a %s dialog (%q)", s.Dialog.Kind, clip(s.Dialog.Heading))
	case s.Dialog != nil:
		return "a " + string(s.Dialog.Kind) + " dialog"
	case s.Prompt != nil && s.Prompt.Text != "":
		return fmt.Sprintf("text being typed in the prompt (%q)", clip(s.Prompt.Text))
	case s.Prompt != nil:
		return "the prompt with no dialog"
	default:
		return "no dialog and no prompt"
	}
}

func clip(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:39]) + "…"
	}
	return s
}

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

// sameLabel compares a label on screen with the one asked for. A label too
// long for the screen ends in "…" or wraps onto the next line, so the
// first line is a prefix of the full label at a word boundary (Q63).
func sameLabel(onScreen, want string) bool {
	onScreen, want = norm(onScreen), norm(want)
	if onScreen == want {
		return true
	}
	cut, ellipsis := strings.CutSuffix(onScreen, "…")
	if cut == "" || !strings.HasPrefix(want, cut) {
		return false
	}
	rest := want[len(cut):]
	return ellipsis || rest == "" || strings.HasPrefix(rest, " ")
}

// sameHeading compares a question's text. A question cut at 2000
// characters ends in "…"; wrapping is joined by the parser, so only the
// ellipsis cut is a prefix (Q63).
func sameHeading(onScreen, want string) bool {
	onScreen, want = norm(onScreen), norm(want)
	if onScreen == want {
		return true
	}
	cut, ok := strings.CutSuffix(onScreen, "…")
	return ok && cut != "" && strings.HasPrefix(want, cut)
}

func digit(n int) string { return strconv.Itoa(n) }

// Question is one question of an AskUserQuestion call, as in its input.
type Question struct {
	Question    string
	Header      string
	Options     []string
	MultiSelect bool
}

// Pick is the answer to one question: options by number (1-based), and/or
// Other text. A single-choice question takes one of the two.
type Pick struct {
	Options []int
	Other   string
}

// Answer answers an AskUserQuestion dialog, pressing every answer in one go
// (Q18).
func (dr *Driver) Answer(qs []Question, picks []Pick) error {
	if len(qs) == 0 || len(qs) != len(picks) {
		return fmt.Errorf("%d questions and %d answers", len(qs), len(picks))
	}
	for i, q := range qs {
		if err := validPick(q, picks[i]); err != nil {
			return fmt.Errorf("question %d: %w", i+1, err)
		}
	}
	tabbed := len(qs) > 1 || qs[0].MultiSelect
	var done []string
	annotate := func(err error) error {
		var me *MismatchError
		if errors.As(err, &me) {
			me.Done = append([]string(nil), done...)
		}
		return err
	}

	// Untouched: on the first question, nothing answered or ticked, the
	// cursor on option 1, the Other field empty.
	started := false
	_, err := dr.await("question 1 of this dialog", func(s screen.Screen) string {
		d := s.Dialog
		if d != nil && d.Kind == screen.Review {
			started = true
			return ""
		}
		if d == nil || d.Kind != screen.Question {
			return describe(s)
		}
		if saw := matchQuestion(d, qs[0]); saw != "" {
			return saw
		}
		for _, t := range d.Tabs {
			started = started || t.Done
		}
		for _, o := range d.Options {
			started = started || o.Checked
		}
		other := d.Option(len(qs[0].Options) + 1)
		started = started || d.Cursor != 1 || other == nil || !strings.HasPrefix(other.Label, "Type something")
		return ""
	})
	if err != nil {
		return annotate(err)
	}
	if started {
		return ErrStarted
	}

	for i, q := range qs {
		if i > 0 {
			if _, err := dr.onQuestion(q, i); err != nil {
				return annotate(err)
			}
		}
		var err error
		if q.MultiSelect {
			err = dr.answerMulti(q, picks[i])
		} else {
			err = dr.answerSingle(q, picks[i])
		}
		if err != nil {
			return annotate(err)
		}
		if q.Header != "" {
			done = append(done, q.Header)
		} else {
			done = append(done, clip(q.Question))
		}
	}

	if tabbed {
		if _, err := dr.await("the review screen", func(s screen.Screen) string {
			if s.Dialog == nil || s.Dialog.Kind != screen.Review {
				return describe(s)
			}
			if o := s.Dialog.Option(1); o == nil || o.Label != "Submit answers" || s.Dialog.Cursor != 1 {
				return "a review screen without Submit answers under the cursor"
			}
			return ""
		}); err != nil {
			return annotate(err)
		}
		if err := dr.keys("1"); err != nil {
			return err
		}
	}
	_, err = dr.await("the dialog closed", func(s screen.Screen) string {
		if d := s.Dialog; d != nil && (d.Kind == screen.Review || d.Kind == screen.Question && matchQuestion(d, qs[len(qs)-1]) == "") {
			return "the dialog still open"
		}
		return ""
	})
	return annotate(err)
}

func validPick(q Question, p Pick) error {
	n := len(q.Options)
	for _, k := range p.Options {
		if k < 1 || k > n {
			return fmt.Errorf("no option %d", k)
		}
	}
	if strings.ContainsAny(p.Other, "\r\n") {
		return errors.New("Other text must be one line")
	}
	switch {
	case !q.MultiSelect && len(p.Options)+boolInt(p.Other != "") != 1:
		return errors.New("a single-choice question takes exactly one answer")
	case q.MultiSelect && len(p.Options) == 0 && p.Other == "":
		return errors.New("no answer")
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// matchQuestion checks the dialog shows q: its text and its options.
func matchQuestion(d *screen.Dialog, q Question) string {
	if d.Preview {
		return "a preview pane"
	}
	if !sameHeading(d.Heading, q.Question) {
		return fmt.Sprintf("another question (%q)", clip(d.Heading))
	}
	for k, want := range q.Options {
		o := d.Option(k + 1)
		if o == nil || !sameLabel(o.Label, want) {
			return fmt.Sprintf("other options for %q", clip(q.Question))
		}
	}
	return ""
}

func (dr *Driver) onQuestion(q Question, i int) (*screen.Dialog, error) {
	s, err := dr.await(fmt.Sprintf("question %d (%q)", i+1, clip(q.Question)), func(s screen.Screen) string {
		if s.Dialog == nil || s.Dialog.Kind != screen.Question {
			return describe(s)
		}
		if saw := matchQuestion(s.Dialog, q); saw != "" {
			return saw
		}
		if s.Dialog.Cursor != 1 {
			return "the cursor away from option 1"
		}
		return ""
	})
	return s.Dialog, err
}

func (dr *Driver) answerSingle(q Question, p Pick) error {
	if p.Other == "" {
		return dr.keys(digit(p.Options[0]))
	}
	return dr.typeOther(q, len(q.Options)+1, p.Other, func() error { return dr.keys(digit(len(q.Options) + 1)) }, "Enter")
}

// typeOther puts the cursor in the Other field with toField, types text,
// checks it shows, and leaves with the keys in after.
func (dr *Driver) typeOther(q Question, row int, text string, toField func() error, after ...string) error {
	if err := toField(); err != nil {
		return err
	}
	if _, err := dr.await("the cursor in the Other field", func(s screen.Screen) string {
		if s.Dialog == nil || s.Dialog.Kind != screen.Question || matchQuestion(s.Dialog, q) != "" {
			return describe(s)
		}
		if s.Dialog.Cursor != row {
			return fmt.Sprintf("the cursor on row %d", s.Dialog.Cursor)
		}
		return ""
	}); err != nil {
		return err
	}
	if err := dr.literal(text); err != nil {
		return err
	}
	if _, err := dr.await("the Other text typed", func(s screen.Screen) string {
		if s.Dialog == nil || s.Dialog.Kind != screen.Question {
			return describe(s)
		}
		if o := s.Dialog.Option(row); o == nil || !sameLabel(o.Label, text) {
			return "the Other field without the text"
		}
		return ""
	}); err != nil {
		return err
	}
	if len(after) == 0 {
		return nil
	}
	return dr.keys(after...)
}

func (dr *Driver) answerMulti(q Question, p Pick) error {
	n := len(q.Options)
	for _, k := range p.Options {
		if err := dr.keys(digit(k)); err != nil {
			return err
		}
		if _, err := dr.await(fmt.Sprintf("option %d ticked", k), func(s screen.Screen) string {
			if s.Dialog == nil || s.Dialog.Kind != screen.Question {
				return describe(s)
			}
			if o := s.Dialog.Option(k); o == nil || !o.Checked {
				return fmt.Sprintf("option %d not ticked", k)
			}
			return ""
		}); err != nil {
			return err
		}
	}
	if p.Other == "" {
		return dr.keys("Tab")
	}
	// The digit only ticks Other; the cursor has to move onto the row to
	// type (step 1).
	err := dr.typeOther(q, n+1, p.Other, func() error {
		if err := dr.keys(digit(n + 1)); err != nil {
			return err
		}
		down := make([]string, n)
		for i := range down {
			down[i] = "Down"
		}
		return dr.keys(down...)
	})
	if err != nil {
		return err
	}
	if err := dr.keys("Down"); err != nil {
		return err
	}
	if _, err := dr.await("the cursor on Next", func(s screen.Screen) string {
		if s.Dialog == nil || s.Dialog.Kind != screen.Question || !s.Dialog.OnNext {
			return "the cursor off the Next row"
		}
		return ""
	}); err != nil {
		return err
	}
	return dr.keys("Enter")
}

// Permission identifies a permission prompt: Needle is text the dialog must
// show, such as the start of the command.
type Permission struct {
	Needle string
}

func (dr *Driver) awaitPermission(p Permission) (*screen.Dialog, error) {
	started := false
	s, err := dr.await("the permission prompt", func(s screen.Screen) string {
		d := s.Dialog
		if d == nil || d.Kind != screen.Permission {
			return describe(s)
		}
		if p.Needle != "" && !strings.Contains(norm(d.Text()), norm(p.Needle)) {
			return fmt.Sprintf("a permission prompt for something else (%q)", clip(d.Heading))
		}
		if o := d.Option(1); o == nil || o.Label != "Yes" {
			return "a permission prompt whose option 1 isn't Yes"
		}
		if last := d.Options[len(d.Options)-1]; !strings.HasPrefix(last.Label, "No") {
			return "a permission prompt whose last option isn't No"
		}
		started = d.Cursor != 1
		return ""
	})
	if err != nil {
		return nil, err
	}
	if started {
		return nil, ErrStarted
	}
	return s.Dialog, nil
}

func (dr *Driver) awaitClosed(what string, still func(*screen.Dialog) bool) error {
	_, err := dr.await(what+" closed", func(s screen.Screen) string {
		if s.Dialog != nil && still(s.Dialog) {
			return "the dialog still open"
		}
		return ""
	})
	return err
}

// Allow answers a permission prompt with "Yes", allowing once (Q19).
func (dr *Driver) Allow(p Permission) error {
	d, err := dr.awaitPermission(p)
	if err != nil {
		return err
	}
	if err := dr.keys("1"); err != nil {
		return err
	}
	return dr.awaitClosed("the permission prompt", func(n *screen.Dialog) bool { return n.Kind == screen.Permission && n.Text() == d.Text() })
}

// Deny answers a permission prompt with "No", with a reason when one is
// given (Q19).
func (dr *Driver) Deny(p Permission, reason string) error {
	reason = norm(reason)
	d, err := dr.awaitPermission(p)
	if err != nil {
		return err
	}
	no := d.Options[len(d.Options)-1].Num
	fingerprint := strings.Join(d.Body, "\n")
	closed := func() error {
		return dr.awaitClosed("the permission prompt", func(n *screen.Dialog) bool {
			return n.Kind == screen.Permission && strings.Join(n.Body, "\n") == fingerprint
		})
	}
	if reason == "" {
		if err := dr.keys(digit(no)); err != nil {
			return err
		}
		return closed()
	}
	down := make([]string, no-1)
	for i := range down {
		down[i] = "Down"
	}
	if err := dr.keys(down...); err != nil {
		return err
	}
	if _, err := dr.await("the cursor on No", func(s screen.Screen) string {
		if s.Dialog == nil || s.Dialog.Kind != screen.Permission || s.Dialog.Cursor != no {
			return describe(s)
		}
		return ""
	}); err != nil {
		return err
	}
	if err := dr.keys("Tab"); err != nil {
		return err
	}
	if _, err := dr.await("the reason field", func(s screen.Screen) string {
		if s.Dialog == nil || s.Dialog.Kind != screen.Permission {
			return describe(s)
		}
		if o := s.Dialog.Option(no); o == nil || o.Label != "No, and tell Claude what to do differently" {
			return "No without its reason field"
		}
		return ""
	}); err != nil {
		return err
	}
	if err := dr.literal(reason); err != nil {
		return err
	}
	if _, err := dr.await("the reason typed", func(s screen.Screen) string {
		if s.Dialog == nil || s.Dialog.Kind != screen.Permission {
			return describe(s)
		}
		if o := s.Dialog.Option(no); o == nil || !sameLabel(o.Label, "No, "+reason) {
			return "the reason field without the reason"
		}
		return ""
	}); err != nil {
		return err
	}
	if err := dr.keys("Enter"); err != nil {
		return err
	}
	return closed()
}

// The plan dialog's options that Telegram may choose (Q33).
const (
	planApprove = "Yes, manually approve edits"
	planChange  = "Tell Claude what to change"
)

func (dr *Driver) awaitPlan() (*screen.Dialog, error) {
	started := false
	s, err := dr.await("the plan approval", func(s screen.Screen) string {
		d := s.Dialog
		if d == nil || d.Kind != screen.Plan {
			return describe(s)
		}
		if planOption(d, planApprove) == 0 || planOption(d, planChange) == 0 {
			return "a plan dialog without the expected options"
		}
		started = d.Cursor != 1
		return ""
	})
	if err != nil {
		return nil, err
	}
	if started {
		return nil, ErrStarted
	}
	return s.Dialog, nil
}

func planOption(d *screen.Dialog, label string) int {
	for _, o := range d.Options {
		if o.Label == label {
			return o.Num
		}
	}
	return 0
}

// Approve approves a plan, keeping manual approval of edits (Q33).
func (dr *Driver) Approve() error {
	d, err := dr.awaitPlan()
	if err != nil {
		return err
	}
	if err := dr.keys(digit(planOption(d, planApprove))); err != nil {
		return err
	}
	return dr.awaitClosed("the plan approval", func(n *screen.Dialog) bool { return n.Kind == screen.Plan })
}

// Change answers a plan with "Tell Claude what to change" and the text.
func (dr *Driver) Change(text string) error {
	text = norm(text)
	if text == "" {
		return errors.New("no change given")
	}
	d, err := dr.awaitPlan()
	if err != nil {
		return err
	}
	row := planOption(d, planChange)
	if err := dr.keys(digit(row)); err != nil {
		return err
	}
	if _, err := dr.await("the cursor in the change field", func(s screen.Screen) string {
		if s.Dialog == nil || s.Dialog.Kind != screen.Plan || s.Dialog.Cursor != row {
			return describe(s)
		}
		return ""
	}); err != nil {
		return err
	}
	if err := dr.literal(text); err != nil {
		return err
	}
	if _, err := dr.await("the change typed", func(s screen.Screen) string {
		if s.Dialog == nil || s.Dialog.Kind != screen.Plan {
			return describe(s)
		}
		if o := s.Dialog.Option(row); o == nil || !sameLabel(o.Label, text) {
			return "the change field without the text"
		}
		return ""
	}); err != nil {
		return err
	}
	if err := dr.keys("Enter"); err != nil {
		return err
	}
	return dr.awaitClosed("the plan approval", func(n *screen.Dialog) bool {
		o := n.Option(row)
		return n.Kind == screen.Plan && o != nil && sameLabel(o.Label, text)
	})
}

// Type types a prompt and sends it. It refuses while a dialog is open or
// someone is typing in the terminal. Claude Code holds the prompt if a turn
// is running (Q5).
func (dr *Driver) Type(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("nothing to type")
	}
	if _, err := dr.await("an empty prompt", func(s screen.Screen) string {
		if s.Dialog != nil || s.Prompt == nil || s.Prompt.Text != "" {
			return describe(s)
		}
		return ""
	}); err != nil {
		return err
	}
	var err error
	if strings.Contains(text, "\n") {
		err = dr.paste(text)
	} else {
		err = dr.literal(text)
	}
	if err != nil {
		return err
	}
	if _, err := dr.await("the text in the prompt", func(s screen.Screen) string {
		if s.Dialog != nil || s.Prompt == nil {
			return describe(s)
		}
		if norm(s.Prompt.Text) != norm(text) && !strings.Contains(s.Prompt.Text, "[Pasted text") {
			return fmt.Sprintf("other text in the prompt (%q)", clip(s.Prompt.Text))
		}
		return ""
	}); err != nil {
		return err
	}
	if err := dr.keys("Enter"); err != nil {
		return err
	}
	_, err = dr.await("the prompt sent", func(s screen.Screen) string {
		if s.Prompt != nil && s.Prompt.Text != "" {
			return "the text still in the prompt"
		}
		return ""
	})
	return err
}

// escPause follows Esc: a key sent sooner reaches Claude Code as Alt+key,
// which the step-3 live probe saw eat the first letter of the text.
const escPause = 300 * time.Millisecond

// ShiftTab presses Shift+Tab once and reads the screen afterwards (Q85).
// A dialog means the press landed in the wrong place, so the caller stops.
func (dr *Driver) ShiftTab() error {
	s, err := dr.read()
	if err != nil {
		return err
	}
	if s.Dialog != nil {
		return fmt.Errorf("a dialog is open")
	}
	if err := dr.keys("BTab"); err != nil {
		return err
	}
	s, err = dr.read()
	if err != nil {
		return err
	}
	if s.Dialog != nil {
		return fmt.Errorf("a dialog is open")
	}
	return nil
}

// Slash types a slash command and Enter. It stops when a dialog is open.
func (dr *Driver) Slash(cmd string) error {
	s, err := dr.read()
	if err != nil {
		return err
	}
	if s.Dialog != nil {
		return fmt.Errorf("a dialog is open")
	}
	if err := dr.T.Literal(cmd); err != nil {
		return err
	}
	if dr.Pressed != nil {
		dr.Pressed(cmd)
	}
	dr.sent = true
	return dr.keys("Enter")
}

func (dr *Driver) read() (screen.Screen, error) {
	text, err := dr.T.Capture()
	if err != nil {
		return screen.Screen{}, err
	}
	return screen.Parse(text), nil
}

// Interrupt presses Esc once, which stops a running turn (Q5), and waits
// until the turn has stopped. It also cancels an open dialog.
func (dr *Driver) Interrupt() error {
	if err := dr.keys("Escape"); err != nil {
		return err
	}
	time.Sleep(escPause)
	_, err := dr.await("the turn stopped", func(s screen.Screen) string {
		if s.Busy {
			return "the turn still running"
		}
		return ""
	})
	return err
}
