package daemon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/audit"
	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/transcript"
)

var commands = []telegram.BotCommand{
	{Command: "list", Description: "Sessions"},
	{Command: "new", Description: "Start a session: /new name folder [-- prompt]"},
	{Command: "resume", Description: "Resume a past session: /resume [text]"},
	{Command: "pending", Description: "What is waiting for you"},
	{Command: "dismiss", Description: "Dismiss waiting items: /dismiss [num|n|all]"},
	{Command: "last", Description: "Last messages: /last [N] [agent|chat|full]"},
	{Command: "more", Description: "The next page of a long message"},
	{Command: "follow", Description: "Stream a session: /follow [n] [agent|chat|full]"},
	{Command: "unfollow", Description: "Stop streaming: /unfollow [n|all]"},
	{Command: "mute", Description: "Stop pings: /mute [n] [30m|2h|1d]"},
	{Command: "unmute", Description: "Pings back on: /unmute [n]"},
	{Command: "interrupt", Description: "Esc, then text: /interrupt n [text]"},
	{Command: "screen", Description: "The selected session's screen, with a keyboard"},
	{Command: "cancel", Description: "Stop typing an answer"},
	{Command: "settings", Description: "Idle pings and reminders"},
	{Command: "grep", Description: "Search transcripts: /grep [n] text"},
	{Command: "status", Description: "Uptime, versions, and what is installed"},
	{Command: "end", Description: "End or remove a session: /end [n]"},
	{Command: "mode", Description: "Switch mode: /mode [n]"},
	{Command: "legend", Description: "What the emoji icons mean"},
	{Command: "help", Description: "How to use ctg"},
}

const helpText = `ctg: your Claude Code sessions, from Telegram.

/list shows the sessions. Tap one to select it: plain text you send then goes to that session, typed into its prompt. A session that isn't in tmux gets it through the mailbox, without your authority.
/new [name] [folder] [-- prompt] starts a session. The name is required. MyApp becomes my-app. The folder stays under your home directory: an exact path starts, a partial path like proj/claude-tel searches, and a missing folder needs a tap on Create. 🆕 New asks for the name, then the folder.
/resume [text] lists past sessions, newest first, 10 a page. Text filters the title, branch, and folder. A session that is already running is selected. Its folder can be anywhere it can still be read. ⏪ Resume opens the list.
/pending [n] shows what is waiting, for all sessions or session n. Tap an item to answer it, or ✖ Dismiss to close it without answering.
/dismiss [num|n|all] closes waiting items without sending an answer (e.g. /dismiss 2.1, /dismiss 2, or /dismiss all).
/last [N] [agent|chat|full] shows the selected session's last N messages (10 by default, 50 at most). 💬 Chat is the default: your prompts and Claude's text. 🤖 Agent is Claude's text only. 🧰 Full adds tool calls, their results, and messages to and from other sessions. 📜 Last asks with buttons; a number only moves the mark, and a mode sends. ✍️ asks for a number. Long messages are cut into pages: ⏬ More or /more shows the next.
/follow [n] [agent|chat|full] streams a session's new messages until /unfollow [n|all]. Multiple sessions can be followed at once. 👁 Follow asks which mode.
/interrupt n [text] presses Esc in session n, then types the text.
/screen shows the selected session's screen with a keyboard: ↑ ↓ ⏎ Esc Tab and 1–9, one key a tap, and ⌨️ Type. The screen is read again after each key; if it changed before your tap, nothing is pressed.
🔒 Secret, on the screen's keyboard, types the next message you send and deletes it from this chat at once; only its length is logged. Don't use it for long-lived secrets: it still passes through Telegram's servers.
/cancel drops the answer you were asked to type.
/mute [n] [30m|2h|1d] stops pings for all sessions or session n, idle pings and reminders included. Without a duration the mute lasts until /unmute.
/unmute [n] turns pings back on. Plain /unmute ends every mute.
/settings turns idle pings and reminders on and off. A session shows ⚙️ working, ❓ waiting, 🐚 shell, 📡 monitor, 🤖 agent, 💤 idle, or 💤⏰ when it will wake itself. 🐚, 📡, and 🤖 also sit beside ⚙️ during a turn. An idle session pings once. The ping is skipped when the turn was typed in the terminal and that tmux client was used in the last minute. Reminders are off until /settings turns them on. ⋯ on a session sets that session's idle pings.
/grep [n] text searches transcripts. n is a session that is running, and only counts when more text follows. Otherwise it searches every session from the last 30 days. Up to 10 hits, newest first. 📜 Around shows the five messages around a hit. 🔎 Search asks for the text.
/status shows how long the bot has been up, its version, a newer release when there is one, the Claude Code versions in use, whether the hooks, MCP server, service and shell wrapper are installed, and the last error.
/end [n] asks first. It says when a turn or background work is running, types /exit, and closes a claude-* tmux session that has no other windows. Otherwise it closes only the pane. A read-only session is removed from the list.
/mode [n] offers Manual, Accept edits and Plan. Auto and bypass are never offered. A switch that passes through auto waits until the session is idle.
/legend explains every emoji icon.
A photo or a file sent here is saved under the selected session's .ctg-uploads/, and that name is added to .git/info/exclude. A caption is sent with the file. Without one, the file waits for the next message and the header shows it waiting. 20 MB is as large as a bot can download.
The session view shows ctx as a percentage of the model's window. 🧾 Diff, on an Edit or a Write in 🧰 Full and on its permission card, shows the change. A long one is sent as a file, and 📄 As file does that on purpose.

Muted items still go to /pending; only the ping is skipped.`

