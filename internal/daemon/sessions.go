package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/audit"
	"github.com/mehyaa/claude-telegram/internal/drive"
	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/transcript"
)

type session struct {
	ID         string    `json:"id"`
	Num        int       `json:"num"`
	PID        int       `json:"pid,omitempty"`
	ProcStart  string    `json:"proc_start,omitempty"` // with PID, tells a reused PID apart
	Cwd        string    `json:"cwd"`
	Branch     string    `json:"branch,omitempty"`
	Name       string    `json:"name,omitempty"`
	Pane       string    `json:"pane,omitempty"`
	TmuxSocket string    `json:"tmux_socket,omitempty"`
	Mailbox    string    `json:"mailbox,omitempty"`
	Transcript string    `json:"transcript,omitempty"`
	ReadTo     int64     `json:"read_to,omitempty"` // how far the transcript has been read
	Version    string    `json:"version,omitempty"`
	Busy       bool      `json:"busy"`
	Ran        bool      `json:"ran,omitempty"`         // a turn has started or ended (Q58)
	TurnFrom   string    `json:"turn_from,omitempty"`   // terminal, telegram, or task
	Tasks      []bgTask  `json:"tasks,omitempty"`       // background work still running (Q82)
	Crons      int       `json:"crons,omitempty"`       // pending scheduled wake-ups
	IdleAt     time.Time `json:"idle_at,omitzero"`      // when this idle stretch began
	Pinged     bool      `json:"idle_pinged,omitempty"` // the one idle ping was sent or skipped
	RemindAt   int       `json:"remind_at,omitempty"`   // next reminder index; -1 after the last line
	Pings      *bool     `json:"pings,omitempty"`       // nil follows the global setting (Q58)
	NextItem   int       `json:"next_item"`
	Items      []*item   `json:"items,omitempty"`
	Files      []string  `json:"files,omitempty"` // uploads waiting for the next message (Q75)

	tail      *transcript.Tail
	toolKeys  map[string]string // tool_use_id → PermKey, from the transcript
	toolNames map[string]string // tool_use_id → tool name
	stopIDs   map[string]string // tool_use_id → task id, for TaskStop
	woke      bool              // a task notification is about to start a turn
	driving   bool              // keys are being pressed in its pane
	secrets   []secret          // typed with 🔒 Secret; in memory only, to mask them
}

// bgTask is background work still running. Kind is "shell" or "agent".
type bgTask struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// secret is text typed with 🔒 Secret. It may show on the screen or in the
// transcript, so what the bot sends masks it for a while (Q27).
type secret struct {
	text  string
	until time.Time
}

// item is something pending for the owner, numbered under its session
// (Q11): 2.1, 2.2, …
type item struct {
	Num        string          `json:"num"`
	Key        string          `json:"key"`
	Kind       string          `json:"kind"`
	Tool       string          `json:"tool,omitempty"`
	Summary    string          `json:"summary"`
	Detail     json.RawMessage `json:"detail,omitempty"`
	Needle     string          `json:"needle,omitempty"`
	Added      time.Time       `json:"added"`
	PingMsg    int64           `json:"ping_msg,omitempty"`    // the ping's Telegram message, to edit when the item closes
	Draft      string          `json:"draft,omitempty"`       // a ❓ block's answer waiting for 📤 Send (Q22)
	AnsweredBy string          `json:"answered_by,omitempty"` // "telegram" once Telegram has sent the answer
	Blocked    string          `json:"blocked,omitempty"`     // why it must be answered in the terminal (Q17)
	TurnSeen   bool            `json:"turn_seen,omitempty"`   // a turn has ended since this item was added

	sending bool
	closed  bool
	picks   []drive.Pick // a question's answers collected so far (Q18)
	views   []int64      // messages showing the item with its buttons

	replay     func(*drive.Driver) error // last answer, for 🔁 Retry (Q65)
	replayWhat string
}

const labelMax = 24

// label is the session's own name, or else its folder and branch, shortened
// for a phone (Q12).
func (s *session) label() string {
	l := s.Name
	if l == "" {
		l = filepath.Base(s.Cwd)
		if s.Branch != "" {
			l += "/" + s.Branch
		}
	}
	return telegram.Clip(l, labelMax)
}

// ref is how messages name a session: "2 myapp-build".
func (s *session) ref() string { return fmt.Sprintf("%d %s", s.Num, s.label()) }

// tag is how message headers name a session: "#2 myapp-build".
func (s *session) tag() string {
	if s == nil {
		return ""
	}
	if s.Num > 0 {
		return fmt.Sprintf("#%d %s", s.Num, s.label())
	}
	return s.Name
}

func validCwd(cwd string) bool {
	cwd = strings.TrimSpace(cwd)
	return cwd != "" && filepath.Clean(cwd) != "."
}

// scope is the session's token in button data. The ID prefix keeps a stale
// button from acting on a later session that reused the number.
func (s *session) scope() string { return fmt.Sprintf("%d.%.8s", s.Num, s.ID) }

