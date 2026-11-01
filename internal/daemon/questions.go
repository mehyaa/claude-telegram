package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/blocks"
	"github.com/mehyaa/claude-telegram/internal/drive"
	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram"
)

// blockDetail is a ❓ block item's Detail (Q22).
type blockDetail struct {
	Label          string `json:"label,omitempty"`
	Title          string `json:"title,omitempty"`
	Question       string `json:"question"`
	Recommendation string `json:"recommendation,omitempty"`
}

func parseBlock(it *item) blockDetail {
	var b blockDetail
	json.Unmarshal(it.Detail, &b)
	return b
}

// accepted is the answer ✅ Accept puts in.
const accepted = "As you recommended."

func blockKey(b blocks.Block) string {
	if b.Label != "" {
		return "block:" + b.Label
	}
	sum := sha256.Sum256([]byte(b.Question))
	return "block:h:" + hex.EncodeToString(sum[:6])
}

// answerLabel is how an answer names its question in the prompt: "Q16", or
// the item's number for a block without one.
func answerLabel(it *item) string {
	if l := parseBlock(it).Label; l != "" {
		return l
	}
	return it.Num
}

func blockSummary(b blocks.Block) string {
	s := b.Question
	if b.Title != "" {
		s = b.Title + ": " + s
	}
	if b.Label != "" {
		s = b.Label + " " + s
	}
	return s
}

// replySeen turns the ❓ blocks of a turn's last reply into pending items
// (Q22). A question asked again keeps its item; one from an earlier reply
// that isn't asked again is closed.
func (d *Daemon) replySeen(s *session, reply string, replay bool) {
	keep := map[string]bool{}
	for _, b := range blocks.Parse(reply) {
		key := blockKey(b)
		if keep[key] {
			continue
		}
		keep[key] = true
		detail, _ := json.Marshal(blockDetail{Label: b.Label, Title: b.Title, Question: b.Question, Recommendation: b.Recommendation})
		if i := slices.IndexFunc(s.Items, func(it *item) bool { return it.Key == key }); i >= 0 {
			s.Items[i].Summary, s.Items[i].Detail = oneLine(blockSummary(b)), detail
			continue
		}
		d.addItem(s, ipc.Item{Key: key, Kind: ipc.KindBlock, Summary: blockSummary(b), Detail: detail}, time.Time{}, replay)
	}
	for i := len(s.Items) - 1; i >= 0; i-- {
		it := s.Items[i]
		switch {
		case it.Kind == ipc.KindBlock && !keep[it.Key] && !it.sending:
			d.closeWith(s, i, fmt.Sprintf("✅ %s · %s · no longer asked", it.Num, s.label()))
		case it.Kind == ipc.KindPost && it.TurnSeen && !it.sending:
			d.closeWith(s, i, fmt.Sprintf("✅ %s · %s · no longer asked", it.Num, s.label()))
		case it.Kind == ipc.KindPost:
			it.TurnSeen = true
		}
	}
	d.refreshStatus(s)
}

// blockButtons are a block's buttons, on its ping and its view.
func (d *Daemon) blockButtons(s *session, it *item) [][]telegram.InlineKeyboardButton {
	var r []telegram.InlineKeyboardButton
	if parseBlock(it).Recommendation != "" {
		r = append(r, button("✅ Accept", d.itemData("ba", s, it)))
	}
	r = append(r, button("✍️ Reply…", d.itemData("br", s, it)))
	return [][]telegram.InlineKeyboardButton{r}
}

func (d *Daemon) blockView(s *session, it *item) string {
	b := parseBlock(it)
	var t strings.Builder
	fmt.Fprintf(&t, "%s\n\n❓ %s · %s\n", d.header(), it.Num, s.label())
	if b.Label != "" || b.Title != "" {
		fmt.Fprintf(&t, "%s\n", strings.TrimSpace(b.Label+" "+b.Title))
	}
	t.WriteString(b.Question)
	if b.Recommendation != "" {
		t.WriteString("\n➡️ " + b.Recommendation)
	}
	if it.Draft != "" {
		t.WriteString("\n\n✍️ Your answer: " + it.Draft)
	}
	return t.String()
}

// onBlock handles a block's buttons. Called with d.mu held.
func (d *Daemon) onBlock(s *session, it *item, op string) string {
	switch op {
	case "ba":
		it.Draft = accepted
		d.save()
		d.refreshStatus(s)
		return "Accepted " + answerLabel(it)
	case "br":
		d.askFor(s, it, "block", 0, fmt.Sprintf("✍️ Type your answer to %s. /cancel to stop.", answerLabel(it)))
	}
	return ""
}

// drafts are the session's open questions with an answer ready, in order.
func drafts(s *session) []*item {
	var out []*item
	for _, it := range s.Items {
		if it.Kind == ipc.KindBlock && it.Draft != "" && !it.sending {
			out = append(out, it)
		}
	}
	return out
}

// refreshStatus shows the answers collected for the session's questions,
// with 📤 Send, in one message it keeps up to date.
func (d *Daemon) refreshStatus(s *session) { d.showStatus(s, false) }

