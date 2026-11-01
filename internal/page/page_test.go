package page

import (
	"strings"
	"testing"
)

func TestShortTextIsOnePage(t *testing.T) {
	if p := Pages("  hello\nworld  "); len(p) != 1 || p[0] != "hello\nworld" {
		t.Fatalf("%q", p)
	}
}

func TestProseCutsAtLines(t *testing.T) {
	line := strings.Repeat("word ", 59) + "end" // 298 characters
	text := strings.TrimSpace(strings.Repeat(line+"\n", 12))
	pages := Pages(text)
	if len(pages) != 3 {
		t.Fatalf("%d pages", len(pages))
	}
	for _, p := range pages {
		if runes(p) > Size {
			t.Errorf("page of %d characters", runes(p))
		}
		if !strings.HasSuffix(p, "end") {
			t.Errorf("page cut inside a line: …%q", p[len(p)-20:])
		}
	}
	if strings.Join(pages, "\n") != text {
		t.Error("pages don't add up to the text")
	}
}

func TestCodeBlockStaysWhole(t *testing.T) {
	code := "```go\n" + strings.Repeat("fmt.Println(\"a line of code\")\n", 60) + "```"
	text := strings.Repeat("intro line\n", 100) + code + "\nafter"
	pages := Pages(text)
	whole := 0
	for _, p := range pages {
		if strings.Contains(p, "```go") {
			if !strings.HasPrefix(p, "```go") || !strings.HasSuffix(p, "```") || strings.Count(p, "fmt.Println") != 60 {
				t.Fatalf("code block split or mixed with prose: %q", p)
			}
			whole++
		}
	}
	if whole != 1 {
		t.Fatalf("code block on %d pages", whole)
	}
}

func TestHugeCodeBlockIsRefenced(t *testing.T) {
	code := "```sh\n" + strings.Repeat("echo something fairly long to fill the page up\n", 200) + "```"
	pages := Pages(code)
	if len(pages) < 3 {
		t.Fatalf("%d pages", len(pages))
	}
	total := 0
	for _, p := range pages {
		if !strings.HasPrefix(p, "```sh\n") || !strings.HasSuffix(p, "\n```") || runes(p) > codeMax {
			t.Fatalf("part not fenced or too long (%d): %q…", runes(p), p[:40])
		}
		total += strings.Count(p, "echo something")
	}
	if total != 200 {
		t.Fatalf("%d lines across the parts, want 200", total)
	}
}

func TestOneVeryLongLine(t *testing.T) {
	long := strings.Repeat("abcdefghij ", 400)
	for _, p := range Pages(long) {
		if runes(p) > Size {
			t.Fatalf("page of %d characters", runes(p))
		}
	}
}
