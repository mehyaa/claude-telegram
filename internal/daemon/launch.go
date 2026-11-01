package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mehyaa/claude-telegram/internal/launch"
	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/tmux"
)

// draft is the new-session or resume flow in progress. It is not saved.
type draft struct {
	Kind    string // "new" or "resume"
	Name    string
	Prompt  string
	Filter  string
	Matches []string
	Create  string
	Past    *launch.Past
}

func (d *Daemon) home() string {
	if d.cfg.Home != "" {
		return d.cfg.Home
	}
	h, ok := homeDir()
	if !ok {
		return ""
	}
	return h
}

func (d *Daemon) projects() string {
	if d.cfg.Projects != "" {
		return d.cfg.Projects
	}
	return filepath.Join(d.home(), ".claude", "projects")
}

func (d *Daemon) tmux(args ...string) ([]byte, error) {
	if d.cfg.Tmux != nil {
		return d.cfg.Tmux(args...)
	}
	return tmux.Default(args...)
}

func (d *Daemon) ask(text, placeholder string) {
	chat := d.chatID
	d.send(func(ctx context.Context) {
		if _, err := d.tg.SendForceReply(ctx, chat, text, placeholder); err != nil {
			d.cfg.Log.Warn("asking", "err", err)
		}
	})
}

func launchAwait(action string) bool {
	switch action {
	case "newname", "newdir", "resumename":
		return true
	}
	return false
}

func (d *Daemon) cmdNew(args []string) {
	if len(args) == 0 {
		d.askNew()
		return
	}
	name, ok := launch.SessionName(args[0])
	if !ok {
		d.reply("That isn't a session name. Use letters, digits, - and _, starting with a letter, at most 40 characters. MyApp becomes my-app.", nil)
		return
	}
	folder, prompt := splitNew(args[1:])
	d.draft = &draft{Kind: "new", Name: name, Prompt: prompt}
	if strings.TrimSpace(folder) == "" {
		d.askFolder()
		return
	}
	d.offerFolders(folder)
}

func splitNew(args []string) (folder, prompt string) {
	for i, a := range args {
		if a == "--" {
			return strings.Join(args[:i], " "), strings.Join(args[i+1:], " ")
		}
	}
	return strings.Join(args, " "), ""
}

func (d *Daemon) askNew() {
	d.draft = &draft{Kind: "new"}
	d.st.Await = &awaiting{Action: "newname", Until: d.now().Add(awaitFor)}
	d.ask("🆕 New session. Send a name. MyApp becomes my-app.", "name")
}

func (d *Daemon) takeNewName(text string) {
	name, ok := launch.SessionName(text)
	if !ok {
		d.st.Await = &awaiting{Action: "newname", Until: d.now().Add(awaitFor)}
		d.ask("That isn't a session name. Use letters, digits, - and _, starting with a letter, at most 40 characters. MyApp becomes my-app.", "name")
		return
	}
	if d.draft == nil {
		d.draft = &draft{Kind: "new"}
	}
	d.draft.Name = name
	d.askFolder()
}

func (d *Daemon) askFolder() {
	name := ""
	if d.draft != nil {
		name = d.draft.Name
	}
	d.st.Await = &awaiting{Action: "newdir", Until: d.now().Add(awaitFor)}
	if name == "" {
		d.ask("Send a folder. A path, or a few pieces of it, like proj/claude-tel.", "folder")
		return
	}
	d.ask("Folder for "+name+". A path, or a few pieces of it, like proj/claude-tel.", "folder")
}

func (d *Daemon) takeNewDir(text string) {
	d.offerFolders(text)
}

func (d *Daemon) offerFolders(query string) {
	if d.draft == nil || d.draft.Name == "" {
		d.askNew()
		return
	}
	matches, create, err := launch.Search(d.home(), query)
	switch {
	case errors.Is(err, launch.ErrOutside):
		d.reply("New sessions stay under your home folder.", nil)
		d.askFolder()
		return
	case errors.Is(err, launch.ErrNotDir):
		d.reply("That's a file, not a folder.", nil)
		d.askFolder()
		return
	case errors.Is(err, launch.ErrWide):
		d.reply("That matches too much of your home folder. Send a longer path.", nil)
		d.askFolder()
		return
	case err != nil:
		d.reply("Couldn't search folders: "+err.Error()+".", nil)
		return
	}
	switch len(matches) {
	case 0:
		if create == "" {
			d.askFolder()
			return
		}
		d.draft.Create = create
		shown := launch.Short(d.home(), create)
		d.reply("No folder matches "+shown+".", keyboard(row(button(clipRunes("📁 Create "+shown, 40), "nc:a"))))
	case 1:
		d.startNew(matches[0])
	default:
		if len(matches) > 8 {
			d.reply(fmt.Sprintf("%d folders match. Send a longer path.", len(matches)), nil)
			d.askFolder()
			return
		}
		d.draft.Matches = matches
		var rows [][]telegram.InlineKeyboardButton
		for i, m := range matches {
			rows = append(rows, row(button(clipRunes(launch.Short(d.home(), m), 40), fmt.Sprintf("nf:a:%d", i))))
		}
		d.reply("Pick a folder for "+d.draft.Name+".", keyboard(rows...))
	}
}

