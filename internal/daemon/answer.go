package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/audit"
	"github.com/mehyaa/claude-telegram/internal/drive"
	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram"
)

// awaiting is a piece of text the owner was asked for: an Other answer, a
// reason for denying, a change to a plan, a reply, or text for the remote
// keyboard. It is saved, so a secret sent after a restart is still handled
// as one (Q27); the text itself never is.
type awaiting struct {
	Session string `json:"session"`       // session ID
	Key     string `json:"key,omitempty"` // item key
	Action  string `json:"action"`        // "other", "reason", "change", "reply", "block", "ktype", "ksecret"
	QI      int    `json:"qi,omitempty"`  // the question, for "other"
	Msg     int64  `json:"msg,omitempty"` // the remote keyboard's message, for "ktype" and "ksecret"
	FP      string `json:"fp,omitempty"`  // the screen it showed (Q17)
	// Until is when the request lapses, so a forgotten one doesn't take a
	// later message (found in the step-6 live probe).
	Until time.Time `json:"until"`
}

// awaitFor is how long a request for text stays open.
const awaitFor = 5 * time.Minute

type askInput struct {
	Questions []struct {
		Question    string `json:"question"`
		Header      string `json:"header"`
		MultiSelect bool   `json:"multiSelect"`
		Options     []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
			Preview     string `json:"preview"`
		} `json:"options"`
	} `json:"questions"`
}

func parseAsk(raw json.RawMessage) askInput {
	var in askInput
	json.Unmarshal(raw, &in)
	return in
}

func hasPreview(in askInput) bool {
	for _, q := range in.Questions {
		for _, o := range q.Options {
			if strings.TrimSpace(o.Preview) != "" {
				return true
			}
		}
	}
	return false
}

type postInput struct {
	Question       string   `json:"question"`
	Options        []string `json:"options"`
	Recommendation string   `json:"recommendation"`
}

func detailString(raw json.RawMessage, field string) string {
	var m map[string]any
	json.Unmarshal(raw, &m)
	s, _ := m[field].(string)
	return s
}

