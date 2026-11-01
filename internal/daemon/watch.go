package daemon

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/transcript"
)

// watchLoop reads the sessions' transcripts. Only the transcript says when
// a dialog was cancelled, a permission denied or a turn cut short by Esc:
// no hook runs for those (step 1, unknowns 5 and 6).
func (d *Daemon) watchLoop(ctx context.Context) {
	t := time.NewTicker(d.cfg.WatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.watch()
		}
	}
}

func (d *Daemon) watch() {
	type job struct {
		s    *session
		tail *transcript.Tail
	}
	var jobs []job
	d.mu.Lock()
	for _, s := range d.st.Sessions {
		if s.Transcript == "" {
			continue
		}
		if s.tail == nil {
			if s.ReadTo > 0 {
				// After a restart, carry on where the last run stopped.
				s.tail = transcript.NewTailAt(s.Transcript, s.ReadTo)
			} else {
				// History is skipped: what matters is what happens from now.
				s.tail = transcript.NewTail(s.Transcript, true)
			}
		}
		jobs = append(jobs, job{s, s.tail})
	}
	d.mu.Unlock()

	// The files are read without the lock; only this goroutine uses tails.
	for _, j := range jobs {
		lines, err := j.tail.PollLines()
		if err != nil {
			d.cfg.Log.Warn("reading a transcript", "session", j.s.Num, "err", err)
			continue
		}
		var events []transcript.Event
		for _, l := range lines {
			events = append(events, transcript.Parse(l)...)
		}
		d.mu.Lock()
		if d.byID(j.s.ID) == j.s {
			moved := j.s.ReadTo != j.tail.Offset()
			j.s.ReadTo = j.tail.Offset()
			d.apply(j.s, events)
			d.feedFollow(j.s, lines)
			if moved || len(events) > 0 {
				d.save()
			}
		}
		d.mu.Unlock()
	}
	d.mu.Lock()
	d.flushFollow(false)
	d.mu.Unlock()
}

const keepTools = 200

// isDialog reports whether an item is a dialog on the screen, which can't
// outlive its turn.
func isDialog(it *item) bool {
	return it.Kind == ipc.KindQuestion || it.Kind == ipc.KindPermission || it.Kind == ipc.KindPlan
}

func (d *Daemon) apply(s *session, events []transcript.Event) {
	for _, e := range events {
		switch e.Kind {
		case transcript.ToolUse:
			if s.toolKeys == nil || len(s.toolKeys) > keepTools {
				s.toolKeys, s.toolNames = map[string]string{}, map[string]string{}
			}
			s.toolKeys[e.ToolID] = ipc.PermKey(e.ToolName, e.Input)
			s.toolNames[e.ToolID] = e.ToolName
			if e.StopID != "" {
				if s.stopIDs == nil {
					s.stopIDs = map[string]string{}
				}
				s.stopIDs[e.ToolID] = e.StopID
			}
		case transcript.ToolResult:
			d.toolEnded(s, e)
			if e.StartID != "" {
				kind := e.StartKind
				if s.toolNames[e.ToolID] == "Monitor" {
					kind = "monitor"
				}
				d.startTask(s, e.StartID, kind)
			}
			if id := s.stopIDs[e.ToolID]; id != "" {
				delete(s.stopIDs, e.ToolID)
				if !e.IsError {
					d.endTask(s, id, false)
				}
			}
		case transcript.TaskEnd:
			d.endTask(s, e.EndedID, true)
		case transcript.Interrupted:
			d.closeDialogs(s, "interrupted")
			s.Busy = false
			s.woke = false
			s.Ran = true
		case transcript.TurnEnd:
			d.closeDialogs(s, "closed")
			s.Busy = false
			s.woke = false
			s.Ran = true
		}
	}
	d.noteIdle(s, false)
}

