package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/drive"
	"github.com/mehyaa/claude-telegram/internal/screen"
	"github.com/mehyaa/claude-telegram/internal/telegram"
)

// The remote keyboard (Q26): the pane as text, and one key per tap.

const (
	screenKeep = 3400 // characters of screen a message shows, from the bottom
	ruleKeep   = 30   // how long a frame line stays
	// keySettle is how long the pane gets to redraw before it is read again.
	keySettle = 400 * time.Millisecond
)

// keyNames maps button data to tmux key names.
var keyNames = map[string]string{
	"up": "Up", "dn": "Down", "ent": "Enter", "esc": "Escape", "tab": "Tab",
	"1": "1", "2": "2", "3": "3", "4": "4", "5": "5", "6": "6", "7": "7", "8": "8", "9": "9",
}

var (
	ruleRE  = regexp.MustCompile(`([─╌▔━═])[─╌▔━═]{` + fmt.Sprint(ruleKeep) + `,}`)
	digitRE = regexp.MustCompile(`[0-9]+`)
)

// screenText fits a capture to a phone: trailing space and runs of blank
// lines go, long frame lines are shortened, the bottom is kept.
func screenText(capture string) string {
	var lines []string
	blank := false
	for _, l := range strings.Split(capture, "\n") {
		l = strings.TrimRight(ruleRE.ReplaceAllStringFunc(l, func(r string) string {
			return strings.Repeat(string([]rune(r)[:1]), ruleKeep)
		}), " ")
		if l == "" {
			if blank || len(lines) == 0 {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		lines = append(lines, l)
	}
	text := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if r := []rune(text); len(r) > screenKeep {
		text = "…" + string(r[len(r)-screenKeep:])
	}
	return text
}

// secretFor is how long a secret is masked.
const secretFor = time.Hour

// mask hides the session's recent secrets in text the bot is about to send.
func (d *Daemon) mask(s *session, text string) string {
	if s == nil || len(s.secrets) == 0 {
		return text
	}
	keep := s.secrets[:0]
	for _, sec := range s.secrets {
		if d.now().Before(sec.until) {
			keep = append(keep, sec)
			text = strings.ReplaceAll(text, sec.text, "••••")
		}
	}
	s.secrets = keep
	return text
}

// fingerprint names what a key would act on, so a key isn't pressed on a
// screen the owner hasn't seen (Q17): the dialog, or the prompt box, or for
// other screens their last lines without digits, which a timer changes.
func fingerprint(capture string) string {
	var basis string
	s := screen.Parse(capture)
	switch {
	case s.Dialog != nil:
		basis = "dialog\n" + s.Dialog.Text()
	case s.Prompt != nil:
		basis = "prompt\n" + s.Prompt.Text
	default:
		var lines []string
		for _, l := range strings.Split(capture, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, digitRE.ReplaceAllString(l, "#"))
			}
		}
		lines = lines[max(0, len(lines)-20):]
		basis = "other\n" + strings.Join(lines, "\n")
	}
	sum := sha256.Sum256([]byte(basis))
	return hex.EncodeToString(sum[:4])
}

func (d *Daemon) screenKeyboard(s *session, fp string) *telegram.InlineKeyboardMarkup {
	sc := s.scope()
	k := func(label, key string) telegram.InlineKeyboardButton {
		return button(label, fmt.Sprintf("k:%s:%s:%s", sc, key, fp))
	}
	digits := func(from, to int) []telegram.InlineKeyboardButton {
		var r []telegram.InlineKeyboardButton
		for i := from; i <= to; i++ {
			r = append(r, k(fmt.Sprint(i), fmt.Sprint(i)))
		}
		return r
	}
	return keyboard(
		row(k("↑", "up"), k("↓", "dn"), k("⏎", "ent"), k("Esc", "esc"), k("Tab", "tab")),
		digits(1, 5),
		digits(6, 9),
		row(button("⌨️ Type", "kt:"+sc+":"+fp), button("🔒 Secret", "ks:"+sc+":"+fp), button("↻", "kr:"+sc), button("✕", "kx:"+sc)),
	)
}

func (d *Daemon) screenView(s *session, capture, note string) view {
	head := fmt.Sprintf("📺 %s · %s", s.ref(), d.now().Local().Format("15:04:05"))
	if note != "" {
		head += "\n" + note
	}
	text := html.EscapeString(head) + "\n<pre>" + html.EscapeString(d.mask(s, screenText(capture))) + "</pre>"
	return view{text: text, kb: d.screenKeyboard(s, fingerprint(capture))}
}

// canKeys reports whether the session's pane can take keys from the remote
// keyboard. Unlike canType it doesn't need a checked version: the owner
// sees the screen and picks every key.
func canKeys(s *session) (bool, string) {
	switch {
	case s.Pane == "":
		return false, "it isn't running in tmux"
	case s.TmuxSocket == "":
		return false, "its tmux server is unknown"
	}
	return true, ""
}

// showScreen reads the pane and shows it in message msg, or in a new
// message when msg is 0. Called with d.mu held; reads without it.
func (d *Daemon) showScreen(s *session, msg int64, note string) {
	if ok, why := canKeys(s); !ok {
		d.reply("No screen for "+s.ref()+": "+why+".", nil)
		return
	}
	term := d.cfg.Terminal(s.TmuxSocket, s.Pane)
	go func() {
		text, err := term.Capture()
		d.mu.Lock()
		defer d.mu.Unlock()
		if err != nil {
			d.reply("Couldn't read "+s.ref()+"'s screen: "+err.Error(), nil)
			return
		}
		v := d.screenView(s, text, note)
		if msg != 0 {
			d.editMode(msg, v, "HTML")
		} else {
			d.sendMode(v, "HTML")
		}
	}()
}

