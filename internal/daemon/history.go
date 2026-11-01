package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/audit"
	"github.com/mehyaa/claude-telegram/internal/page"
	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/transcript"
)

const (
	defaultLast = 10
	maxLast     = 50
	// msgMax is how full a Telegram message gets before the next entry
	// starts a new one; Telegram's limit is 4096.
	msgMax = 3800
	// keepPaged is how many cut messages are kept for ⏬ More and /more.
	keepPaged = 30
)

// follow is the session being streamed to the phone (Q2). Mode is agent,
// chat or full (Q53). Full is the older flag, kept so a saved follow still
// reads.
type follow struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode,omitempty"`
	Full      bool   `json:"full,omitempty"`
}

func (f *follow) mode() string {
	if f == nil {
		return modeChat
	}
	if f.Mode != "" {
		return f.Mode
	}
	if f.Full {
		return modeFull
	}
	return modeChat
}

type followStream struct {
	stream transcript.Stream
	buf    []transcript.Entry
	sent   time.Time
}

type renderedView struct {
	v       view
	sources []string
}

// paged is a message cut into pages (Q6).
type paged struct {
	head  string // the message's mark, "🤖 "
	pages []string
	next  int // the page /more sends next
}

// entryHead is the mark repeated on later pages of a cut message.
func (d *Daemon) entryHead(s *session, e transcript.Entry) string {
	badge := ""
	if s != nil && s.tag() != "" {
		badge = "<b>" + htmlEscape(s.tag()) + "</b> · "
	}
	switch e.Kind {
	case transcript.Assistant:
		if badge != "" {
			return "🤖 " + badge + "Claude\n"
		}
		return "🤖 <b>Claude</b>\n"
	case transcript.Peer:
		if badge != "" {
			return "📨 " + badge + d.peerEntryTitlePlain(e) + "\n"
		}
		return d.peerEntryTitle(e) + "\n"
	case transcript.User:
		if e.Term {
			if badge != "" {
				return "🖥 " + badge + "Terminal\n"
			}
			return "🖥 <b>Terminal</b>\n"
		}
		if e.Cmd {
			return "⚙️ " + badge
		}
		if badge != "" {
			return "👤 " + badge + "You\n"
		}
		return "👤 <b>You</b>\n"
	default:
		return ""
	}
}

// keepPages stores a cut message's pages and returns its ID.
func (d *Daemon) keepPages(head string, pages []string) string {
	if d.paged == nil {
		d.paged = map[string]*paged{}
	}
	d.pagedSeq++
	id := strconv.Itoa(d.pagedSeq)
	d.paged[id] = &paged{head: head, pages: pages, next: 1}
	d.pagedOrder = append(d.pagedOrder, id)
	for len(d.pagedOrder) > keepPaged {
		delete(d.paged, d.pagedOrder[0])
		d.pagedOrder = d.pagedOrder[1:]
	}
	d.lastPaged = id
	return id
}

func moreButton(id string, next, total int) telegram.InlineKeyboardButton {
	return button(fmt.Sprintf("⏬ More (%d/%d)", next+1, total), fmt.Sprintf("mr:%s:%d", id, next))
}

// render lays entries out as Telegram HTML messages, each starting with
// header. mode picks which entries show (Q53). A message longer than a page
// ends its Telegram message with a ⏬ More button. extra goes under the last
// message.
func (d *Daemon) render(s *session, header string, entries []transcript.Entry, mode string, extra ...telegram.InlineKeyboardButton) []view {
	rvs := d.renderEntries(s, header, entries, mode, false, extra...)
	out := make([]view, len(rvs))
	for i, rv := range rvs {
		out[i] = rv.v
	}
	return out
}