// view is a message's text and buttons.
type view struct {
	text string
	kb   *telegram.InlineKeyboardMarkup
}

// handleUpdate acts on one update. Called with d.mu held.
func (d *Daemon) handleUpdate(u telegram.Update) {
	switch {
	case u.Message != nil:
		d.onMessage(u.Message)
	case u.CallbackQuery != nil:
		d.onCallback(u.CallbackQuery)
	}
}

func (d *Daemon) onMessage(m *telegram.Message) {
	if d.tryPair(m) {
		return
	}
	if d.chatID == 0 || m.Chat.ID != d.chatID {
		return // Q7: only the paired chat counts
	}
	if age := d.now().Sub(time.Unix(m.Date, 0)); age > staleAfter {
		d.cfg.Log.Info("ignoring a message sent while the bot was down", "age", age.Round(time.Second))
		return
	}
	if m.Document != nil || len(m.Photo) > 0 {
		d.takeFile(m)
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	if a := d.st.Await; a != nil && d.now().After(a.Until) {
		d.st.Await = nil
		d.draft = nil
		d.reply("(The request for text had lapsed; this message was taken as usual.)", nil)
	}
	// Text for the remote keyboard is typed as it is, slash commands too
	// ("/model"); only /cancel stops it.
	if a := d.st.Await; a != nil && (a.Action == "ktype" || a.Action == "ksecret") && text != "/cancel" {
		d.takeText(m, m.Text)
		return
	}
	if !strings.HasPrefix(text, "/") {
		if d.st.Await != nil {
			d.takeText(m, m.Text)
		} else {
			d.sendText(text)
		}
		return
	}
	fields := strings.Fields(text)
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	cmd, _, _ = strings.Cut(cmd, "@") // "/list@my_bot" in a menu tap
	args := fields[1:]
	if d.st.Await != nil && (launchAwait(d.st.Await.Action) || d.st.Await.Action == "grep") && cmd != "cancel" {
		d.st.Await = nil
		d.draft = nil
	}
	switch cmd {
	case "start", "list", "sessions":
		d.sendView(d.listView())
	case "new":
		d.cmdNew(args)
	case "resume":
		d.cmdResume(args)
	case "pending":
		s, err := d.sessionArg(args)
		if err != nil {
			d.reply(err.Error(), nil)
			return
		}
		d.sendView(d.pendingView(s))
	case "dismiss":
		d.cmdDismiss(args)
	case "mute":
		d.cmdMute(args)
	case "unmute":
		d.cmdUnmute(args)
	case "help":
		d.sendView(view{helpText, keyboard(row(button("ℹ️ Legend", "lg:a")))})
	case "legend":
		d.sendView(d.legendView(nil))
	case "cancel":
		if d.st.Await == nil {
			d.reply("Nothing to cancel.", nil)
			return
		}
		d.st.Await = nil
		d.draft = nil
		d.reply("Cancelled.", nil)
	case "interrupt":
		d.cmdInterrupt(text)
	case "last":
		d.cmdLast(args)
	case "more":
		d.cmdMore()
	case "follow":
		d.cmdFollow(args)
	case "screen":
		s := d.byID(d.st.Selected)
		if s == nil {
			d.reply("Pick a session first: /list, then tap one.", nil)
			return
		}
		d.showScreen(s, 0, "")
	case "secret":
		d.reply("Open 📺 Screen and use 🔒 Secret there, so you can see where the secret goes.", nil)
	case "unfollow":
		d.cmdUnfollow(args)
	case "settings":
		d.sendView(d.settingsView())
	case "grep":
		d.cmdGrep(args)
	case "status":
		d.openStatus(0)
	case "end":
		d.cmdEnd(args)
	case "mode":
		d.cmdMode(args)
	default:
		d.reply("Unknown command /"+cmd+". /help lists the commands.", nil)
	}
}

// sessionArg reads an optional session number; no argument means all.
func (d *Daemon) sessionArg(args []string) (*session, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if len(args) > 1 {
		return nil, fmt.Errorf("Too many arguments. /help shows the usage.")
	}
	return d.lookup(args[0])
}

func (d *Daemon) lookup(arg string) (*session, error) {
	n, err := strconv.Atoi(arg)
	if err != nil {
		return nil, fmt.Errorf("%q isn't a session number. /list shows them.", arg)
	}
	s := d.byNum(n)
	if s == nil {
		return nil, fmt.Errorf("No session %d. /list shows the sessions.", n)
	}
	return s, nil
}

func (d *Daemon) cmdMute(args []string) {
	var s *session
	var dur time.Duration
	for _, a := range args {
		if _, err := strconv.Atoi(a); err == nil {
			var lerr error
			if s, lerr = d.lookup(a); lerr != nil {
				d.reply(lerr.Error(), nil)
				return
			}
			continue
		}
		var err error
		if dur, err = parseDuration(a); err != nil {
			d.reply(err.Error()+". Use e.g. 30m, 2h or 1d.", nil)
			return
		}
	}
	d.reply(d.applyMute(s, dur), nil)
}

// cmdFollow is /follow [n] [agent|chat|full]; without n, the selected session.
func (d *Daemon) cmdFollow(args []string) {
	mode := modeChat
	s := d.byID(d.st.Selected)
	for _, a := range args {
		if m, ok := namedMode(a); ok {
			mode = m
			continue
		}
		var err error
		if s, err = d.lookup(a); err != nil {
			d.reply(err.Error(), nil)
			return
		}
	}
	if s == nil {
		d.reply("Pick a session first: /list, then tap one, or /follow n.", nil)
		return
	}
	d.startFollow(s, mode)
}

// cmdInterrupt is /interrupt <n> [text]: Esc, then the text (Q5).
func (d *Daemon) cmdInterrupt(line string) {
	_, rest, _ := strings.Cut(line, " ")
	numArg, text, _ := strings.Cut(strings.TrimSpace(rest), " ")
	if numArg == "" {
		d.reply("Usage: /interrupt n [text]", nil)
		return
	}
	s, err := d.lookup(numArg)
	if err != nil {
		d.reply(err.Error(), nil)
		return
	}
	if ok, why := d.canType(s); !ok {
		d.reply("Can't press keys in "+s.ref()+": "+why+".", nil)
		return
	}
	d.typeInto(s, strings.TrimSpace(text), true)
}

func (d *Daemon) cmdUnmute(args []string) {
	s, err := d.sessionArg(args)
	if err != nil {
		d.reply(err.Error(), nil)
		return
	}
	d.reply(d.applyUnmute(s), nil)
}

func (d *Daemon) applyMute(s *session, dur time.Duration) string {
	sp := d.setMute(s, dur)
	who, ref := "all sessions", ""
	if s != nil {
		who, ref = s.ref(), s.ref()
	}
	desc := d.describeMute(sp)
	d.record("mute", ref, desc)
	d.save()
	return fmt.Sprintf("🔕 Muted %s %s. Items still go to /pending.", who, desc)
}

func (d *Daemon) applyUnmute(s *session) string {
	d.unmute(s)
	who, ref := "Pings are on for all sessions", ""
	if s != nil {
		who, ref = "Pings are on for "+s.ref(), s.ref()
	}
	d.record("unmute", ref, "")
	d.save()
	if n := d.pendingCount(s); n > 0 {
		return fmt.Sprintf("🔔 %s. %s pending: /pending", who, plural(n, "item"))
	}
	return "🔔 " + who + "."
}

// Button data: "l" the list; "s:<scope>" a session's menu; "o:<scope>" the
// same menu as a new message, from a ping; "p:<scope>" pending; "m:<scope>"
// the mute menu; "mu:<scope>:<duration>" mute; "um:<scope>" unmute. <scope>
// is "a" for all sessions or a session's scope().
func (d *Daemon) onCallback(q *telegram.CallbackQuery) {
	if d.chatID == 0 || q.Message == nil || q.Message.Chat.ID != d.chatID {
		return // Q7
	}
	toast := ""
	defer func() {
		id := q.ID
		d.send(func(ctx context.Context) {
			if err := d.tg.AnswerCallbackQuery(ctx, id, toast); err != nil {
				d.cfg.Log.Warn("answering a button", "err", err)
			}
		})
	}()

	parts := strings.Split(q.Data, ":")
	if parts[0] == "mr" {
		if len(parts) == 3 {
			if i, err := strconv.Atoi(parts[2]); err == nil {
				d.sendPage(parts[1], i)
			}
		}
		return
	}
	s, ok := d.scopeArg(parts)
	if !ok {
		toast = "That session has ended."
		d.edit(q.Message.MessageID, d.listView())
		return
	}
	switch parts[0] {
	case "a", "i", "dm", "ir", "pa", "pd", "pr", "la", "lc", "qo", "qx", "qs", "po", "pk", "px", "ba", "br":
		toast = d.onItem(q, parts)
		return
	case "dma":
		n := d.dismissAll(s)
		toast = fmt.Sprintf("Dismissed %d", n)
		d.edit(q.Message.MessageID, d.pendingView(s))
		return
	case "bs":
		if s == nil {
			return
		}
		toast = d.sendBlocks(s)
		if toast == "" {
			toast = "Sending"
		}
		return
	case "k", "kt", "ks", "kr", "kx":
		toast = d.onKeyboard(q, s, parts)
		return
	case "sc":
		if s == nil {
			return
		}
		d.st.Selected = s.ID
		d.showScreen(s, 0, "")
		return
	case "uf":
		if s != nil {
			d.stopFollowSession(s, "")
			return
		}
		if len(d.st.Follows) == 1 {
			if only := d.byID(d.st.Follows[0].SessionID); only != nil {
				d.stopFollowSession(only, "")
				return
			}
		}
		if len(d.st.Follows) > 1 {
			d.edit(q.Message.MessageID, d.followingView())
			return
		}
		d.stopAllFollows()
		return
	case "ufa":
		d.stopAllFollows()
		d.edit(q.Message.MessageID, d.listView())
		return
	case "lw":
		if s == nil {
			return
		}
		d.askLastN(s, q.Message.MessageID)
		return
	case "ls":
		if s == nil || len(parts) < 4 {
			return
		}
		n, err := strconv.Atoi(parts[2])
		mode, ok := parseMode(parts[3])
		if err != nil || !ok {
			return
		}
		d.st.Selected = s.ID
		d.showLast(s, clampLast(n), mode)
		toast = "Showing"
		return
	case "x":
		if s == nil {
			return
		}
		if ok, why := d.canType(s); !ok {
			toast = "Can't press keys there: " + why
			return
		}
		d.typeInto(s, "", true)
		toast = "Esc"
		return
	case "gs":
		d.askGrep(s)
		return
	case "ey":
		if s == nil {
			return
		}
		d.endSession(s)
		return
	case "md":
		if s == nil || len(parts) < 3 {
			return
		}
		d.setMode(s, parts[2])
		return
	case "df":
		if s == nil || len(parts) < 3 {
			return
		}
		d.showToolDiff(s, strings.Join(parts[2:], ":"))
		return
	case "di":
		ses, it := d.itemByScope(parts)
		if it == nil {
			toast = "That item is closed."
			return
		}
		d.showItemDiff(ses, it)
		return
	case "da":
		if len(parts) < 3 {
			return
		}
		d.sendDiffFile(parts[2], "change")
		return
	case "ga":
		if len(parts) < 3 {
			return
		}
		i, err := strconv.Atoi(parts[2])
		if err != nil {
			return
		}
		d.grepAround(i)
		return
	case "up":
		d.runUpdate()
		return
	}
	var v view
	switch parts[0] {
	case "l":
		v = d.listView()
	case "nw":
		d.askNew()
		return
	case "rs":
		v = d.resumeView("", 0)
	case "rp":
		page := 0
		if len(parts) >= 3 {
			page, _ = strconv.Atoi(parts[2])
		}
		filter := ""
		if d.draft != nil {
			filter = d.draft.Filter
		}
		v = d.resumeView(filter, page)
	case "rr":
		if len(parts) < 3 {
			return
		}
		d.pickResume(parts[2])
		return
	case "nf":
		if len(parts) < 3 {
			return
		}
		i, err := strconv.Atoi(parts[2])
		if err != nil {
			return
		}
		d.pickFolder(i)
		return
	case "nc":
		d.createFolder()
		return
	case "s", "o":
		if s == nil {
			v = d.listView()
			break
		}
		d.st.Selected = s.ID
		d.touchSession(s)
		d.save()
		if parts[0] == "o" {
			d.sendView(d.sessionView(s))
			return
		}
		v = d.sessionView(s)
	case "p":
		v = d.pendingView(s)
	case "lt":
		if s == nil {
			v = d.listView()
			break
		}
		d.st.Selected = s.ID
		d.save()
		v = d.lastPicker(s, defaultLast)
	case "ln":
		if s == nil || len(parts) < 3 {
			return
		}
		n, err := strconv.Atoi(parts[2])
		if err != nil {
			return
		}
		v = d.lastPicker(s, clampLast(n))
	case "fp":
		if s == nil {
			v = d.listView()
			break
		}
		d.st.Selected = s.ID
		d.save()
		v = d.followPicker(s)
	case "fs":
		if s == nil || len(parts) < 3 {
			return
		}
		mode, ok := parseMode(parts[2])
		if !ok {
			return
		}
		d.startFollow(s, mode)
		v = d.sessionView(s)
	case "fm":
		if s != nil && len(parts) >= 3 {
			if mode, ok := parseMode(parts[2]); ok {
				if f := d.followOf(s.ID); f != nil {
					f.Mode = mode
					f.Full = mode == modeFull
					d.save()
					toast = modeName(mode)
				}
			}
		}
		v = d.followingView()
	case "mm":
		v = d.moreView(s)
	case "hp":
		v = d.helpView(s)
	case "lg":
		v = d.legendView(s)
	case "st":
		v = d.settingsView()
	case "sg":
		if len(parts) == 3 {
			d.togglePref(parts[2])
			d.save()
		}
		v = d.settingsView()
	case "ip":
		if s == nil {
			return
		}
		d.cyclePings(s)
		d.save()
		v = d.moreView(s)
	case "su":
		d.openStatus(q.Message.MessageID)
		return
	case "en":
		if s == nil {
			return
		}
		v = d.endConfirm(s)
	case "mo":
		if s == nil {
			return
		}
		v = d.modeView(s)
	case "m":
		v = d.muteView(s)
	case "mu":
		dur := time.Duration(0)
		if len(parts) == 3 && parts[2] != "inf" {
			var err error
			if dur, err = parseDuration(parts[2]); err != nil {
				return
			}
		}
		toast = d.applyMute(s, dur)
		v = d.backView(s)
	case "um":
		toast = d.applyUnmute(s)
		v = d.backView(s)
	default:
		return
	}
	d.edit(q.Message.MessageID, v)
}

// scopeArg reads the scope in parts[1]: nil for "a" or no scope, the session
// for a session scope, and ok false for a session that has ended.
func (d *Daemon) scopeArg(parts []string) (*session, bool) {
	if len(parts) < 2 || parts[1] == "a" {
		return nil, true
	}
	numStr, idPrefix, _ := strings.Cut(parts[1], ".")
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return nil, false
	}
	s := d.byNum(n)
	if s == nil || !strings.HasPrefix(s.ID, idPrefix) {
		return nil, false
	}
	return s, true
}

