package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/diff"
)

// EntryKind is what an entry of the conversation is.
type EntryKind int

const (
	User      EntryKind = iota + 1 // a prompt the owner typed or sent, or a slash command
	Assistant                      // a block of the model's text
	Tool                           // a tool call, with its result once known
	Peer                           // a message from another session (no user authority)
	Note                           // an interruption
)

type Status int

const (
	Running Status = iota
	OK
	Failed
)

// ToolArg is a named argument and value shown on a tool call card.
type ToolArg struct {
	Name  string
	Value string
	Block bool // multi-line or long block (like Agent prompt)
}

// Entry is one line of the conversation as `Last N` shows it (Q6, Q56).
type Entry struct {
	Kind       EntryKind
	Text       string // User, Assistant, Peer, Note; for Tool, its one-line summary
	ToolID     string
	ToolName   string    // Bash, Edit, …
	Desc       string    // short label after the tool name
	Command    string    // Bash command
	Path       string    // Edit, Write, Read, Grep, Glob path
	Plus       int       // lines added
	Minus      int       // lines removed
	Diff       string    // unified diff for Edit, MultiEdit, Write, or Bash bashEditDiff
	Args       []ToolArg // structured named tool arguments
	Output     string    // tool output, when there is some
	Dur        time.Duration
	Cmd        bool   // a slash command, shown as ⚙️
	Term       bool   // a terminal ! command or output, shown as 🖥 Terminal
	Result     string // a slash command's, terminal command's, or task notification's body
	Via        string // an "[answer to N]" item number, when the prompt is one
	PeerPID    int    // a peer message's verified sender PID, when present
	PeerFrom   string // peer origin.from (e.g. subagent ID or uds socket)
	PeerName   string // peer origin.name (e.g. "general-purpose" or session name)
	Subagent   bool   // true when the peer message is a subagent hand-back
	TaskID     string // <task-id> for task-notification entries
	Status     Status // Tool
	Time       time.Time
	Raw        string // raw transcript JSONL line(s) that produced this entry
	resolve    bool   // a tool result, applied to an earlier Tool entry
	attach     bool   // stdout for the slash command before it
	attachTerm bool   // stdout/stderr for the terminal command before it
}

// IsMessage reports whether the entry counts towards `Last N`: user and
// assistant text, and messages from other sessions.
func (e Entry) IsMessage() bool { return e.Kind == User || e.Kind == Assistant || e.Kind == Peer }

type originInfo struct {
	Kind               string `json:"kind"`
	From               string `json:"from"`
	Name               string `json:"name"`
	Handback           bool   `json:"handback"`
	HandbackReportText string `json:"handbackReportText"`
	Body               string `json:"body"`
	VerifiedPeerPID    int    `json:"verifiedPeerPid"`
}

type entryLine struct {
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Timestamp   string          `json:"timestamp"`
	Origin      originInfo      `json:"origin"`
	Result      json.RawMessage `json:"toolUseResult"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Attachment struct {
		Type   string     `json:"type"`
		Mode   string     `json:"commandMode"`
		Prompt string     `json:"prompt"`
		IsMeta bool       `json:"isMeta"`
		Origin originInfo `json:"origin"`
	} `json:"attachment"`
	Subtype string `json:"subtype"`
	Body    string `json:"content"` // system lines; a user line's text is Message.Content
}

var (
	commandNameRE    = regexp.MustCompile(`(?s)<command-name>\s*(.*?)\s*</command-name>`)
	commandArgsRE    = regexp.MustCompile(`(?s)<command-args>\s*(.*?)\s*</command-args>`)
	commandMsgRE     = regexp.MustCompile(`(?s)<command-message>\s*(.*?)\s*</command-message>`)
	stdoutRE         = regexp.MustCompile(`(?s)<local-command-stdout>(.*?)</local-command-stdout>`)
	stderrRE         = regexp.MustCompile(`(?s)<local-command-stderr>(.*?)</local-command-stderr>`)
	bashInputRE      = regexp.MustCompile(`(?s)<bash-input>(.*?)</bash-input>`)
	bashStdoutRE     = regexp.MustCompile(`(?s)<bash-stdout>(.*?)</bash-stdout>`)
	bashStderrRE     = regexp.MustCompile(`(?s)<bash-stderr>(.*?)</bash-stderr>`)
	internalTagRE    = regexp.MustCompile(`(?s)<(?:local-command-caveat|user-prompt-submit-hook|ide_opened_file|ide_selection|available-deferred-tools|fast_mode_info|claude_background_info|system-reminder)\b[^>]*>.*?</(?:local-command-caveat|user-prompt-submit-hook|ide_opened_file|ide_selection|available-deferred-tools|fast_mode_info|claude_background_info|system-reminder)>`)
	crossSessionRE   = regexp.MustCompile(`(?s)<cross-session-message\b([^>]*)>\s*(.*?)\s*</cross-session-message>`)
	agentMessageRE   = regexp.MustCompile(`(?s)<agent-message\b([^>]*)>\s*(.*?)\s*</agent-message>`)
	fromAttrRE       = regexp.MustCompile(`\bfrom="([^"]+)"`)
	fromNameAttrRE   = regexp.MustCompile(`\bfrom-name="([^"]+)"`)
	taskStatusRE     = regexp.MustCompile(`(?s)<status>\s*(.*?)\s*</status>`)
	taskSummaryRE    = regexp.MustCompile(`(?s)<summary>\s*(.*?)\s*</summary>`)
	taskNoteRE       = regexp.MustCompile(`(?s)<note>\s*(.*?)\s*</note>`)
	taskResultRE     = regexp.MustCompile(`(?s)<result>\s*(.*?)\s*</result>`)
	taskTokensRE     = regexp.MustCompile(`<(?:subagent_tokens|total_tokens)>\s*(\d+)\s*</(?:subagent_tokens|total_tokens)>`)
	taskToolUsesRE   = regexp.MustCompile(`<tool_uses>\s*(\d+)\s*</tool_uses>`)
	taskDurationMsRE = regexp.MustCompile(`<duration_ms>\s*(\d+)\s*</duration_ms>`)
	answerToRE       = regexp.MustCompile(`^\[answer to ([^]]+)\]`)
	tagRE            = regexp.MustCompile(`<[^>]+>`)
)

