package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServe(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.308"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"post_question","arguments":{"question":"Use SQLite?","options":["yes","no"],"recommendation":"yes: one file"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"post_question","arguments":{"question":" "}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	}, "\n") + "\n"
	var got Question
	var out strings.Builder
	err := Serve(context.Background(), strings.NewReader(in), &out, "test", func(_ context.Context, q Question) (string, error) {
		got = q
		return "Posted as 2.3.", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d replies (a notification gets none), want 5:\n%s", len(lines), out.String())
	}
	var replies []map[string]any
	for _, l := range lines {
		var m map[string]any
		json.Unmarshal([]byte(l), &m)
		replies = append(replies, m)
	}
	init := replies[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" || !strings.Contains(init["instructions"].(string), "post_question") ||
		!strings.Contains(init["instructions"].(string), "❓ **Q<n>**") {
		t.Fatalf("initialize: %v", init)
	}
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "post_question" {
		t.Fatalf("tools: %v", tools)
	}
	call := replies[2]["result"].(map[string]any)
	if call["isError"] != false || call["content"].([]any)[0].(map[string]any)["text"] != "Posted as 2.3." {
		t.Fatalf("call: %v", call)
	}
	if got.Question != "Use SQLite?" || len(got.Options) != 2 || got.Recommendation != "yes: one file" {
		t.Fatalf("posted %+v", got)
	}
	if replies[3]["result"].(map[string]any)["isError"] != true {
		t.Fatalf("an empty question should be a tool error: %v", replies[3])
	}
	if replies[4]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method: %v", replies[4])
	}
}