func (d *Daemon) backView(s *session) view {
	if s == nil {
		return d.listView()
	}
	return d.sessionView(s)
}

// header heads every view: the selected session (Q14) and 🔕 while any mute
// is on (Q20).
func (d *Daemon) header() string {
	h := "ctg"
	if s := d.byID(d.st.Selected); s != nil {
		h += " · → " + s.ref()
	}
	if d.anyMute() {
		h += " · 🔕"
	}
	if s := d.byID(d.st.Selected); s != nil && len(s.Files) > 0 {
		h += " · 📎 " + plural(len(s.Files), "file") + " waiting"
	}
	return h
}

// line is a session's row in the list (Q12, Q57): "2 · myapp-build · 💤 idle · 1❓".
func (d *Daemon) line(s *session) string {
	l := fmt.Sprintf("%d · %s · %s", s.Num, s.label(), s.stateLabel())
	if n := len(s.Items); n > 0 {
		l += fmt.Sprintf(" · %d❓", n)
	}
	if f := d.followOf(s.ID); f != nil {
		l += " · 👁 " + f.mode()
	}
	if s.Pane == "" {
		l += " · 👁 read-only"
	}
	if d.sessionMuted(s) {
		l += " · 🔕"
	}
	return l
}

func (d *Daemon) listView() view {
	var b strings.Builder
	b.WriteString(d.header())
	b.WriteString("\n\n")
	if len(d.st.Sessions) == 0 {
		b.WriteString("No sessions. A Claude Code session shows up here when it starts, or tap 🆕 New.")
	}
	var rows [][]telegram.InlineKeyboardButton
	var r []telegram.InlineKeyboardButton
	for _, s := range d.st.Sessions {
		b.WriteString(d.line(s))
		b.WriteString("\n")
		r = append(r, button(fmt.Sprintf("%d · %s %s", s.Num, s.label(), s.mark()), "s:"+s.scope()))
		if len(r) == 2 {
			rows, r = append(rows, r), nil
		}
	}
	if len(r) > 0 {
		rows = append(rows, r)
	}
	followed := 0
	for _, f := range d.st.Follows {
		if d.byID(f.SessionID) != nil {
			followed++
		}
	}
	if followed > 0 {
		rows = append(rows, row(button(fmt.Sprintf("👁 Following (%d)", followed), "fm:a")))
	}
	rows = append(rows, row(
		button("🆕 New", "nw"),
		button("⏪ Resume", "rs"),
		button(fmt.Sprintf("❓ Pending (%d)", d.pendingCount(nil)), "p:a"),
		button("⋯", "mm:a"),
	))
	return view{strings.TrimRight(b.String(), "\n"), keyboard(rows...)}
}

