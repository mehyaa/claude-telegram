package daemon

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/tmux"
)

// prefs is the global idle-ping settings (Q58). Nil means the default:
// pings on, reminders off.
type prefs struct {
	Pings     *bool `json:"pings,omitempty"`
	Reminders *bool `json:"reminders,omitempty"`
}

// reminderAfter is how long after going idle each reminder is sent (Q73).
var reminderAfter = []time.Duration{
	5 * time.Minute,
	10 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
	time.Hour,
	2 * time.Hour,
	4 * time.Hour,
	8 * time.Hour,
	12 * time.Hour,
	16 * time.Hour,
}

var reminderLabel = []string{"5m", "10m", "15m", "30m", "1h", "2h", "4h", "8h", "12h", "16h"}

func kindOf(typ string) string {
	switch typ {
	case "shell":
		return "shell"
	case "monitor":
		return "monitor"
	default:
		return "agent"
	}
}

func (s *session) hasDialog() bool {
	for _, it := range s.Items {
		if isDialog(it) {
			return true
		}
	}
	return false
}

func (s *session) taskCounts() (shells, monitors, agents int) {
	for _, t := range s.Tasks {
		switch t.Kind {
		case "shell":
			shells++
		case "monitor":
			monitors++
		default:
			agents++
		}
	}
	return shells, monitors, agents
}

func (s *session) taskMarks() (shell, monitor, agent bool) {
	shells, monitors, agents := s.taskCounts()
	return shells > 0, monitors > 0, agents > 0
}

func (s *session) hasWork() bool {
	shell, monitor, agent := s.taskMarks()
	return shell || monitor || agent
}

func (s *session) taskSummary() string {
	shells, monitors, agents := s.taskCounts()
	var parts []string
	if shells > 0 {
		parts = append(parts, "🐚 "+plural(shells, "shell"))
	}
	if monitors > 0 {
		parts = append(parts, "📡 "+plural(monitors, "monitor"))
	}
	if agents > 0 {
		parts = append(parts, "🤖 "+plural(agents, "agent"))
	}
	return strings.Join(parts, " · ")
}

// settled is the idle that pings: the turn has ended, no dialog is open,
// and no background work is running. A scheduled wake-up still counts (Q82).
func (s *session) settled() bool {
	return !s.Busy && !s.hasDialog() && !s.hasWork() && len(s.Items) == 0
}

// mark is the short state on a list button (Q57).
func (s *session) mark() string {
	shell, monitor, agent := s.taskMarks()
	if s.hasDialog() {
		return "❓"
	}
	bg := ""
	if shell {
		bg += "🐚"
	}
	if monitor {
		bg += "📡"
	}
	if agent {
		bg += "🤖"
	}
	if s.Busy {
		return "⚙️" + bg
	}
	if bg != "" {
		return bg
	}
	if s.Crons > 0 {
		return "💤⏰"
	}
	return "💤"
}

// stateLabel is the state in the list line (Q57).
func (s *session) stateLabel() string {
	m := s.mark()
	switch m {
	case "❓":
		return "❓ waiting"
	case "🐚":
		return "🐚 shell"
	case "📡":
		return "📡 monitor"
	case "🤖":
		return "🤖 agent"
	case "💤⏰":
		return "💤⏰"
	case "💤":
		return "💤 idle"
	}
	if strings.HasPrefix(m, "⚙️") {
		return m + " working"
	}
	return m
}

// titled is how a ping names a session: "2 · myapp-build".
func (s *session) titled() string {
	return fmt.Sprintf("%d · %s", s.Num, s.label())
}

func (d *Daemon) pingsOn(s *session) bool {
	if s != nil && s.Pings != nil {
		return *s.Pings
	}
	if d.st.Prefs.Pings != nil {
		return *d.st.Prefs.Pings
	}
	return true
}

func (d *Daemon) remindersOn(s *session) bool {
	if s != nil && !d.pingsOn(s) {
		return false
	}
	if d.st.Prefs.Reminders != nil {
		return *d.st.Prefs.Reminders
	}
	return false
}

func turnSource(d *Daemon, s *session, prompt string) string {
	if strings.Contains(prompt, "task-notification") {
		return "task"
	}
	if d.fromPhone(s, prompt) {
		return "telegram"
	}
	return "terminal"
}

func (d *Daemon) setTasks(s *session, tasks []ipc.Task, crons int) {
	s.Tasks = nil
	for _, t := range tasks {
		if t.ID == "" {
			continue
		}
		s.Tasks = append(s.Tasks, bgTask{ID: t.ID, Kind: kindOf(t.Type)})
	}
	s.Crons = crons
}

func (d *Daemon) startTask(s *session, id, kind string) {
	if id == "" {
		return
	}
	if kind == "" {
		kind = "agent"
	}
	for i, t := range s.Tasks {
		if t.ID == id {
			s.Tasks[i].Kind = kind
			return
		}
	}
	s.Tasks = append(s.Tasks, bgTask{ID: id, Kind: kind})
}