func (d *Daemon) createFolder() {
	if d.draft == nil || d.draft.Create == "" {
		d.reply("Nothing to create. Send /new to start again.", nil)
		return
	}
	path := d.draft.Create
	if _, _, err := launch.Search(d.home(), path); errors.Is(err, launch.ErrOutside) {
		d.reply("New sessions stay under your home folder.", nil)
		return
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		d.reply("Couldn't create "+launch.Short(d.home(), path)+": "+err.Error()+".", nil)
		return
	}
	d.startNew(path)
}

func (d *Daemon) pickFolder(i int) {
	if d.draft == nil || i < 0 || i >= len(d.draft.Matches) {
		d.reply("That list is out of date. Send the folder again.", nil)
		return
	}
	d.startNew(d.draft.Matches[i])
}

func (d *Daemon) startNew(dir string) {
	if d.draft == nil || d.draft.Name == "" {
		d.askNew()
		return
	}
	name, prompt := d.draft.Name, d.draft.Prompt
	d.draft = nil
	d.st.Await = nil
	tmuxName := "claude-" + name
	live, exists := d.occupant(tmuxName)
	if live != nil {
		d.reply(tmuxName+" is already running. Selected "+live.ref()+".", nil)
		d.selectSession(live)
		return
	}
	if exists {
		d.reply(tmuxName+" is already running, and it hasn't checked in. It will show on /list when it does.", nil)
		return
	}
	if err := d.spawn(tmuxName, name, dir, prompt, ""); err != nil {
		d.reply("Couldn't start "+tmuxName+": "+err.Error()+".", nil)
		return
	}
	d.reply("🆕 Starting "+tmuxName+" in "+launch.Short(d.home(), dir)+".", nil)
}

func (d *Daemon) cmdResume(args []string) {
	d.sendView(d.resumeView(strings.Join(args, " "), 0))
}

func (d *Daemon) resumeView(filter string, page int) view {
	d.st.Await = nil
	d.draft = &draft{Kind: "resume", Filter: strings.TrimSpace(filter)}
	all, err := launch.List(d.projects())
	if err != nil {
		return view{"Couldn't read past sessions: " + err.Error() + ".", keyboard(row(button("« Sessions", "l")))}
	}
	all = launch.Filter(all, filter, d.home())
	page, pages, rows := launch.Page(all, page)
	d.draft.Filter = strings.TrimSpace(filter)
	var b strings.Builder
	b.WriteString("⏪ Resume")
	if strings.TrimSpace(filter) != "" {
		b.WriteString(" · " + strings.TrimSpace(filter))
	}
	b.WriteString("\n")
	if len(all) == 0 {
		if strings.TrimSpace(filter) == "" {
			b.WriteString("No past sessions.")
		} else {
			b.WriteString("No sessions match.")
		}
		return view{b.String(), keyboard(row(button("« Sessions", "l")))}
	}
	for i, p := range rows {
		fmt.Fprintf(&b, "%d · %s\n", page*10+i+1, p.Line(d.home(), d.now()))
	}
	var kb [][]telegram.InlineKeyboardButton
	for i, p := range rows {
		kb = append(kb, row(button(clipRunes(fmt.Sprintf("%d %s", page*10+i+1, p.Label), 40), "rr:a:"+p.ID)))
	}
	var nav []telegram.InlineKeyboardButton
	if page > 0 {
		nav = append(nav, button("◀", fmt.Sprintf("rp:a:%d", page-1)))
	}
	if page+1 < pages {
		nav = append(nav, button("▶", fmt.Sprintf("rp:a:%d", page+1)))
	}
	if len(nav) > 0 {
		kb = append(kb, nav)
	}
	kb = append(kb, row(button("« Sessions", "l")))
	return view{strings.TrimRight(b.String(), "\n"), keyboard(kb...)}
}

func (d *Daemon) pickResume(id string) {
	p, ok := launch.Load(d.projects(), id)
	if !ok {
		d.reply("That session is no longer in the list.", nil)
		return
	}
	d.startResume(p, "")
}

