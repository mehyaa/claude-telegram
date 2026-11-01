package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFetchLatest(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	asset := AssetName()
	payload := []byte("not-a-real-binary")
	sum := sha256.Sum256(payload)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n")
	sig := ed25519.Sign(priv, sums)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/mehyaa/claude-telegram/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.3.1",
			"assets": []map[string]string{
				{"name": asset, "browser_download_url": srv.URL + "/bin"},
				{"name": "SHA256SUMS", "browser_download_url": srv.URL + "/sums"},
				{"name": "SHA256SUMS.sig", "browser_download_url": srv.URL + "/sig"},
			},
		})
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) { w.Write(payload) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { w.Write(sums) })
	mux.HandleFunc("/sig", func(w http.ResponseWriter, r *http.Request) { w.Write(sig) })

	t.Setenv("CTG_RELEASE_API", srv.URL)
	dest := filepath.Join(t.TempDir(), "ctg")
	tag, err := fetchLatest(dest, asset, pub)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v0.3.1" {
		t.Fatalf("tag = %s", tag)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("downloaded %q, %v", got, err)
	}

	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	bad := ed25519.Sign(otherPriv, sums)
	mux2 := http.NewServeMux()
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	mux2.HandleFunc("/repos/mehyaa/claude-telegram/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.3.1",
			"assets": []map[string]string{
				{"name": asset, "browser_download_url": srv2.URL + "/bin"},
				{"name": "SHA256SUMS", "browser_download_url": srv2.URL + "/sums"},
				{"name": "SHA256SUMS.sig", "browser_download_url": srv2.URL + "/sig"},
			},
		})
	})
	mux2.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) { w.Write(payload) })
	mux2.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { w.Write(sums) })
	mux2.HandleFunc("/sig", func(w http.ResponseWriter, r *http.Request) { w.Write(bad) })
	t.Setenv("CTG_RELEASE_API", srv2.URL)
	if _, err := fetchLatest(filepath.Join(t.TempDir(), "ctg"), asset, pub); err == nil {
		t.Fatal("bad signature accepted")
	}

	t.Setenv("CTG_RELEASE_API", srv.URL+"-missing")
	if _, err := fetchLatest(filepath.Join(t.TempDir(), "ctg"), asset, pub); err == nil {
		t.Fatal("missing API accepted")
	}
}

func TestRunWithoutKey(t *testing.T) {
	if err := Run(os.Stderr, os.Stderr); err == nil {
		t.Fatal("update without a compiled key should fail")
	}
}

func TestSumFor(t *testing.T) {
	sums := []byte("# comment\n" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  ctg-linux-amd64\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb *ctg-linux-arm64\n")
	got, err := sumFor(sums, "ctg-linux-amd64")
	if err != nil || got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("amd64: %s, %v", got, err)
	}
	got, err = sumFor(sums, "ctg-linux-arm64")
	if err != nil || got[:4] != "bbbb" {
		t.Fatalf("arm64: %s, %v", got, err)
	}
	if _, err := sumFor(sums, "nope"); err == nil {
		t.Fatal("missing name")
	}
}