func (d *Daemon) isVerified(v string) bool {
	if v == "" {
		return false
	}
	if slices.Contains(d.cfg.Verified, v) {
		return true
	}
	b, err := os.ReadFile(filepath.Join(d.cfg.Paths.ConfigDir, "verified-versions"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == v {
			return true
		}
	}
	return false
}

// canType reports whether keys may be pressed in the session's pane, and if
// not, why.
func (d *Daemon) canType(s *session) (bool, string) {
	switch {
	case s.Pane == "":
		return false, "it isn't running in tmux"
	case s.TmuxSocket == "":
		return false, "its tmux server is unknown"
	case s.Version == "":
		return false, "its Claude Code version is unknown"
	case !d.isVerified(s.Version):
		return false, fmt.Sprintf("Claude Code %s hasn't been checked with ctg yet (re-run the step-1 probe on it)", s.Version)
	}
	return true, ""
}

// whyNotAnswer says why an item can't be answered from Telegram, or "".
func (d *Daemon) whyNotAnswer(s *session, it *item) string {
	if it.Blocked != "" {
		return it.Blocked
	}
	ok, why := d.canType(s)
	if !ok && (it.Kind == ipc.KindPost || it.Kind == ipc.KindBlock) && s.Mailbox != "" {
		return "" // a reply can go through the mailbox
	}
	if !ok {
		return why
	}
	return ""
}

// drive runs f against the session's pane in the background, one at a time
// per session. done runs afterwards with d.mu held. Called with d.mu held.
func (d *Daemon) drive(s *session, what string, f func(*drive.Driver) error, done func(error)) bool {
	if s.driving {
		return false
	}
	s.driving = true
	term := d.cfg.Terminal(s.TmuxSocket, s.Pane)
	ref := s.ref()
	go func() {
		dr := &drive.Driver{T: term, Wait: d.cfg.DriveWait, Pressed: func(k string) {
			d.record("keys", ref, what+": "+k)
		}}
		err := f(dr)
		d.mu.Lock()
		defer d.mu.Unlock()
		s.driving = false
		done(err)
		d.save()
	}()
	return true
}

// whyNot turns a driver error into the reason shown to the owner (Q17, Q64).
func whyNot(err error) string {
	var me *drive.MismatchError
	switch {
	case errors.Is(err, drive.ErrStarted):
		return err.Error()
	case errors.As(err, &me) && me.Sent:
		why := err.Error()
		if len(me.Done) > 0 {
			why = "already answered " + strings.Join(me.Done, ", ") + "; " + why
		}
		return why
	default:
		return err.Error()
	}
}

func (d *Daemon) itemByScope(parts []string) (*session, *item) {
	if len(parts) < 3 {
		return nil, nil
	}
	s, ok := d.scopeArg(parts)
	if !ok || s == nil {
		return nil, nil
	}
	for _, it := range s.Items {
		if it.Num == parts[2] {
			return s, it
		}
	}
	return s, nil
}

func (d *Daemon) itemData(op string, s *session, it *item, extra ...any) string {
	data := op + ":" + s.scope() + ":" + it.Num
	for _, e := range extra {
		data += fmt.Sprintf(":%v", e)
	}
	return data
}

// picksFor returns a question's answers so far, one per question.
func picksFor(it *item, n int) []drive.Pick {
	if len(it.picks) != n {
		it.picks = make([]drive.Pick, n)
	}
	return it.picks
}

func picked(p drive.Pick, k int) bool { return slices.Contains(p.Options, k) }

func complete(it *item) bool {
	qs := parseAsk(it.Detail).Questions
	picks := picksFor(it, len(qs))
	for i := range qs {
		if len(picks[i].Options) == 0 && picks[i].Other == "" {
			return false
		}
	}
	return len(qs) > 0
}

func (d *Daemon) itemView(s *session, it *item) view {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s %s · %s\n", d.header(), icon(it.Kind), it.Num, s.label())
	switch it.Kind {
	case ipc.KindPermission:
		b.WriteString(it.Summary)
		if cmd := detailString(it.Detail, "command"); strings.Contains(cmd, "\n") || len(cmd) > 150 {
			b.WriteString("\n\n" + telegram.Clip(cmd, 1500))
		}
	case ipc.KindPlan:
		b.WriteString(telegram.Clip(detailString(it.Detail, "plan"), 1500))
	case ipc.KindQuestion:
		qs := parseAsk(it.Detail).Questions
		picks := picksFor(it, len(qs))
		for qi, q := range qs {
			if qi > 0 {
				b.WriteString("\n")
			}
			prefix := ""
			if len(qs) > 1 {
				prefix = fmt.Sprintf("Q%d · ", qi+1)
			}
			kind := ""
			if q.MultiSelect {
				kind = " (pick any)"
			}
			fmt.Fprintf(&b, "%s%s: %s%s\n", prefix, q.Header, q.Question, kind)
			for oi, o := range q.Options {
				mark := "  "
				if picked(picks[qi], oi+1) {
					mark = "✓ "
				}
				fmt.Fprintf(&b, "%s%d. %s", mark, oi+1, o.Label)
				if o.Description != "" {
					fmt.Fprintf(&b, ": %s", o.Description)
				}
				b.WriteString("\n")
			}
			if picks[qi].Other != "" {
				fmt.Fprintf(&b, "✓ ✍️ %s\n", picks[qi].Other)
			}
		}
	case ipc.KindPost:
		var p postInput
		json.Unmarshal(it.Detail, &p)
		b.WriteString(p.Question)
		for oi, o := range p.Options {
			fmt.Fprintf(&b, "\n%d. %s", oi+1, o)
		}
		if p.Recommendation != "" {
			b.WriteString("\n➡️ " + p.Recommendation)
		}
	default:
		b.WriteString(it.Summary)
	}
	text := strings.TrimRight(b.String(), "\n")
	if it.Kind == ipc.KindBlock {
		text = d.blockView(s, it)
	}
	text = htmlCard(text)
	back := row(button("✖ Dismiss", d.itemData("dm", s, it)), button("« Pending", "p:"+s.scope()))
	if it.sending {
		return view{text + "\n\n⏳ Answering from Telegram…", keyboard(back)}
	}
	if why := d.whyNotAnswer(s, it); why != "" {
		var extra []telegram.InlineKeyboardButton
		if it.Blocked != "" {
			extra = append(extra, button("🔁 Retry", d.itemData("ir", s, it)))
		}
		if ok, _ := canKeys(s); ok {
			extra = append(extra, button("📺 Screen", "sc:"+s.scope()))
		}
		rows := [][]telegram.InlineKeyboardButton{back}
		if len(extra) > 0 {
			rows = [][]telegram.InlineKeyboardButton{extra, back}
		}
		return view{text + "\n\n⚠️ Answer in the terminal: " + why + ".", keyboard(rows...)}
	}

	var rows [][]telegram.InlineKeyboardButton
	switch it.Kind {
	case ipc.KindPermission:
		rows = append(rows,
			row(button("✅ Allow once", d.itemData("pa", s, it)), button("❌ Deny", d.itemData("pd", s, it))),
			row(button("💬 Deny with a reason…", d.itemData("pr", s, it))))
		if diffable(it.Tool) {
			rows = append(rows, row(button("🧾 Diff", "di:"+s.scope()+":"+it.Num)))
		}
	case ipc.KindPlan:
		rows = append(rows, row(button("✅ Approve", d.itemData("la", s, it)), button("✍️ Change…", d.itemData("lc", s, it))))
	case ipc.KindQuestion:
		qs := parseAsk(it.Detail).Questions
		picks := picksFor(it, len(qs))
		for qi, q := range qs {
			var r []telegram.InlineKeyboardButton
			for oi, o := range q.Options {
				label := o.Label
				if len(qs) > 1 {
					label = fmt.Sprintf("Q%d %s", qi+1, label)
				}
				if picked(picks[qi], oi+1) {
					label = "✓ " + label
				}
				r = append(r, button(telegram.Clip(label, 40), d.itemData("qo", s, it, qi, oi+1)))
				if len(r) == 2 {
					rows, r = append(rows, r), nil
				}
			}
			if len(r) > 0 {
				rows = append(rows, r)
			}
			other := "✍️ Other…"
			if len(qs) > 1 {
				other = fmt.Sprintf("✍️ Q%d Other…", qi+1)
			}
			if picks[qi].Other != "" {
				other = "✓ ✍️ " + telegram.Clip(picks[qi].Other, 24)
			}
			rows = append(rows, row(button(other, d.itemData("qx", s, it, qi))))
		}
		if !immediate(it) && complete(it) {
			rows = append(rows, row(button("📤 Send", d.itemData("qs", s, it))))
		}
	case ipc.KindPost:
		var p postInput
		json.Unmarshal(it.Detail, &p)
		var r []telegram.InlineKeyboardButton
		for oi, o := range p.Options {
			r = append(r, button(telegram.Clip(o, 40), d.itemData("po", s, it, oi+1)))
			if len(r) == 2 {
				rows, r = append(rows, r), nil
			}
		}
		if len(r) > 0 {
			rows = append(rows, r)
		}
		if p.Recommendation != "" {
			rows = append(rows, row(button("✅ Accept the recommendation", d.itemData("pk", s, it))))
		}
		rows = append(rows, row(button("✍️ Reply…", d.itemData("px", s, it))))
	case ipc.KindBlock:
		rows = append(rows, d.blockButtons(s, it)...)
		if n := len(drafts(s)); n > 0 {
			rows = append(rows, row(button("📤 Send "+plural(n, "answer"), "bs:"+s.scope())))
		}
	}
	rows = append(rows, back)
	return view{text, keyboard(rows...)}
}

// immediate is a lone single-choice question, answered as soon as an
// option is tapped.
func immediate(it *item) bool {
	qs := parseAsk(it.Detail).Questions
	return len(qs) == 1 && !qs[0].MultiSelect
}

// showItem shows an item's view: in message msg, or in a new message when
// msg is 0. The item remembers the message, so closing it updates it too.
func (d *Daemon) showItem(s *session, it *item, msg int64) {
	v := d.itemView(s, it)
	if msg != 0 {
		it.addView(msg)
		d.editMode(msg, v, "HTML")
		return
	}
	chat := d.chatID
	tag := s.tag()
	src := d.mask(s, it.sourceJSON())
	btns := buttonLabels(v.kb)
	d.send(func(ctx context.Context) {
		m, err := d.tg.SendMessageMode(ctx, chat, v.text, "HTML", v.kb)
		if err != nil {
			d.cfg.Log.Warn("sending an item", "item", it.Num, "err", err)
			return
		}
		d.recordMessage(audit.MessageEntry{
			Op: "send", MsgID: m.MessageID, Kind: "item", Session: tag, Mode: "HTML",
			Text: v.text, Buttons: btns, Sources: []string{src},
		})
		d.mu.Lock()
		it.addView(m.MessageID)
		d.mu.Unlock()
	})
}

func (it *item) addView(msg int64) {
	if !slices.Contains(it.views, msg) {
		it.views = append(it.views, msg)
	}
}

// onItem handles the buttons of an item. Called with d.mu held.
func (d *Daemon) onItem(q *telegram.CallbackQuery, parts []string) string {
	msg := q.Message.MessageID
	s, it := d.itemByScope(parts)
	if it == nil {
		d.edit(msg, d.pendingView(s))
		return "That item is closed."
	}
	op := parts[0]
	switch op {
	case "a":
		d.showItem(s, it, 0)
		return ""
	case "i":
		d.showItem(s, it, msg)
		return ""
	case "dm":
		d.dismissItem(s, it)
		return "Dismissed " + it.Num
	}
	if it.sending {
		return "Still answering it."
	}
	if op == "ir" {
		return d.retryItem(s, it, msg)
	}
	if why := d.whyNotAnswer(s, it); why != "" {
		d.showItem(s, it, msg)
		return "Answer it in the terminal."
	}
	arg := func(i int) int {
		if len(parts) <= i {
			return -1
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return -1
		}
		return n
	}
	switch op {
	case "pa":
		d.answer(s, it, "allowed once", func(dr *drive.Driver) error { return dr.Allow(drive.Permission{Needle: it.Needle}) })
	case "pd":
		d.answer(s, it, "denied", func(dr *drive.Driver) error { return dr.Deny(drive.Permission{Needle: it.Needle}, "") })
	case "pr":
		d.askFor(s, it, "reason", 0, "💬 Type the reason for denying "+it.Num+". /cancel to stop.")
	case "la":
		d.answer(s, it, "approved", func(dr *drive.Driver) error { return dr.Approve() })
	case "lc":
		d.askFor(s, it, "change", 0, "✍️ Type what Claude should change in the plan for "+it.Num+". /cancel to stop.")
	case "qo":
		qi, oi := arg(3), arg(4)
		qs := parseAsk(it.Detail).Questions
		if qi < 0 || qi >= len(qs) || oi < 1 || oi > len(qs[qi].Options) {
			return ""
		}
		picks := picksFor(it, len(qs))
		p := &picks[qi]
		switch {
		case !qs[qi].MultiSelect:
			p.Options, p.Other = []int{oi}, ""
		case picked(*p, oi):
			p.Options = slices.DeleteFunc(p.Options, func(k int) bool { return k == oi })
		default:
			p.Options = append(p.Options, oi)
			slices.Sort(p.Options)
		}
		if immediate(it) {
			d.sendAnswers(s, it)
			return ""
		}
		d.showItem(s, it, msg)
	case "qx":
		qi := arg(3)
		if qs := parseAsk(it.Detail).Questions; qi < 0 || qi >= len(qs) {
			return ""
		}
		d.askFor(s, it, "other", qi, fmt.Sprintf("✍️ Type your own answer for Q%d. /cancel to stop.", qi+1))
	case "qs":
		if !complete(it) {
			return "Answer every question first."
		}
		d.sendAnswers(s, it)
	case "po":
		var p postInput
		json.Unmarshal(it.Detail, &p)
		if oi := arg(3); oi >= 1 && oi <= len(p.Options) {
			d.reply1(s, it, p.Options[oi-1])
		}
	case "pk":
		var p postInput
		json.Unmarshal(it.Detail, &p)
		d.reply1(s, it, "Go with your recommendation: "+p.Recommendation)
	case "px":
		d.askFor(s, it, "reply", 0, "✍️ Type your answer to "+it.Num+". /cancel to stop.")
	case "ba", "br":
		toast := d.onBlock(s, it, op)
		if op == "ba" && slices.Contains(it.views, msg) {
			d.showItem(s, it, msg)
		}
		return toast
	}
	return ""
}

func (d *Daemon) askFor(s *session, it *item, action string, qi int, prompt string) {
	d.st.Await = &awaiting{Session: s.ID, Key: it.Key, Action: action, QI: qi, Until: d.now().Add(awaitFor)}
	d.reply(prompt, nil)
}

// takeText uses a message as the text asked for. Called with d.mu held.
func (d *Daemon) takeText(m *telegram.Message, text string) {
	a := d.st.Await
	d.st.Await = nil
	if a.Action == "ktype" || a.Action == "ksecret" {
		d.keyboardText(a, m, text)
		return
	}
	if a.Action == "lastn" {
		d.takeLastN(a, text)
		return
	}
	switch a.Action {
	case "newname":
		d.takeNewName(text)
		return
	case "newdir":
		d.takeNewDir(text)
		return
	case "resumename":
		d.takeResumeName(text)
		return
	case "grep":
		d.startGrep(a.Session, text)
		return
	}
	s := d.byID(a.Session)
	var it *item
	if s != nil {
		for _, i := range s.Items {
			if i.Key == a.Key {
				it = i
			}
		}
	}
	if it == nil {
		d.reply("That item is closed, so the text went nowhere.", nil)
		return
	}
	if why := d.whyNotAnswer(s, it); why != "" {
		d.reply("Answer "+it.Num+" in the terminal: "+why+".", nil)
		return
	}
	switch a.Action {
	case "reason":
		d.answer(s, it, "denied: "+oneLine(text), func(dr *drive.Driver) error { return dr.Deny(drive.Permission{Needle: it.Needle}, text) })
	case "change":
		d.answer(s, it, "sent back: "+oneLine(text), func(dr *drive.Driver) error { return dr.Change(text) })
	case "reply":
		d.reply1(s, it, text)
	case "block":
		it.Draft = oneLine(text)
		d.save()
		d.showStatus(s, true)
	case "other":
		qs := parseAsk(it.Detail).Questions
		picks := picksFor(it, len(qs))
		p := &picks[a.QI]
		p.Other = oneLine(text)
		if !qs[a.QI].MultiSelect {
			p.Options = nil
		}
		if immediate(it) {
			d.sendAnswers(s, it)
			return
		}
		d.showItem(s, it, 0)
	}
}

// sendAnswers presses a question's collected answers in one go (Q18).
func (d *Daemon) sendAnswers(s *session, it *item) {
	ask := parseAsk(it.Detail)
	var qs []drive.Question
	var parts []string
	picks := picksFor(it, len(ask.Questions))
	for qi, q := range ask.Questions {
		dq := drive.Question{Question: q.Question, Header: q.Header, MultiSelect: q.MultiSelect}
		var chosen []string
		for _, o := range q.Options {
			dq.Options = append(dq.Options, o.Label)
		}
		for _, k := range picks[qi].Options {
			chosen = append(chosen, q.Options[k-1].Label)
		}
		if picks[qi].Other != "" {
			chosen = append(chosen, picks[qi].Other)
		}
		qs = append(qs, dq)
		parts = append(parts, q.Question+" → "+strings.Join(chosen, ", "))
	}
	answers := slices.Clone(picks)
	d.answer(s, it, "answered: "+strings.Join(parts, "; "), func(dr *drive.Driver) error { return dr.Answer(qs, answers) })
}

// answer presses keys to answer an item, and reports how it went in the
// item's views.
func (d *Daemon) answer(s *session, it *item, what string, f func(*drive.Driver) error) {
	ok := d.drive(s, "answer "+it.Num, f, func(err error) {
		it.sending = false
		if it.closed && err == nil {
			return // its views already say how it ended
		}
		if err != nil {
			it.AnsweredBy = ""
			it.Blocked = whyNot(err)
			d.record("refused", s.ref(), it.Num+": "+it.Blocked)
			for _, msg := range it.views {
				d.showItem(s, it, msg)
			}
			var me *drive.MismatchError
			if errors.As(err, &me) && me.Sent {
				d.showScreen(s, 0, fmt.Sprintf("⚠️ %s · %s · answer in the terminal: %s.", it.Num, s.label(), it.Blocked))
			}
			return
		}
		d.editViews(it, fmt.Sprintf("📤 %s · %s · sent from Telegram (%s). Waiting for Claude Code.", it.Num, s.label(), what))
	})
	if !ok {
		d.reply("Keys are being pressed in "+s.ref()+" already; try again in a moment.", nil)
		return
	}
	it.sending, it.AnsweredBy, it.Blocked = true, "telegram", ""
	it.replay, it.replayWhat = f, what
	d.record("answer", s.ref(), it.Num+" "+what)
	d.editViews(it, fmt.Sprintf("⏳ %s · %s · answering from Telegram (%s)…", it.Num, s.label(), what))
}

// retryItem runs a refused item's last answer again (Q65): the same check,
// and no keys unless the screen matches.
func (d *Daemon) retryItem(s *session, it *item, msg int64) string {
	if hasPreview(parseAsk(it.Detail)) {
		it.Blocked = "this dialog has a preview pane"
		d.showItem(s, it, msg)
		return "This dialog has a preview pane; answer it in the terminal."
	}
	switch {
	case it.replay != nil:
		it.Blocked = ""
		d.answer(s, it, it.replayWhat, it.replay)
	case it.Kind == ipc.KindQuestion && complete(it):
		it.Blocked = ""
		d.sendAnswers(s, it)
	default:
		it.Blocked = ""
		d.showItem(s, it, msg)
		return "Try the buttons again."
	}
	return ""
}

func (d *Daemon) editViews(it *item, text string) {
	for _, msg := range it.views {
		d.edit(msg, view{text, nil})
	}
}

func (d *Daemon) dismissItem(s *session, it *item) bool {
	if s == nil || it == nil {
		return false
	}
	i := slices.IndexFunc(s.Items, func(x *item) bool { return x == it || x.Num == it.Num })
	if i < 0 {
		return false
	}
	target := s.Items[i]
	d.closeWith(s, i, fmt.Sprintf("✖ %s · %s · dismissed", target.Num, s.label()))
	d.record("dismiss", s.ref(), target.Num)
	d.refreshStatus(s)
	d.noteIdle(s, false)
	return true
}

func (d *Daemon) dismissAll(s *session) int {
	n := 0
	for _, ses := range d.st.Sessions {
		if s != nil && ses != s {
			continue
		}
		for i := len(ses.Items) - 1; i >= 0; i-- {
			it := ses.Items[i]
			d.closeWith(ses, i, fmt.Sprintf("✖ %s · %s · dismissed", it.Num, ses.label()))
			d.record("dismiss", ses.ref(), it.Num)
			n++
		}
		d.refreshStatus(ses)
		d.noteIdle(ses, false)
	}
	return n
}

func (d *Daemon) cmdDismiss(args []string) {
	if len(args) == 0 {
		s := d.byID(d.st.Selected)
		n := d.dismissAll(s)
		if n == 0 {
			d.reply("Nothing pending to dismiss.", nil)
			return
		}
		d.reply(fmt.Sprintf("✖ Dismissed %s.", plural(n, "item")), nil)
		return
	}
	arg := strings.TrimSpace(args[0])
	if strings.EqualFold(arg, "all") {
		n := d.dismissAll(nil)
		if n == 0 {
			d.reply("Nothing pending to dismiss.", nil)
			return
		}
		d.reply(fmt.Sprintf("✖ Dismissed %s.", plural(n, "item")), nil)
		return
	}
	if strings.Contains(arg, ".") {
		for _, s := range d.st.Sessions {
			for _, it := range s.Items {
				if it.Num == arg {
					d.dismissItem(s, it)
					d.reply("✖ Dismissed "+it.Num+".", nil)
					return
				}
			}
		}
		d.reply("No pending item "+arg+".", nil)
		return
	}
	s, err := d.lookup(arg)
	if err != nil {
		d.reply(err.Error(), nil)
		return
	}
	n := d.dismissAll(s)
	if n == 0 {
		d.reply("Nothing pending for "+s.ref()+".", nil)
		return
	}
	d.reply(fmt.Sprintf("✖ Dismissed %s for %s.", plural(n, "item"), s.ref()), nil)
}

// reply1 answers a non-blocking question by typing "[answer to 2.3] …"
// into the prompt (Q21), or through the mailbox for a session not in tmux.
func (d *Daemon) reply1(s *session, it *item, text string) {
	msg := fmt.Sprintf("[answer to %s] %s", it.Num, text)
	if ok, _ := d.canType(s); !ok && s.Mailbox != "" {
		it.AnsweredBy = "telegram"
		d.mailbox(s, msg)
		d.editViews(it, fmt.Sprintf("📨 %s · %s · answered from Telegram through the mailbox.", it.Num, s.label()))
		return
	}
	d.answer(s, it, "answered: "+text, func(dr *drive.Driver) error { return dr.Type(msg) })
}

// sendText sends plain text to the selected session (Q14).
func (d *Daemon) sendText(text string) {
	s := d.byID(d.st.Selected)
	if s == nil {
		d.reply("Pick a session first: /list, then tap one.", nil)
		return
	}
	if ok, why := d.canType(s); !ok {
		if s.Pane == "" && s.Mailbox != "" {
			d.mailbox(s, d.withFiles(s, text))
			return
		}
		d.reply("Can't type into "+s.ref()+": "+why+".", nil)
		return
	}
	d.typeInto(s, d.withFiles(s, text), false)
}

// typeInto types text into the session's prompt, after Esc when interrupt
// is set (Q5).
func (d *Daemon) typeInto(s *session, text string, interrupt bool) {
	ref := s.ref()
	action := "type"
	if interrupt {
		action = "interrupt"
	}
	ok := d.drive(s, action, func(dr *drive.Driver) error {
		if interrupt {
			if err := dr.Interrupt(); err != nil {
				return err
			}
		}
		if text == "" {
			return nil
		}
		return dr.Type(text)
	}, func(err error) {
		if err != nil {
			d.noteProblem("tmux: " + err.Error())
			d.record("refused", ref, action+": "+whyNot(err))
			d.reply("Didn't type into "+ref+": "+whyNot(err)+".", nil)
			return
		}
		switch {
		case text == "":
			d.reply("⏹ Esc pressed in "+ref+".", nil)
		case interrupt:
			d.reply("⏹ Esc, then ⌨️ typed into "+ref+".", nil)
		default:
			d.reply("⌨️ Typed into "+ref+".", nil)
		}
	})
	if !ok {
		d.reply("Keys are being pressed in "+ref+" already; try again in a moment.", nil)
		return
	}
	if text != "" {
		d.notePhone(s.ID, text)
	}
	d.record(action, ref, text)
}

// mailbox posts text to a session's messaging socket. It arrives as a
// message from another session, with no user authority (Q9).
func (d *Daemon) mailbox(s *session, text string) {
	ref, path := s.ref(), s.Mailbox
	d.record("mailbox", ref, text)
	go func() {
		err := mailboxPost(path, text)
		d.mu.Lock()
		defer d.mu.Unlock()
		if err != nil {
			d.reply("Couldn't reach "+ref+"'s mailbox: "+err.Error(), nil)
			return
		}
		d.reply("📨 Sent to "+ref+" through the mailbox. It arrives as a message from another session, without your authority.", nil)
	}()
}

func mailboxPost(path, text string) error {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	line, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
	if err != nil {
		return err
	}
	_, err = c.Write(append(line, '\n'))
	return err
}