// toolEnded closes the item for a tool call that has finished, one way or
// the other.
func (d *Daemon) toolEnded(s *session, e transcript.Event) {
	i := slices.IndexFunc(s.Items, func(it *item) bool {
		return it.Key == e.ToolID || it.Key == s.toolKeys[e.ToolID] && it.Key != ""
	})
	if i < 0 {
		// The prompt's input may differ from the model's, say a path made
		// absolute; fall back to the oldest prompt for the same tool.
		name := s.toolNames[e.ToolID]
		i = slices.IndexFunc(s.Items, func(it *item) bool { return it.Kind == ipc.KindPermission && name != "" && it.Tool == name })
	}
	if i < 0 {
		return
	}
	it := s.Items[i]
	var note string
	switch {
	case it.Kind == ipc.KindQuestion && e.IsError:
		note = "cancelled"
	case it.Kind == ipc.KindQuestion:
		note = "answered"
		if len(e.Answers) > 0 {
			note += ": " + answersText(it, e.Answers)
		}
	case it.Kind == ipc.KindPlan && e.IsError:
		note = "sent back"
	case it.Kind == ipc.KindPlan:
		note = "approved"
	case e.IsError:
		note = "denied"
	default:
		note = "allowed"
	}
	d.closeAt(s, i, note)
}

func answersText(it *item, answers map[string]string) string {
	var parts []string
	for _, q := range parseAsk(it.Detail).Questions {
		if a, ok := answers[q.Question]; ok {
			parts = append(parts, q.Question+" → "+a)
		}
	}
	return strings.Join(parts, "; ")
}

// closeDialogs closes the session's open dialogs: none survives its turn.
func (d *Daemon) closeDialogs(s *session, note string) {
	for i := len(s.Items) - 1; i >= 0; i-- {
		it := s.Items[i]
		if isDialog(it) && !it.sending {
			d.closeAt(s, i, note)
		} else if it.Kind == ipc.KindPost && note == "closed" {
			if it.TurnSeen && !it.sending {
				d.closeWith(s, i, "✅ "+it.Num+" · "+s.label()+" · no longer asked")
			} else {
				it.TurnSeen = true
			}
		}
	}
}

var (
	// "[answer to 2.3] …" is how Telegram types a post_question answer (Q21).
	answerToRE = regexp.MustCompile(`^\[answer to (\d+\.\d+)\]\s*(.*)`)
	// "2.3 b" is how the owner answers one in the terminal.
	itemNumRE = regexp.MustCompile(`^(\d+\.\d+)(?:[\s:.,)]+(.*)|$)`)
)

// promptSeen closes the non-blocking question a prompt answers (Q21).
func (d *Daemon) promptSeen(s *session, prompt string) {
	prompt = strings.TrimSpace(prompt)
	num, rest, by := "", "", ""
	if m := answerToRE.FindStringSubmatch(prompt); m != nil {
		num, rest, by = m[1], m[2], "telegram"
	} else if m := itemNumRE.FindStringSubmatch(prompt); m != nil {
		num, rest = m[1], m[2]
	} else {
		if !strings.Contains(prompt, "<task-notification>") && !qLineRE.MatchString(prompt) {
			for i := len(s.Items) - 1; i >= 0; i-- {
				if it := s.Items[i]; it.Kind == ipc.KindPost && !it.sending {
					note := "answered"
					if rest := oneLine(prompt); rest != "" {
						note += ": " + rest
					}
					d.closeAt(s, i, note)
				}
			}
		}
		return
	}
	i := slices.IndexFunc(s.Items, func(it *item) bool {
		return it.Num == num && (it.Kind == ipc.KindPost || it.Kind == ipc.KindBlock)
	})
	if i < 0 {
		return
	}
	if by != "" {
		s.Items[i].AnsweredBy = by
	}
	note := "answered"
	if rest = oneLine(rest); rest != "" {
		note += ": " + rest
	}
	d.closeAt(s, i, note)
	d.refreshStatus(s)
}