// moreView is the ⋯ menu (Q74). Rare actions sit here; « goes back.
func (d *Daemon) moreView(s *session) view {
	back := button("«", "l")
	who := "all sessions"
	mute := button("🔕 Mute all", "m:a")
	if d.anyMute() {
		mute = button("🔔 Unmute all", "um:a")
	}
	if s != nil {
		back = button("«", "s:"+s.scope())
		who = s.ref()
		mute = button("🔕 Mute", "m:"+s.scope())
		if d.sessionMuted(s) {
			mute = button("🔔 Unmute", "um:"+s.scope())
		}
	}
	rows := [][]telegram.InlineKeyboardButton{}
	if s != nil && s.Pane != "" {
		rows = append(rows, row(button("🔀 Mode", "mo:"+s.scope())))
	}
	rows = append(rows, row(mute))
	if s != nil {
		rows = append(rows, row(d.pingButton(s)))
		rows = append(rows, row(button("🔎 Search", "gs:"+s.scope())))
		if s.Pane != "" {
			rows = append(rows, row(button("🛑 End", "en:"+s.scope())))
		} else {
			rows = append(rows, row(button("🗑 Remove", "en:"+s.scope())))
		}
	} else {
		rows = append(rows,
			row(button("⚙️ Settings", "st:a")),
			row(button("🔎 Search", "gs:a")),
			row(button("🩺 Status", "su:a")),
			row(button("ℹ️ Legend", "lg:a")),
			row(button("❔ Help", "hp:a")),
		)
	}
	rows = append(rows, row(back))
	return view{fmt.Sprintf("%s\n\n⋯ %s", d.header(), who), keyboard(rows...)}
}

