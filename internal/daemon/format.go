package daemon

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mehyaa/claude-telegram/internal/transcript"
)

const (
	modeAgent = "agent" // the model's text only (Q53)
	modeChat  = "chat"  // prompts and the model's text; the default
	modeFull  = "full"  // tools, their results, and other sessions too
)

func modeName(mode string) string {
	switch mode {
	case modeAgent:
		return "🤖 Agent"
	case modeFull:
		return "🧰 Full"
	default:
		return "💬 Chat"
	}
}

func namedMode(word string) (string, bool) {
	switch strings.ToLower(word) {
	case "agent":
		return modeAgent, true
	case "chat":
		return modeChat, true
	case "full":
		return modeFull, true
	default:
		return "", false
	}
}

func parseMode(word string) (string, bool) {
	if m, ok := namedMode(word); ok {
		return m, true
	}
	switch word {
	case "a":
		return modeAgent, true
	case "c":
		return modeChat, true
	case "f":
		return modeFull, true
	default:
		return "", false
	}
}

func showEntry(e transcript.Entry, mode string) bool {
	switch mode {
	case modeAgent:
		return e.Kind == transcript.Assistant
	case modeFull:
		return true
	default:
		return e.Kind == transcript.User || e.Kind == transcript.Assistant || e.Kind == transcript.Note
	}
}

// htmlCard is an item card in Telegram HTML (Q56): the title line is bold
// and the body is rendered with htmlBody, so a "<" in a question is not a tag.
func htmlCard(text string) string {
	lines := strings.Split(text, "\n")
	titleIdx := -1
	// The title is the first non-empty line after the header blank line.
	for i, l := range lines {
		if i > 0 && l != "" && lines[i-1] == "" {
			titleIdx = i
			break
		}
	}
	if titleIdx < 0 {
		return htmlBody(text)
	}
	var b strings.Builder
	for i := 0; i <= titleIdx; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i == titleIdx {
			b.WriteString("<b>" + htmlEscape(lines[i]) + "</b>")
		} else {
			b.WriteString(htmlEscape(lines[i]))
		}
	}
	if titleIdx+1 < len(lines) {
		b.WriteByte('\n')
		b.WriteString(htmlBody(strings.Join(lines[titleIdx+1:], "\n")))
	}
	return b.String()
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

var (
	headingRE   = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*$`)
	hrRE        = regexp.MustCompile(`^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	bulletRE    = regexp.MustCompile(`^(\s*)[-*]\s+(.+)$`)
	tableSepRE  = regexp.MustCompile(`^\s*\|?(?:\s*:?-{2,}:?\s*\|)+\s*:?-{2,}:?\s*\|?\s*$`)
	mdLinkRE    = regexp.MustCompile(`\[([^\[\]]+)\]\((https?://[^\s()]+)\)`)
	boldStarRE  = regexp.MustCompile(`\*\*([^\s*](?:.*?[^\s*])?)\*\*`)
	strikeRE    = regexp.MustCompile(`~~([^\s~](?:.*?[^\s~])?)~~`)
	heredocOpRE = regexp.MustCompile(`<<-?\s*(?:'([^']+)'|"([^"]+)"|([A-Za-z0-9_]+))`)
)

// htmlBody turns a message's text into Telegram HTML. ``` fences become
// <pre><code>, Markdown tables become aligned <pre> blocks, and inline
// Markdown (**bold**, *italic*, `code`, ~~strike~~, [link](url), headings,
// blockquotes) is converted into Telegram-supported HTML tags.
func htmlBody(text string) string {
	var b strings.Builder
	lines := strings.Split(text, "\n")
	in := false
	lang := ""
	var code []string
	var quote []string
	flushQuote := func() {
		if len(quote) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "<blockquote>%s</blockquote>", strings.Join(quote, "\n"))
		quote = nil
	}
	flush := func() {
		if !in {
			return
		}
		body := htmlEscape(strings.Join(code, "\n"))
		if lang != "" {
			fmt.Fprintf(&b, "<pre><code class=\"language-%s\">%s</code></pre>", lang, body)
		} else {
			fmt.Fprintf(&b, "<pre>%s</pre>", body)
		}
		code = nil
		in = false
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "```") {
			flushQuote()
			if in {
				flush()
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			in = true
			lang = fenceLang(strings.TrimPrefix(trim, "```"))
			continue
		}
		if in {
			code = append(code, l)
			continue
		}
		if isTableStart(lines, i) {
			flushQuote()
			end := i + 2
			for end < len(lines) && isTableRow(lines[end]) {
				end++
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "<pre>%s</pre>", htmlEscape(formatMarkdownTable(lines[i:end])))
			i = end - 1
			continue
		}
		if strings.HasPrefix(trim, ">") && (len(trim) == 1 || trim[1] == ' ') {
			qLine := strings.TrimPrefix(strings.TrimPrefix(trim, ">"), " ")
			quote = append(quote, inlineMarkdown(qLine))
			continue
		}
		flushQuote()
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(formatMarkdownLine(l))
	}
	flushQuote()
	if in {
		// An unclosed fence is still a code block.
		flush()
	}
	return b.String()
}