func (d *Daemon) renderEntries(s *session, header string, entries []transcript.Entry, mode string, numbered bool, extra ...telegram.InlineKeyboardButton) []renderedView {
	var out []renderedView
	var b strings.Builder
	var diffs [][]telegram.InlineKeyboardButton
	var srcs []string
	b.WriteString(header)
	empty := true
	total := 0
	if numbered {
		for _, e := range entries {
			if showEntry(e, mode) {
				total++
			}
		}
	}
	flush := func(kb *telegram.InlineKeyboardMarkup) {
		if len(diffs) > 0 {
			var rows [][]telegram.InlineKeyboardButton
			if kb != nil {
				rows = append(rows, kb.InlineKeyboard...)
			}
			rows = append(rows, diffs...)
			kb = keyboard(rows...)
			diffs = nil
		}
		out = append(out, renderedView{v: view{b.String(), kb}, sources: srcs})
		srcs = nil
		b.Reset()
		b.WriteString(header)
		empty = true
	}
	appendBlock := func(block string) {
		if empty && header == "" {
			b.WriteString(block)
		} else {
			b.WriteString("\n\n" + block)
		}
		empty = false
	}
	idx := 0
	for _, e := range entries {
		if !showEntry(e, mode) {
			continue
		}
		idx++
		head, body := d.entryHTMLAt(s, e, idx, total)
		if e.Kind == transcript.Tool || e.Cmd || (e.Kind == transcript.Note && body == "") {
			block := head
			if body != "" {
				block = body
			}
			if b.Len()+len(block)+2 > msgMax && !empty {
				flush(nil)
			}
			appendBlock(block)
			if e.Raw != "" {
				srcs = append(srcs, d.mask(s, e.Raw))
			}
			if mode == modeFull && s != nil && s.ID != "" && (diffable(e.ToolName) || e.Diff != "") && e.ToolID != "" {
				data := "df:" + s.scope() + ":" + e.ToolID
				if len(data) <= 64 {
					label := "🧾 Diff"
					if e.Path != "" {
						label = "🧾 " + telegram.Clip(filepath.Base(e.Path), 24)
					}
					diffs = append(diffs, row(button(label, data)))
				}
			}
			continue
		}
		pages := page.Pages(body)
		if len(pages) == 0 {
			pages = []string{""}
		}
		htmlPages := make([]string, len(pages))
		for i, p := range pages {
			htmlPages[i] = htmlBody(p)
		}
		first := head
		if htmlPages[0] != "" {
			if head != "" {
				first += "\n"
			}
			first += htmlPages[0]
		}
		if len(pages) > 1 {
			first += fmt.Sprintf("\n(1/%d)", len(pages))
		}
		if b.Len()+len(first)+2 > msgMax && !empty {
			flush(nil)
		}
		appendBlock(first)
		if e.Raw != "" {
			srcs = append(srcs, d.mask(s, e.Raw))
		}
		if len(pages) > 1 {
			id := d.keepPages(d.entryHead(s, e), htmlPages)
			flush(keyboard(row(moreButton(id, 1, len(pages)))))
		}
	}
	if empty && len(out) == 0 {
		if header != "" {
			b.WriteString("\n\nNothing in this view.")
		} else {
			b.WriteString("Nothing in this view.")
		}
	}
	if !empty || len(out) == 0 {
		flush(nil)
	}
	if len(extra) > 0 && len(out) > 0 {
		last := &out[len(out)-1].v
		if last.kb == nil {
			last.kb = keyboard()
		}
		last.kb.InlineKeyboard = append(last.kb.InlineKeyboard, extra)
	}
	return out
}

func (d *Daemon) sendRendered(rv renderedView, parseMode, kind, sessionRef string) {
	chat := d.chatID
	if chat == 0 {
		return
	}
	btns := buttonLabels(rv.v.kb)
	srcs := rv.sources
	text := rv.v.text
	kb := rv.v.kb
	d.send(func(ctx context.Context) {
		m, err := d.tg.SendMessageMode(ctx, chat, text, parseMode, kb)
		if err != nil {
			d.noteProblem("telegram: " + err.Error())
			d.cfg.Log.Warn("sending a message", "err", err)
			return
		}
		d.recordMessage(audit.MessageEntry{
			Op:      "send",
			MsgID:   m.MessageID,
			Kind:    kind,
			Session: sessionRef,
			Mode:    parseMode,
			Text:    text,
			Buttons: btns,
			Sources: srcs,
		})
	})
}

func diffable(name string) bool {
	return name == "Edit" || name == "Write" || name == "MultiEdit"
}

