// Package hook is `ctg hook <event>`: it turns a Claude Code hook call into
// requests to the daemon.
//
// Hooks only observe (Q10). They print nothing, always exit 0, and give up
// on the daemon after about 2 s (Q29); what they couldn't deliver goes to
// the spool, for the daemon to replay when it is back. SessionEnd has only
// 1.5 s in all (step 1), so it waits 1 s.
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/config"
	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/tmux"
)

// Input is what Claude Code passes a hook on stdin, for the events ctg uses.
type Input struct {
	SessionID      string            `json:"session_id"`
	TranscriptPath string            `json:"transcript_path"`
	Cwd            string            `json:"cwd"`
	Source         string            `json:"source"` // SessionStart
	Reason         string            `json:"reason"` // SessionEnd
	SessionTitle   string            `json:"session_title"`
	Prompt         string            `json:"prompt"`
	ToolName       string            `json:"tool_name"`
	ToolInput      json.RawMessage   `json:"tool_input"`
	ToolUseID      string            `json:"tool_use_id"`
	ToolResponse   json.RawMessage   `json:"tool_response"`
	LastMessage    string            `json:"last_assistant_message"` // Stop
	Background     []ipc.Task        `json:"background_tasks"`       // Stop, SubagentStop
	Crons          []json.RawMessage `json:"session_crons"`
}

// Env reads the hook's environment.
type Env func(string) string

const (
	askTool  = "AskUserQuestion"
	planTool = "ExitPlanMode"
	// promptKept is how much of a prompt the daemon gets: enough for the
	// item numbers and Qn: lines that answer questions (Q21, Q22).
	promptKept = 4000
	// replyKept is how much of the model's last reply the daemon gets, to
	// find its ❓ blocks (Q22).
	replyKept = 64 << 10
)

// Requests builds the daemon requests for one hook call.
func Requests(event string, in Input, env Env) []ipc.Request {
	// Only interactive sessions join (Q1). `claude -p` runs report
	// CLAUDE_CODE_SESSION_ATTENDED=0 (entrypoint sdk-cli); interactive ones 1.
	if env("CLAUDE_CODE_SESSION_ATTENDED") == "0" {
		return nil
	}
	sid := in.SessionID
	switch event {
	case "SessionStart":
		cwd := strings.TrimSpace(in.Cwd)
		if cwd == "" || filepath.Clean(cwd) == "." {
			return nil
		}
		pid, _ := strconv.Atoi(env("CLAUDE_PID"))
		if pid <= 0 && env("CLAUDE_PID") == "" {
			pid = findClaudePID(os.Getppid())
		}
		return []ipc.Request{{Op: ipc.OpRegister, Session: &ipc.Session{
			ID:         sid,
			PID:        pid,
			Cwd:        cwd,
			Pane:       env("TMUX_PANE"),
			TmuxSocket: tmux.SocketFromEnv(env("TMUX")),
			Mailbox:    env("CLAUDE_CODE_MESSAGING_SOCKET"),
			Transcript: in.TranscriptPath,
			Version:    version(pid),
			Name:       in.SessionTitle,
		}}}
	case "SessionEnd":
		if in.Reason == "clear" {
			return nil // the same process registers again at once and keeps its number
		}
		return []ipc.Request{{Op: ipc.OpEnd, SessionID: sid}}
	case "UserPromptSubmit":
		r := ipc.Request{Op: ipc.OpUpdate, SessionID: sid, State: ipc.StateBusy, Cwd: in.Cwd, Prompt: clip(in.Prompt, promptKept)}
		if in.SessionTitle != "" {
			r.Name = &in.SessionTitle
		}
		return []ipc.Request{r}
	case "Stop":
		tasks := make([]ipc.Task, 0, len(in.Background))
		for _, t := range in.Background {
			if t.ID != "" {
				tasks = append(tasks, t)
			}
		}
		crons := len(in.Crons)
		return []ipc.Request{{
			Op: ipc.OpUpdate, SessionID: sid, State: ipc.StateIdle, Cwd: in.Cwd,
			Reply: clip(in.LastMessage, replyKept), Tasks: &tasks, Crons: &crons,
		}}
	case "PreToolUse":
		switch in.ToolName {
		case askTool:
			return []ipc.Request{{Op: ipc.OpItemAdd, SessionID: sid, Item: &ipc.Item{
				Key: in.ToolUseID, Kind: ipc.KindQuestion, Tool: askTool, Summary: questionSummary(in.ToolInput), Detail: in.ToolInput,
			}}}
		case planTool:
			return []ipc.Request{{Op: ipc.OpItemAdd, SessionID: sid, Item: &ipc.Item{
				Key: in.ToolUseID, Kind: ipc.KindPlan, Tool: planTool, Summary: planSummary(in.ToolInput), Detail: in.ToolInput,
			}}}
		}
	case "PermissionRequest":
		if in.ToolName == askTool || in.ToolName == planTool {
			return nil // PreToolUse reported those, with their tool_use_id
		}
		summary, needle := describeTool(in.ToolName, in.ToolInput, in.Cwd)
		return []ipc.Request{{Op: ipc.OpItemAdd, SessionID: sid, Item: &ipc.Item{
			Key: ipc.PermKey(in.ToolName, in.ToolInput), Kind: ipc.KindPermission, Tool: in.ToolName,
			Summary: summary, Needle: needle, Detail: in.ToolInput,
		}}}
	case "PostToolUse":
		switch in.ToolName {
		case askTool:
			return []ipc.Request{{Op: ipc.OpItemDone, SessionID: sid, Key: in.ToolUseID, Note: "answered: " + answersNote(in.ToolResponse)}}
		case planTool:
			return []ipc.Request{{Op: ipc.OpItemDone, SessionID: sid, Key: in.ToolUseID, Note: "approved"}}
		}
	}
	return nil
}

