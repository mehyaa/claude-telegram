package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mehyaa/claude-telegram/internal/config"
	"github.com/mehyaa/claude-telegram/internal/ipc"
)

// gapAfter is how stale the heartbeat must be before a restart counts as
// downtime worth reporting. The heartbeat is written every tickEvery.
const gapAfter = 90 * time.Second

func (d *Daemon) beat() {
	stamp := d.now().UTC().Format(time.RFC3339)
	if err := config.WriteFileAtomic(d.cfg.Paths.HeartbeatFile(), []byte(stamp+"\n")); err != nil {
		d.cfg.Log.Warn("writing the heartbeat", "err", err)
	}
}

func (d *Daemon) lastBeat() (time.Time, bool) {
	b, err := os.ReadFile(d.cfg.Paths.HeartbeatFile())
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	return t, err == nil
}

// startupReport replays what the hooks spooled while the daemon was down and
// tells the owner about the gap (Q29): "bot was down 14:02–14:20; 2 items
// waited".
func (d *Daemon) startupReport() {
	last, ok := d.lastBeat()
	waited := d.replaySpool()

	d.mu.Lock()
	defer d.mu.Unlock()
	var msg string
	if now := d.now(); ok && now.Sub(last) > gapAfter {
		msg = fmt.Sprintf("bot was down %s–%s", d.clock(last), d.clock(now))
		if waited > 0 {
			msg += fmt.Sprintf("; %s waited", plural(waited, "item"))
		}
	} else if waited > 0 {
		msg = fmt.Sprintf("%s waited while the bot was down", plural(waited, "item"))
	}
	d.beat()
	d.save()
	if msg == "" || d.chatID == 0 {
		return
	}
	if waited > 0 {
		d.notify(msg, "p:a")
	} else {
		d.reply(msg, nil)
	}
}

// replaySpool applies the requests hooks wrote to the spool file while the
// daemon was down, then removes it. It returns how many of the items they
// added are still pending.
func (d *Daemon) replaySpool() int {
	path := d.cfg.Paths.SpoolFile()
	replaying := path + ".replaying"
	// A hook appending right now goes on writing to the renamed file, which
	// is read next; one that starts after the rename makes a new spool.
	if err := os.Rename(path, replaying); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.cfg.Log.Warn("reading the spool", "err", err)
		return 0
	}
	data, err := os.ReadFile(replaying)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		d.cfg.Log.Warn("reading the spool", "err", err)
		return 0
	}
	type key struct{ session, item string }
	added := map[key]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var req ipc.Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			d.cfg.Log.Warn("skipping a bad spool line", "err", err)
			continue
		}
		resp := d.handle(req, true)
		if !resp.OK {
			d.cfg.Log.Warn("replaying a spooled request", "op", req.Op, "err", resp.Error)
			continue
		}
		if req.Op == ipc.OpItemAdd {
			added[key{req.SessionID, req.Item.Key}] = true
		}
	}
	if err := os.Remove(replaying); err != nil {
		d.cfg.Log.Warn("removing the replayed spool", "err", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	// Sessions that ended while the daemon was down take their items along.
	d.prune()
	waited := 0
	for k := range added {
		if s := d.byID(k.session); s != nil {
			for _, it := range s.Items {
				if it.Key == k.item {
					waited++
					break
				}
			}
		}
	}
	return waited
}

func homeDir() (string, bool) {
	h, err := os.UserHomeDir()
	return h, err == nil && h != ""
}