func cleanTags(s string) string {
	return strings.Join(strings.Fields(tagRE.ReplaceAllString(s, " ")), " ")
}

func extractLocalOutput(s string) string {
	var parts []string
	for _, re := range []*regexp.Regexp{stdoutRE, stderrRE} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if t := strings.TrimSpace(m[1]); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func extractBashOutput(s string) string {
	var parts []string
	for _, re := range []*regexp.Regexp{bashStdoutRE, bashStderrRE} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if t := strings.TrimSpace(m[1]); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func parseCommand(s string, at time.Time) (Entry, bool) {
	m := commandNameRE.FindStringSubmatch(s)
	if m == nil {
		if mm := commandMsgRE.FindStringSubmatch(s); mm != nil && !strings.Contains(s, "<command-name>") {
			cmd := strings.TrimSpace(mm[1])
			if cmd == "" {
				return Entry{}, false
			}
			return Entry{Kind: Note, Cmd: true, Text: cmd, Result: extractLocalOutput(s), Time: at}, true
		}
		return Entry{}, false
	}
	cmd := strings.TrimSpace(m[1])
	if cmd != "" && !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	if am := commandArgsRE.FindStringSubmatch(s); am != nil {
		if args := strings.TrimSpace(am[1]); args != "" {
			cmd = strings.TrimSpace(cmd + " " + args)
		}
	}
	return Entry{Kind: User, Cmd: true, Text: cmd, Result: extractLocalOutput(s), Time: at}, true
}

func parseTaskNotification(s string, at time.Time) (Entry, bool) {
	s = strings.TrimSpace(internalTagRE.ReplaceAllString(s, ""))
	if s == "" {
		return Entry{}, false
	}
	var taskID, status, summary, note, result string
	if m := taskIDRE.FindStringSubmatch(s); m != nil {
		taskID = strings.TrimSpace(m[1])
	}
	if m := taskStatusRE.FindStringSubmatch(s); m != nil {
		status = strings.TrimSpace(m[1])
	}
	if m := taskSummaryRE.FindStringSubmatch(s); m != nil {
		summary = strings.TrimSpace(m[1])
	}
	if m := taskNoteRE.FindStringSubmatch(s); m != nil {
		note = strings.TrimSpace(m[1])
	}
	if m := taskResultRE.FindStringSubmatch(s); m != nil {
		result = strings.TrimSpace(m[1])
	}
	var tokens, toolUses, durMs int
	if m := taskTokensRE.FindStringSubmatch(s); m != nil {
		tokens, _ = strconv.Atoi(m[1])
	}
	if m := taskToolUsesRE.FindStringSubmatch(s); m != nil {
		toolUses, _ = strconv.Atoi(m[1])
	}
	if m := taskDurationMsRE.FindStringSubmatch(s); m != nil {
		durMs, _ = strconv.Atoi(m[1])
	}

	var meta []string
	switch strings.ToLower(status) {
	case "":
	case "completed", "success", "succeeded", "ok":
		meta = append(meta, "✓ "+status)
	case "failed", "error":
		meta = append(meta, "✗ "+status)
	case "stopped", "killed", "cancelled", "canceled":
		meta = append(meta, "⏹ "+status)
	default:
		meta = append(meta, status)
	}
	if tokens > 0 {
		meta = append(meta, formatTokenCount(tokens))
	}
	if toolUses > 0 {
		if toolUses == 1 {
			meta = append(meta, "1 tool")
		} else {
			meta = append(meta, fmt.Sprintf("%d tools", toolUses))
		}
	}
	if durMs > 0 {
		meta = append(meta, formatMillis(durMs))
	}

	headline := summary
	if headline == "" && len(meta) == 0 && result == "" && note == "" {
		headline = cleanTags(s)
		if headline == "" {
			return Entry{}, false
		}
		return Entry{Kind: Note, Cmd: true, TaskID: taskID, Text: headline, Time: at}, true
	}
	if headline == "" {
		if taskID != "" {
			headline = "Task " + shortID(taskID)
		} else {
			headline = "Task"
		}
	}
	if len(meta) > 0 {
		headline += " (" + strings.Join(meta, " · ") + ")"
	}
	var bodyParts []string
	if note != "" && !strings.HasPrefix(note, "A task-notification fires each time this agent stops") {
		bodyParts = append(bodyParts, note)
	}
	if result != "" {
		bodyParts = append(bodyParts, result)
	}
	return Entry{
		Kind:   Note,
		Cmd:    true,
		TaskID: taskID,
		Text:   headline,
		Result: strings.Join(bodyParts, "\n\n"),
		Time:   at,
	}, true
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func formatTokenCount(n int) string {
	if n >= 1_000_000 {
		v := fmt.Sprintf("%.1f", float64(n)/1_000_000)
		return strings.TrimSuffix(v, ".0") + "M tok"
	}
	if n >= 1000 {
		v := fmt.Sprintf("%.1f", float64(n)/1000)
		return strings.TrimSuffix(v, ".0") + "k tok"
	}
	return fmt.Sprintf("%d tok", n)
}

func formatMillis(ms int) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return fmt.Sprintf("%dms", ms)
	}
	if d < time.Minute {
		s := fmt.Sprintf("%.1f", d.Seconds())
		return strings.TrimSuffix(s, ".0") + "s"
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	if mins >= 60 {
		hrs := mins / 60
		mins = mins % 60
		if mins == 0 {
			return fmt.Sprintf("%dh", hrs)
		}
		return fmt.Sprintf("%dh %dm", hrs, mins)
	}
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm %ds", mins, secs)
}

func parsePeer(text string, orig originInfo, at time.Time) Entry {
	from := strings.TrimSpace(orig.From)
	name := strings.TrimSpace(orig.Name)
	subagent := orig.Handback
	if m := crossSessionRE.FindStringSubmatch(text); m != nil {
		if from == "" || from == "unknown" {
			if fm := fromAttrRE.FindStringSubmatch(m[1]); fm != nil {
				from = fm[1]
			}
		}
		if name == "" {
			if nm := fromNameAttrRE.FindStringSubmatch(m[1]); nm != nil {
				name = nm[1]
			}
		}
	}
	if m := agentMessageRE.FindStringSubmatch(text); m != nil {
		subagent = true
		if from == "" || from == "unknown" {
			if fm := fromAttrRE.FindStringSubmatch(m[1]); fm != nil {
				from = fm[1]
			}
		}
	}
	if strings.Contains(text, "[Subagent hand-back]") {
		subagent = true
	}
	if !subagent && from != "" && from != "unknown" && !strings.HasPrefix(from, "uds:") && orig.VerifiedPeerPID == 0 {
		subagent = true
	}
	var body string
	switch {
	case strings.TrimSpace(orig.HandbackReportText) != "":
		body = strings.TrimSpace(orig.HandbackReportText)
	case strings.TrimSpace(orig.Body) != "":
		body = strings.TrimSpace(orig.Body)
	default:
		body = peerText(text)
	}
	return Entry{
		Kind:     Peer,
		Text:     body,
		PeerPID:  orig.VerifiedPeerPID,
		PeerFrom: from,
		PeerName: name,
		Subagent: subagent,
		Time:     at,
	}
}

func classifyUserText(text string, isMeta bool, orig originInfo, at time.Time) []Entry {
	if (isMeta && orig.Kind == "peer") || orig.Kind == "peer" || strings.Contains(text, "<cross-session-message") || strings.Contains(text, "<agent-message") {
		if e := parsePeer(text, orig, at); e.Text != "" {
			return []Entry{e}
		}
		return nil
	}
	if strings.Contains(text, "<task-notification>") || strings.Contains(text, "<bash-notification>") || orig.Kind == "task-notification" {
		if e, ok := parseTaskNotification(text, at); ok {
			return []Entry{e}
		}
		return nil
	}
	if isMeta {
		return nil
	}
	text = strings.TrimSpace(internalTagRE.ReplaceAllString(text, ""))
	if text == "" {
		return nil
	}
	if m := bashInputRE.FindStringSubmatch(text); m != nil {
		return []Entry{{Kind: User, Term: true, Text: strings.TrimSpace(m[1]), Result: extractBashOutput(text), Time: at}}
	}
	if strings.Contains(text, "<bash-stdout>") || strings.Contains(text, "<bash-stderr>") {
		out := extractBashOutput(text)
		return []Entry{{Kind: User, Term: true, attachTerm: true, Text: out, Result: out, Time: at}}
	}
	if e, ok := parseCommand(text, at); ok {
		return []Entry{e}
	}
	if strings.Contains(text, "<local-command-stdout>") || strings.Contains(text, "<local-command-stderr>") {
		return []Entry{{attach: true, Text: extractLocalOutput(text), Time: at}}
	}
	if strings.HasPrefix(text, "<local-command-") {
		return nil
	}
	if strings.HasPrefix(text, "<command-message>") {
		cleaned := cleanTags(text)
		if cleaned == "" {
			return nil
		}
		return []Entry{{Kind: Note, Cmd: true, Text: cleaned, Time: at}}
	}
	return []Entry{{Kind: User, Text: text, Via: answerVia(text), Time: at}}
}

// entriesOf reads the conversation entries in one transcript line.
func entriesOf(raw []byte) []Entry {
	es := entriesOfRaw(raw)
	rawStr := strings.TrimSpace(string(raw))
	for i := range es {
		es[i].Raw = rawStr
	}
	return es
}

func entriesOfRaw(raw []byte) []Entry {
	var l entryLine
	if json.Unmarshal(raw, &l) != nil || l.IsSidechain {
		return nil
	}
	at, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
	switch l.Type {
	case "system":
		if l.Subtype != "local_command" {
			return nil
		}
		if e, ok := parseCommand(l.Body, at); ok {
			return []Entry{e}
		}
		if strings.Contains(l.Body, "<local-command-stdout>") || strings.Contains(l.Body, "<local-command-stderr>") {
			return []Entry{{attach: true, Text: extractLocalOutput(l.Body), Time: at}}
		}
		return nil
	case "attachment":
		// Text typed or posted while a turn ran, absorbed into it (step 1).
		if l.Attachment.Type == "queued_command" && l.Attachment.Prompt != "" {
			if l.Attachment.Mode == "task-notification" || strings.Contains(l.Attachment.Prompt, "<task-notification>") || strings.Contains(l.Attachment.Prompt, "<bash-notification>") {
				if e, ok := parseTaskNotification(l.Attachment.Prompt, at); ok {
					return []Entry{e}
				}
				return nil
			}
			if l.Attachment.Origin.Kind == "peer" || l.Attachment.IsMeta || strings.Contains(l.Attachment.Prompt, "<cross-session-message") || strings.Contains(l.Attachment.Prompt, "<agent-message") {
				if e := parsePeer(l.Attachment.Prompt, l.Attachment.Origin, at); e.Text != "" {
					return []Entry{e}
				}
				return nil
			}
			return classifyUserText(l.Attachment.Prompt, false, l.Attachment.Origin, at)
		}
		return nil
	case "user", "assistant":
	default:
		return nil
	}
	var text string
	if json.Unmarshal(l.Message.Content, &text) == nil {
		if l.Type != "user" {
			return nil
		}
		return classifyUserText(text, l.IsMeta, l.Origin, at)
	}
	var blocks []block
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return nil
	}
	var out []Entry
	for _, b := range blocks {
		switch {
		case l.Type == "assistant" && b.Type == "text" && strings.TrimSpace(b.Text) != "":
			out = append(out, Entry{Kind: Assistant, Text: strings.TrimSpace(b.Text), Time: at})
		case l.Type == "assistant" && b.Type == "tool_use":
			desc, cmd, path, diffText, plus, minus, args := toolFields(b.Name, b.Input)
			text := ToolSummary(b.Name, b.Input)
			if b.Name == "SendMessage" {
				if msg := sendMessageBody(b.Input); msg != "" {
					text = msg
				}
			}
			out = append(out, Entry{
				Kind: Tool, Text: text, ToolID: b.ID, ToolName: b.Name,
				Desc: desc, Command: cmd, Path: path, Plus: plus, Minus: minus,
				Diff: diffText, Args: args, Time: at,
			})
		case l.Type == "user" && b.Type == "tool_result":
			st := OK
			if b.IsError {
				st = Failed
			}
			res := Entry{Kind: Tool, ToolID: b.ToolUseID, Status: st, Output: resultText(b.Content), Time: at, resolve: true}
			if rDiff, rPath, rPlus, rMinus, ok := diff.OfResult(l.Result); ok {
				res.Diff = rDiff
				res.Path = rPath
				res.Plus = rPlus
				res.Minus = rMinus
			}
			out = append(out, res)
		case l.Type == "user" && b.Type == "text" && strings.HasPrefix(b.Text, "[Request interrupted by user"):
			out = append(out, Entry{Kind: Note, Text: "interrupted", Time: at})
		case l.Type == "user" && b.Type == "text" && strings.TrimSpace(b.Text) != "":
			out = append(out, classifyUserText(b.Text, l.IsMeta, l.Origin, at)...)
		}
	}
	return out
}

// peerText takes the message out of Claude Code's wrapper: "Another Claude
// session sent a message:\n<message>\n\nThis came from another Claude
// session — …", and strips <cross-session-message> / <agent-message> tags
// and subagent hand-back boilerplate.
func peerText(s string) string {
	s = strings.TrimSpace(internalTagRE.ReplaceAllString(s, ""))
	rest := s
	if first, after, ok := strings.Cut(s, "\n"); ok && strings.HasPrefix(strings.TrimSpace(first), "Another Claude session sent a message") {
		rest = after
	}
	for _, footer := range []string{
		"\n\nThis came from another Claude session",
		"\n\nThat \"other Claude session\" is an agent working inside this same session",
	} {
		if i := strings.LastIndex(rest, footer); i >= 0 {
			rest = rest[:i]
		}
	}
	rest = strings.TrimSpace(rest)
	if m := crossSessionRE.FindStringSubmatch(rest); m != nil {
		rest = strings.TrimSpace(m[2])
	} else if m := agentMessageRE.FindStringSubmatch(rest); m != nil {
		rest = strings.TrimSpace(m[2])
	}
	if strings.HasPrefix(rest, "[Subagent hand-back]") {
		if _, after, ok := strings.Cut(rest, "The report follows:\n"); ok {
			rest = unindent2(after)
		} else if _, after, ok := strings.Cut(rest, "\n"); ok {
			rest = unindent2(after)
		}
	}
	return strings.TrimSpace(rest)
}

func unindent2(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, "  ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func joinRaw(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "" || a == b:
		return a
	default:
		return a + "\n" + b
	}
}