var errStale = errors.New("the screen changed")

// onKeyboard handles the keyboard's buttons. Called with d.mu held.
func (d *Daemon) onKeyboard(q *telegram.CallbackQuery, s *session, parts []string) string {
	msg := q.Message.MessageID
	if s == nil {
		return ""
	}
	if ok, why := canKeys(s); !ok {
		return "No keys: " + why
	}
	switch parts[0] {
	case "kr":
		d.showScreen(s, msg, "")
		return ""
	case "kx":
		d.edit(msg, view{text: "📺 " + s.ref() + ": keyboard closed."})
		return ""
	case "kt", "ks":
		if len(parts) < 3 {
			return ""
		}
		action, prompt := "ktype", "⌨️ Type the text for "+s.ref()+". It is typed as it is, without Enter; tap ⏎ after. /cancel to stop."
		if parts[0] == "ks" {
			action = "ksecret"
			prompt = "🔒 Type the secret for " + s.ref() + ". It is typed without Enter and deleted from this chat at once, " +
				"and only its length is logged. Not for long-lived secrets: it still passes through Telegram's servers. /cancel to stop."
		}
		d.st.Await = &awaiting{Session: s.ID, Action: action, Msg: msg, FP: parts[2], Until: d.now().Add(awaitFor)}
		d.reply(prompt, nil)
		return ""
	case "k":
		if len(parts) < 4 {
			return ""
		}
		key, ok := keyNames[parts[2]]
		if !ok {
			return ""
		}
		d.pressKey(s, msg, key, parts[3])
		return key
	}
	return ""
}

// pressKey presses one key, if the screen is still the one the owner saw,
// then shows the screen again (Q26).
func (d *Daemon) pressKey(s *session, msg int64, key, fp string) {
	ref := s.ref()
	ok := d.drive(s, "keyboard", func(dr *drive.Driver) error {
		text, err := dr.T.Capture()
		if err != nil {
			return err
		}
		if fingerprint(text) != fp {
			return errStale
		}
		if err := dr.T.Keys(key); err != nil {
			return err
		}
		time.Sleep(keySettle)
		return nil
	}, func(err error) {
		note := ""
		switch {
		case errors.Is(err, errStale):
			d.record("refused", ref, "key "+key+": the screen changed")
			note = "⚠️ The screen changed before your key, so nothing was pressed. Here it is now."
		case err != nil:
			note = "⚠️ " + err.Error()
		default:
			d.record("keys", ref, "keyboard: "+key)
		}
		d.showScreen(s, msg, note)
	})
	if !ok {
		d.reply("Keys are being pressed in "+ref+" already; try again in a moment.", nil)
	}
}

// keyboardText types what the owner sent after ⌨️ Type or 🔒 Secret.
// Called with d.mu held.
func (d *Daemon) keyboardText(a *awaiting, m *telegram.Message, text string) {
	s := d.byID(a.Session)
	if s == nil {
		d.reply("That session has ended.", nil)
		return
	}
	isSecret := a.Action == "ksecret"
	if isSecret {
		// Out of the chat first, whatever happens next (Q27).
		chat, id := d.chatID, m.MessageID
		d.send(func(ctx context.Context) {
			if err := d.tg.DeleteMessage(ctx, chat, id); err != nil {
				d.cfg.Log.Warn("deleting a secret message", "err", err)
			}
		})
		if strings.ContainsAny(text, "\r\n") {
			d.reply("🔒 Not typed: a secret must be one line.", nil)
			return
		}
	}
	if !isSecret {
		d.notePhone(s.ID, text)
	}
	ref, fp := s.ref(), a.FP
	what := "type"
	if isSecret {
		what = "secret"
	}
	ok := d.drive(s, what, func(dr *drive.Driver) error {
		capture, err := dr.T.Capture()
		if err != nil {
			return err
		}
		if fingerprint(capture) != fp {
			return errStale
		}
		if !isSecret && strings.Contains(text, "\n") {
			err = dr.T.Paste(text)
		} else {
			err = dr.T.Literal(text)
		}
		if err != nil {
			return err
		}
		time.Sleep(keySettle)
		return nil
	}, func(err error) {
		note := ""
		switch {
		case errors.Is(err, errStale):
			d.record("refused", ref, what+": the screen changed")
			note = "⚠️ The screen changed, so nothing was typed. Here it is now."
		case err != nil && isSecret:
			// The error could quote what was being typed; it isn't shown.
			d.record("refused", ref, "secret: typing failed")
			note = "⚠️ The secret couldn't be typed."
		case err != nil:
			note = "⚠️ " + err.Error()
		case isSecret:
			s.secrets = append(s.secrets, secret{text: text, until: d.now().Add(secretFor)})
			d.record("secret", ref, fmt.Sprintf("typed %d secret chars", len([]rune(text))))
			note = fmt.Sprintf("🔒 Typed %d secret characters.", len([]rune(text)))
		default:
			d.record("type", ref, text)
		}
		// The screen comes again below what was typed; the old one loses
		// its keys.
		d.edit(a.Msg, view{text: "📺 " + ref + ": the screen is below."})
		d.showScreen(s, 0, note)
	})
	if !ok {
		d.reply("Keys are being pressed in "+ref+" already; try again in a moment.", nil)
	}
}
