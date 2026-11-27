package daemon

import (
	"fmt"
	"strings"

	"github.com/mehyaa/claude-telegram/internal/page"
	"github.com/mehyaa/claude-telegram/internal/telegram"
)

// sendOutbound pushes Markdown text to the paired chat as Telegram HTML,
// with the same paging as session content. When s is set, the message is
// prefixed with that session's tag.
func (d *Daemon) sendOutbound(text string, s *session) {
	text = strings.TrimSpace(text)
	if text == "" || d.chatID == 0 {
		return
	}
	pages := page.Pages(text)
	if len(pages) == 0 {
		pages = []string{""}
	}
	htmlPages := make([]string, len(pages))
	for i, p := range pages {
		htmlPages[i] = htmlBody(p)
	}
	head := ""
	pageHead := ""
	sessionRef := ""
	if s != nil && s.tag() != "" {
		sessionRef = s.tag()
		head = "<b>" + htmlEscape(sessionRef) + "</b>"
		pageHead = head + "\n"
	}
	first := ""
	if head != "" {
		first = head + "\n\n"
	}
	first += htmlPages[0]
	var kb *telegram.InlineKeyboardMarkup
	if len(pages) > 1 {
		first += fmt.Sprintf("\n(1/%d)", len(pages))
		id := d.keepPages(pageHead, htmlPages)
		kb = keyboard(row(moreButton(id, 1, len(pages))))
	}
	d.sendRendered(renderedView{v: view{first, kb}}, "HTML", "send", sessionRef)
}
