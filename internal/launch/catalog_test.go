package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCatalog(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	cwd := filepath.Join(home, "work", "api")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(projects, Slug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	stamp := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	id := "11111111-1111-4111-8111-111111111111"
	body := `{"type":"user","cwd":"` + cwd + `","gitBranch":"main","timestamp":"` + stamp + `","customTitle":"api-work","message":{"content":"hello"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Touched later, but hidden.
	side := "22222222-2222-4222-8222-222222222222"
	if err := os.WriteFile(filepath.Join(dir, side+".jsonl"), []byte(`{"isSidechain":true,"cwd":"`+cwd+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(filepath.Join(dir, side+".jsonl"), now, now)
	loopID := "33333333-3333-4333-8333-333333333333"
	if err := os.WriteFile(filepath.Join(dir, loopID+".jsonl"), []byte(`{"cwd":"`+cwd+`","message":{"content":"<command-name>/loop</command-name>"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sdk := "44444444-4444-4444-8444-444444444444"
	if err := os.WriteFile(filepath.Join(dir, sdk+".jsonl"), []byte(`{"cwd":"`+cwd+`","entrypoint":"sdk-cli"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	team := "55555555-5555-4555-8555-555555555555"
	if err := os.WriteFile(filepath.Join(dir, team+".jsonl"), []byte(`{"cwd":"`+cwd+`","teamName":"bees"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	daemon := "66666666-6666-4666-8666-666666666666"
	if err := os.WriteFile(filepath.Join(dir, daemon+".jsonl"), []byte(`{"cwd":"`+cwd+`","sessionKind":"daemon"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldID := "77777777-7777-4777-8777-777777777777"
	if err := os.WriteFile(filepath.Join(dir, oldID+".jsonl"), []byte(`{"type":"continued-in","sessionId":"`+id+`","cwd":"`+cwd+`","aiTitle":"old"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Filed under the wrong project directory.
	wrong := filepath.Join(projects, "somewhere-else")
	os.MkdirAll(wrong, 0o755)
	wrongID := "88888888-8888-4888-8888-888888888888"
	os.WriteFile(filepath.Join(wrong, wrongID+".jsonl"), []byte(`{"cwd":"`+cwd+`","customTitle":"nope"}`+"\n"), 0o644)
	// A subagent transcript is not a session.
	os.MkdirAll(filepath.Join(dir, id, "subagents"), 0o755)
	os.WriteFile(filepath.Join(dir, id, "subagents", "99999999-9999-4999-8999-999999999999.jsonl"), []byte(`{"cwd":"`+cwd+`"}`+"\n"), 0o644)
	// No title: the fallback label.
	bareID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	os.WriteFile(filepath.Join(dir, bareID+".jsonl"), []byte(`{"cwd":"`+cwd+`","gitBranch":"wip"}`+"\n"), 0o644)
	os.Chtimes(filepath.Join(dir, id+".jsonl"), now, now)
	os.Chtimes(filepath.Join(dir, bareID+".jsonl"), now.Add(-time.Hour), now.Add(-time.Hour))

	all, err := List(projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("listed %d sessions, want 2: %+v", len(all), labels(all))
	}
	p := all[0]
	if p.ID != id || p.Label != "api-work" || p.Branch != "main" || p.Dir != cwd {
		t.Fatalf("past %+v", p)
	}
	line := p.Line(home, now)
	if !strings.Contains(line, "api-work · ~/work/api · main · 2h") {
		t.Fatalf("line %q", line)
	}
	if name, ok := p.TmuxName(); !ok || name != "api-work" {
		t.Fatalf("tmux name %q %v", name, ok)
	}
	filtered := Filter(all, "wip", home)
	if len(filtered) != 1 || filtered[0].ID != bareID || filtered[0].Label != "(session)" {
		t.Fatalf("filter %+v", filtered)
	}
	if _, ok := Load(projects, side); ok {
		t.Fatal("sidechain loaded")
	}
	if _, ok := Load(projects, oldID); ok {
		t.Fatal("continued-in loaded")
	}
}

func labels(all []Past) []string {
	var s []string
	for _, p := range all {
		s = append(s, p.ID+" "+p.Label)
	}
	return s
}
