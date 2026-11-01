package daemon

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// mute holds the mutes in force (Q20). A muted item still goes to the
// pending list; only its ping is skipped.
type mute struct {
	All      *span            `json:"all,omitempty"`
	Sessions map[string]*span `json:"sessions,omitempty"` // by session ID
}

type span struct {
	Until  time.Time `json:"until,omitzero"`   // zero: until turned back on
	Waited int       `json:"waited,omitempty"` // items that arrived during the mute
}

func (sp *span) on(now time.Time) bool {
	return sp != nil && (sp.Until.IsZero() || now.Before(sp.Until))
}

// noteMuted reports whether a ping for s would be muted, and counts the item
// against the mutes that cover it.
func (d *Daemon) noteMuted(s *session) bool {
	muted := false
	for _, sp := range []*span{d.st.Mute.All, d.st.Mute.Sessions[s.ID]} {
		if sp.on(d.now()) {
			sp.Waited++
			muted = true
		}
	}
	return muted
}

// anyMute reports whether any mute is on, for the 🔕 in the header.
func (d *Daemon) anyMute() bool {
	if d.st.Mute.All.on(d.now()) {
		return true
	}
	for _, sp := range d.st.Mute.Sessions {
		if sp.on(d.now()) {
			return true
		}
	}
	return false
}

func (d *Daemon) sessionMuted(s *session) bool {
	return d.st.Mute.Sessions[s.ID].on(d.now())
}

// setMute mutes s, or every session when s is nil, for dur, or until turned
// back on when dur is 0.
func (d *Daemon) setMute(s *session, dur time.Duration) *span {
	sp := &span{}
	if dur > 0 {
		sp.Until = d.now().Add(dur)
	}
	if s == nil {
		d.st.Mute.All = sp
	} else {
		if d.st.Mute.Sessions == nil {
			d.st.Mute.Sessions = map[string]*span{}
		}
		d.st.Mute.Sessions[s.ID] = sp
	}
	return sp
}

// unmute ends the mute on s, or every mute when s is nil.
func (d *Daemon) unmute(s *session) {
	if s == nil {
		d.st.Mute = mute{}
		return
	}
	delete(d.st.Mute.Sessions, s.ID)
}

// expireMutes ends timed mutes that ran out. If items came in during a mute
// and are still pending, one line says so: their pings were skipped.
func (d *Daemon) expireMutes() {
	now := d.now()
	if sp := d.st.Mute.All; sp != nil && !sp.on(now) {
		d.st.Mute.All = nil
		if n := d.pendingCount(nil); sp.Waited > 0 && n > 0 {
			d.notify(fmt.Sprintf("🔔 Mute ended · %s pending", plural(n, "item")), "p:a")
		}
	}
	for id, sp := range d.st.Mute.Sessions {
		if sp.on(now) {
			continue
		}
		delete(d.st.Mute.Sessions, id)
		if s := d.byID(id); s != nil {
			if n := d.pendingCount(s); sp.Waited > 0 && n > 0 {
				d.notify(fmt.Sprintf("🔔 %s unmuted · %s pending", s.ref(), plural(n, "item")), "p:"+s.scope())
			}
		}
	}
}

// describeMute says how long a mute lasts: "for 2h, until 16:30".
func (d *Daemon) describeMute(sp *span) string {
	if sp.Until.IsZero() {
		return "until you turn it back on"
	}
	return fmt.Sprintf("for %s, until %s", shortDuration(sp.Until.Sub(d.now())), d.clock(sp.Until))
}

// parseDuration reads "30m", "2h", "1d" or "1h30m".
func parseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("can't read %q as a duration", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil || dur <= 0 {
		return 0, fmt.Errorf("can't read %q as a duration", s)
	}
	return dur, nil
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", d/time.Hour, (d%time.Hour)/time.Minute)
	default:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