// showLast sends the session's last n messages (Q6, Q53). The transcript is
// read without the lock.
func (d *Daemon) showLast(s *session, n int, mode string) {
	if s.Transcript == "" {
		d.reply(s.ref()+" has no transcript yet.", nil)
		return
	}
	path, ref, tag, id := s.Transcript, s.ref(), s.tag(), s.ID
	go func() {
		entries, err := transcript.Last(path, n)
		d.mu.Lock()
		defer d.mu.Unlock()
		if err != nil {
			d.reply("Couldn't read "+ref+"'s transcript: "+err.Error(), nil)
			return
		}
		emptyHeader := fmt.Sprintf("📜 <b>%s</b> · last %d · %s", htmlEscape(tag), n, modeName(mode))
		if len(entries) == 0 {
			d.sendRendered(renderedView{v: view{emptyHeader + "\n\nNothing yet.", nil}}, "HTML", "last", tag)
			return
		}
		s := d.byID(id)
		if s == nil {
			return
		}
		shown := 0
		for _, e := range entries {
			if showEntry(e, mode) {
				shown++
			}
		}
		if shown == 0 {
			d.sendRendered(renderedView{v: view{emptyHeader + "\n\nNothing in this view.", nil}}, "HTML", "last", s.tag())
			return
		}
		for _, rv := range d.renderEntries(s, "", entries, mode, true) {
			d.sendRendered(rv, "HTML", "last", s.tag())
		}
	}()
}

// cmdLast is /last [N] [agent|chat|full], for the selected session.
func (d *Daemon) cmdLast(args []string) {
	n, mode := defaultLast, modeChat
	for _, a := range args {
		if m, ok := namedMode(a); ok {
			mode = m
			continue
		}
		v, err := strconv.Atoi(a)
		if err != nil || v < 1 {
			d.reply("Usage: /last [N] [agent|chat|full], for the selected session.", nil)
			return
		}
		n = min(v, maxLast)
	}
	s := d.byID(d.st.Selected)
	if s == nil {
		d.reply("Pick a session first: /list, then tap one.", nil)
		return
	}
	d.showLast(s, n, mode)
}

func clampLast(n int) int {
	if n < 1 {
		return 1
	}
	return min(n, maxLast)
}

// lastPicker is the 📜 Last message (Q54): a number moves the ✓, a mode sends.
func (d *Daemon) lastPicker(s *session, n int) view {
	n = clampLast(n)
	text := fmt.Sprintf("%s\n\n📜 Last · %s\nA number moves the mark. Show sends that many.", d.header(), s.ref())
	presets := []int{5, 10, 20, 50}
	if !slices.Contains(presets, n) {
		presets = append(presets, n)
		slices.Sort(presets)
	}
	var nums []telegram.InlineKeyboardButton
	for _, v := range presets {
		label := strconv.Itoa(v)
		if v == n {
			label = "[" + label + "✓]"
		}
		nums = append(nums, button(label, fmt.Sprintf("ln:%s:%d", s.scope(), v)))
	}
	nums = append(nums, button("✍️", "lw:"+s.scope()))
	show := row(
		button("🤖 Agent", fmt.Sprintf("ls:%s:%d:a", s.scope(), n)),
		button("💬 Chat", fmt.Sprintf("ls:%s:%d:c", s.scope(), n)),
		button("🧰 Full", fmt.Sprintf("ls:%s:%d:f", s.scope(), n)),
	)
	return view{text, keyboard(nums, show, row(button("« "+s.ref(), "s:"+s.scope())))}
}

// followPicker is the 👁 Follow message: only the Show row (Q54).
func (d *Daemon) followPicker(s *session) view {
	text := fmt.Sprintf("%s\n\n👁 Follow · %s\nShow:", d.header(), s.ref())
	cur := ""
	if f := d.followOf(s.ID); f != nil {
		cur = f.mode()
	}
	mark := func(m, label string) string {
		if cur == m {
			return label + " ✓"
		}
		return label
	}
	show := row(
		button(mark(modeAgent, "🤖 Agent"), "fs:"+s.scope()+":a"),
		button(mark(modeChat, "💬 Chat"), "fs:"+s.scope()+":c"),
		button(mark(modeFull, "🧰 Full"), "fs:"+s.scope()+":f"),
	)
	rows := [][]telegram.InlineKeyboardButton{show}
	if cur != "" {
		rows = append(rows, row(button("⏹ Unfollow", "uf:"+s.scope())))
	}
	rows = append(rows, row(button("« "+s.ref(), "s:"+s.scope())))
	return view{text, keyboard(rows...)}
}

