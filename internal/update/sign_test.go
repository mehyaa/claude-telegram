package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeygenSignVerify(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "ctg-release.key")
	var out bytes.Buffer
	if err := Keygen(key, &out); err != nil {
		t.Fatal(err)
	}
	if err := Keygen(key, &out); err == nil {
		t.Fatal("keygen overwrote an existing file")
	}
	fi, err := os.Stat(key)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode: %v", err)
	}
	body, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte(strings.TrimSpace(string(body)))) &&
		!bytes.Contains(out.Bytes(), []byte("public key")) {
		t.Fatal("keygen printed the private key")
	}
	privLine := ""
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			privLine = line
			break
		}
	}
	if bytes.Contains(out.Bytes(), []byte(privLine)) {
		t.Fatal("keygen printed the private key hex")
	}
	pubHex := ""
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if len(line) == hex.EncodedLen(ed25519.PublicKeySize) {
			pubHex = line
		}
	}
	if pubHex == "" {
		t.Fatalf("no public hex in:\n%s", out.String())
	}
	pub, err := hex.DecodeString(pubHex)
	if err != nil {
		t.Fatal(err)
	}

	sums := filepath.Join(dir, "SHA256SUMS")
	payload := []byte("abc  ctg-linux-amd64\n")
	if err := os.WriteFile(sums, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SignFile(sums, key, ""); err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(sums + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(payload, sig, ed25519.PublicKey(pub)); err != nil {
		t.Fatal(err)
	}
	if err := Verify(payload, []byte("nope"), ed25519.PublicKey(pub)); err == nil {
		t.Fatal("bad sig accepted")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := Verify(payload, sig, other); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestCompiledKeyEmpty(t *testing.T) {
	if _, err := CompiledKey(); err == nil {
		t.Fatal("empty PublicKeyHex should refuse")
	}
}