// showStatus is refreshStatus; with fresh, the status comes as a new
// message, below what the owner just typed.
func (d *Daemon) showStatus(s *session, fresh bool) {
	var open []*item
	for _, it := range s.Items {
		if it.Kind == ipc.KindBlock {
			open = append(open, it)
		}
	}
	ready := drafts(s)
	msg := d.statusMsg[s.ID]
	if msg == -1 {
		d.statusDirty[s.ID] = true // sent again once its message ID is in
		return
	}
	if len(ready) == 0 {
		if msg != 0 {
			// Every question is closed or dismissed: the message loses its Send button.
			delete(d.statusMsg, s.ID)
			if len(open) == 0 {
				d.edit(msg, view{fmt.Sprintf("📝 %s · all answered", s.ref()), nil})
			} else {
				d.edit(msg, view{fmt.Sprintf("📝 %s · no answers ready", s.ref()), nil})
			}
		}
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📝 %s · answers to send", s.ref())
	for _, it := range open {
		a := it.Draft
		if a == "" {
			a = "(none yet)"
		}
		fmt.Fprintf(&b, "\n%s: %s", answerLabel(it), a)
	}
	v := view{b.String(), keyboard(row(button(fmt.Sprintf("📤 Send %s", plural(len(ready), "answer")), "bs:"+s.scope())))}
	if msg != 0 && !fresh {
		d.edit(msg, v)
		return
	}
	if msg != 0 {
		d.edit(msg, view{v.text, nil}) // the new message below takes over
	}
	d.statusMsg[s.ID] = -1 // being sent; the next refresh waits for its ID
	chat, id := d.chatID, s.ID
	d.send(func(ctx context.Context) {
		m, err := d.tg.SendMessage(ctx, chat, v.text, v.kb)
		d.mu.Lock()
		defer d.mu.Unlock()
		if err != nil {
			delete(d.statusMsg, id)
			d.cfg.Log.Warn("sending the answers so far", "err", err)
			return
		}
		d.statusMsg[id] = m.MessageID
		if d.statusDirty[id] {
			delete(d.statusDirty, id)
			if s := d.byID(id); s != nil {
				d.refreshStatus(s)
			}
		}
	})
}

// sendBlocks types the collected answers into the prompt as one message,
// "Q16: … Q17: …", one answer a line (Q22).
func (d *Daemon) sendBlocks(s *session) string {
	ready := drafts(s)
	if len(ready) == 0 {
		return "No answers to send yet."
	}
	var lines []string
	for _, it := range ready {
		lines = append(lines, answerLabel(it)+": "+it.Draft)
	}
	text := strings.Join(lines, "\n")
	// The status message is taken now: UserPromptSubmit may close the
	// questions before the driver is done (seen in the live probe).
	msg := d.statusMsg[s.ID]
	if msg == -1 {
		// Still being sent: once its ID is in, it is refreshed and loses
		// its button when the questions close.
		d.statusDirty[s.ID] = true
	} else {
		delete(d.statusMsg, s.ID)
	}
	finish := func(note string) {
		if msg > 0 {
			d.edit(msg, view{note, nil})
		} else {
			d.reply(note, nil)
		}
	}
	if ok, why := d.canType(s); !ok {
		if s.Pane == "" && s.Mailbox != "" {
			for _, it := range ready {
				it.AnsweredBy = "telegram"
			}
			d.mailbox(s, text)
			finish(fmt.Sprintf("📨 %s sent to %s through the mailbox:\n%s", plural(len(ready), "answer"), s.ref(), text))
			return ""
		}
		return "Can't type into " + s.ref() + ": " + why
	}
	ref := s.ref()
	ok := d.drive(s, "answers", func(dr *drive.Driver) error { return dr.Type(text) }, func(err error) {
		for _, it := range ready {
			it.sending = false
		}
		if err != nil {
			for _, it := range ready {
				it.AnsweredBy = ""
			}
			d.record("refused", ref, "answers: "+whyNot(err))
			d.reply("Didn't send the answers to "+ref+": "+whyNot(err)+".", nil)
			d.refreshStatus(s)
			return
		}
		finish(fmt.Sprintf("📤 %s sent to %s:\n%s", plural(len(ready), "answer"), ref, text))
	})
	if !ok {
		return "Keys are being pressed in " + ref + " already; try again in a moment."
	}
	for _, it := range ready {
		it.sending, it.AnsweredBy = true, "telegram"
	}
	d.notePhone(s.ID, text)
	d.record("type", ref, text)
	return ""
}

var (
	qLineRE   = regexp.MustCompile(`^\s*(Q\d+)\s*:\s*(.*)$`)
	numLineRE = regexp.MustCompile(`^\s*(\d+\.\d+)\s*:\s*(.*)$`)
)

// questionsAnswered closes the questions a prompt answers line by line,
// "Q16: …", whether Send typed it or the owner did (Q22).
func (d *Daemon) questionsAnswered(s *session, prompt string) {
	for _, line := range strings.Split(prompt, "\n") {
		match := func(it *item) bool { return false }
		var m []string
		if m = qLineRE.FindStringSubmatch(line); m != nil {
			match = func(it *item) bool { return it.Key == "block:"+m[1] }
		} else if m = numLineRE.FindStringSubmatch(line); m != nil {
			match = func(it *item) bool { return it.Kind == ipc.KindBlock && it.Num == m[1] }
		} else {
			continue
		}
		if i := slices.IndexFunc(s.Items, match); i >= 0 {
			note := "answered"
			if a := oneLine(m[2]); a != "" {
				note += ": " + a
			}
			d.closeAt(s, i, note)
		}
	}
	d.refreshStatus(s)
}