const legendText = `ℹ️ Icon Legend

Session status
⚙️ working — turn in progress
❓ waiting — question, permission, or plan waiting
🐚 shell — background shell running
📡 monitor — background monitor running
🤖 agent — background subagent running
💤 idle — waiting for your next prompt
💤⏰ idle (scheduled) — will wake itself on a timer

Session flags
→ selected — plain text goes to this session
👁 chat / agent / full — streaming with /follow
👁 read-only — not in tmux (mailbox only)
🔕 muted — pings paused
📎 file waiting — upload queued for next message

Pending items
❓ question — AskUserQuestion or closing question
🔐 permission — tool call needs approval
📋 plan — ExitPlanMode waiting for review
✖ dismissed — closed from Telegram without answering

Messages (/last & /follow)
🤖 Claude — assistant reply
👤 You — your prompt (terminal or Telegram)
🖥 Terminal — shell command & output (! cmd)
🔧 Tool — tool call & result (🧰 Full)
⚙️ Command / task — slash command or task note
📨 / 📤 Peer — message from / to another session
⏹ Interrupted — turn or follow stopped`

func (d *Daemon) legendView(s *session) view {
	back := button("« Sessions", "l")
	if s != nil {
		back = button("« "+s.ref(), "s:"+s.scope())
	}
	return view{d.header() + "\n\n" + legendText, keyboard(row(back))}
}

