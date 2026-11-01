package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeEnvHidesToken(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "secret-token")
	t.Setenv("TMUX_PANE", "%1")
	t.Setenv("OTHER", "no")
	names, values, n := claudeEnv()
	if n != len("secret-token") {
		t.Fatalf("token length %d", n)
	}
	if values["CLAUDE_CODE_SESSION_ID"] != "sess" || values["TMUX_PANE"] != "%1" {
		t.Fatalf("values %+v", values)
	}
	if _, ok := values["CLAUDE_CODE_MESSAGING_TOKEN"]; ok {
		t.Fatal("token value was logged")
	}
	if _, ok := values["OTHER"]; ok {
		t.Fatal("unrelated env was logged")
	}
	prev := ""
	for _, name := range names {
		if name < prev {
			t.Fatalf("names not sorted: %v", names)
		}
		prev = name
		if name == "OTHER" {
			t.Fatal("unrelated name was logged")
		}
	}
}

func TestServe(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "secret-token")
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"arguments":{"text":"hi <there>"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		"",
	}, "\n"))
	var out, logw bytes.Buffer
	if err := serve(in, &out, &logw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(logw.Bytes(), []byte("secret-token")) {
		t.Fatal("log contains the token")
	}
	logLines := bytes.Split(bytes.TrimSpace(logw.Bytes()), []byte("\n"))
	if len(logLines) != 6 { // startup plus five messages
		t.Fatalf("log lines %d:\n%s", len(logLines), logw.String())
	}
	var start startupLog
	if err := json.Unmarshal(logLines[0], &start); err != nil {
		t.Fatal(err)
	}
	if start.TokenLen != len("secret-token") {
		t.Fatalf("startup: %+v", start)
	}

	replies := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(replies) != 4 {
		t.Fatalf("replies %d:\n%s", len(replies), out.String())
	}
	var init struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Instructions    string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(replies[0], &init); err != nil {
		t.Fatal(err)
	}
	if init.ID != 1 || init.Result.ProtocolVersion != "2024-11-05" {
		t.Fatalf("initialize: %s", replies[0])
	}
	if !bytes.Contains(replies[1], []byte(`"probe_echo"`)) || !bytes.Contains(replies[1], []byte(`"abc"`)) {
		t.Fatalf("tools/list: %s", replies[1])
	}
	var call struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(replies[2], &call); err != nil {
		t.Fatal(err)
	}
	if len(call.Result.Content) != 1 || call.Result.Content[0].Text != "echo: hi <there>" {
		t.Fatalf("tools/call: %s", replies[2])
	}
	var ping struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(replies[3], &ping); err != nil {
		t.Fatal(err)
	}
	if string(ping.Result) != "{}" {
		t.Fatalf("ping result: %s", ping.Result)
	}
}
