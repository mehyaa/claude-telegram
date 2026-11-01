// Package audit is the record of remote actions (Q24): one JSON line per
// action in audit.jsonl, kept for 90 days. Secret input is logged by length
// only; callers pass it that way.
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mehyaa/claude-telegram/internal/config"
)

// Retention is how long entries are kept.
const (
	Retention        = 90 * 24 * time.Hour
	MessageRetention = 14 * 24 * time.Hour
)

type Entry struct {
	Time    time.Time `json:"time"`
	Action  string    `json:"action"`
	Session string    `json:"session,omitempty"` // "2 myapp-build"
	Detail  string    `json:"detail,omitempty"`
}

// MessageEntry is one outbound Telegram message (send or edit) and the
// transcript or hook sources that produced it.
type MessageEntry struct {
	Time    time.Time `json:"time"`
	Op      string    `json:"op"`                // "send" or "edit"
	MsgID   int64     `json:"msg_id,omitempty"`  // Telegram message ID
	Kind    string    `json:"kind,omitempty"`    // "follow", "last", "ping", "idle", "view", etc.
	Session string    `json:"session,omitempty"` // "#2 myapp-build"
	Mode    string    `json:"parse_mode,omitempty"`
	Text    string    `json:"text"`
	Buttons []string  `json:"buttons,omitempty"`
	Sources []string  `json:"sources,omitempty"`
}

type Log struct {
	path      string
	now       func() time.Time
	retention time.Duration
	mu        sync.Mutex
}

func New(path string, now func() time.Time) *Log {
	return &Log{path: path, now: now, retention: Retention}
}

func NewMessages(path string, now func() time.Time) *Log {
	return &Log{path: path, now: now, retention: MessageRetention}
}

// Add appends an entry stamped with the current time.
func (l *Log) Add(action, session, detail string) error {
	return l.writeJSON(Entry{Time: l.now().UTC(), Action: action, Session: session, Detail: detail})
}

// AddMessage appends an outbound message log entry stamped with the current time.
func (l *Log) AddMessage(e MessageEntry) error {
	if e.Time.IsZero() {
		e.Time = l.now().UTC()
	} else {
		e.Time = e.Time.UTC()
	}
	return l.writeJSON(e)
}

func (l *Log) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Prune drops entries older than the log's retention. Lines it can't read are kept.
func (l *Log) Prune() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ret := l.retention
	if ret <= 0 {
		ret = Retention
	}
	cutoff := l.now().Add(-ret)
	var kept bytes.Buffer
	dropped := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 4<<20)
	for sc.Scan() {
		var e struct {
			Time time.Time `json:"time"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil && !e.Time.IsZero() && e.Time.Before(cutoff) {
			dropped++
			continue
		}
		kept.Write(sc.Bytes())
		kept.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if dropped == 0 {
		return nil
	}
	return config.WriteFileAtomic(l.path, kept.Bytes())
}
