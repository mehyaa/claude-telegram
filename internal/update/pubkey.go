package update

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strings"
)

// PublicKeyHex is the ed25519 public key that verifies SHA256SUMS (Q87).
// Empty until the owner runs `ctg keygen` and pastes the public hex here,
// before the first release.
const PublicKeyHex = ""

// CompiledKey returns the public key compiled into this binary.
func CompiledKey() (ed25519.PublicKey, error) {
	h := strings.TrimSpace(PublicKeyHex)
	if h == "" {
		return nil, fmt.Errorf("no release signing key compiled in; this build can't verify updates")
	}
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("compiled release public key is invalid")
	}
	return ed25519.PublicKey(b), nil
}
