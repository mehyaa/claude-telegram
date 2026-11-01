package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mehyaa/claude-telegram/internal/drive"
	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/transcript"
)

// exitWait is how long End waits for the process to leave before it
// gives up and leaves the tmux session alone.
var exitWait = 20 * time.Second

const maxUpload = 20 << 20

func (d *Daemon) noteProblem(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	d.problem.Store(msg)
}

func (d *Daemon) lastProblem() string {
	v, _ := d.problem.Load().(string)
	return v
}

func (d *Daemon) tmuxOn(socket string, args ...string) ([]byte, error) {
	if socket != "" {
		args = append([]string{"-S", socket}, args...)
	}
	return d.tmux(args...)
}

// takeFile saves a photo or document into the selected session (Q75).
// Called with d.mu held. The download itself does not hold the lock.
func (d *Daemon) takeFile(m *telegram.Message) {
	s := d.byID(d.st.Selected)
	if s == nil {
		d.reply("Pick a session first: /list, then tap one.", nil)
		return
	}
	if s.Cwd == "" {
		d.reply(s.ref()+" has no folder, so the file has nowhere to go.", nil)
		return
	}
	id, name, size := incomingFile(m)
	if id == "" {
		return
	}
	if size > maxUpload {
		d.reply("That file is over 20 MB. That's as large as Telegram lets a bot download.", nil)
		return
	}
	sid, cwd, caption := s.ID, s.Cwd, strings.TrimSpace(m.Caption)
	d.send(func(ctx context.Context) {
		f, err := d.tg.GetFile(ctx, id)
		var body []byte
		if err == nil {
			body, err = d.tg.Download(ctx, f.FilePath, maxUpload+1)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		s := d.byID(sid)
		if s == nil {
			d.reply("That session has ended, so the file was not saved.", nil)
			return
		}
		if err != nil {
			d.noteProblem("telegram: " + err.Error())
			d.reply("Couldn't download that file: "+err.Error()+".", nil)
			return
		}
		if int64(len(body)) > maxUpload {
			d.reply("That file is over 20 MB. That's as large as Telegram lets a bot download.", nil)
			return
		}
		rel, err := saveUpload(cwd, name, body, d.now())
		if err != nil {
			d.reply("Couldn't save that file: "+err.Error()+".", nil)
			return
		}
		excludeUploads(cwd)
		if caption != "" {
			d.deliver(s, caption+"\n[attached: "+rel+"]")
			return
		}
		s.Files = append(s.Files, rel)
		d.save()
		d.reply("📎 Saved "+rel+". It goes with the next message to "+s.ref()+".", nil)
	})
}

func incomingFile(m *telegram.Message) (id, name string, size int64) {
	if m.Document != nil {
		name = m.Document.FileName
		if name == "" {
			name = "file"
		}
		return m.Document.FileID, name, m.Document.FileSize
	}
	if len(m.Photo) > 0 {
		p := m.Photo[len(m.Photo)-1]
		return p.FileID, "photo.jpg", p.FileSize
	}
	return "", "", 0
}

func saveUpload(cwd, name string, body []byte, now time.Time) (string, error) {
	name = cleanFile(name)
	dir := filepath.Join(cwd, ".ctg-uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := now.UTC().Format("20060102-150405") + "-" + name
	path := filepath.Join(dir, base)
	for i := 2; ; i++ {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", err
		}
		if i > 100 {
			return "", fmt.Errorf("too many uploads with that name")
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d-%s", now.UTC().Format("20060102-150405"), i, name))
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		rel = filepath.Join(".ctg-uploads", filepath.Base(path))
	}
	return filepath.ToSlash(rel), nil
}

func cleanFile(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '/' {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

func excludeUploads(cwd string) {
	git := filepath.Join(cwd, ".git")
	st, err := os.Stat(git)
	if err != nil || !st.IsDir() {
		return
	}
	info := filepath.Join(git, "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		return
	}
	path := filepath.Join(info, "exclude")
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), ".ctg-uploads/") {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f, ".ctg-uploads/")
	f.Close()
}

// withFiles appends waiting uploads to text and clears them (Q75).
func (d *Daemon) withFiles(s *session, text string) string {
	if len(s.Files) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	for _, f := range s.Files {
		b.WriteString("\n[attached: " + f + "]")
	}
	s.Files = nil
	d.save()
	return b.String()
}

// deliver sends text to a session, through the mailbox when it isn't in tmux.
func (d *Daemon) deliver(s *session, text string) {
	if ok, why := d.canType(s); !ok {
		if s.Pane == "" && s.Mailbox != "" {
			d.mailbox(s, text)
			return
		}
		d.reply("Can't type into "+s.ref()+": "+why+".", nil)
		return
	}
	d.typeInto(s, text, false)
}

func (d *Daemon) endConfirm(s *session) view {
	if s.Pane == "" {
		text := fmt.Sprintf("%s\n\nRemove %s from the list?\nIt isn't running in tmux, so this only forgets it here.", d.header(), s.ref())
		return view{text, keyboard(
			row(button("🗑 Remove it", "ey:"+s.scope())),
			row(button("« "+s.ref(), "s:"+s.scope())),
		)}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nEnd %s?\n", d.header(), s.ref())
	if s.Busy {
		b.WriteString("A turn is running, so Esc is pressed first.\n")
	}
	if s.hasWork() {
		b.WriteString("Background work is still running.\n")
	}
	b.WriteString("This types /exit. A claude-* tmux session with no other windows is closed. Otherwise only this pane is closed.")
	return view{b.String(), keyboard(
		row(button("🛑 End it", "ey:"+s.scope())),
		row(button("« "+s.ref(), "s:"+s.scope())),
	)}
}

func (d *Daemon) endSession(s *session) {
	if s.Pane == "" {
		ref := s.ref()
		d.end(s.ID)
		d.record("end", ref, "removed read-only session")
		d.reply("🗑 Removed "+ref+".", nil)
		return
	}
	busy := s.Busy
	pid, procStart, sock, pane := s.PID, s.ProcStart, s.TmuxSocket, s.Pane
	ref := s.ref()
	if !d.drive(s, "end", func(dr *drive.Driver) error {
		if busy {
			if err := dr.Interrupt(); err != nil {
				return err
			}
		}
		return dr.Slash("/exit")
	}, func(err error) {
		if err != nil {
			d.noteProblem("tmux: " + err.Error())
			d.reply("Couldn't end "+ref+": "+whyNot(err)+".", nil)
			return
		}
		go d.finishEnd(ref, pid, procStart, sock, pane)
	}) {
		d.reply("Keys are being pressed in "+ref+" already; try again in a moment.", nil)
	}
}

func (d *Daemon) finishEnd(ref string, pid int, procStart, sock, pane string) {
	if pid <= 0 {
		d.mu.Lock()
		d.reply("/exit was typed into "+ref+", and the process wasn't known, so the tmux session was left as it is.", nil)
		d.mu.Unlock()
		return
	}
	deadline := time.Now().Add(exitWait)
	alive := true
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			alive = false
			break
		}
		if d.cfg.ProcStart != nil {
			if start, ok := d.cfg.ProcStart(pid); !ok || (procStart != "" && start != procStart) {
				alive = false
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if alive {
		d.mu.Lock()
		d.reply("/exit was typed into "+ref+", and the process is still running, so the tmux session was left as it is.", nil)
		d.mu.Unlock()
		return
	}
	out, err := d.tmuxOn(sock, "display-message", "-p", "-t", pane, "#{session_name} #{session_windows}")
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.noteProblem("tmux: " + err.Error())
		d.reply("/exit was typed into "+ref+", and the tmux session was left as it is: "+err.Error()+".", nil)
		return
	}
	name, windows, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	n := 0
	fmt.Sscanf(windows, "%d", &n)
	if strings.HasPrefix(name, "claude-") && n <= 1 {
		if _, err := d.tmuxOn(sock, "kill-session", "-t", name); err != nil {
			d.noteProblem("tmux: " + err.Error())
			d.reply("Couldn't close tmux session "+name+": "+err.Error()+".", nil)
			return
		}
		d.reply("🛑 Ended "+ref+" and closed tmux session "+name+".", nil)
		return
	}
	if _, err := d.tmuxOn(sock, "kill-pane", "-t", pane); err != nil {
		d.noteProblem("tmux: " + err.Error())
		d.reply("Couldn't close the pane: "+err.Error()+".", nil)
		return
	}
	d.reply("🛑 Ended "+ref+" and closed its pane.", nil)
}

func (d *Daemon) modeView(s *session) view {
	if s.Pane == "" {
		return view{d.header() + "\n\nA read-only session has no mode to switch.", keyboard(row(button("« "+s.ref(), "s:"+s.scope())))}
	}
	cur := transcript.Mode(s.Transcript)
	label := modeLabel(cur)
	if label == "" {
		label = "unknown"
	}
	text := fmt.Sprintf("%s\n\n🔀 Mode · %s\nNow: %s\nAuto and bypass are not offered.", d.header(), s.ref(), label)
	mark := func(id, name string) string {
		if cur == id {
			return name + " ✓"
		}
		return name
	}
	return view{text, keyboard(
		row(
			button(mark("manual", "Manual"), "md:"+s.scope()+":manual"),
			button(mark("accept", "Accept edits"), "md:"+s.scope()+":accept"),
			button(mark("plan", "Plan"), "md:"+s.scope()+":plan"),
		),
		row(button("« "+s.ref(), "s:"+s.scope())),
	)}
}

func modeLabel(id string) string {
	switch id {
	case "manual":
		return "Manual"
	case "accept":
		return "Accept edits"
	case "plan":
		return "Plan"
	case "auto":
		return "auto"
	case "bypass":
		return "bypass"
	default:
		return ""
	}
}

// modeSteps is how many Shift+Tab presses reach to from from, along
// manual → accept edits → plan → auto → manual. viaAuto is set when the
// path passes through auto.
func modeSteps(from, to string) (presses int, viaAuto, ok bool) {
	if to == "auto" || to == "bypass" || from == "bypass" || from == "" || to == "" {
		return 0, false, false
	}
	cycle := []string{"manual", "accept", "plan", "auto"}
	fi, ti := -1, -1
	for i, m := range cycle {
		if m == from {
			fi = i
		}
		if m == to {
			ti = i
		}
	}
	if fi < 0 || ti < 0 {
		return 0, false, false
	}
	if fi == ti {
		return 0, false, true
	}
	i := fi
	for i != ti {
		i = (i + 1) % len(cycle)
		presses++
		if cycle[i] == "auto" && i != ti {
			viaAuto = true
		}
	}
	return presses, viaAuto, true
}

func (d *Daemon) setMode(s *session, to string) {
	if s.Pane == "" {
		d.reply("A read-only session has no mode to switch.", nil)
		return
	}
	from := transcript.Mode(s.Transcript)
	if from == "" {
		d.reply("The transcript doesn't say the mode yet, so nothing was pressed.", nil)
		return
	}
	if from == "bypass" {
		d.reply("This session is in bypass mode. Switch it from the terminal.", nil)
		return
	}
	n, via, ok := modeSteps(from, to)
	if !ok {
		d.reply("That mode isn't offered.", nil)
		return
	}
	if n == 0 {
		d.reply(s.ref()+" is already "+modeLabel(to)+".", nil)
		return
	}
	if via && s.Busy {
		d.reply("That passes through auto mode, and a turn is running. Wait until it is idle.", nil)
		return
	}
	ref, want := s.ref(), modeLabel(to)
	if !d.drive(s, "mode", func(dr *drive.Driver) error {
		for i := 0; i < n; i++ {
			if err := dr.ShiftTab(); err != nil {
				return err
			}
		}
		return nil
	}, func(err error) {
		if err != nil {
			d.noteProblem("tmux: " + err.Error())
			d.reply("Couldn't switch "+ref+" to "+want+": "+whyNot(err)+".", nil)
			return
		}
		d.reply("🔀 "+ref+" → "+want+".", nil)
	}) {
		d.reply("Keys are being pressed in "+ref+" already; try again in a moment.", nil)
	}
}

func (d *Daemon) cmdEnd(args []string) {
	s, err := d.sessionArg(args)
	if err != nil {
		d.reply(err.Error(), nil)
		return
	}
	if s == nil {
		s = d.byID(d.st.Selected)
	}
	if s == nil {
		d.reply("Pick a session first: /list, then tap one.", nil)
		return
	}
	d.sendView(d.endConfirm(s))
}

func (d *Daemon) cmdMode(args []string) {
	s, err := d.sessionArg(args)
	if err != nil {
		d.reply(err.Error(), nil)
		return
	}
	if s == nil {
		s = d.byID(d.st.Selected)
	}
	if s == nil {
		d.reply("Pick a session first: /list, then tap one.", nil)
		return
	}
	d.sendView(d.modeView(s))
}
