// Package ipc is the protocol between the daemon and its local clients
// (`ctg hook`, `ctg pair`): over the daemon's Unix socket, one JSON request
// line and one JSON response line per connection.
//
// When the daemon can't be reached, hooks append their requests to the spool
// file instead (Q29); the daemon replays it on its next start.
package ipc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"time"
)

// Operations.
const (
	OpRegister = "register"       // a session started (SessionStart); Session is set
	OpUpdate   = "session.update" // State, Name and/or Cwd changed
	OpEnd      = "session.end"    // the session ended (SessionEnd)
	OpItemAdd  = "item.add"       // something needs the owner; Item is set
	OpItemDone = "item.close"     // that something was dealt with; Key and Note
	OpPair     = "pair"           // start pairing; the response has Code
)

// Session states for OpUpdate.
const (
	StateBusy = "busy" // a turn is running
	StateIdle = "idle" // the turn ended
)

// Item kinds (Q3).
const (
	KindPermission = "permission"    // a permission prompt
	KindQuestion   = "question"      // the AskUserQuestion dialog
	KindPlan       = "plan"          // plan approval (ExitPlanMode)
	KindPost       = "post_question" // a non-blocking post_question (Q21)
	KindBlock      = "block"         // a ❓ block in the model's reply (Q22)
)

type Request struct {
	Op        string    `json:"op"`
	Time      time.Time `json:"time,omitzero"` // when it happened; set on spooled requests
	SessionID string    `json:"session_id,omitempty"`
	PID       int       `json:"pid,omitempty"` // instead of SessionID: the session whose process this is (ctg mcp)
	Session   *Session  `json:"session,omitempty"`
	State     string    `json:"state,omitempty"`
	Name      *string   `json:"name,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	Prompt    string    `json:"prompt,omitempty"` // OpUpdate: the start of a submitted prompt (Q21)
	Reply     string    `json:"reply,omitempty"`  // OpUpdate at a turn's end: the model's last reply (Q22)
	Tasks     *[]Task   `json:"tasks,omitempty"`  // Stop: background_tasks, replacing the session's list (Q82)
	Crons     *int      `json:"crons,omitempty"`  // Stop: how many session_crons are pending
	Item      *Item     `json:"item,omitempty"`
	Key       string    `json:"key,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// Task is one background task from a Stop hook's background_tasks.
type Task struct {
	ID   string `json:"id"`
	Type string `json:"type"` // shell, monitor, subagent, workflow, teammate, …
}

// Session describes a Claude Code session as its SessionStart hook sees it.
type Session struct {
	ID         string `json:"id"`                    // session_id
	PID        int    `json:"pid,omitempty"`         // CLAUDE_PID
	Cwd        string `json:"cwd"`                   // cwd
	Pane       string `json:"pane,omitempty"`        // TMUX_PANE; empty outside tmux
	TmuxSocket string `json:"tmux_socket,omitempty"` // the first field of $TMUX
	Mailbox    string `json:"mailbox,omitempty"`     // CLAUDE_CODE_MESSAGING_SOCKET
	Transcript string `json:"transcript,omitempty"`  // transcript_path
	Version    string `json:"version,omitempty"`     // Claude Code version
	Name       string `json:"name,omitempty"`        // the session's own name, if any
}

// Item is something waiting for the owner.
type Item struct {
	Key     string          `json:"key"` // unique within the session: the tool_use_id, or PermKey
	Kind    string          `json:"kind"`
	Tool    string          `json:"tool,omitempty"`   // the tool's name
	Summary string          `json:"summary"`          // one line, e.g. "Bash: make test"
	Detail  json.RawMessage `json:"detail,omitempty"` // the tool's input, for answering from Telegram
	Needle  string          `json:"needle,omitempty"` // text the dialog must show (Q17)
}

// PermKey is a permission prompt's item key. PermissionRequest has no
// tool_use_id, so the key is a hash of the tool and its input, which the
// transcript's tool_use line gives too.
func PermKey(tool string, input json.RawMessage) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	canon := []byte(input)
	if dec.Decode(&v) == nil {
		if b, err := json.Marshal(v); err == nil {
			canon = b // maps marshal with sorted keys
		}
	}
	sum := sha256.Sum256(append([]byte(tool+"\x00"), canon...))
	return "perm:" + hex.EncodeToString(sum[:8])
}

type Response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Number int    `json:"number,omitempty"` // OpRegister: the session's number
	Item   string `json:"item,omitempty"`   // OpItemAdd: the item's number, e.g. "2.1"
	Code   string `json:"code,omitempty"`   // OpPair: the pairing code
}

// Call sends one request to the daemon at socket and waits for the answer,
// within ctx's deadline.
func Call(ctx context.Context, socket string, req Request) (Response, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return Response{}, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return Response{}, err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, err
	}
	if !resp.OK {
		return resp, &DaemonError{resp.Error}
	}
	return resp, nil
}

// DaemonError is an error the daemon sent back, as opposed to not reaching
// it.
type DaemonError struct{ Msg string }

func (e *DaemonError) Error() string { return e.Msg }
