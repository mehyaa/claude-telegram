package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPruneKeepsNinetyDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	clock := now.Add(-100 * 24 * time.Hour)
	l := New(path, func() time.Time { return clock })

	if err := l.Add("mute", "", "old"); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(-10 * 24 * time.Hour)
	if err := l.Add("mute", "2 myapp-build", "recent"); err != nil {
		t.Fatal(err)
	}
	clock = now
	if err := l.Prune(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "old") || !strings.Contains(string(b), "recent") {
		t.Fatalf("after prune:\n%s", b)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("audit log mode %04o, want 0600", fi.Mode().Perm())
	}
}

func TestMessagesLogAndPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.jsonl")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	clock := now.Add(-20 * 24 * time.Hour)
	l := NewMessages(path, func() time.Time { return clock })

	if err := l.AddMessage(MessageEntry{Op: "send", Kind: "follow", Session: "#1 a", Text: "old msg", Sources: []string{`{"type":"assistant"}`}}); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(-2 * 24 * time.Hour)
	if err := l.AddMessage(MessageEntry{Op: "send", MsgID: 5, Kind: "follow", Session: "#1 a", Text: "recent msg", Buttons: []string{"⏹ Unfollow"}, Sources: []string{`{"type":"user"}`}}); err != nil {
		t.Fatal(err)
	}
	clock = now
	if err := l.Prune(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "old msg") || !strings.Contains(string(b), "recent msg") || !strings.Contains(string(b), `"sources":["{\"type\":\"user\"}"]`) {
		t.Fatalf("after prune:\n%s", b)
	}
}