func applyToolResolve(target *Entry, res Entry) {
	target.Status = res.Status
	target.Output = res.Output
	if (target.ToolName == "Agent" || target.ToolName == "Task") && isAgentLaunchBoilerplate(target.Output) {
		target.Output = ""
	}
	if res.Diff != "" {
		if target.Diff == "" {
			target.Diff = res.Diff
		}
		if target.Path == "" {
			target.Path = res.Path
		}
		if target.Plus == 0 && target.Minus == 0 {
			target.Plus = res.Plus
			target.Minus = res.Minus
		}
	}
	target.Raw = joinRaw(target.Raw, res.Raw)
	if !res.Time.IsZero() && !target.Time.IsZero() && res.Time.After(target.Time) {
		target.Dur = res.Time.Sub(target.Time)
	}
}

func isAgentLaunchBoilerplate(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "Async agent launched successfully.") ||
		strings.Contains(s, "This tool result is internal metadata")
}

// Entries reads the conversation entries in transcript lines, with each tool
// call's result applied to it.
func Entries(lines [][]byte) []Entry {
	var out []Entry
	index := map[string]int{}
	seenTasks := map[string]bool{}
	peerReports := map[string]string{}
	for _, raw := range lines {
		for _, e := range entriesOf(raw) {
			if e.attach {
				if n := len(out); n > 0 && out[n-1].Cmd && out[n-1].Result == "" {
					out[n-1].Result = e.Text
					out[n-1].Raw = joinRaw(out[n-1].Raw, e.Raw)
					continue
				}
				if e.Text != "" {
					out = append(out, Entry{Kind: User, Cmd: true, Text: e.Text, Time: e.Time, Raw: e.Raw})
				}
				continue
			}
			if e.attachTerm {
				if n := len(out); n > 0 && out[n-1].Term && out[n-1].Result == "" {
					out[n-1].Result = e.Result
					out[n-1].Raw = joinRaw(out[n-1].Raw, e.Raw)
					continue
				}
				if e.Result != "" {
					out = append(out, Entry{Kind: User, Term: true, Result: e.Result, Time: e.Time, Raw: e.Raw})
				}
				continue
			}
			if e.resolve {
				if i, ok := index[e.ToolID]; ok {
					applyToolResolve(&out[i], e)
				}
				continue
			}
			if e.Kind == Peer {
				if n := len(out); n > 0 && out[n-1].Kind == Peer && out[n-1].Text == e.Text && out[n-1].PeerFrom == e.PeerFrom {
					continue
				}
				if e.PeerFrom != "" {
					peerReports[e.PeerFrom] = e.Text
				}
			}
			if e.Kind == Note && e.Cmd {
				if e.TaskID != "" {
					key := e.TaskID + "\x00" + e.Text
					if seenTasks[key] {
						continue
					}
					seenTasks[key] = true
					if prev, ok := peerReports[e.TaskID]; ok && (prev == e.Result || strings.Contains(e.Result, prev)) {
						e.Result = ""
					}
				} else if n := len(out); n > 0 && out[n-1].Kind == Note && out[n-1].Cmd && out[n-1].Text == e.Text {
					continue
				}
			}
			if e.Kind == Tool {
				index[e.ToolID] = len(out)
			}
			out = append(out, e)
		}
	}
	return out
}