func isTableRow(line string) bool {
	trim := strings.TrimSpace(line)
	return trim != "" && strings.Contains(trim, "|")
}

func isTableStart(lines []string, i int) bool {
	if i+2 > len(lines) {
		return false
	}
	return isTableRow(lines[i]) && tableSepRE.MatchString(lines[i+1])
}

func parseTableCells(line string) []string {
	trim := strings.TrimSpace(line)
	trim = strings.TrimPrefix(trim, "|")
	trim = strings.TrimSuffix(trim, "|")
	parts := strings.Split(trim, "|")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.ReplaceAll(p, "**", "")
		p = strings.ReplaceAll(p, "`", "")
		parts[i] = p
	}
	return parts
}

func formatMarkdownTable(lines []string) string {
	var rows [][]string
	cols := 0
	for idx, l := range lines {
		if idx == 1 && tableSepRE.MatchString(l) {
			continue
		}
		cells := parseTableCells(l)
		if len(cells) > cols {
			cols = len(cells)
		}
		rows = append(rows, cells)
	}
	if cols == 0 || len(rows) == 0 {
		return strings.Join(lines, "\n")
	}
	widths := make([]int, cols)
	for _, r := range rows {
		for c := 0; c < len(r); c++ {
			if w := len([]rune(r[c])); w > widths[c] {
				widths[c] = w
			}
		}
	}
	var out []string
	for rIdx, r := range rows {
		var line strings.Builder
		for c := 0; c < cols; c++ {
			val := ""
			if c < len(r) {
				val = r[c]
			}
			if c > 0 {
				line.WriteString(" | ")
			}
			pad := widths[c] - len([]rune(val))
			line.WriteString(val)
			if c < cols-1 && pad > 0 {
				line.WriteString(strings.Repeat(" ", pad))
			}
		}
		out = append(out, line.String())
		if rIdx == 0 && len(rows) > 1 {
			var sep strings.Builder
			for c := 0; c < cols; c++ {
				if c > 0 {
					sep.WriteString("-+-")
				}
				w := widths[c]
				if w < 3 {
					w = 3
				}
				sep.WriteString(strings.Repeat("-", w))
			}
			out = append(out, sep.String())
		}
	}
	return strings.Join(out, "\n")
}

func formatMarkdownLine(l string) string {
	if hrRE.MatchString(l) {
		return "───"
	}
	if m := headingRE.FindStringSubmatch(l); m != nil {
		return "<b>" + inlineMarkdown(m[2]) + "</b>"
	}
	if m := bulletRE.FindStringSubmatch(l); m != nil {
		return m[1] + "• " + inlineMarkdown(m[2])
	}
	return inlineMarkdown(l)
}

func inlineMarkdown(s string) string {
	if s == "" {
		return ""
	}
	var codes []string
	s = extractCodeSpans(s, &codes)

	var links []string
	s = mdLinkRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLinkRE.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		label := formatInlineStyles(htmlEscape(sub[1]))
		url := strings.ReplaceAll(htmlEscape(sub[2]), `"`, "&quot;")
		idx := len(links)
		links = append(links, fmt.Sprintf(`<a href="%s">%s</a>`, url, label))
		return fmt.Sprintf("\x00L%d\x00", idx)
	})

	s = formatInlineStyles(htmlEscape(s))

	for i, l := range links {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00L%d\x00", i), l)
	}
	for i, c := range codes {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00C%d\x00", i), c)
	}
	return s
}

