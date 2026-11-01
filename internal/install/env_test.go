package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "TELEGRAM_BOT_NAME=test_bot   # a comment\n" +
		"TELEGRAM_BOT_TOKEN=\"123456:fake-token\"\n" +
		"export TELEGRAM_USER_ID=42\r\n" +
		"EMPTY=\n" +
		"TELEGRAM_BOT_NAME=later\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, ok := envValue(path, "TELEGRAM_BOT_TOKEN"); !ok || v != "123456:fake-token" {
		t.Fatalf("token = %q, %v; want quoted value", v, ok)
	}
	if v, ok := envValue(path, "TELEGRAM_USER_ID"); !ok || v != "42" {
		t.Fatalf("user = %q, %v; want 42 from export and CR", v, ok)
	}
	if v, ok := envValue(path, "TELEGRAM_BOT_NAME"); !ok || v != "later" {
		t.Fatalf("name = %q, %v; want last line", v, ok)
	}
	if v, ok := envValue(path, "EMPTY"); ok || v != "" {
		t.Fatalf("empty = %q, %v; want missing", v, ok)
	}
	if _, ok := envValue(path, "NOPE"); ok {
		t.Fatal("missing key reported present")
	}
	if _, ok := envValue(filepath.Join(dir, "nope"), "X"); ok {
		t.Fatal("missing file reported present")
	}
}
