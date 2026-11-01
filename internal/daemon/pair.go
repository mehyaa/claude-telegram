package daemon

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/config"
	"github.com/mehyaa/claude-telegram/internal/telegram"
)

const (
	pairFor = 15 * time.Minute
	// pairMisses is how many wrong codes turn pairing off, so the code
	// can't be guessed by sending the bot every number.
	pairMisses = 5
)

// pairing is an open pairing window (Q28).
type pairing struct {
	code    string
	expires time.Time
	misses  int
}

// startPairing opens a pairing window with a new 6-digit code. A chat that is
// already paired stays paired until another chat sends the code.
func (d *Daemon) startPairing() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err) // crypto/rand doesn't fail on Linux
	}
	d.pair = &pairing{code: fmt.Sprintf("%06d", n), expires: d.now().Add(pairFor)}
	return d.pair.code
}

func isCode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// tryPair checks a message against the open pairing window, and reports
// whether the message was a pairing attempt. Wrong codes get no reply, like
// everything else from a chat that isn't paired (Q7).
func (d *Daemon) tryPair(m *telegram.Message) bool {
	p := d.pair
	text := strings.TrimSpace(m.Text)
	if p == nil || !isCode(text) || m.Chat.Type != "private" {
		return false
	}
	if d.now().After(p.expires) {
		d.pair = nil
		d.cfg.Log.Warn("a pairing code arrived after the window closed; run `ctg pair` for a new one")
		return true
	}
	if subtle.ConstantTimeCompare([]byte(text), []byte(p.code)) != 1 {
		p.misses++
		d.cfg.Log.Warn("wrong pairing code", "chat", m.Chat.ID, "misses", p.misses)
		if p.misses >= pairMisses {
			d.pair = nil
			d.cfg.Log.Warn("too many wrong pairing codes; pairing is off until `ctg pair`")
		}
		return true
	}

	old := d.chatID
	d.pair = nil
	d.chatID = m.Chat.ID
	if err := config.WriteChat(d.cfg.Paths, d.chatID); err != nil {
		d.cfg.Log.Error("saving the paired chat; pairing lasts until restart", "err", err)
	}
	d.record("pair", "", fmt.Sprintf("chat %d", d.chatID))
	d.cfg.Log.Info("paired", "chat", d.chatID)
	if old != 0 && old != d.chatID {
		d.sendTo(old, "ctg is now paired with another chat. This chat no longer controls it.", nil)
	}
	d.reply("Paired. This chat now controls ctg on this workstation.", nil)
	d.sendView(d.listView())
	return true
}