func (d *Daemon) helpView(s *session) view {
	scope := "a"
	back := button("« Sessions", "l")
	if s != nil {
		scope = s.scope()
		back = button("« "+s.ref(), "s:"+s.scope())
	}
	return view{helpText, keyboard(row(button("ℹ️ Legend", "lg:"+scope)), row(back))}
}

func (d *Daemon) sessionView(s *session) view {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n%s", d.header(), d.line(s), shortPath(s.Cwd))
	if pct, ok := transcript.Context(s.Transcript, s.PID); ok {
		fmt.Fprintf(&b, " · ctx %d%%", pct)
	}
	if s.Branch != "" {
		fmt.Fprintf(&b, " · %s", s.Branch)
	}
	b.WriteString("\n")
	if s.Version != "" {
		fmt.Fprintf(&b, "Claude Code %s · ", s.Version)
	}
	if s.Pane != "" {
		fmt.Fprintf(&b, "tmux pane %s", s.Pane)
	} else {
		b.WriteString("not in tmux: read-only")
	}
	if s.hasWork() {
		fmt.Fprintf(&b, "\n%s", s.taskSummary())
	}
	if sp := d.st.Mute.Sessions[s.ID]; sp.on(d.now()) {
		fmt.Fprintf(&b, "\n🔕 muted %s", d.describeMute(sp))
	}
	if ok, why := d.canType(s); !ok && s.Pane != "" {
		fmt.Fprintf(&b, "\n⚠️ Answer in the terminal: %s.", why)
	}
	top := []telegram.InlineKeyboardButton{button("📜 Last", "lt:"+s.scope())}
	if f := d.followOf(s.ID); f != nil {
		top = append(top, button("👁 "+modeName(f.mode())+" ▾", "fp:"+s.scope()), button("⏹ Unfollow", "uf:"+s.scope()))
	} else {
		top = append(top, button("👁 Follow", "fp:"+s.scope()))
	}
	if ok, _ := canKeys(s); ok {
		top = append(top, button("📺 Screen", "sc:"+s.scope()))
	}
	second := []telegram.InlineKeyboardButton{button(fmt.Sprintf("❓ Pending (%d)", len(s.Items)), "p:"+s.scope())}
	if ok, _ := d.canType(s); ok {
		second = append(second, button("⏹ Interrupt", "x:"+s.scope()))
	}
	second = append(second, button("⋯", "mm:"+s.scope()))
	rows := [][]telegram.InlineKeyboardButton{top, second, row(button("« Sessions", "l"))}
	return view{b.String(), keyboard(rows...)}
}