func extractCodeSpans(s string, codes *[]string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '`' {
			b.WriteByte(s[i])
			i++
			continue
		}
		ticks := 1
		for i+ticks < len(s) && s[i+ticks] == '`' {
			ticks++
		}
		delim := s[i : i+ticks]
		end := strings.Index(s[i+ticks:], delim)
		if end < 0 {
			b.WriteString(delim)
			i += ticks
			continue
		}
		inner := s[i+ticks : i+ticks+end]
		if inner != "" && !strings.Contains(inner, "\n") {
			idx := len(*codes)
			*codes = append(*codes, "<code>"+htmlEscape(inner)+"</code>")
			fmt.Fprintf(&b, "\x00C%d\x00", idx)
			i += ticks + end + ticks
			continue
		}
		b.WriteString(delim)
		i += ticks
	}
	return b.String()
}

func formatInlineStyles(s string) string {
	s = boldStarRE.ReplaceAllString(s, "<b>$1</b>")
	s = strikeRE.ReplaceAllString(s, "<s>$1</s>")
	s = replaceBoundedEmphasis(s, '*')
	s = replaceBoundedEmphasis(s, '_')
	return s
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func replaceBoundedEmphasis(s string, mark rune) string {
	if !strings.ContainsRune(s, mark) {
		return s
	}
	runes := []rune(s)
	var out strings.Builder
	for i := 0; i < len(runes); {
		if runes[i] != mark {
			out.WriteRune(runes[i])
			i++
			continue
		}
		// Do not match repeated marks (** or __).
		if i+1 < len(runes) && runes[i+1] == mark {
			for i < len(runes) && runes[i] == mark {
				out.WriteRune(runes[i])
				i++
			}
			continue
		}
		prevOK := i == 0 || (!isWordRune(runes[i-1]) && runes[i-1] != '/' && runes[i-1] != mark)
		if !prevOK || i+1 >= len(runes) || unicode.IsSpace(runes[i+1]) {
			out.WriteRune(runes[i])
			i++
			continue
		}
		closeIdx := -1
		for j := i + 1; j < len(runes); j++ {
			if runes[j] == '\n' || (mark == '_' && (runes[j] == '/' || runes[j] == '_')) {
				if runes[j] == mark && j > i+1 && !unicode.IsSpace(runes[j-1]) {
					nextOK := j+1 == len(runes) || (!isWordRune(runes[j+1]) && runes[j+1] != '/' && runes[j+1] != mark)
					if nextOK {
						closeIdx = j
					}
				}
				break
			}
			if runes[j] == mark {
				if j+1 < len(runes) && runes[j+1] == mark {
					break
				}
				if j > i+1 && !unicode.IsSpace(runes[j-1]) {
					nextOK := j+1 == len(runes) || (!isWordRune(runes[j+1]) && runes[j+1] != '/' && runes[j+1] != mark)
					if nextOK {
						closeIdx = j
					}
				}
				break
			}
		}
		if closeIdx < 0 {
			out.WriteRune(runes[i])
			i++
			continue
		}
		out.WriteString("<i>")
		out.WriteString(string(runes[i+1 : closeIdx]))
		out.WriteString("</i>")
		i = closeIdx + 1
	}
	return out.String()
}

func fenceLang(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '-' && r != '_' {
			return ""
		}
	}
	return s
}

func codeBlockHTML(lang, code string) string {
	escaped := htmlEscape(code)
	pre := "<pre>" + escaped + "</pre>"
	if lang != "" {
		pre = fmt.Sprintf(`<pre><code class="language-%s">%s</code></pre>`, lang, escaped)
	}
	if strings.Count(code, "\n") >= 8 || len([]rune(code)) >= 400 {
		return "<blockquote expandable>" + pre + "</blockquote>"
	}
	return pre
}

