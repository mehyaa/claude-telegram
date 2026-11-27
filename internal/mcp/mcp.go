// Package mcp is `ctg mcp`: a stdio MCP server with post_question and
// send_message (Q21). Its server instructions carry the conventions for
// asking the owner (Q23), so nothing has to go in a CLAUDE.md.
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

Push a message: call send_message with Markdown text to send it to the owner's phone now (same formatting as session content). It is not a question and does not wait for a reply.

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

// Message is send_message's input.
type Message struct {
	Text string `json:"text"`
}

// Poster delivers a question and says what the model should be told.
type Poster func(ctx context.Context, q Question) (string, error)

// Sender delivers a free-form message and says what the model should be told.
type Sender func(ctx context.Context, text string) (string, error)

var postTool = map[string]any{
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

var sendTool = map[string]any{
	"name": "send_message",
	"description": "Send a Markdown message to the owner's phone now. Same formatting as session content. " +
		"Not a question — use post_question when you need an answer.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "The message body in Markdown."},
		},
		"required": []string{"text"},
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
func Serve(ctx context.Context, r io.Reader, w io.Writer, version string, post Poster, send Sender) error {
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
		result, rerr := handle(ctx, req, version, post, send)
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

func handle(ctx context.Context, req request, version string, post Poster, send Sender) (any, *rpcError) {
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
		return map[string]any{"tools": []any{postTool, sendTool}}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "bad arguments: " + err.Error()}
		}
		switch p.Name {
		case "post_question":
			var q Question
			if err := json.Unmarshal(p.Arguments, &q); err != nil {
				return nil, &rpcError{-32602, "bad arguments: " + err.Error()}
			}
			if strings.TrimSpace(q.Question) == "" {
				return toolResult("post_question needs a question.", true), nil
			}
			text, err := post(ctx, q)
			if err != nil {
				return toolResult(err.Error(), true), nil
			}
			return toolResult(text, false), nil
		case "send_message":
			var m Message
			if err := json.Unmarshal(p.Arguments, &m); err != nil {
				return nil, &rpcError{-32602, "bad arguments: " + err.Error()}
			}
			if strings.TrimSpace(m.Text) == "" {
				return toolResult("send_message needs text.", true), nil
			}
			text, err := send(ctx, m.Text)
			if err != nil {
				return toolResult(err.Error(), true), nil
			}
			return toolResult(text, false), nil
		default:
			return nil, &rpcError{-32602, fmt.Sprintf("no tool %q", p.Name)}
		}
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
}