// Last returns the entries holding the last n messages of the transcript at
// path, tool calls between them included. It reads from the end, more as
// needed: transcripts grow to many megabytes.
func Last(path string, n int) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	for chunk := int64(1 << 20); ; chunk *= 4 {
		start := max(0, size-chunk)
		buf := make([]byte, size-start)
		if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if start > 0 {
			// Drop the line the chunk starts inside.
			if i := bytes.IndexByte(buf, '\n'); i >= 0 {
				buf = buf[i+1:]
			}
		}
		entries := Entries(bytes.Split(buf, []byte("\n")))
		if start == 0 || countMessages(entries) > n {
			return lastMessages(entries, n), nil
		}
	}
}

func countMessages(entries []Entry) int {
	n := 0
	for _, e := range entries {
		if e.IsMessage() {
			n++
		}
	}
	return n
}

// lastMessages cuts entries to start at the n-th message from the end.
func lastMessages(entries []Entry, n int) []Entry {
	seen := 0
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].IsMessage() {
			if seen++; seen == n {
				return entries[i:]
			}
		}
	}
	return entries
}

// ToolSummary is a tool call in a few words: "ran make test-unit" (Q6).
func ToolSummary(name string, input json.RawMessage) string {
	var in struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		FilePath    string `json:"file_path"`
		Notebook    string `json:"notebook_path"`
		Path        string `json:"path"`
		Pattern     string `json:"pattern"`
		URL         string `json:"url"`
		Query       string `json:"query"`
		Prompt      string `json:"prompt"`
		Plan        string `json:"plan"`
		Questions   []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	json.Unmarshal(input, &in)
	base := func(p string) string { return filepath.Base(p) }
	switch name {
	case "Bash":
		first, _, _ := strings.Cut(strings.TrimSpace(in.Command), "\n")
		return "ran " + clip(first, 80)
	case "Monitor":
		if in.Description != "" {
			return "monitored " + clip(in.Description, 60)
		}
		first, _, _ := strings.Cut(strings.TrimSpace(in.Command), "\n")
		if first != "" {
			return "monitored " + clip(first, 80)
		}
		return "started a monitor"
	case "Read":
		return "read " + base(in.FilePath)
	case "Write":
		return "wrote " + base(in.FilePath)
	case "Edit", "MultiEdit":
		return "edited " + base(in.FilePath)
	case "NotebookEdit":
		return "edited " + base(in.Notebook)
	case "Grep":
		return "searched for " + clip(in.Pattern, 60)
	case "Glob":
		return "listed " + clip(in.Pattern, 60)
	case "WebFetch":
		return "fetched " + clip(in.URL, 80)
	case "WebSearch":
		return "searched the web for " + clip(in.Query, 60)
	case "AskUserQuestion":
		if len(in.Questions) > 0 {
			return "asked: " + clip(in.Questions[0].Question, 80)
		}
		return "asked a question"
	case "ExitPlanMode":
		return "presented a plan"
	case "Task", "Agent":
		return "ran an agent: " + clip(in.Description, 60)
	}
	if server, tool, ok := strings.Cut(strings.TrimPrefix(name, "mcp__"), "__"); ok && strings.HasPrefix(name, "mcp__") {
		return "used " + server + " · " + tool
	}
	return "used " + name
}

