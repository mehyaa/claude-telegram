package diff

import (
	"strings"
	"testing"
)

func TestUnifiedOneLine(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\n"
	newText := "a\nb\nc\nD\ne\nf\ng\n"
	got := Unified("main.go", old, newText)
	want := "" +
		"--- a/main.go\n" +
		"+++ b/main.go\n" +
		"@@ -1,7 +1,7 @@\n" +
		" a\n" +
		" b\n" +
		" c\n" +
		"-d\n" +
		"+D\n" +
		" e\n" +
		" f\n" +
		" g\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnifiedNewFile(t *testing.T) {
	got := Unified("n.txt", "", "one\ntwo\n")
	if !strings.HasPrefix(got, "--- /dev/null\n+++ b/n.txt\n@@ -0,0 +1,2 @@\n+one\n+two\n") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestOfTool(t *testing.T) {
	body, ok := OfTool("Write", []byte(`{"file_path":"n.txt","content":"hi\n"}`))
	if !ok || !strings.Contains(body, "+++ b/n.txt") || !strings.Contains(body, "+hi") {
		t.Fatalf("write: %v %s", ok, body)
	}
	body, ok = OfTool("Edit", []byte(`{"file_path":"n.txt","old_string":"hi\n","new_string":"yo\n"}`))
	if !ok || !strings.Contains(body, "-hi") || !strings.Contains(body, "+yo") {
		t.Fatalf("edit: %v %s", ok, body)
	}
	_, ok = OfTool("Bash", []byte(`{"command":"ls"}`))
	if ok {
		t.Fatal("bash should have no diff")
	}
}

func TestOfResultBashEditDiff(t *testing.T) {
	raw := []byte(`{"stdout":"","stderr":"","bashEditDiff":{"files":[{"filePath":"/work/app.py","created":false,"hunks":[{"oldStart":10,"oldLines":3,"newStart":10,"newLines":4,"lines":[" def foo():","-    return 1","+    x = 2","+    return x"," "]}]}]}}`)
	body, path, plus, minus, ok := OfResult(raw)
	if !ok || path != "/work/app.py" || plus != 2 || minus != 1 {
		t.Fatalf("OfResult: ok=%v path=%q +%d -%d body=%q", ok, path, plus, minus, body)
	}
	if !strings.Contains(body, "--- a//work/app.py") || !strings.Contains(body, "@@ -10,3 +10,4 @@") || !strings.Contains(body, "+    x = 2") {
		t.Fatalf("unexpected diff body:\n%s", body)
	}
}
