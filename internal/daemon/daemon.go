// Package daemon is `ctg daemon`: the Telegram bot, and the registry of
// Claude Code sessions that the hooks report to over a Unix socket.
//
// All state sits behind one mutex. Nothing holds it across a network call:
// Telegram traffic goes through an outbox that a single goroutine drains in
// order, so a hook's request never waits on Telegram (Q29).
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mehyaa/claude-telegram/internal/audit"
	"github.com/mehyaa/claude-telegram/internal/config"
	"github.com/mehyaa/claude-telegram/internal/drive"
	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/tmux"
)

type Config struct {
	Paths config.Paths
	Token string
	API   string    // Bot API root; telegram.DefaultAPI when empty
	Out   io.Writer // the terminal, for the pairing code (Q28)
	Log   *slog.Logger

	// Replaceable in tests; nil means the real thing.
	Now         func() time.Time
	ProcStart   func(pid int) (start string, ok bool) // a live process's start time
	Branch      func(dir string) string               // the git branch checked out in dir
	PollTimeout int                                   // getUpdates timeout in seconds
	// Terminal returns the pane to drive; a tmux.Pane by default.
	Terminal func(socket, pane string) drive.Terminal
	// Verified lists the Claude Code versions whose dialogs step 1 checked.
	// Others are answered in the terminal until the probe is re-run on them.
	Verified   []string
	DriveWait  time.Duration // how long the driver retries a screen check (Q17)
	WatchEvery time.Duration // how often transcripts are read
	// FollowEvery is how often Follow sends what came in (Q2).
	FollowEvery time.Duration
	// ClientActive reports whether the tmux client for a pane was used in
	// the last minute (Q58). Nil means it was not.
	ClientActive func(socket, pane string, now time.Time) bool
	// Home is the directory new sessions search under. Empty means the
	// owner's home directory.
	Home string
	// Projects is ~/.claude/projects, or a stand-in in tests.
	Projects string
	// Tmux runs a command on the default tmux server. Nil means tmux.Default.
	Tmux func(args ...string) ([]byte, error)
	// Version is what /status reports. Empty in tests.
	Version string
	// Release returns the latest tag, such as v0.3.1. Nil skips the check,
	// so a test never calls GitHub.
	Release func() (string, error)
	// Update runs ctg update. Nil means update.Run.
	Update func() (string, error)
}

// verified are the Claude Code versions the step-1 probe has checked. More
// can be listed, one per line, in ~/.config/claude-telegram/verified-versions.
var verified = []string{"2.1.308"}

type Daemon struct {
	cfg      Config
	tg       *telegram.Client
	audit    *audit.Log
	messages *audit.Log
	out      chan func(context.Context)

	mu         sync.Mutex
	st         state
	chatID     int64    // the paired chat; 0 before pairing
	pair       *pairing // nil when pairing is off
	lastPruned time.Time

	paged      map[string]*paged // cut messages, for ⏬ More and /more
	pagedSeq   int
	pagedOrder []string
	lastPaged  string

	statusMsg   map[string]int64 // per session: the message with the answers to send (Q22); -1 while it is sent
	statusDirty map[string]bool

	streams   map[string]*followStream // per followed session: its stream buffer and last send time
	phoneText map[string][]string      // recent text typed from the phone, per session

	draft  *draft            // a new or resume flow; not saved
	rename map[string]string // session id → /rename once it checks in

	booted    time.Time
	problem   atomic.Value // last Telegram or tmux error (Q78)
	greps     []grepHit
	diffs     map[string]string // short id → diff text, for 📄 As file
	diffOrder []string
}

const (
	tickEvery = 15 * time.Second
	// staleAfter is how old a typed message may be and still be acted on;
	// older ones were sent while the bot was down.
	staleAfter = 10 * time.Minute
)