func answerVia(text string) string {
	m := answerToRE.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return ""
	}
	return m[1]
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// toolFields pulls the bits 🧰 Full shows past the one-line summary (Q56).
func toolFields(name string, input json.RawMessage) (desc, command, path, diffText string, plus, minus int, args []ToolArg) {
	var in struct {
		Command         string `json:"command"`
		Description     string `json:"description"`
		FilePath        string `json:"file_path"`
		Notebook        string `json:"notebook_path"`
		Path            string `json:"path"`
		Pattern         string `json:"pattern"`
		Glob            string `json:"glob"`
		Type            string `json:"type"`
		OutputMode      string `json:"output_mode"`
		HeadLimit       int    `json:"head_limit"`
		Offset          int    `json:"offset"`
		Limit           int    `json:"limit"`
		Pages           string `json:"pages"`
		OldString       string `json:"old_string"`
		NewString       string `json:"new_string"`
		ReplaceAll      bool   `json:"replace_all"`
		Content         string `json:"content"`
		URL             string `json:"url"`
		Query           string `json:"query"`
		Prompt          string `json:"prompt"`
		SubagentType    string `json:"subagent_type"`
		Model           string `json:"model"`
		Name            string `json:"name"`
		RunInBackground bool   `json:"run_in_background"`
		Timeout         int    `json:"timeout"`
		TimeoutMs       int    `json:"timeout_ms"`
		Persistent      bool   `json:"persistent"`
		TaskID          string `json:"task_id"`
		To              string `json:"to"`
		Recipient       string `json:"recipient"`
		Summary         string `json:"summary"`
	}
	json.Unmarshal(input, &in)
	desc = strings.TrimSpace(in.Description)
	switch name {
	case "Bash":
		command = strings.TrimSpace(in.Command)
		first, _, _ := strings.Cut(command, "\n")
		if desc == "" {
			desc = clip(first, 60)
		}
		if in.Timeout > 0 {
			args = append(args, ToolArg{Name: "Timeout", Value: formatMillis(in.Timeout)})
		}
		if in.RunInBackground {
			args = append(args, ToolArg{Name: "Background", Value: "true"})
		}
	case "Monitor":
		command = strings.TrimSpace(in.Command)
		first, _, _ := strings.Cut(command, "\n")
		if desc == "" {
			desc = clip(first, 60)
		}
		if in.TimeoutMs > 0 {
			args = append(args, ToolArg{Name: "Timeout", Value: formatMillis(in.TimeoutMs)})
		}
		if in.Persistent {
			args = append(args, ToolArg{Name: "Persistent", Value: "true"})
		}
	case "Write":
		path = in.FilePath
		diffText, _ = diff.OfTool(name, input)
		plus, minus = diff.Stats(diffText)
		if plus == 0 {
			plus = lineCount(in.Content)
		}
		if desc == "" {
			desc = filepath.Base(path)
		}
	case "Edit", "MultiEdit":
		path = in.FilePath
		diffText, _ = diff.OfTool(name, input)
		plus, minus = diff.Stats(diffText)
		if plus == 0 && minus == 0 {
			plus, minus = lineCount(in.NewString), lineCount(in.OldString)
		}
		if desc == "" {
			desc = filepath.Base(path)
		}
		if in.ReplaceAll {
			args = append(args, ToolArg{Name: "ReplaceAll", Value: "true"})
		}
	case "Read":
		path = in.FilePath
		if desc == "" && path != "" {
			desc = filepath.Base(path)
		}
		if in.Offset > 0 {
			args = append(args, ToolArg{Name: "Offset", Value: strconv.Itoa(in.Offset)})
		}
		if in.Limit > 0 {
			args = append(args, ToolArg{Name: "Limit", Value: strconv.Itoa(in.Limit)})
		}
		if in.Pages != "" {
			args = append(args, ToolArg{Name: "Pages", Value: in.Pages})
		}
	case "Grep":
		path = in.Path
		if desc == "" && in.Pattern != "" {
			desc = clip(in.Pattern, 60)
		}
		if in.Pattern != "" {
			args = append(args, ToolArg{Name: "Pattern", Value: in.Pattern})
		}
		if in.Glob != "" {
			args = append(args, ToolArg{Name: "Glob", Value: in.Glob})
		}
		if in.Type != "" {
			args = append(args, ToolArg{Name: "Type", Value: in.Type})
		}
		if in.OutputMode != "" {
			args = append(args, ToolArg{Name: "Mode", Value: in.OutputMode})
		}
		if in.HeadLimit > 0 {
			args = append(args, ToolArg{Name: "Limit", Value: strconv.Itoa(in.HeadLimit)})
		}
	case "Glob":
		path = in.Path
		if desc == "" && in.Pattern != "" {
			desc = clip(in.Pattern, 60)
		}
		if in.Pattern != "" {
			args = append(args, ToolArg{Name: "Pattern", Value: in.Pattern})
		}
	case "WebFetch":
		if desc == "" && in.URL != "" {
			desc = clip(in.URL, 60)
		}
		if in.URL != "" {
			args = append(args, ToolArg{Name: "URL", Value: in.URL})
		}
		if in.Prompt != "" {
			args = append(args, ToolArg{Name: "Prompt", Value: in.Prompt})
		}
	case "WebSearch":
		if desc == "" && in.Query != "" {
			desc = clip(in.Query, 60)
		}
		if in.Query != "" {
			args = append(args, ToolArg{Name: "Query", Value: in.Query})
		}
	case "Task", "Agent":
		if in.SubagentType != "" {
			args = append(args, ToolArg{Name: "Agent", Value: in.SubagentType})
		}
		if in.Model != "" {
			args = append(args, ToolArg{Name: "Model", Value: in.Model})
		}
		if in.RunInBackground {
			args = append(args, ToolArg{Name: "Background", Value: "true"})
		}
		if in.Name != "" {
			args = append(args, ToolArg{Name: "Name", Value: in.Name})
		}
		if p := strings.TrimSpace(in.Prompt); p != "" {
			args = append(args, ToolArg{Name: "Prompt", Value: p, Block: true})
		}
	case "TaskStop", "TaskOutput":
		if in.TaskID != "" {
			args = append(args, ToolArg{Name: "TaskID", Value: in.TaskID})
		}
		if desc == "" {
			desc = ToolSummary(name, input)
		}
	case "SendMessage":
		to := strings.TrimSpace(in.To)
		if to == "" {
			to = strings.TrimSpace(in.Recipient)
		}
		if to != "" {
			desc = to
		}
		if sum := strings.TrimSpace(in.Summary); sum != "" && sum != sendMessageBody(input) {
			args = append(args, ToolArg{Name: "Summary", Value: sum})
		}
	default:
		if desc == "" {
			desc = ToolSummary(name, input)
		}
		args = genericToolArgs(name, input)
	}
	return desc, command, path, diffText, plus, minus, args
}

