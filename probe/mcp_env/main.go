// Command mcp_env is the step-3 probe (throwaway): a minimal stdio MCP server
// that logs its context.
//
//	go run ./probe/mcp_env <log-file>
//
// Logs the names of CLAUDE* and TMUX* environment variables (values only for
// the session ID, PID, and project dir; the messaging token by length), the
// parent PID, and the parent's executable. Answers initialize, tools/list,
// and one tool, probe_echo, so Claude Code keeps the server.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: mcp_env <log-file>\n")
		os.Exit(2)
	}
	logf, err := os.OpenFile(os.Args[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer logf.Close()
	if err := serve(os.Stdin, os.Stdout, logf); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve(in io.Reader, out, logw io.Writer) error {
	if err := writeJSON(logw, startup()); err != nil {
		return err
	}
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			continue
		}
		raw := append([]byte(nil), bytes.TrimRight(line, "\r\n")...)
		resp, ok, aerr := answer(raw)
		if aerr != nil {
			return aerr
		}
		if err := writeJSON(logw, loggedIn{T: now(), In: json.RawMessage(raw)}); err != nil {
			return err
		}
		if ok {
			if err := writeJSON(out, resp); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

type startupLog struct {
	T         float64           `json:"t"`
	PPID      int               `json:"ppid"`
	ParentExe string            `json:"parent_exe"`
	EnvNames  []string          `json:"env_names"`
	EnvValues map[string]string `json:"env_values"`
	TokenLen  int               `json:"token_len"`
}

type loggedIn struct {
	T  float64         `json:"t"`
	In json.RawMessage `json:"in"`
}

func startup() startupLog {
	names, values, tokenLen := claudeEnv()
	ppid := os.Getppid()
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", ppid))
	if err != nil {
		exe = err.Error()
	}
	return startupLog{
		T: now(), PPID: ppid, ParentExe: exe,
		EnvNames: names, EnvValues: values, TokenLen: tokenLen,
	}
}

func claudeEnv() (names []string, values map[string]string, tokenLen int) {
	values = map[string]string{}
	shown := map[string]bool{
		"CLAUDE_CODE_SESSION_ID": true,
		"CLAUDE_PID":             true,
		"CLAUDE_PROJECT_DIR":     true,
		"TMUX_PANE":              true,
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "TMUX") {
			names = append(names, k)
		}
		if shown[k] {
			values[k] = v
		}
		if k == "CLAUDE_CODE_MESSAGING_TOKEN" {
			tokenLen = len(v)
		}
	}
	sort.Strings(names)
	if names == nil {
		names = []string{}
	}
	return names, values, tokenLen
}

func now() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

func answer(line []byte) (rpcResponse, bool, error) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return rpcResponse{}, false, err
	}
	id, ok := msg["id"]
	if !ok {
		return rpcResponse{}, false, nil
	}
	var method string
	if raw, ok := msg["method"]; ok {
		if err := json.Unmarshal(raw, &method); err != nil {
			return rpcResponse{}, false, err
		}
	}
	result, err := resultFor(method, msg["params"])
	if err != nil {
		return rpcResponse{}, false, err
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}, true, nil
}

func resultFor(method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
		}
		var result initResult
		result.ProtocolVersion = p.ProtocolVersion
		result.ServerInfo.Name = "probe"
		result.ServerInfo.Version = "0"
		result.Instructions = "Probe server. Call probe_echo only when asked."
		return result, nil
	case "tools/list":
		return toolList{Tools: []toolDef{probeEcho()}}, nil
	case "tools/call":
		var p struct {
			Arguments struct {
				Text string `json:"text"`
			} `json:"arguments"`
		}
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
		}
		return callResult{Content: []textPart{{Type: "text", Text: "echo: " + p.Arguments.Text}}}, nil
	default:
		return json.RawMessage(`{}`), nil
	}
}

type initResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	Capabilities    struct {
		Tools struct{} `json:"tools"`
	} `json:"capabilities"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
	Instructions string `json:"instructions"`
}

type toolList struct {
	Tools []toolDef `json:"tools"`
}

type toolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string `json:"type"`
	Properties struct {
		Text struct {
			Type string `json:"type"`
		} `json:"text"`
	} `json:"properties"`
	Required []string `json:"required"`
}

func probeEcho() toolDef {
	var s inputSchema
	s.Type = "object"
	s.Properties.Text.Type = "string"
	s.Required = []string{"text"}
	return toolDef{
		Name:        "probe_echo",
		Description: "Echo the text back.",
		InputSchema: s,
	}
}

type callResult struct {
	Content []textPart `json:"content"`
}

type textPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