func New(cfg Config) *Daemon {
	if cfg.API == "" {
		cfg.API = telegram.DefaultAPI
	}
	if cfg.Out == nil {
		cfg.Out = os.Stderr
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ProcStart == nil {
		cfg.ProcStart = procStart
	}
	if cfg.Branch == nil {
		cfg.Branch = gitBranch
	}
	if cfg.PollTimeout == 0 {
		cfg.PollTimeout = 50
	}
	if cfg.Terminal == nil {
		cfg.Terminal = func(socket, pane string) drive.Terminal { return tmux.Pane{Socket: socket, ID: pane} }
	}
	if cfg.Verified == nil {
		cfg.Verified = verified
	}
	if cfg.WatchEvery == 0 {
		cfg.WatchEvery = time.Second
	}
	if cfg.FollowEvery == 0 {
		cfg.FollowEvery = 3 * time.Second
	}
	if cfg.ClientActive == nil {
		cfg.ClientActive = clientRecent
	}
	return &Daemon{
		statusMsg:   map[string]int64{},
		statusDirty: map[string]bool{},
		streams:     map[string]*followStream{},
		rename:      map[string]string{},
		diffs:       map[string]string{},
		cfg:         cfg,
		tg:          telegram.New(cfg.API, cfg.Token),
		audit:       audit.New(cfg.Paths.AuditFile(), cfg.Now),
		messages:    audit.NewMessages(cfg.Paths.MessagesFile(), cfg.Now),
		out:         make(chan func(context.Context), 256),
	}
}

func (d *Daemon) now() time.Time { return d.cfg.Now() }

// Run serves until ctx is done.
func (d *Daemon) Run(ctx context.Context) error {
	if err := os.MkdirAll(d.cfg.Paths.StateDir, 0o700); err != nil {
		return err
	}
	ln, err := listen(d.cfg.Paths.Socket)
	if err != nil {
		return err
	}
	defer ln.Close()

	d.mu.Lock()
	err = d.load()
	if err == nil {
		d.prune()
		if d.chatID == 0 {
			code := d.startPairing()
			fmt.Fprintf(d.cfg.Out, "ctg: not paired yet. Send this code to the bot from your Telegram account: %s\n"+
				"It is valid for %d minutes; `ctg pair` makes a new one.\n", code, int(pairFor.Minutes()))
		}
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	d.pruneAudit()
	d.startupReport()
	d.send(func(ctx context.Context) {
		if err := d.tg.SetMyCommands(ctx, commands); err != nil {
			d.cfg.Log.Warn("setting the command menu", "err", err)
		}
	})

	// Set before any goroutine starts, so /status reads it without a race.
	d.booted = d.now()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { d.sendLoop(ctx) })
	wg.Go(func() { d.pollLoop(ctx) })
	wg.Go(func() { d.tickLoop(ctx) })
	wg.Go(func() { d.watchLoop(ctx) })
	wg.Go(func() { d.serve(ctx, ln) })
	d.cfg.Log.Info("ctg daemon running", "socket", d.cfg.Paths.Socket)
	<-ctx.Done()
	wg.Wait()

	d.mu.Lock()
	d.beat()
	d.save()
	d.mu.Unlock()
	return nil
}

// send queues a Telegram call. It never blocks: when the outbox is full the
// call is dropped, because the bridge must never be what stops a session.
func (d *Daemon) send(f func(context.Context)) {
	select {
	case d.out <- f:
	default:
		d.cfg.Log.Warn("outbox full; dropping a Telegram call")
	}
}

// sendLoop makes the queued Telegram calls in order. Shutting down doesn't
// cut a call short, so a ping's message ID is still saved; what is queued
// by then gets a few seconds to go out.
func (d *Daemon) sendLoop(ctx context.Context) {
	call := func(f func(context.Context)) {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		f(cctx)
	}
	for {
		select {
		case <-ctx.Done():
			deadline := time.After(3 * time.Second)
			for {
				select {
				case f := <-d.out:
					call(f)
				case <-deadline:
					return
				default:
					return
				}
			}
		case f := <-d.out:
			call(f)
		}
	}
}

func (d *Daemon) pollLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		d.mu.Lock()
		offset := d.st.Offset
		d.mu.Unlock()
		ups, err := d.tg.GetUpdates(ctx, offset, d.cfg.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.cfg.Log.Warn("polling Telegram", "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, u := range ups {
			d.mu.Lock()
			d.handleUpdate(u)
			d.st.Offset = u.UpdateID + 1
			d.save()
			d.mu.Unlock()
		}
	}
}