// formatBashCommand splits top-level ;, &&, and || command chains onto
// separate lines outside quotes, parentheses, and heredoc bodies so commands
// are easy to read in <pre>.
func formatBashCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	rawLines := strings.Split(cmd, "\n")
	var outLines []string
	inHeredoc := ""
	heredocStripTabs := false
	for _, line := range rawLines {
		if inHeredoc != "" {
			outLines = append(outLines, line)
			check := line
			if heredocStripTabs {
				check = strings.TrimLeft(check, "\t")
			}
			if strings.TrimSpace(check) == inHeredoc {
				inHeredoc = ""
			}
			continue
		}
		if m := heredocOpRE.FindStringSubmatch(line); m != nil {
			for _, g := range m[1:] {
				if g != "" {
					inHeredoc = g
					heredocStripTabs = strings.HasPrefix(m[0], "<<-")
					break
				}
			}
		}
		outLines = append(outLines, splitBashChainLine(line)...)
	}
	return strings.Join(outLines, "\n")
}

func splitBashChainLine(line string) []string {
	var parts []string
	var cur strings.Builder
	var quote byte
	parenDepth := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == '\\' && quote != '\'' && i+1 < len(line) {
				i++
				cur.WriteByte(line[i])
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\\' && i+1 < len(line) {
			cur.WriteByte(c)
			i++
			cur.WriteByte(line[i])
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			cur.WriteByte(c)
			continue
		}
		if c == '(' {
			parenDepth++
			cur.WriteByte(c)
			continue
		}
		if c == ')' && parenDepth > 0 {
			parenDepth--
			cur.WriteByte(c)
			continue
		}
		if parenDepth == 0 {
			if (c == '&' || c == '|') && i+1 < len(line) && line[i+1] == c {
				if i+2 < len(line) && (line[i+2] == ' ' || line[i+2] == '\t') {
					cur.WriteByte(c)
					cur.WriteByte(c)
					parts = append(parts, strings.TrimRight(cur.String(), " \t"))
					cur.Reset()
					i += 2
					for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
						i++
					}
					i--
					continue
				}
			}
			if c == ';' && (i+1 >= len(line) || line[i+1] != ';') {
				if i+1 < len(line) && (line[i+1] == ' ' || line[i+1] == '\t') {
					rest := strings.TrimSpace(line[i+1:])
					if rest != "" && !strings.HasPrefix(rest, "then") && !strings.HasPrefix(rest, "do") && !strings.HasPrefix(rest, "#") {
						cur.WriteByte(';')
						parts = append(parts, strings.TrimRight(cur.String(), " \t"))
						cur.Reset()
						i++
						for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
							i++
						}
						i--
						continue
					}
				}
			}
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 || len(parts) == 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

func (d *Daemon) entryHTML(s *session, e transcript.Entry) (head, body string) {
	return d.entryHTMLAt(s, e, 0, 0)
}

func (d *Daemon) entryHTMLAt(s *session, e transcript.Entry, idx, total int) (head, body string) {
	badge := ""
	if s != nil && s.tag() != "" {
		badge = "<b>" + htmlEscape(s.tag()) + "</b> · "
	}
	meta := ""
	if total > 0 && idx > 0 {
		meta += fmt.Sprintf(" · %d/%d", idx, total)
	}
	if !e.Time.IsZero() {
		meta += " · " + htmlEscape(d.clock(e.Time))
	}
	switch e.Kind {
	case transcript.User:
		if e.Term {
			title := "🖥 <b>Terminal</b>" + meta
			if badge != "" {
				title = "🖥 " + badge + "Terminal" + meta
			}
			var parts []string
			if e.Text != "" {
				parts = append(parts, "```bash\n$ "+formatBashCommand(d.mask(s, e.Text))+"\n```")
			}
			if e.Result != "" {
				parts = append(parts, "```\n"+d.mask(s, e.Result)+"\n```")
			}
			return title, strings.Join(parts, "\n")
		}
		if e.Cmd {
			line := "⚙️ " + badge + htmlEscape(e.Text)
			if e.Result != "" {
				line += " → " + htmlEscape(oneLine(e.Result))
			}
			line += meta
			return line, ""
		}
		who := "You (terminal)"
		switch {
		case e.Via != "":
			who = "You ([answer to " + e.Via + "])"
		case d.fromPhone(s, e.Text):
			who = "You (Telegram)"
		}
		title := "👤 <b>" + htmlEscape(who) + "</b>" + meta
		if badge != "" {
			title = "👤 " + badge + htmlEscape(who) + meta
		}
		return title, d.mask(s, e.Text)
	case transcript.Assistant:
		if badge != "" {
			return "🤖 " + badge + "Claude" + meta, d.mask(s, e.Text)
		}
		return "🤖 <b>Claude</b>" + meta, d.mask(s, e.Text)
	case transcript.Peer:
		if badge != "" {
			return "📨 " + badge + d.peerEntryTitlePlain(e) + meta, d.mask(s, e.Text)
		}
		return d.peerEntryTitle(e) + meta, d.mask(s, e.Text)
	case transcript.Note:
		if e.Cmd {
			headLine := "⚙️ " + badge + htmlEscape(d.mask(s, e.Text)) + meta
			if res := strings.TrimSpace(d.mask(s, e.Result)); res != "" {
				if r := []rune(res); len(r) > 1200 {
					res = string(r[:1199]) + "…"
				}
				return headLine, headLine + "\n<blockquote expandable>" + htmlEscape(res) + "</blockquote>"
			}
			return headLine, ""
		}
		return "⏹ " + badge + htmlEscape(d.mask(s, e.Text)) + meta, ""
	case transcript.Tool:
		return "", d.toolHTMLAt(s, e, badge, meta)
	default:
		return htmlEscape(d.mask(s, e.Text)), ""
	}
}