// version reads the Claude Code version from the session's executable,
// ~/.local/share/claude/versions/2.1.308.
func version(pid int) string {
	if pid <= 0 {
		return ""
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return ""
	}
	if v := filepath.Base(exe); versionRE.MatchString(v) {
		return v
	}
	return ""
}

var versionRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// findClaudePID walks up the process tree from pid to find the Claude Code
// process when CLAUDE_PID isn't set in the environment.
func findClaudePID(pid int) int {
	for range 8 {
		if pid <= 1 {
			return 0
		}
		if version(pid) != "" {
			return pid
		}
		comm, ppid := procParent(pid)
		if comm == "claude" || versionRE.MatchString(comm) {
			return pid
		}
		if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil && filepath.Base(exe) == "claude" {
			return pid
		}
		pid = ppid
	}
	return 0
}

func procParent(pid int) (comm string, ppid int) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", 0
	}
	open := strings.IndexByte(string(b), '(')
	close := strings.LastIndexByte(string(b), ')')
	if open < 0 || close <= open {
		return "", 0
	}
	comm = string(b[open+1 : close])
	fields := strings.Fields(string(b[close+1:]))
	if len(fields) < 2 {
		return comm, 0
	}
	ppid, _ = strconv.Atoi(fields[1])
	return comm, ppid
}

type askInput struct {
	Questions []struct {
		Question string `json:"question"`
		Header   string `json:"header"`
	} `json:"questions"`
}

func questionSummary(input json.RawMessage) string {
	var in askInput
	if json.Unmarshal(input, &in) != nil || len(in.Questions) == 0 {
		return "a question"
	}
	s := in.Questions[0].Question
	if n := len(in.Questions); n > 1 {
		s += fmt.Sprintf(" (+%d more)", n-1)
	}
	return clip(s, 200)
}

func planSummary(input json.RawMessage) string {
	var in struct {
		Plan string `json:"plan"`
	}
	json.Unmarshal(input, &in)
	for _, l := range strings.Split(in.Plan, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(l, "# ")); l != "" {
			return clip(l, 200)
		}
	}
	return "a plan"
}

// describeTool gives a permission prompt's one-line summary and the text
// its dialog shows, as far as step 1 knows it.
func describeTool(tool string, input json.RawMessage, cwd string) (summary, needle string) {
	var in struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Path     string `json:"notebook_path"`
		URL      string `json:"url"`
	}
	json.Unmarshal(input, &in)
	switch {
	case in.Command != "":
		first, _, _ := strings.Cut(strings.TrimSpace(in.Command), "\n")
		return clip(tool+": "+strings.TrimSpace(in.Command), 200), prefix(strings.TrimSpace(first), 40)
	case in.FilePath != "" || in.Path != "":
		p := in.FilePath + in.Path
		shown := p
		if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
			shown = rel
		}
		return clip(tool+": "+shown, 200), filepath.Base(p)
	case in.URL != "":
		return clip(tool+": "+in.URL, 200), ""
	}
	if server, name, ok := strings.Cut(strings.TrimPrefix(tool, "mcp__"), "__"); ok && strings.HasPrefix(tool, "mcp__") {
		return server + " · " + name, ""
	}
	return tool, ""
}

func answersNote(response json.RawMessage) string {
	var r struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
		Answers map[string]string `json:"answers"`
	}
	json.Unmarshal(response, &r)
	var parts []string
	for _, q := range r.Questions {
		if a, ok := r.Answers[q.Question]; ok {
			parts = append(parts, q.Question+" → "+a)
		}
	}
	if len(parts) == 0 {
		return "(no answers reported)"
	}
	return clip(strings.Join(parts, "; "), 300)
}

// prefix is the first n runes of s. A needle must be text the screen
// shows, so unlike clip it adds no "…".
func prefix(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// maxSpool caps the spool, in case the daemon stays away for long.
const maxSpool = 4 << 20

// Run handles one hook call: it reads the input, sends the requests, and
// spools what didn't get through. It never fails the hook.
func Run(event string, stdin io.Reader, env Env, p config.Paths) {
	data, err := io.ReadAll(io.LimitReader(stdin, 16<<20))
	if err != nil {
		return
	}
	var in Input
	if json.Unmarshal(data, &in) != nil {
		return
	}
	reqs := Requests(event, in, env)
	if len(reqs) == 0 {
		return
	}
	budget := 2 * time.Second
	if event == "SessionEnd" {
		budget = time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	for i, req := range reqs {
		if _, err := ipc.Call(ctx, p.Socket, req); err != nil && !answered(err) {
			Spool(p, reqs[i:])
			return
		}
	}
}

// answered tells an error the daemon sent back, which spooling won't fix,
// from not reaching the daemon at all.
func answered(err error) bool {
	_, ok := err.(*ipc.DaemonError)
	return ok
}

// Spool appends requests to the spool file, stamped with the time.
func Spool(p config.Paths, reqs []ipc.Request) {
	if fi, err := os.Stat(p.SpoolFile()); err == nil && fi.Size() > maxSpool {
		return
	}
	if err := os.MkdirAll(p.StateDir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(p.SpoolFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	for _, r := range reqs {
		r.Time = time.Now()
		if b, err := json.Marshal(r); err == nil {
			f.Write(append(b, '\n'))
		}
	}
}
