package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

const KeygenUsage = `usage: ctg keygen [file]

Generate an ed25519 key pair for signing SHA256SUMS (Q87). The private key
is written to file (ctg-release.key if omitted), mode 0600. The public hex
is printed; compile it into ctg as update.PublicKeyHex before a release.
The private key is the GitHub Actions secret CTG_RELEASE_KEY.
`

const SignUsage = `usage: ctg sign <SHA256SUMS> <key>

Sign SHA256SUMS with the ed25519 private key and write SHA256SUMS.sig.
The key file is the output of ctg keygen, or a file containing only the hex
private key (the GitHub Actions secret).
`

// Keygen writes an ed25519 private key to path (mode 0600) and prints the
// public hex to out.
func Keygen(path string, out io.Writer) error {
	if path == "" {
		path = "ctg-release.key"
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("# ctg ed25519 release signing key (Q87).\n"+
		"# GitHub Actions secret CTG_RELEASE_KEY is the hex private key (next line).\n"+
		"# Public key (compile into ctg as update.PublicKeyHex):\n"+
		"# %s\n%s\n", hex.EncodeToString(pub), hex.EncodeToString(priv))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %s (mode 0600)\n", path)
	fmt.Fprintf(out, "public key (compile as update.PublicKeyHex):\n%s\n", hex.EncodeToString(pub))
	return nil
}

// SignFile signs the SHA256SUMS at sumsPath with the private key at keyPath
// and writes the raw 64-byte signature to sigPath (sumsPath+".sig" if empty).
func SignFile(sumsPath, keyPath, sigPath string) error {
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	priv, err := parsePrivateKey(key)
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	if sigPath == "" {
		sigPath = sumsPath + ".sig"
	}
	return os.WriteFile(sigPath, ed25519.Sign(priv, sums), 0o644)
}

func parsePrivateKey(b []byte) (ed25519.PrivateKey, error) {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		raw, err := hex.DecodeString(line)
		if err != nil {
			return nil, fmt.Errorf("private key: not hex")
		}
		switch len(raw) {
		case ed25519.SeedSize:
			return ed25519.NewKeyFromSeed(raw), nil
		case ed25519.PrivateKeySize:
			return ed25519.PrivateKey(raw), nil
		default:
			return nil, fmt.Errorf("private key: want 32 or 64 bytes, got %d", len(raw))
		}
	}
	return nil, fmt.Errorf("private key file has no key line")
}

// Verify reports whether sig is a valid ed25519 signature of the exact
// SHA256SUMS bytes. sig may be raw (64 bytes), hex, or standard base64.
func Verify(sums, sig []byte, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key is invalid")
	}
	raw, err := parseSig(sig)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, sums, raw) {
		return fmt.Errorf("SHA256SUMS signature is invalid")
	}
	return nil
}

func parseSig(sig []byte) ([]byte, error) {
	if len(sig) == ed25519.SignatureSize {
		return sig, nil
	}
	sig = bytes.TrimSpace(sig)
	switch {
	case len(sig) == ed25519.SignatureSize:
		return sig, nil
	case len(sig) == hex.EncodedLen(ed25519.SignatureSize):
		raw, err := hex.DecodeString(string(sig))
		if err != nil {
			return nil, fmt.Errorf("SHA256SUMS.sig: not hex")
		}
		return raw, nil
	default:
		raw, err := base64.StdEncoding.DecodeString(string(sig))
		if err != nil || len(raw) != ed25519.SignatureSize {
			return nil, fmt.Errorf("SHA256SUMS.sig: not a 64-byte ed25519 signature")
		}
		return raw, nil
	}
}