func (d *Daemon) byID(id string) *session {
	for _, s := range d.st.Sessions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// byPID finds a live session by its process, for ctg mcp.
func (d *Daemon) byPID(pid int) *session {
	if pid <= 0 {
		return nil
	}
	start, ok := d.cfg.ProcStart(pid)
	for _, s := range d.st.Sessions {
		if s.PID == pid && ok && s.ProcStart == start {
			return s
		}
	}
	return nil
}

func (d *Daemon) byNum(n int) *session {
	for _, s := range d.st.Sessions {
		if s.Num == n {
			return s
		}
	}
	return nil
}

// register adds a session or refreshes one it already knows. A session keeps
// its number for its lifetime; a new one takes the lowest free number (Q11).
func (d *Daemon) register(in ipc.Session, branch string) *session {
	var start string
	if in.PID > 0 {
		start, _ = d.cfg.ProcStart(in.PID)
	}
	s := d.byID(in.ID)
	if s == nil && in.PID > 0 {
		// /clear gives the same process a new session ID; it keeps its number.
		for _, o := range d.st.Sessions {
			if o.PID == in.PID && o.ProcStart == start {
				d.rekey(o, in.ID)
				s = o
				break
			}
		}
	}
	if s == nil {
		s = &session{ID: in.ID, Num: d.freeNum()}
		d.st.Sessions = append(d.st.Sessions, s)
		slices.SortFunc(d.st.Sessions, func(a, b *session) int { return a.Num - b.Num })
	}
	s.PID, s.ProcStart = in.PID, start
	s.Cwd, s.Branch = in.Cwd, branch
	s.Pane, s.TmuxSocket, s.Mailbox = in.Pane, in.TmuxSocket, in.Mailbox
	if s.Transcript != in.Transcript {
		s.tail, s.ReadTo = nil, 0
	}
	s.Transcript, s.Version = in.Transcript, in.Version
	if in.Name != "" {
		s.Name = in.Name
	}
	return s
}

func (d *Daemon) rekey(s *session, id string) {
	if sp, ok := d.st.Mute.Sessions[s.ID]; ok {
		delete(d.st.Mute.Sessions, s.ID)
		d.st.Mute.Sessions[id] = sp
	}
	if d.st.Selected == s.ID {
		d.st.Selected = id
	}
	if f := d.followOf(s.ID); f != nil {
		f.SessionID = id
	}
	if st, ok := d.streams[s.ID]; ok {
		delete(d.streams, s.ID)
		d.streams[id] = st
	}
	s.ID = id
}

func (d *Daemon) freeNum() int {
	for n := 1; ; n++ {
		if d.byNum(n) == nil {
			return n
		}
	}
}

// end forgets a session. Pings for its open items say so.
func (d *Daemon) end(id string) {
	i := slices.IndexFunc(d.st.Sessions, func(s *session) bool { return s.ID == id })
	if i < 0 {
		return
	}
	s := d.st.Sessions[i]
	for _, it := range s.Items {
		d.finishPing(it, fmt.Sprintf("⏹ %s · %s · session ended", it.Num, s.label()))
	}
	if d.followOf(id) != nil {
		d.flushFollowSession(s, true)
		d.removeFollow(id)
		d.reply("⏹ "+s.ref()+" ended; stopped following it.", nil)
	}
	d.st.Sessions = slices.Delete(d.st.Sessions, i, i+1)
	delete(d.st.Mute.Sessions, id)
	if d.st.Selected == id {
		d.st.Selected = ""
	}
}

// prune ends sessions whose process is gone, or ghost sessions without a
// valid working directory. A SessionEnd hook can be cut off after 1.5 s, so
// the daemon can't rely on hearing about every end.
func (d *Daemon) prune() {
	var gone []string
	for _, s := range d.st.Sessions {
		if !validCwd(s.Cwd) || (s.PID == 0 && s.Pane == "" && s.Mailbox == "") {
			gone = append(gone, s.ID)
			continue
		}
		if s.PID == 0 {
			continue
		}
		if start, ok := d.cfg.ProcStart(s.PID); !ok || start != s.ProcStart {
			gone = append(gone, s.ID)
		}
	}
	for _, id := range gone {
		d.end(id)
	}
}

// addItem records a pending item and pings, unless a mute covers the
// session or the item is being replayed. Adding a key twice is harmless.
func (d *Daemon) addItem(s *session, in ipc.Item, at time.Time, replay bool) *item {
	for _, it := range s.Items {
		if it.Key == in.Key {
			return it
		}
	}
	if at.IsZero() {
		at = d.now()
	}
	s.NextItem++
	it := &item{
		Num:     fmt.Sprintf("%d.%d", s.Num, s.NextItem),
		Key:     in.Key,
		Kind:    in.Kind,
		Tool:    in.Tool,
		Summary: oneLine(in.Summary),
		Detail:  in.Detail,
		Needle:  in.Needle,
		Added:   at,
	}
	if in.Kind == ipc.KindQuestion && hasPreview(parseAsk(in.Detail)) {
		it.Blocked = "this dialog has a preview pane"
	}
	s.Items = append(s.Items, it)
	muted := d.noteMuted(s)
	if !muted && !replay && d.chatID != 0 {
		d.ping(s, it)
	}
	return it
}

func (it *item) sourceJSON() string {
	b, _ := json.Marshal(map[string]any{
		"num":     it.Num,
		"key":     it.Key,
		"kind":    it.Kind,
		"tool":    it.Tool,
		"summary": it.Summary,
		"detail":  it.Detail,
	})
	return string(b)
}

func (d *Daemon) ping(s *session, it *item) {
	chat := d.chatID
	text := fmt.Sprintf("%s %s · %s · %s", icon(it.Kind), it.Num, s.label(), it.Summary)
	kb := keyboard(row(
		button("Answer", "a:"+s.scope()+":"+it.Num),
		button("✖ Dismiss", "dm:"+s.scope()+":"+it.Num),
		button("Open "+fmt.Sprint(s.Num), "o:"+s.scope()),
	))
	if it.Kind == ipc.KindBlock {
		// Accept and Reply right on the ping (Q22), plus Dismiss.
		kb = keyboard(append(d.blockButtons(s, it), row(
			button("✖ Dismiss", "dm:"+s.scope()+":"+it.Num),
			button("Open "+fmt.Sprint(s.Num), "o:"+s.scope()),
		))...)
	}
	tag := s.tag()
	src := d.mask(s, it.sourceJSON())
	clipped := telegram.Clip(text, 300)
	btns := buttonLabels(kb)
	d.send(func(ctx context.Context) {
		m, err := d.tg.SendMessage(ctx, chat, clipped, kb)
		if err != nil {
			d.cfg.Log.Warn("sending a ping", "item", it.Num, "err", err)
			return
		}
		d.recordMessage(audit.MessageEntry{
			Op: "send", MsgID: m.MessageID, Kind: "ping", Session: tag,
			Text: clipped, Buttons: btns, Sources: []string{src},
		})
		d.mu.Lock()
		it.PingMsg = m.MessageID
		d.save()
		d.mu.Unlock()
	})
}

// closeItem removes an item. Its ping is edited to say how it ended, and
// loses its buttons (Q18). note is "<verb>" or "<verb>: <detail>", such as
// "answered: Colour → Green"; where it was answered is added to the verb.
func (d *Daemon) closeItem(s *session, key, note string) {
	i := slices.IndexFunc(s.Items, func(it *item) bool { return it.Key == key })
	if i < 0 {
		return
	}
	d.closeAt(s, i, note)
}

func (d *Daemon) closeAt(s *session, i int, note string) {
	it := s.Items[i]
	verb, detail, _ := strings.Cut(note, ": ")
	if verb == "" {
		verb = "closed"
	}
	where := "in terminal"
	if it.AnsweredBy == "telegram" {
		where = "from Telegram"
	}
	text := fmt.Sprintf("✅ %s · %s · %s %s", it.Num, s.label(), verb, where)
	if detail != "" {
		text += ": " + oneLine(detail)
	}
	d.closeWith(s, i, text)
}

// closeWith removes item i and edits its ping and views to text.
func (d *Daemon) closeWith(s *session, i int, text string) {
	it := s.Items[i]
	it.closed = true
	s.Items = slices.Delete(s.Items, i, i+1)
	d.finishPing(it, text)
}

// finishPing edits an item's ping, and any message showing the item, to
// text without buttons. It runs through the outbox, so it comes after the
// ping itself even when the item closes at once.
func (d *Daemon) finishPing(it *item, text string) {
	chat := d.chatID
	clipped := telegram.Clip(text, 300)
	src := it.sourceJSON()
	d.send(func(ctx context.Context) {
		d.mu.Lock()
		msgs := append([]int64{it.PingMsg}, it.views...)
		d.mu.Unlock()
		for _, msg := range msgs {
			if msg == 0 || chat == 0 {
				continue
			}
			if err := d.tg.EditMessageText(ctx, chat, msg, clipped, nil); err != nil {
				d.cfg.Log.Warn("editing a ping", "item", it.Num, "err", err)
				continue
			}
			d.recordMessage(audit.MessageEntry{
				Op: "edit", MsgID: msg, Kind: "ping",
				Text: clipped, Sources: []string{src},
			})
		}
	})
}

func icon(kind string) string {
	switch kind {
	case ipc.KindPermission:
		return "🔐"
	case ipc.KindPlan:
		return "📋"
	default:
		return "❓"
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// procStart returns a process's start time from /proc, so a PID that the
// kernel reused doesn't pass for the session that ended.
func procStart(pid int) (string, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false
	}
	// The command name (field 2) may hold spaces and parentheses; the fields
	// after its closing parenthesis are fixed. They start at field 3, the
	// state, and field 22 is the start time.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return "", false
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return "", false
	}
	return f[19], true
}

func gitBranch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