func shortPeerID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (d *Daemon) peerEntryTitlePlain(e transcript.Entry) string {
	if e.Subagent {
		switch {
		case e.PeerName != "" && e.PeerFrom != "":
			return fmt.Sprintf("Subagent %s (%s)", htmlEscape(e.PeerName), htmlEscape(shortPeerID(e.PeerFrom)))
		case e.PeerName != "":
			return "Subagent " + htmlEscape(e.PeerName)
		case e.PeerFrom != "":
			return "Subagent " + htmlEscape(shortPeerID(e.PeerFrom))
		default:
			return "Subagent"
		}
	}
	if e.PeerPID > 0 {
		for _, p := range d.st.Sessions {
			if p.PID == e.PeerPID {
				return fmt.Sprintf("From session %d (%s)", p.Num, htmlEscape(p.label()))
			}
		}
	}
	if e.PeerName != "" {
		for _, p := range d.st.Sessions {
			if strings.EqualFold(p.Name, e.PeerName) || strings.EqualFold(p.label(), e.PeerName) {
				return fmt.Sprintf("From session %d (%s)", p.Num, htmlEscape(p.label()))
			}
		}
		return "From session " + htmlEscape(e.PeerName)
	}
	return d.peerFromTitlePlain(e.PeerPID)
}

func (d *Daemon) peerEntryTitle(e transcript.Entry) string {
	return "📨 <b>" + d.peerEntryTitlePlain(e) + "</b>"
}

func (d *Daemon) peerFromTitlePlain(pid int) string {
	if pid > 0 {
		for _, p := range d.st.Sessions {
			if p.PID == pid {
				return fmt.Sprintf("From session %d (%s)", p.Num, htmlEscape(p.label()))
			}
		}
	}
	return "From another session"
}

func (d *Daemon) peerFromTitle(pid int) string {
	return "📨 <b>" + d.peerFromTitlePlain(pid) + "</b>"
}

func (d *Daemon) peerToTitlePlain(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return "To another session"
	}
	for _, p := range d.st.Sessions {
		if p.ID == target || strings.EqualFold(p.Name, target) || strings.EqualFold(p.label(), target) || strconv.Itoa(p.Num) == target || (p.PID > 0 && strconv.Itoa(p.PID) == target) {
			return fmt.Sprintf("To session %d (%s)", p.Num, htmlEscape(p.label()))
		}
	}
	return "To " + htmlEscape(target)
}

func (d *Daemon) peerToTitle(target string) string {
	return "📤 <b>" + d.peerToTitlePlain(target) + "</b>"
}

func (d *Daemon) toolHTML(s *session, e transcript.Entry) string {
	badge := ""
	if s != nil && s.tag() != "" {
		badge = "<b>" + htmlEscape(s.tag()) + "</b> · "
	}
	meta := ""
	if !e.Time.IsZero() {
		meta = " · " + htmlEscape(d.clock(e.Time))
	}
	return d.toolHTMLAt(s, e, badge, meta)
}

