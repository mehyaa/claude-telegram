package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/install"
)

const Usage = `usage: ctg update

Download the latest GitHub release for this CPU, check it against SHA256SUMS
and its ed25519 signature, replace ~/.local/bin/ctg, re-run install steps
whose embedded files changed (hooks, service, shell), and restart the service.
`

const repo = "mehyaa/claude-telegram"

var httpClient = &http.Client{Timeout: 60 * time.Second}

// AssetName is the release asset for this CPU, e.g. ctg-linux-amd64.
func AssetName() string {
	return "ctg-linux-" + runtime.GOARCH
}

// Run downloads the latest signed release into ~/.local/bin/ctg and then
// execs that binary as `ctg update --apply` so the new embeds are used (Q79).
func Run(out, errw io.Writer) error {
	pub, err := CompiledKey()
	if err != nil {
		return err
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("no release for linux/%s (amd64 and arm64 only)", runtime.GOARCH)
	}
	p, err := install.Resolve()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.BinDir, 0o755); err != nil {
		return err
	}
	asset := AssetName()
	fmt.Fprintf(out, "\n== Download %s\n", asset)
	tmp := p.Bin + ".download"
	tag, err := fetchLatest(tmp, asset, pub)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	newp := p.Bin + ".new"
	if err := os.Rename(tmp, newp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(newp, p.Bin); err != nil {
		os.Remove(newp)
		return err
	}
	fmt.Fprintf(out, "  installed %s (%s)\n", p.Bin, tag)
	cmd := exec.Command(p.Bin, "update", "--apply")
	cmd.Stdout, cmd.Stderr = out, errw
	cmd.Env = os.Environ()
	return cmd.Run()
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func apiRoot() string {
	if u := os.Getenv("CTG_RELEASE_API"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://api.github.com"
}

// LatestTag is the tag of the newest GitHub release, such as v0.3.1.
func LatestTag() (string, error) {
	body, err := get(apiRoot() + "/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("latest release has no tag")
	}
	return rel.TagName, nil
}

func fetchLatest(dest, asset string, pub ed25519.PublicKey) (tag string, err error) {
	body, err := get(apiRoot() + "/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("latest release has no tag")
	}
	urlOf := map[string]string{}
	for _, a := range rel.Assets {
		urlOf[a.Name] = a.URL
	}
	sumsURL, ok := urlOf["SHA256SUMS"]
	if !ok {
		return "", fmt.Errorf("release %s has no SHA256SUMS", rel.TagName)
	}
	sigURL, ok := urlOf["SHA256SUMS.sig"]
	if !ok {
		return "", fmt.Errorf("release %s has no SHA256SUMS.sig", rel.TagName)
	}
	binURL, ok := urlOf[asset]
	if !ok {
		return "", fmt.Errorf("release %s has no %s", rel.TagName, asset)
	}
	sums, err := get(sumsURL)
	if err != nil {
		return "", fmt.Errorf("SHA256SUMS: %w", err)
	}
	sig, err := get(sigURL)
	if err != nil {
		return "", fmt.Errorf("SHA256SUMS.sig: %w", err)
	}
	if err := Verify(sums, sig, pub); err != nil {
		return "", err
	}
	want, err := sumFor(sums, asset)
	if err != nil {
		return "", err
	}
	bin, err := get(binURL)
	if err != nil {
		return "", fmt.Errorf("%s: %w", asset, err)
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("%s does not match SHA256SUMS", asset)
	}
	if err := os.WriteFile(dest, bin, 0o755); err != nil {
		return "", err
	}
	return rel.TagName, nil
}

func sumFor(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		hash, file, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		file = strings.TrimSpace(file)
		file = strings.TrimPrefix(file, "*")
		if file == name {
			if len(hash) != 64 {
				return "", fmt.Errorf("SHA256SUMS: bad hash for %s", name)
			}
			return strings.ToLower(hash), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no line for %s", name)
}

func get(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ctg")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("no GitHub release at %s", url)
		}
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return b, nil
}
