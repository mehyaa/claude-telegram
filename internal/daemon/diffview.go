package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mehyaa/claude-telegram/internal/diff"
	"github.com/mehyaa/claude-telegram/internal/page"
)

func (d *Daemon) showToolDiff(s *session, toolID string) {
	raw, name := toolInput(s.Transcript, toolID)
	body, ok := diff.OfTool(name, raw)
	if !ok {
		body, ok = toolResultDiff(s.Transcript, toolID)
	}
	if !ok {
		d.reply("That edit has no diff.", nil)
		return
	}
	if name == "" {
		name = "Diff"
	}
	d.showDiff(name, body)
}

func (d *Daemon) showItemDiff(s *session, it *item) {
	body, ok := diff.OfTool(it.Tool, it.Detail)
	if !ok {
		d.reply("That edit has no diff.", nil)
		return
	}
	d.showDiff(it.Tool, body)
}

func (d *Daemon) showDiff(title, body string) {
	if d.diffs == nil {
		d.diffs = map[string]string{}
	}
	d.pagedSeq++
	id := fmt.Sprintf("d%d", d.pagedSeq)
	d.diffs[id] = body
	d.diffOrder = append(d.diffOrder, id)
	for len(d.diffOrder) > keepPaged {
		delete(d.diffs, d.diffOrder[0])
		d.diffOrder = d.diffOrder[1:]
	}
	if len([]rune(body)) > 3*page.Size {
		d.sendDiffFile(id, title)
		return
	}
	html := diffHTML(body)
	if len([]rune(html)) > 3*page.Size {
		d.sendDiffFile(id, title)
		return
	}
	d.sendMode(view{
		"🧾 <b>" + htmlEscape(title) + "</b>\n" + html,
		keyboard(row(button("📄 As file", "da:a:"+id))),
	}, "HTML")
}

func (d *Daemon) sendDiffFile(id, title string) {
	body := d.diffs[id]
	name := "change.diff"
	if title != "" {
		name = strings.Map(func(r rune) rune {
			if r < 32 || r == '/' || r == ' ' {
				return '-'
			}
			return r
		}, title) + ".diff"
	}
	chat := d.chatID
	file := []byte(body)
	caption := "🧾 " + title
	d.send(func(ctx context.Context) {
		if err := d.tg.SendDocument(ctx, chat, name, file, caption); err != nil {
			d.noteProblem("telegram: " + err.Error())
			d.cfg.Log.Warn("sending a diff", "err", err)
		}
	})
}

func diffHTML(body string) string {
	var b strings.Builder
	var hunk []string
	flush := func() {
		if len(hunk) == 0 {
			return
		}
		fmt.Fprintf(&b, "<pre><code class=\"language-diff\">%s</code></pre>\n", htmlEscape(strings.Join(hunk, "\n")))
		hunk = nil
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "@@") && len(hunk) > 0 {
			flush()
		}
		hunk = append(hunk, line)
	}
	flush()
	return strings.TrimRight(b.String(), "\n")
}

func toolInput(path, id string) ([]byte, string) {
	if path == "" || id == "" {
		return nil, ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, id) || !strings.Contains(line, `"tool_use"`) {
			continue
		}
		var row struct {
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		for _, c := range row.Message.Content {
			if c.Type == "tool_use" && c.ID == id {
				return c.Input, c.Name
			}
		}
	}
	return nil, ""
}

func toolResultDiff(path, id string) (string, bool) {
	if path == "" || id == "" {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, id) || !strings.Contains(line, `"tool_result"`) {
			continue
		}
		var row struct {
			Message struct {
				Content []struct {
					Type      string `json:"type"`
					ToolUseID string `json:"tool_use_id"`
				} `json:"content"`
			} `json:"message"`
			ToolUseResult json.RawMessage `json:"toolUseResult"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || len(row.ToolUseResult) == 0 {
			continue
		}
		for _, c := range row.Message.Content {
			if c.Type == "tool_result" && c.ToolUseID == id {
				body, _, _, _, ok := diff.OfResult(row.ToolUseResult)
				if ok {
					return body, true
				}
			}
		}
	}
	return "", false
}