// askLastN asks for a count with ForceReply (Q54). msg is the picker to update.
func (d *Daemon) askLastN(s *session, msg int64) {
	d.st.Await = &awaiting{Session: s.ID, Action: "lastn", Msg: msg, Until: d.now().Add(awaitFor)}
	d.save()
	chat := d.chatID
	d.send(func(ctx context.Context) {
		if _, err := d.tg.SendForceReply(ctx, chat, "How many messages? Send a number from 1 to 50.", "1–50"); err != nil {
			d.cfg.Log.Warn("asking for a number", "err", err)
		}
	})
}

func (d *Daemon) takeLastN(a *awaiting, text string) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	s := d.byID(a.Session)
	if err != nil || n < 1 {
		d.st.Await = a
		a.Until = d.now().Add(awaitFor)
		d.reply("Send a number from 1 to 50.", nil)
		return
	}
	if s == nil {
		d.reply("That session has ended.", nil)
		return
	}
	d.edit(a.Msg, d.lastPicker(s, clampLast(n)))
}

// sendPage sends page i of a cut message, with a button for the next.
func (d *Daemon) sendPage(id string, i int) {
	p := d.paged[id]
	if p == nil {
		d.reply("That message is no longer kept; /last shows it again.", nil)
		return
	}
	if i >= len(p.pages) {
		d.reply("That was the last page.", nil)
		return
	}
	var kb *telegram.InlineKeyboardMarkup
	if i+1 < len(p.pages) {
		kb = keyboard(row(moreButton(id, i+1, len(p.pages))))
	}
	d.lastPaged, p.next = id, i+1
	d.sendMode(view{fmt.Sprintf("%s%s\n(%d/%d)", p.head, p.pages[i], i+1, len(p.pages)), kb}, "HTML")
}

// cmdMore is /more: the next page of the message cut last.
func (d *Daemon) cmdMore() {
	p := d.paged[d.lastPaged]
	if p == nil {
		d.reply("Nothing to page.", nil)
		return
	}
	d.sendPage(d.lastPaged, p.next)
}

func (d *Daemon) followOf(id string) *follow {
	if id == "" {
		return nil
	}
	for _, f := range d.st.Follows {
		if f.SessionID == id {
			return f
		}
	}
	if d.st.Follow != nil && d.st.Follow.SessionID == id {
		return d.st.Follow
	}
	return nil
}

func (d *Daemon) removeFollow(id string) bool {
	removed := false
	out := d.st.Follows[:0]
	for _, f := range d.st.Follows {
		if f.SessionID == id {
			removed = true
			continue
		}
		out = append(out, f)
	}
	d.st.Follows = out
	if d.st.Follow != nil && d.st.Follow.SessionID == id {
		d.st.Follow = nil
		removed = true
	}
	if d.streams != nil {
		delete(d.streams, id)
	}
	return removed
}

func (d *Daemon) streamFor(id string) *followStream {
	if d.streams == nil {
		d.streams = map[string]*followStream{}
	}
	fs := d.streams[id]
	if fs == nil {
		fs = &followStream{}
		d.streams[id] = fs
	}
	return fs
}

// startFollow streams a session's new messages until Unfollow (Q2). Multiple
// sessions can be followed simultaneously.
func (d *Daemon) startFollow(s *session, mode string) {
	if f := d.followOf(s.ID); f != nil {
		f.Mode = mode
		f.Full = mode == modeFull
	} else {
		d.st.Follows = append(d.st.Follows, &follow{SessionID: s.ID, Mode: mode, Full: mode == modeFull})
	}
	d.st.Follow = nil
	if d.streams == nil {
		d.streams = map[string]*followStream{}
	}
	d.streams[s.ID] = &followStream{}
	d.st.Selected = s.ID
	d.save()
	d.reply(fmt.Sprintf("👁 Following %s (%s): new messages arrive as they come.", s.ref(), modeName(mode)),
		keyboard(row(button("⏹ Unfollow", "uf:"+s.scope()), button("Open "+strconv.Itoa(s.Num), "o:"+s.scope()))))
}

func (d *Daemon) stopFollowSession(s *session, why string) {
	if s == nil || d.followOf(s.ID) == nil {
		d.reply("Not following any session.", nil)
		return
	}
	d.flushFollowSession(s, true)
	d.removeFollow(s.ID)
	d.save()
	d.reply("Stopped following "+s.ref()+why+".", nil)
}

