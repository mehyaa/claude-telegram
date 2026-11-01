package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContextPercent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	body := "" +
		`{"type":"assistant","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":1000,"cache_creation_input_tokens":1000,"cache_read_input_tokens":80000,"output_tokens":50}}}` + "\n" +
		`{"type":"assistant","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":1000,"cache_creation_input_tokens":1000,"cache_read_input_tokens":80000,"output_tokens":9}}}` + "\n" +
		`{"type":"assistant","isSidechain":true,"message":{"id":"m2","model":"claude-opus-4-6","usage":{"input_tokens":999999,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pct, ok := Context(path, 0)
	if !ok || pct != 41 {
		t.Fatalf("context %d ok=%v, want 41", pct, ok)
	}
}

func TestContextWindow(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"claude-opus-4-6", 200_000},
		{"claude-sonnet-4-5", 200_000},
		{"claude-haiku-4-5", 200_000},
		{"claude-opus-4-6[1m]", 1_000_000},
		{"claude-opus-4-7", 1_000_000},
		{"claude-opus-5-5", 1_000_000},
		{"claude-sonnet-5", 1_000_000},
		{"claude-haiku-5-5", 1_000_000},
		{"claude-fable-5", 1_000_000},
		{"some-unknown-model", 200_000},
	}
	for _, c := range cases {
		if got := contextWindow(c.model); got != c.want {
			t.Errorf("%s: %d, want %d", c.model, got, c.want)
		}
	}
}

func TestMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	body := `{"type":"user","permission_mode":"default"}` + "\n" +
		`{"type":"permission-mode","permissionMode":"acceptEdits"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Mode(path); got != "accept" {
		t.Fatalf("mode %q", got)
	}
}

func TestProcEnv(t *testing.T) {
	if procEnv(os.Getpid(), "PATH") == "" {
		t.Fatal("PATH missing from this process")
	}
	if procEnv(0, "PATH") != "" {
		t.Fatal("pid 0 should not be read")
	}
}