func (d *Daemon) tickLoop(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.tick()
		}
	}
}

// tick does the periodic work: drop ended sessions, end mutes and pairing
// that ran out, write the heartbeat.
func (d *Daemon) tick() {
	d.mu.Lock()
	d.prune()
	d.expireMutes()
	d.remind()
	if d.pair != nil && d.now().After(d.pair.expires) {
		d.pair = nil
		d.cfg.Log.Warn("pairing code expired; run `ctg pair` for a new one")
	}
	d.beat()
	d.save()
	d.mu.Unlock()
	d.pruneAudit()
}

// pruneAudit trims the audit log once a day.
func (d *Daemon) pruneAudit() {
	d.mu.Lock()
	due := d.now().Sub(d.lastPruned) >= 24*time.Hour
	if due {
		d.lastPruned = d.now()
	}
	d.mu.Unlock()
	if !due {
		return
	}
	if err := d.audit.Prune(); err != nil {
		d.cfg.Log.Warn("pruning the audit log", "err", err)
	}
	if err := d.messages.Prune(); err != nil {
		d.cfg.Log.Warn("pruning the messages log", "err", err)
	}
}

func (d *Daemon) record(action, session, detail string) {
	if err := d.audit.Add(action, session, detail); err != nil {
		d.cfg.Log.Warn("writing the audit log", "err", err)
	}
}

func (d *Daemon) recordMessage(e audit.MessageEntry) {
	if d.messages == nil {
		return
	}
	if err := d.messages.AddMessage(e); err != nil {
		d.cfg.Log.Warn("writing the messages log", "err", err)
	}
}

// state is what survives a restart, in state.json.
type state struct {
	Offset   int64      `json:"offset"`             // next getUpdates offset
	Selected string     `json:"selected,omitempty"` // session ID shown as "→ n" in the header (Q14)
	Sessions []*session `json:"sessions"`
	Mute     mute       `json:"mute"`
	Follow   *follow    `json:"follow,omitempty"`  // legacy single follow, migrated on load
	Follows  []*follow  `json:"follows,omitempty"` // sessions being streamed
	Await    *awaiting  `json:"await,omitempty"`   // the text the owner was asked to type next
	Prefs    prefs      `json:"prefs,omitempty"`
}

func (d *Daemon) load() error {
	id, err := config.ReadChat(d.cfg.Paths)
	if err != nil {
		return err
	}
	d.chatID = id
	b, err := os.ReadFile(d.cfg.Paths.StateFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &d.st); err != nil {
		return fmt.Errorf("%s: %w", d.cfg.Paths.StateFile(), err)
	}
	if d.st.Follow != nil {
		if d.st.Follow.SessionID != "" && d.followOf(d.st.Follow.SessionID) == nil {
			d.st.Follows = append(d.st.Follows, d.st.Follow)
		}
		d.st.Follow = nil
	}
	return nil
}

func (d *Daemon) save() {
	b, err := json.MarshalIndent(d.st, "", "  ")
	if err == nil {
		err = config.WriteFileAtomic(d.cfg.Paths.StateFile(), b)
	}
	if err != nil {
		d.cfg.Log.Error("saving state", "err", err)
	}
}