func (d *Daemon) pendingView(s *session) view {
	var b strings.Builder
	b.WriteString(d.header())
	b.WriteString("\n\n")
	n := 0
	var rows [][]telegram.InlineKeyboardButton
	var r []telegram.InlineKeyboardButton
	for _, ses := range d.st.Sessions {
		if s != nil && ses != s {
			continue
		}
		for _, it := range ses.Items {
			fmt.Fprintf(&b, "%s %s · %s · %s · %s\n", icon(it.Kind), it.Num, ses.label(), it.Summary, d.ago(it.Added))
			n++
			r = append(r, button(icon(it.Kind)+" "+it.Num, "i:"+ses.scope()+":"+it.Num))
			if len(r) == 4 {
				rows, r = append(rows, r), nil
			}
		}
	}
	if len(r) > 0 {
		rows = append(rows, r)
	}
	if n == 0 {
		b.WriteString("Nothing pending")
		if s != nil {
			b.WriteString(" for " + s.ref())
		}
		b.WriteString(".")
	}
	scope := "a"
	back := button("« Sessions", "l")
	if s != nil {
		scope = s.scope()
		back = button("« "+s.ref(), "s:"+s.scope())
	}
	if n > 0 {
		rows = append(rows, row(button("✖ Dismiss all", "dma:"+scope), back))
	} else {
		rows = append(rows, row(back))
	}
	return view{strings.TrimRight(b.String(), "\n"), keyboard(rows...)}
}

func (d *Daemon) muteView(s *session) view {
	scope, who, back := "a", "all sessions", button("« Sessions", "l")
	if s != nil {
		scope, who, back = s.scope(), s.ref(), button("« "+s.ref(), "s:"+s.scope())
	}
	return view{
		fmt.Sprintf("%s\n\nMute pings for %s:", d.header(), who),
		keyboard(
			row(button("1h", "mu:"+scope+":1h"), button("2h", "mu:"+scope+":2h"), button("8h", "mu:"+scope+":8h")),
			row(button("Until I turn it back on", "mu:"+scope+":inf")),
			row(back),
		),
	}
}