func (d *Daemon) stopAllFollows() {
	if len(d.st.Follows) == 0 && d.st.Follow == nil {
		d.reply("Not following any session.", nil)
		return
	}
	d.flushFollow(true)
	n := len(d.st.Follows)
	var only *session
	if n == 1 {
		only = d.byID(d.st.Follows[0].SessionID)
	} else if n == 0 && d.st.Follow != nil {
		n = 1
		only = d.byID(d.st.Follow.SessionID)
	}
	d.st.Follows, d.st.Follow, d.streams = nil, nil, nil
	d.save()
	if n == 1 && only != nil {
		d.reply("Stopped following "+only.ref()+".", nil)
		return
	}
	d.reply(fmt.Sprintf("Stopped following %s.", plural(n, "session")), nil)
}

func (d *Daemon) cmdUnfollow(args []string) {
	if len(args) == 1 && (strings.EqualFold(args[0], "all") || args[0] == "*") {
		d.stopAllFollows()
		return
	}
	if len(args) >= 1 {
		s, err := d.sessionArg(args)
		if err != nil {
			d.reply(err.Error(), nil)
			return
		}
		if d.followOf(s.ID) == nil {
			d.reply("Not following "+s.ref()+".", nil)
			return
		}
		d.stopFollowSession(s, "")
		return
	}
	if len(d.st.Follows) == 0 && d.st.Follow == nil {
		d.reply("Not following any session.", nil)
		return
	}
	if len(d.st.Follows) == 1 {
		if s := d.byID(d.st.Follows[0].SessionID); s != nil {
			d.stopFollowSession(s, "")
			return
		}
		d.stopAllFollows()
		return
	}
	d.sendView(d.followingView())
}

func (d *Daemon) followingView() view {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n👁 Following", d.header())
	var rows [][]telegram.InlineKeyboardButton
	shown := 0
	for _, f := range d.st.Follows {
		s := d.byID(f.SessionID)
		if s == nil {
			continue
		}
		shown++
		cur := f.mode()
		fmt.Fprintf(&b, "\n%s · %s", s.ref(), modeName(cur))
		mark := func(m, icon string) string {
			if cur == m {
				return icon + "✓"
			}
			return icon
		}
		rows = append(rows, row(
			button(mark(modeAgent, "🤖"), "fm:"+s.scope()+":a"),
			button(mark(modeChat, "💬"), "fm:"+s.scope()+":c"),
			button(mark(modeFull, "🧰"), "fm:"+s.scope()+":f"),
			button(fmt.Sprintf("⏹ #%d", s.Num), "uf:"+s.scope()),
		))
	}
	if shown == 0 {
		b.WriteString("\nNot following any session.")
	}
	if shown > 1 {
		rows = append(rows, row(button("⏹ Unfollow all", "ufa:a")))
	}
	rows = append(rows, row(button("« Sessions", "l")))
	return view{b.String(), keyboard(rows...)}
}

// feedFollow takes a followed session's new transcript lines.
func (d *Daemon) feedFollow(s *session, lines [][]byte) {
	if d.followOf(s.ID) == nil {
		return
	}
	fs := d.streamFor(s.ID)
	fs.buf = append(fs.buf, fs.stream.Feed(lines)...)
}

// flushFollow sends what followed sessions said, at most every FollowEvery
// per session so a busy session doesn't flood the chat.
func (d *Daemon) flushFollow(now bool) {
	for _, f := range d.st.Follows {
		s := d.byID(f.SessionID)
		if s == nil {
			continue
		}
		d.flushFollowSession(s, now)
	}
}

func (d *Daemon) flushFollowSession(s *session, now bool) {
	f := d.followOf(s.ID)
	if f == nil || d.streams == nil {
		return
	}
	fs := d.streams[s.ID]
	if fs == nil || len(fs.buf) == 0 {
		return
	}
	if !now && d.now().Sub(fs.sent) < d.cfg.FollowEvery {
		return
	}
	mode := f.mode()
	shown := 0
	for _, e := range fs.buf {
		if showEntry(e, mode) {
			shown++
		}
	}
	if shown == 0 {
		fs.buf = nil
		return
	}
	btns := []telegram.InlineKeyboardButton{
		button("⏹ Unfollow", "uf:"+s.scope()),
		button("Open "+strconv.Itoa(s.Num), "o:"+s.scope()),
	}
	for _, rv := range d.renderEntries(s, "", fs.buf, mode, false, btns...) {
		d.sendRendered(rv, "HTML", "follow", s.tag())
	}
	fs.buf, fs.sent = nil, d.now()
}