// endTask drops a background task. wake is set when a notification for an
// idle session is about to start a turn, so the idle ping waits for that
// turn's Stop (Q82).
func (d *Daemon) endTask(s *session, id string, wake bool) {
	n := slices.DeleteFunc(s.Tasks, func(t bgTask) bool { return t.ID == id })
	if len(n) == len(s.Tasks) {
		return
	}
	s.Tasks = n
	if wake && !s.Busy {
		s.woke = true
	}
}

// noteIdle starts or clears an idle stretch and sends the one ping (Q58).
// quiet marks the ping as already dealt with, for a replayed hook.
func (d *Daemon) noteIdle(s *session, quiet bool) {
	if s == nil {
		return
	}
	if !s.settled() || s.woke {
		if !s.settled() {
			s.IdleAt = time.Time{}
			s.Pinged = false
			s.RemindAt = 0
		}
		return
	}
	if !s.Ran {
		return
	}
	if s.IdleAt.IsZero() {
		s.IdleAt = d.now()
		s.Pinged = false
		s.RemindAt = 0
	}
	if s.Pinged || quiet || d.chatID == 0 {
		if quiet {
			s.Pinged = true
		}
		return
	}
	if !d.pingsOn(s) || d.pingsMuted(s) || d.atTerminal(s) {
		s.Pinged = true
		return
	}
	s.Pinged = true
	text := "💤 " + s.titled()
	if s.Crons > 0 {
		text = "💤⏰ " + s.titled()
	}
	d.reply(text, keyboard(row(button("Open "+fmt.Sprint(s.Num), "o:"+s.scope()))))
}

// atTerminal is the skip: the turn was typed in the terminal and that tmux
// client was active in the last minute (Q58).
// pingsMuted reports whether mute covers s, including a mute of every session.
func (d *Daemon) pingsMuted(s *session) bool {
	if d.st.Mute.All.on(d.now()) {
		return true
	}
	return s != nil && d.st.Mute.Sessions[s.ID].on(d.now())
}

func (d *Daemon) atTerminal(s *session) bool {
	if s.TurnFrom != "terminal" || s.Pane == "" || d.cfg.ClientActive == nil {
		return false
	}
	return d.cfg.ClientActive(s.TmuxSocket, s.Pane, d.now())
}

// touchSession restarts the reminder clock when an idle session is opened
// (Q73). The one idle ping is not sent again.
func (d *Daemon) touchSession(s *session) {
	if s == nil || !s.settled() || s.IdleAt.IsZero() {
		return
	}
	s.IdleAt = d.now()
	s.RemindAt = 0
}

func (d *Daemon) remind() {
	for _, s := range d.st.Sessions {
		if !s.settled() || s.IdleAt.IsZero() || s.Crons > 0 || s.RemindAt < 0 {
			continue
		}
		if !d.remindersOn(s) || d.pingsMuted(s) {
			continue
		}
		if s.RemindAt >= len(reminderAfter) {
			d.reply(fmt.Sprintf("💤 %s: no more reminders", s.titled()), nil)
			s.RemindAt = -1
			continue
		}
		if d.now().Sub(s.IdleAt) < reminderAfter[s.RemindAt] {
			continue
		}
		d.reply(fmt.Sprintf("💤 %s · %s", s.titled(), reminderLabel[s.RemindAt]), nil)
		s.RemindAt++
	}
}

func (d *Daemon) settingsView() view {
	pings, rem := "on", "off"
	if !d.pingsOn(nil) {
		pings = "off"
	}
	if d.st.Prefs.Reminders != nil && *d.st.Prefs.Reminders {
		rem = "on"
	}
	text := fmt.Sprintf("%s\n\n⚙️ Settings\nIdle pings: %s\nReminders: %s\n\nReminders start 5 minutes after a session goes idle, and stop after 16 hours.", d.header(), pings, rem)
	return view{text, keyboard(
		row(button("Idle pings: "+pings, "sg:a:p"), button("Reminders: "+rem, "sg:a:r")),
		row(button("«", "l")),
	)}
}

func (d *Daemon) togglePref(which string) {
	switch which {
	case "p":
		d.st.Prefs.Pings = toggle(d.st.Prefs.Pings, true)
	case "r":
		d.st.Prefs.Reminders = toggle(d.st.Prefs.Reminders, false)
	}
}

func toggle(p *bool, def bool) *bool {
	cur := def
	if p != nil {
		cur = *p
	}
	v := !cur
	return &v
}

func (d *Daemon) pingButton(s *session) telegram.InlineKeyboardButton {
	label := "💤 Pings: default"
	switch {
	case s.Pings == nil && d.pingsOn(nil):
		label = "💤 Pings: on"
	case s.Pings == nil:
		label = "💤 Pings: off"
	case *s.Pings:
		label = "💤 Pings: on · session"
	default:
		label = "💤 Pings: off · session"
	}
	return button(label, "ip:"+s.scope())
}

func (d *Daemon) cyclePings(s *session) {
	switch {
	case s.Pings == nil:
		f := false
		s.Pings = &f
	case !*s.Pings:
		t := true
		s.Pings = &t
	default:
		s.Pings = nil
	}
}

func clientRecent(socket, pane string, now time.Time) bool {
	return tmux.ClientRecent(socket, pane, time.Minute, now)
}