func (d *Daemon) pendingCount(s *session) int {
	n := 0
	for _, ses := range d.st.Sessions {
		if s == nil || ses == s {
			n += len(ses.Items)
		}
	}
	return n
}

func buttonLabels(kb *telegram.InlineKeyboardMarkup) []string {
	if kb == nil {
		return nil
	}
	var out []string
	for _, r := range kb.InlineKeyboard {
		for _, b := range r {
			if b.CallbackData != "" {
				out = append(out, b.Text+" ["+b.CallbackData+"]")
			} else {
				out = append(out, b.Text)
			}
		}
	}
	return out
}

// reply sends text to the paired chat.
func (d *Daemon) reply(text string, kb *telegram.InlineKeyboardMarkup) {
	d.sendTo(d.chatID, text, kb)
}

func (d *Daemon) sendTo(chat int64, text string, kb *telegram.InlineKeyboardMarkup) {
	if chat == 0 {
		return
	}
	btns := buttonLabels(kb)
	d.send(func(ctx context.Context) {
		m, err := d.tg.SendMessage(ctx, chat, text, kb)
		if err != nil {
			d.noteProblem("telegram: " + err.Error())
			d.cfg.Log.Warn("sending a message", "err", err)
			return
		}
		d.recordMessage(audit.MessageEntry{
			Op:      "send",
			MsgID:   m.MessageID,
			Kind:    "message",
			Text:    text,
			Buttons: btns,
		})
	})
}

func (d *Daemon) sendView(v view) { d.reply(v.text, v.kb) }

// sendMode sends a view with a parse mode, such as "HTML".
func (d *Daemon) sendMode(v view, mode string) {
	chat := d.chatID
	if chat == 0 {
		return
	}
	btns := buttonLabels(v.kb)
	d.send(func(ctx context.Context) {
		m, err := d.tg.SendMessageMode(ctx, chat, v.text, mode, v.kb)
		if err != nil {
			d.noteProblem("telegram: " + err.Error())
			d.cfg.Log.Warn("sending a message", "err", err)
			return
		}
		d.recordMessage(audit.MessageEntry{
			Op:      "send",
			MsgID:   m.MessageID,
			Kind:    "message",
			Mode:    mode,
			Text:    v.text,
			Buttons: btns,
		})
	})
}

// notify sends one line with a button to the pending items in scope.
func (d *Daemon) notify(text, pendingData string) {
	d.reply(text, keyboard(row(button("❓ Pending", pendingData))))
}

func (d *Daemon) edit(msg int64, v view) { d.editMode(msg, v, "") }

func (d *Daemon) editMode(msg int64, v view, mode string) {
	chat := d.chatID
	if chat == 0 {
		return
	}
	btns := buttonLabels(v.kb)
	d.send(func(ctx context.Context) {
		if err := d.tg.EditMessageTextMode(ctx, chat, msg, v.text, mode, v.kb); err != nil {
			d.noteProblem("telegram: " + err.Error())
			d.cfg.Log.Warn("editing a message", "err", err)
			return
		}
		d.recordMessage(audit.MessageEntry{
			Op:      "edit",
			MsgID:   msg,
			Kind:    "edit",
			Mode:    mode,
			Text:    v.text,
			Buttons: btns,
		})
	})
}

func keyboard(rows ...[]telegram.InlineKeyboardButton) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func row(b ...telegram.InlineKeyboardButton) []telegram.InlineKeyboardButton { return b }

func button(text, data string) telegram.InlineKeyboardButton {
	return telegram.InlineKeyboardButton{Text: text, CallbackData: data}
}

// clock formats t for a message: "14:02", with the date when it isn't today.
func (d *Daemon) clock(t time.Time) string {
	t, now := t.Local(), d.now().Local()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

func (d *Daemon) ago(t time.Time) string {
	age := d.now().Sub(t)
	if age < time.Minute {
		return "now"
	}
	return shortDuration(age) + " ago"
}

func shortPath(p string) string {
	if home, ok := homeDir(); ok {
		if rest, ok := strings.CutPrefix(p, home); ok && (rest == "" || rest[0] == '/') {
			return "~" + rest
		}
	}
	return p
}
