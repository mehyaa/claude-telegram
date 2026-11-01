package launch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSearch(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "Projects", "claude-telegram")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "Projects", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "src", "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".hidden", "thing"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}

	got, create, err := Search(home, "proj/claude-tel")
	if err != nil || create != "" || len(got) != 1 || got[0] != proj {
		t.Fatalf("search: %v create %q err %v", got, create, err)
	}

	got, _, err = Search(home, "~/Projects/claude-telegram")
	if err != nil || len(got) != 1 || got[0] != proj {
		t.Fatalf("exact: %v %v", got, err)
	}

	_, create, err = Search(home, "brand/new")
	if err != nil || create != filepath.Join(home, "brand", "new") {
		t.Fatalf("create: %q %v", create, err)
	}

	_, _, err = Search(home, "/opt/foo")
	if err != nil {
		t.Fatal(err)
	}
	_, create, err = Search(home, "/opt/foo")
	if create != filepath.Join(home, "opt", "foo") {
		t.Fatalf("absolute rewritten: %q %v", create, err)
	}

	if _, _, err = Search(home, "foo/../../../etc"); err != ErrOutside {
		t.Fatalf(".. : %v", err)
	}
	if _, _, err = Search(home, "escape"); err != ErrOutside {
		t.Fatalf("symlink: %v", err)
	}

	got, _, err = Search(home, "src/pkg")
	if err != nil || len(got) != 0 {
		t.Fatalf("node_modules should be skipped: %v %v", got, err)
	}
	got, _, err = Search(home, ".hid")
	if err != nil || len(got) != 1 || got[0] != filepath.Join(home, ".hidden") {
		t.Fatalf("hidden with a dot: %v %v", got, err)
	}
}