func genericToolArgs(name string, input json.RawMessage) []ToolArg {
	switch name {
	case "AskUserQuestion", "ExitPlanMode":
		return nil
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil || len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var args []ToolArg
	for _, k := range keys {
		v := m[k]
		if v == nil {
			continue
		}
		var val string
		switch tv := v.(type) {
		case string:
			val = strings.TrimSpace(tv)
		case bool:
			val = strconv.FormatBool(tv)
		case float64:
			if float64(int64(tv)) == tv {
				val = strconv.FormatInt(int64(tv), 10)
			} else {
				val = fmt.Sprintf("%g", tv)
			}
		default:
			b, err := json.Marshal(tv)
			if err != nil {
				continue
			}
			val = string(b)
		}
		if val == "" || val == "[]" || val == "{}" {
			continue
		}
		block := strings.Contains(val, "\n") || len([]rune(val)) > 120
		args = append(args, ToolArg{Name: k, Value: val, Block: block})
	}
	return args
}

func sendMessageBody(input json.RawMessage) string {
	var in struct {
		Message string `json:"message"`
		Content string `json:"content"`
		Summary string `json:"summary"`
	}
	json.Unmarshal(input, &in)
	for _, s := range []string{in.Message, in.Content, in.Summary} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func cleanResultString(s string) string {
	s = internalTagRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "(file state is current in your context — no need to Read it back)", "")
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "completed with no output") {
		return ""
	}
	return s
}

