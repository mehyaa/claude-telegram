package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/launch"
	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/transcript"
)

type grepHit struct {
	Ref     string
	Path    string
	Index   int
	When    time.Time
	Snippet string
}

func (d *Daemon) askGrep(s *session) {
	id := ""
	who := "every session from the last 30 days"
	if s != nil {
		id = s.ID
		who = s.ref()
	}
	d.st.Await = &awaiting{Action: "grep", Session: id, Until: d.now().Add(awaitFor)}
	d.ask("🔎 Search "+who+". Send the text.", "search")
}

func (d *Daemon) cmdGrep(args []string) {
	if len(args) == 0 {
		d.reply("Usage: /grep [n] text", nil)
		return
	}
	id := ""
	text := strings.Join(args, " ")
	if len(args) >= 2 {
		if s, err := d.lookup(args[0]); err == nil {
			id = s.ID
			text = strings.Join(args[1:], " ")
		}
	}
	d.startGrep(id, text)
}

func (d *Daemon) startGrep(sessionID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		d.reply("Usage: /grep [n] text", nil)
		return
	}
	type src struct {
		ref, path string
		mod       time.Time
	}
	var srcs []src
	if sessionID != "" {
		s := d.byID(sessionID)
		if s == nil || s.Transcript == "" {
			d.reply("That session has no transcript.", nil)
			return
		}
		srcs = append(srcs, src{ref: s.ref(), path: s.Transcript})
	} else {
		cutoff := d.now().Add(-30 * 24 * time.Hour)
		seen := map[string]bool{}
		for _, s := range d.st.Sessions {
			if s.Transcript == "" || seen[s.Transcript] {
				continue
			}
			seen[s.Transcript] = true
			srcs = append(srcs, src{ref: s.ref(), path: s.Transcript, mod: d.now()})
		}
		labels := map[string]string{}
		if past, err := launch.List(d.projects()); err == nil {
			for _, p := range past {
				labels[p.ID] = p.Label
			}
		}
		roots, err := os.ReadDir(d.projects())
		if err == nil {
			for _, root := range roots {
				if !root.IsDir() {
					continue
				}
				files, err := os.ReadDir(filepath.Join(d.projects(), root.Name()))
				if err != nil {
					continue
				}
				for _, f := range files {
					if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
						continue
					}
					info, err := f.Info()
					if err != nil || info.ModTime().Before(cutoff) {
						continue
					}
					path := filepath.Join(d.projects(), root.Name(), f.Name())
					if seen[path] {
						continue
					}
					seen[path] = true
					ref := root.Name()
					if lbl := labels[strings.TrimSuffix(f.Name(), ".jsonl")]; lbl != "" {
						ref = lbl
					}
					srcs = append(srcs, src{ref: ref, path: path, mod: info.ModTime()})
				}
			}
		}
		sort.Slice(srcs, func(i, j int) bool { return srcs[i].mod.After(srcs[j].mod) })
	}
	q := text
	go func() {
		var hits []grepHit
		for _, src := range srcs {
			b, err := os.ReadFile(src.path)
			if err != nil {
				continue
			}
			entries := transcript.Entries(bytes.Split(b, []byte("\n")))
			for i, e := range entries {
				blob := strings.ToLower(e.Text + "\n" + e.Command + "\n" + e.Path + "\n" + e.Output + "\n" + e.Desc)
				if !strings.Contains(blob, strings.ToLower(q)) {
					continue
				}
				hits = append(hits, grepHit{Ref: src.ref, Path: src.path, Index: i, When: e.Time, Snippet: snippet(e.Text+" "+e.Command+" "+e.Desc, q)})
			}
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].When.After(hits[j].When) })
		if len(hits) > 10 {
			hits = hits[:10]
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.showGrep(q, hits)
	}()
}

func (d *Daemon) showGrep(q string, hits []grepHit) {
	d.greps = hits
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n🔎 Search · %s\n", d.header(), q)
	if len(hits) == 0 {
		b.WriteString("No hits.")
		d.sendView(view{b.String(), keyboard(row(button("« Sessions", "l")))})
		return
	}
	var rows [][]telegram.InlineKeyboardButton
	for i, h := range hits {
		when := ""
		if !h.When.IsZero() {
			when = " · " + d.clock(h.When)
		}
		fmt.Fprintf(&b, "%d · %s%s\n%s\n", i+1, h.Ref, when, h.Snippet)
		label := "📜 Around"
		if len(hits) > 1 {
			label = fmt.Sprintf("📜 Around %d", i+1)
		}
		rows = append(rows, row(button(label, fmt.Sprintf("ga:a:%d", i))))
	}
	rows = append(rows, row(button("« Sessions", "l")))
	d.sendView(view{strings.TrimRight(b.String(), "\n"), keyboard(rows...)})
}

func (d *Daemon) grepAround(i int) {
	if i < 0 || i >= len(d.greps) {
		d.reply("That search is out of date. Send /grep again.", nil)
		return
	}
	h := d.greps[i]
	path, ref, index := h.Path, h.Ref, h.Index
	go func() {
		b, err := os.ReadFile(path)
		d.mu.Lock()
		defer d.mu.Unlock()
		if err != nil {
			d.reply("Couldn't read that transcript: "+err.Error()+".", nil)
			return
		}
		entries := transcript.Entries(bytes.Split(b, []byte("\n")))
		from := index - 2
		if from < 0 {
			from = 0
		}
		to := index + 3
		if to > len(entries) {
			to = len(entries)
		}
		s := &session{}
		for _, ses := range d.st.Sessions {
			if ses.Transcript == path {
				s = ses
				break
			}
		}
		header := fmt.Sprintf("📜 <b>Around</b> · %s", htmlEscape(ref))
		for _, v := range d.render(s, header, entries[from:to], modeFull) {
			d.sendMode(v, "HTML")
		}
	}()
}

func snippet(text, q string) string {
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	at := indexRunes([]rune(strings.ToLower(text)), []rune(strings.ToLower(q)))
	if at < 0 {
		return telegram.Clip(text, 80)
	}
	start := at - 20
	if start < 0 {
		start = 0
	}
	end := at + len([]rune(q)) + 20
	if end > len(r) {
		end = len(r)
	}
	out := string(r[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(r) {
		out += "…"
	}
	return out
}

func indexRunes(s, sub []rune) int {
	if len(sub) == 0 || len(sub) > len(s) {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if string(s[i:i+len(sub)]) == string(sub) {
			return i
		}
	}
	return -1
}
