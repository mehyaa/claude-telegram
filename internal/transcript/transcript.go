// Package transcript follows a Claude Code session's transcript JSONL and
// reports the events the hooks don't: a dialog cancelled or a permission
// denied (no PostToolUse, no Stop), and a turn cut short by Esc. The line
// formats are the ones captured from Claude Code 2.1.308
// (internal/transcript/testdata).
package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
)

type Kind int

const (
	ToolUse     Kind = iota + 1 // the model called a tool
	ToolResult                  // a tool call ended: ran, failed, or was refused
	Interrupted                 // "[Request interrupted by user …]"
	TurnEnd                     // a turn_duration line
	TaskEnd                     // a <task-notification> for a background task (Q82)
)

type Event struct {
	Kind      Kind
	ToolID    string          // ToolUse, ToolResult
	ToolName  string          // ToolUse
	Input     json.RawMessage // ToolUse
	IsError   bool            // ToolResult
	Rejected  bool            // ToolResult: the user said no (toolDenialKind "user-rejected")
	Answers   map[string]string
	StartID   string // ToolResult: a background task that began
	StartKind string // "shell" or "agent"
	StopID    string // ToolUse of TaskStop: the task it kills
	EndedID   string // TaskEnd
}

// Tail reads a transcript as it grows.
type Tail struct {
	path    string
	off     int64
	partial []byte
}

// NewTail starts following path. With fromEnd, what the file already holds
// is skipped; a file that doesn't exist yet is read from its start once it
// appears.
func NewTail(path string, fromEnd bool) *Tail {
	t := &Tail{path: path}
	if fromEnd {
		if fi, err := os.Stat(path); err == nil {
			t.off = fi.Size()
		}
	}
	return t
}

// NewTailAt resumes following path at offset, as a restarted reader does.
// An offset past the end of the file means it was rewritten: reading starts
// over.
func NewTailAt(path string, offset int64) *Tail {
	t := &Tail{path: path, off: offset}
	if fi, err := os.Stat(path); err == nil && fi.Size() < offset {
		t.off = 0
	}
	return t
}

// Offset is the end of the last complete line read: where a restarted
// reader should resume.
func (t *Tail) Offset() int64 { return t.off - int64(len(t.partial)) }

// Poll returns the events in the lines written since the last call.
func (t *Tail) Poll() ([]Event, error) {
	lines, err := t.PollLines()
	var events []Event
	for _, line := range lines {
		events = append(events, Parse(line)...)
	}
	return events, err
}

// PollLines returns the complete lines written since the last call.
func (t *Tail) PollLines() ([][]byte, error) {
	f, err := os.Open(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < t.off { // rewritten from scratch
		t.off, t.partial = 0, nil
	}
	if fi.Size() == t.off {
		return nil, nil
	}
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		return nil, err
	}
	t.off += int64(len(data))
	data = append(t.partial, data...)
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		t.partial = data
		return nil, nil
	}
	t.partial = append([]byte(nil), data[end+1:]...)
	return bytes.Split(data[:end], []byte("\n")), nil
}

type line struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	IsSidechain bool            `json:"isSidechain"`
	Denial      string          `json:"toolDenialKind"`
	Result      json.RawMessage `json:"toolUseResult"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Attachment struct {
		Type   string `json:"type"`
		Prompt string `json:"prompt"`
		Mode   string `json:"commandMode"`
	} `json:"attachment"`
}

var taskIDRE = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// Parse reads one transcript line. Subagent lines are skipped.
func Parse(raw []byte) []Event {
	var l line
	if json.Unmarshal(raw, &l) != nil || l.IsSidechain {
		return nil
	}
	if l.Type == "system" && l.Subtype == "turn_duration" {
		return []Event{{Kind: TurnEnd}}
	}
	if l.Type == "attachment" && l.Attachment.Type == "queued_command" &&
		(l.Attachment.Mode == "task-notification" || strings.Contains(l.Attachment.Prompt, "<task-notification>")) {
		return taskEvents(l.Attachment.Prompt)
	}
	if l.Type != "user" && l.Type != "assistant" {
		return nil
	}
	var blocks []block
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		var text string
		if json.Unmarshal(l.Message.Content, &text) == nil {
			return taskEvents(text)
		}
		return nil
	}
	var events []Event
	for _, b := range blocks {
		switch {
		case l.Type == "assistant" && b.Type == "tool_use":
			e := Event{Kind: ToolUse, ToolID: b.ID, ToolName: b.Name, Input: b.Input}
			if b.Name == "TaskStop" {
				var in struct {
					TaskID string `json:"task_id"`
				}
				json.Unmarshal(b.Input, &in)
				e.StopID = in.TaskID
			}
			events = append(events, e)
		case l.Type == "user" && b.Type == "tool_result":
			e := Event{Kind: ToolResult, ToolID: b.ToolUseID, IsError: b.IsError, Rejected: l.Denial == "user-rejected"}
			var r struct {
				Answers          map[string]string `json:"answers"`
				BackgroundTaskID string            `json:"backgroundTaskId"`
				MonitorID        string            `json:"monitorId"`
				TaskID           string            `json:"taskId"`
				Type             string            `json:"type"`
				Status           string            `json:"status"`
				AgentID          string            `json:"agentId"`
				Resumed          string            `json:"resumedAgentId"`
				Revived          string            `json:"revivedAgentId"`
			}
			if json.Unmarshal(l.Result, &r) == nil {
				e.Answers = r.Answers
				switch {
				case r.MonitorID != "":
					e.StartID, e.StartKind = r.MonitorID, "monitor"
				case r.BackgroundTaskID != "" && r.Type == "monitor":
					e.StartID, e.StartKind = r.BackgroundTaskID, "monitor"
				case r.BackgroundTaskID != "":
					e.StartID, e.StartKind = r.BackgroundTaskID, "shell"
				case r.TaskID != "" && r.Type == "monitor":
					e.StartID, e.StartKind = r.TaskID, "monitor"
				case r.Status == "async_launched" && r.AgentID != "":
					e.StartID, e.StartKind = r.AgentID, "agent"
				case r.Resumed != "" || r.Revived != "":
					e.StartID, e.StartKind = r.Resumed, "agent"
					if e.StartID == "" {
						e.StartID = r.Revived
					}
				}
			}
			events = append(events, e)
		case l.Type == "user" && b.Type == "text" && strings.HasPrefix(b.Text, "[Request interrupted by user"):
			events = append(events, Event{Kind: Interrupted})
		case l.Type == "user" && b.Type == "text":
			events = append(events, taskEvents(b.Text)...)
		}
	}
	return events
}

func taskEvents(text string) []Event {
	var out []Event
	for _, m := range taskIDRE.FindAllStringSubmatch(text, -1) {
		out = append(out, Event{Kind: TaskEnd, EndedID: m[1]})
	}
	return out
}
