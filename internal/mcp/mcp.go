// Package mcp is `ctg mcp`: a stdio MCP server with one tool, post_question
// (Q21). Its server instructions carry the conventions for asking the owner
// (Q23), so nothing has to go in a CLAUDE.md.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Instructions are sent to the model with the server (Q23).
const Instructions = `ctg forwards this session's questions to its owner's phone over Telegram.

Blocking questions: use AskUserQuestion as usual. The owner can answer it in the terminal or from Telegram.

Non-blocking questions: when you want the owner's input but can keep working meanwhile, call post_question and carry on. It returns at once with the question's number, such as 2.3. The answer arrives later as a message that starts with "[answer to 2.3]" (sent from Telegram) or "2.3" (typed in the terminal). Don't wait or poll for it.

Questions in a reply: when a reply asks the owner to decide something, write each question as its own block:
❓ **Q<n>** - **<title>**: <question>
➡️ <your recommendation>
Number them Q1, Q2, … across the conversation. The owner may answer them all in one message, as "Q16: … Q17: …".`

// Question is post_question's input.
type Question struct {
	Question       string   `json:"question"`
	Options        []string `json:"options,omitempty"`
	Recommendation string   `json:"recommendation,omitempty"`
}

// Poster delivers a question and says what the model should be told.
type Poster func(ctx context.Context, q Question) (string, error)

var tool = map[string]any{
	"name": "post_question",
	"description": "Post a question to the owner's phone without waiting for the answer. Use it for questions that " +
		"shouldn't stop your work. It returns the question's number at once; the answer arrives later as a message " +
		"starting with \"[answer to <number>]\" or \"<number>\".",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question":       map[string]any{"type": "string", "description": "The question, on its own."},
			"options":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 6, "description": "Short answers the owner can tap, if the question has them."},
			"recommendation": map[string]any{"type": "string", "description": "What you would choose, and why, in one line."},
		},
		"required": []string{"question"},
	},
}

const latestProtocol = "2025-06-18"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve answers MCP requests on r, one JSON-RPC message per line, until r
// ends.
func Serve(ctx context.Context, r io.Reader, w io.Writer, version string, post Poster) error {
	enc := json.NewEncoder(w)
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification, such as notifications/initialized
		}
		result, rerr := handle(ctx, req, version, post)
		msg := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			msg["error"] = rerr
		} else {
			msg["result"] = result
		}
		if err := enc.Encode(msg); err != nil {
			return err
		}
	}
	return sc.Err()
}

func handle(ctx context.Context, req request, version string, post Poster) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		proto := p.ProtocolVersion
		if proto == "" {
			proto = latestProtocol
		}
		return map[string]any{
			"protocolVersion": proto,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ctg", "version": version},
			"instructions":    Instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": []any{tool}}, nil
	case "tools/call":
		var p struct {
			Name      string   `json:"name"`
			Arguments Question `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "bad arguments: " + err.Error()}
		}
		if p.Name != "post_question" {
			return nil, &rpcError{-32602, fmt.Sprintf("no tool %q", p.Name)}
		}
		if strings.TrimSpace(p.Arguments.Question) == "" {
			return toolResult("post_question needs a question.", true), nil
		}
		text, err := post(ctx, p.Arguments)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(text, false), nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
}