// listen binds the daemon socket. A socket that answers means another daemon
// is running; one that doesn't is left over and gets replaced.
func listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("another ctg daemon is already running on %s", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (d *Daemon) serve(ctx context.Context, ln net.Listener) {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.cfg.Log.Warn("accepting a connection", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go d.serveConn(c)
	}
}

func (d *Daemon) serveConn(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if !sameUser(c) {
		d.cfg.Log.Warn("refused a connection from another user")
		return
	}
	line, err := bufio.NewReader(io.LimitReader(c, 1<<20)).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req ipc.Request
	var resp ipc.Response
	if err := json.Unmarshal(line, &req); err != nil {
		resp.Error = "bad request: " + err.Error()
	} else {
		resp = d.handle(req, false)
	}
	json.NewEncoder(c).Encode(resp)
}

// sameUser reports whether the peer on c runs as this user.
func sameUser(c net.Conn) bool {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var cerr error
	err = raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	return err == nil && cerr == nil && int(cred.Uid) == os.Getuid()
}

// handle applies one request. replay is set for spooled requests, which
// were raised while the daemon was down and get no ping of their own.
func (d *Daemon) handle(req ipc.Request, replay bool) ipc.Response {
	// git runs before taking the lock, so a slow repository delays only the
	// hook that asked.
	var branch string
	switch {
	case req.Op == ipc.OpRegister && req.Session != nil && validCwd(req.Session.Cwd):
		branch = d.cfg.Branch(req.Session.Cwd)
	case req.Op == ipc.OpUpdate && validCwd(req.Cwd):
		branch = d.cfg.Branch(req.Cwd)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	fail := func(format string, a ...any) ipc.Response {
		return ipc.Response{Error: fmt.Sprintf(format, a...)}
	}
	var resp ipc.Response
	switch req.Op {
	case ipc.OpRegister:
		if req.Session == nil || req.Session.ID == "" {
			return fail("register needs a session with an id")
		}
		if !validCwd(req.Session.Cwd) {
			return fail("register needs a session with a cwd")
		}
		s := d.register(*req.Session, branch)
		resp.Number = s.Num
		d.applyRename(s)
	case ipc.OpUpdate:
		s := d.byID(req.SessionID)
		if s == nil {
			return fail("unknown session %q", req.SessionID)
		}
		switch req.State {
		case "":
		case ipc.StateBusy:
			s.Busy = true
			s.woke = false
			s.Ran = true
			if req.Prompt != "" {
				s.TurnFrom = turnSource(d, s, req.Prompt)
			} else if s.TurnFrom == "" {
				s.TurnFrom = "terminal"
			}
		case ipc.StateIdle:
			s.Busy = false
			s.woke = false
			s.Ran = true
		default:
			return fail("unknown state %q", req.State)
		}
		if req.Tasks != nil {
			crons := s.Crons
			if req.Crons != nil {
				crons = *req.Crons
			}
			d.setTasks(s, *req.Tasks, crons)
		} else if req.Crons != nil {
			s.Crons = *req.Crons
		}
		if req.Name != nil {
			s.Name = *req.Name
		}
		if validCwd(req.Cwd) {
			s.Cwd, s.Branch = req.Cwd, branch
		}
		if req.Prompt != "" {
			d.promptSeen(s, req.Prompt)
			d.questionsAnswered(s, req.Prompt)
		}
		if req.Reply != "" {
			d.replySeen(s, req.Reply, replay)
		}
		d.noteIdle(s, replay)
	case ipc.OpEnd:
		d.end(req.SessionID)
	case ipc.OpItemAdd:
		s := d.byID(req.SessionID)
		if req.SessionID == "" {
			s = d.byPID(req.PID)
		}
		if s == nil {
			return fail("unknown session %q (pid %d)", req.SessionID, req.PID)
		}
		if req.Item == nil || req.Item.Key == "" {
			return fail("item.add needs an item with a key")
		}
		resp.Item = d.addItem(s, *req.Item, req.Time, replay).Num
		d.noteIdle(s, replay)
	case ipc.OpItemDone:
		if s := d.byID(req.SessionID); s != nil {
			d.closeItem(s, req.Key, req.Note)
			d.noteIdle(s, replay)
		}
	case ipc.OpPair:
		if replay {
			return fail("pairing isn't replayed")
		}
		resp.Code = d.startPairing()
		d.cfg.Log.Info("pairing started from ctg pair")
	default:
		return fail("unknown op %q", req.Op)
	}
	d.save()
	resp.OK = true
	return resp
}
