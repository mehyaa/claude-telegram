package config

import (
	"os"
	"strings"
	"testing"
)

func testPaths(t *testing.T) Paths {
	dir := t.TempDir()
	return Paths{ConfigDir: dir + "/config", StateDir: dir + "/state", Socket: dir + "/d.sock"}
}

func TestReadTokenRefusesLooseMode(t *testing.T) {
	p := testPaths(t)
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.TokenFile(), []byte("123:abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadToken(p)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("ReadToken with mode 0644: got %v, want a chmod hint", err)
	}
	if strings.Contains(err.Error(), "123:abc") {
		t.Fatalf("error leaks the token: %v", err)
	}

	if err := os.Chmod(p.TokenFile(), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := ReadToken(p)
	if err != nil || token != "123:abc" {
		t.Fatalf("ReadToken = %q, %v; want 123:abc", token, err)
	}
}

func TestReadTokenMissing(t *testing.T) {
	_, err := ReadToken(testPaths(t))
	if err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("got %v, want a hint about where the token goes", err)
	}
}

func TestChatRoundTrip(t *testing.T) {
	p := testPaths(t)
	if id, err := ReadChat(p); err != nil || id != 0 {
		t.Fatalf("ReadChat before pairing = %d, %v; want 0, nil", id, err)
	}
	if err := WriteChat(p, 4242); err != nil {
		t.Fatal(err)
	}
	if id, err := ReadChat(p); err != nil || id != 4242 {
		t.Fatalf("ReadChat = %d, %v; want 4242", id, err)
	}
	fi, err := os.Stat(p.ChatFile())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("chat file mode %04o, want 0600", fi.Mode().Perm())
	}
}