func (d *Daemon) toolHTMLAt(s *session, e transcript.Entry, badge, meta string) string {
	if e.ToolName == "SendMessage" {
		head := d.peerToTitle(e.Desc) + meta
		if badge != "" {
			head = "📤 " + badge + d.peerToTitlePlain(e.Desc) + meta
		}
		var b strings.Builder
		b.WriteString(head)
		for _, a := range e.Args {
			fmt.Fprintf(&b, "\n<b>%s:</b> <code>%s</code>", htmlEscape(a.Name), htmlEscape(oneLine(d.mask(s, a.Value))))
		}
		b.WriteString("\n" + htmlBody(d.mask(s, e.Text)))
		return b.String()
	}
	name := e.ToolName
	if name == "" {
		name = "tool"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🔧 %s<b>%s</b>", badge, htmlEscape(name))
	if e.Desc != "" {
		b.WriteString(" · " + htmlEscape(d.mask(s, e.Desc)))
	}
	b.WriteString(meta)
	if e.Command != "" {
		cmd := formatBashCommand(d.mask(s, e.Command))
		if r := []rune(cmd); len(r) > 1500 {
			cmd = string(r[:1499]) + "…"
		}
		b.WriteString("\n<b>Command:</b>\n" + codeBlockHTML("bash", cmd))
	}
	if e.Path != "" {
		b.WriteString("\n<b>Path:</b> <code>" + htmlEscape(d.mask(s, e.Path)) + "</code>")
		if e.Plus != 0 || e.Minus != 0 {
			fmt.Fprintf(&b, "\n<b>Change:</b> +%d −%d", e.Plus, e.Minus)
		}
	}
	for _, a := range e.Args {
		val := strings.TrimSpace(d.mask(s, a.Value))
		if val == "" {
			continue
		}
		if a.Block || strings.Contains(val, "\n") || len([]rune(val)) > 120 {
			if r := []rune(val); len(r) > 1200 {
				val = string(r[:1199]) + "…"
			}
			fmt.Fprintf(&b, "\n<b>%s:</b>\n<blockquote expandable>%s</blockquote>", htmlEscape(a.Name), htmlEscape(val))
		} else {
			fmt.Fprintf(&b, "\n<b>%s:</b> <code>%s</code>", htmlEscape(a.Name), htmlEscape(oneLine(val)))
		}
	}
	mark := "…"
	switch e.Status {
	case transcript.OK:
		mark = "✓"
	case transcript.Failed:
		mark = "✗"
	}
	b.WriteString("\n<b>Result:</b> " + mark)
	if e.Dur >= 100*time.Millisecond {
		b.WriteString(" · " + shortSpan(e.Dur))
	}
	if diffStr := strings.TrimSpace(d.mask(s, e.Diff)); diffStr != "" {
		truncated := false
		if r := []rune(diffStr); len(r) > 2200 {
			diffStr = strings.TrimRight(string(r[:2200]), "\n")
			truncated = true
		}
		b.WriteString("\n" + codeBlockHTML("diff", diffStr))
		if truncated {
			b.WriteString("\n<i>… (tap 🧾 below for full diff)</i>")
		}
	}
	out := strings.TrimSpace(d.mask(s, e.Output))
	if out != "" && !(e.Diff != "" && e.Status == transcript.OK && diffable(e.ToolName)) {
		if r := []rune(out); len(r) > 1200 {
			out = string(r[:1199]) + "…"
		}
		b.WriteString("\n<blockquote expandable>" + htmlEscape(out) + "</blockquote>")
	}
	return b.String()
}

func shortSpan(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func (d *Daemon) fromPhone(s *session, text string) bool {
	if s == nil || d.phoneText == nil {
		return false
	}
	text = strings.TrimSpace(text)
	for _, t := range d.phoneText[s.ID] {
		if t == text {
			return true
		}
	}
	return false
}

func (d *Daemon) notePhone(id, text string) {
	text = strings.TrimSpace(text)
	if id == "" || text == "" {
		return
	}
	if d.phoneText == nil {
		d.phoneText = map[string][]string{}
	}
	list := append(d.phoneText[id], text)
	if len(list) > 20 {
		list = list[len(list)-20:]
	}
	d.phoneText[id] = list
}