func (d *Daemon) takeResumeName(text string) {
	if d.draft == nil || d.draft.Past == nil {
		d.reply("That resume lapsed. Send /resume to start again.", nil)
		return
	}
	name, ok := launch.SessionName(text)
	if !ok {
		d.st.Await = &awaiting{Action: "resumename", Until: d.now().Add(awaitFor)}
		d.ask("That isn't a session name. Use letters, digits, - and _, starting with a letter, at most 40 characters.", "name")
		return
	}
	d.startResume(*d.draft.Past, name)
}

// startResume selects a session that is already running, or starts
// `claude --resume` in its folder (Q49, Q50, Q88). chosen is empty when the
// name should come from the session itself.
func (d *Daemon) startResume(p launch.Past, chosen string) {
	d.st.Await = nil
	if s := d.byID(p.ID); s != nil {
		d.draft = nil
		d.reply("That session is already running. Selected "+s.ref()+".", nil)
		d.selectSession(s)
		return
	}
	if !readableDir(p.Dir) {
		d.draft = nil
		d.reply("That session's folder is gone, so it can't be resumed.", nil)
		return
	}
	name := chosen
	if name == "" {
		var ok bool
		name, ok = p.TmuxName()
		if !ok {
			d.draft = &draft{Kind: "resume", Past: &p}
			d.st.Await = &awaiting{Action: "resumename", Until: d.now().Add(awaitFor)}
			d.ask("Send a name for this session. MyApp becomes my-app.", "name")
			return
		}
	}
	tmuxName := "claude-" + name
	live, exists := d.occupant(tmuxName)
	if live != nil && live.ID == p.ID {
		d.draft = nil
		d.reply(tmuxName+" is already running. Selected "+live.ref()+".", nil)
		d.selectSession(live)
		return
	}
	if exists {
		d.draft = &draft{Kind: "resume", Past: &p}
		d.st.Await = &awaiting{Action: "resumename", Until: d.now().Add(awaitFor)}
		d.ask(tmuxName+" is a different session. Send another name.", "name")
		return
	}
	if err := d.spawn(tmuxName, name, p.Dir, "", p.ID); err != nil {
		d.reply("Couldn't resume "+tmuxName+": "+err.Error()+".", nil)
		return
	}
	if cur, ok := launch.SessionName(p.Custom); !ok || cur != name {
		if d.rename == nil {
			d.rename = map[string]string{}
		}
		d.rename[p.ID] = name
	}
	d.draft = nil
	d.reply("⏪ Resuming "+tmuxName+" in "+launch.Short(d.home(), p.Dir)+".", nil)
}

func (d *Daemon) selectSession(s *session) {
	d.st.Selected = s.ID
	d.touchSession(s)
	d.save()
	d.sendView(d.sessionView(s))
}

// applyRename types /rename once the resumed session has checked in.
// --resume can't be combined with --name (Q50). Called with d.mu held.
func (d *Daemon) applyRename(s *session) {
	if d.rename == nil {
		return
	}
	name, ok := d.rename[s.ID]
	if !ok {
		return
	}
	delete(d.rename, s.ID)
	if s.Name == name {
		return
	}
	d.typeInto(s, "/rename "+name, false)
}

func (d *Daemon) spawn(tmuxName, name, dir, prompt, resumeID string) error {
	shell := os.Getenv("SHELL")
	window := name + ": " + launch.Short(d.home(), dir)
	return d.spawnArgs(launch.Command(shell, tmuxName, clipRunes(window, 80), dir, launch.Script(resumeID, name, prompt)))
}

func (d *Daemon) spawnArgs(args []string) error {
	_, err := d.tmux(args...)
	return err
}

// occupant reports whether tmux session name exists on the default server,
// and the live ctg session in it when one has checked in.
func (d *Daemon) occupant(name string) (live *session, exists bool) {
	if _, err := d.tmux("has-session", "-t", "="+name); err != nil {
		return nil, false
	}
	sock := ""
	if out, err := d.tmux("display-message", "-p", "#{socket_path}"); err == nil {
		sock = strings.TrimSpace(string(out))
	}
	out, err := d.tmux("list-panes", "-t", "="+name, "-F", "#{pane_id}")
	if err != nil {
		return nil, true
	}
	panes := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			panes[line] = true
		}
	}
	for _, s := range d.st.Sessions {
		if !panes[s.Pane] {
			continue
		}
		if sock != "" && s.TmuxSocket != "" && s.TmuxSocket != sock {
			continue
		}
		return s, true
	}
	return nil, true
}

func readableDir(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	fi, err := f.Stat()
	f.Close()
	return err == nil && fi.IsDir()
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