func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return cleanResultString(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil && len(blocks) > 0 {
		var parts []string
		for _, b := range blocks {
			if t := cleanResultString(b.Text); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Stream turns transcript lines into entries as they arrive, for Follow:
// messages at once, a tool call once its result is in, so it shows ✓ or ✗.
type Stream struct {
	pending     map[string]Entry  // tool calls without a result yet, by ID
	seenTasks   map[string]bool   // task-notification keys already emitted
	peerReports map[string]string // subagent hand-back text by task ID
}

// Feed takes the lines written since the last call and returns the entries
// ready to show.
func (s *Stream) Feed(lines [][]byte) []Entry {
	if s.pending == nil {
		s.pending = map[string]Entry{}
	}
	if s.seenTasks == nil {
		s.seenTasks = map[string]bool{}
	}
	if s.peerReports == nil {
		s.peerReports = map[string]string{}
	}
	var out []Entry
	for _, raw := range lines {
		for _, e := range entriesOf(raw) {
			switch {
			case e.attach:
				if n := len(out); n > 0 && out[n-1].Cmd && out[n-1].Result == "" {
					out[n-1].Result = e.Text
					out[n-1].Raw = joinRaw(out[n-1].Raw, e.Raw)
					continue
				}
				if e.Text != "" {
					out = append(out, Entry{Kind: User, Cmd: true, Text: e.Text, Time: e.Time, Raw: e.Raw})
				}
			case e.attachTerm:
				if n := len(out); n > 0 && out[n-1].Term && out[n-1].Result == "" {
					out[n-1].Result = e.Result
					out[n-1].Raw = joinRaw(out[n-1].Raw, e.Raw)
					continue
				}
				if e.Result != "" {
					out = append(out, Entry{Kind: User, Term: true, Result: e.Result, Time: e.Time, Raw: e.Raw})
				}
			case e.resolve:
				if call, ok := s.pending[e.ToolID]; ok {
					delete(s.pending, e.ToolID)
					applyToolResolve(&call, e)
					out = append(out, call)
				}
			case e.Kind == Tool:
				s.pending[e.ToolID] = e
			default:
				if e.Kind == Peer {
					if n := len(out); n > 0 && out[n-1].Kind == Peer && out[n-1].Text == e.Text && out[n-1].PeerFrom == e.PeerFrom {
						continue
					}
					if e.PeerFrom != "" {
						s.peerReports[e.PeerFrom] = e.Text
					}
				}
				if e.Kind == Note && e.Cmd {
					if e.TaskID != "" {
						key := e.TaskID + "\x00" + e.Text
						if s.seenTasks[key] {
							continue
						}
						s.seenTasks[key] = true
						if prev, ok := s.peerReports[e.TaskID]; ok && (prev == e.Result || strings.Contains(e.Result, prev)) {
							e.Result = ""
						}
					} else if n := len(out); n > 0 && out[n-1].Kind == Note && out[n-1].Cmd && out[n-1].Text == e.Text {
						continue
					}
				}
				out = append(out, e)
			}
		}
	}
	return out
}
