// Command ctg bridges the Claude Code sessions on this workstation to
// Telegram. See GLOSSARY.md.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mehyaa/claude-telegram/internal/config"
	"github.com/mehyaa/claude-telegram/internal/daemon"
	"github.com/mehyaa/claude-telegram/internal/hook"
	"github.com/mehyaa/claude-telegram/internal/install"
	"github.com/mehyaa/claude-telegram/internal/ipc"
	"github.com/mehyaa/claude-telegram/internal/mcp"
	"github.com/mehyaa/claude-telegram/internal/update"
)

const usage = `usage: ctg <command>

  daemon                    run the Telegram bot (normally as the systemd user service)
  pair                      print a new pairing code; the running daemon accepts it for 15 minutes
  hook <event>              called by Claude Code's hooks; reads the hook input on stdin
  mcp                       the post_question MCP server, run by Claude Code
  install [--yes] [step…]   install ctg, asking before each step
  uninstall [--yes] [step…] undo the install, last first
  update                    download the latest signed release and put it in place
  keygen [file]             generate an ed25519 key pair for release signatures
  sign <SHA256SUMS> <key>   write SHA256SUMS.sig (release workflow)
  version                   print the version
`

// version is stamped in at release (Q61). `go build -ldflags "-X main.version=…"`.
var version = "0.3.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd == "hook" {
		// Never fails and never prints: the bridge must not be what stops
		// a session (Q29).
		if len(args) == 1 {
			if p, err := config.Default(); err == nil {
				hook.Run(args[0], os.Stdin, os.Getenv, p)
			}
		}
		return
	}
	var err error
	switch cmd {
	case "mcp":
		err = runMCP()
	case "daemon":
		err = runDaemon()
	case "pair":
		err = runPair()
	case "install":
		err = install.Install(args, os.Stdin, os.Stdout, os.Stderr)
	case "uninstall":
		err = install.Uninstall(args, os.Stdin, os.Stdout, os.Stderr)
	case "update":
		err = runUpdate(args)
	case "keygen":
		err = runKeygen(args)
	case "sign":
		err = runSign(args)
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctg:", err)
		os.Exit(1)
	}
}

func runUpdate(args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(update.Usage)
		return nil
	}
	if len(args) == 1 && args[0] == "--apply" {
		return install.ApplyChanged(os.Stdout, os.Stderr)
	}
	if len(args) != 0 {
		fmt.Fprint(os.Stderr, update.Usage)
		os.Exit(2)
	}
	return update.Run(os.Stdout, os.Stderr)
}

func runKeygen(args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(update.KeygenUsage)
		return nil
	}
	path := "ctg-release.key"
	if len(args) == 1 {
		path = args[0]
	} else if len(args) != 0 {
		fmt.Fprint(os.Stderr, update.KeygenUsage)
		os.Exit(2)
	}
	return update.Keygen(path, os.Stdout)
}

func runSign(args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(update.SignUsage)
		return nil
	}
	if len(args) != 2 {
		fmt.Fprint(os.Stderr, update.SignUsage)
		os.Exit(2)
	}
	return update.SignFile(args[0], args[1], "")
}

func runDaemon() error {
	p, err := config.Default()
	if err != nil {
		return err
	}
	token, err := config.ReadToken(p)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d := daemon.New(daemon.Config{
		Paths:   p,
		Token:   token,
		API:     os.Getenv("CTG_TELEGRAM_API"), // tests point this at a fake
		Out:     os.Stderr,
		Log:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Version: version,
		Release: update.LatestTag,
	})
	return d.Run(ctx)
}

func runPair() error {
	p, err := config.Default()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := ipc.Call(ctx, p.Socket, ipc.Request{Op: ipc.OpPair})
	if err != nil {
		return fmt.Errorf("can't reach the daemon on %s (%v); is `ctg daemon` running?", p.Socket, err)
	}
	fmt.Printf("Send this code to the bot from your Telegram account: %s\n"+
		"It is valid for 15 minutes. The chat paired now stays paired until a chat sends the code.\n", resp.Code)
	return nil
}

func runMCP() error {
	p, err := config.Default()
	if err != nil {
		return err
	}
	// Claude Code starts this server as its own child, so the parent PID
	// names the session, even after /clear changes its ID.
	pid := os.Getppid()
	post := func(ctx context.Context, q mcp.Question) (string, error) {
		detail, _ := json.Marshal(q)
		req := ipc.Request{Op: ipc.OpItemAdd, PID: pid, Item: &ipc.Item{
			Key: "post:" + randomID(), Kind: ipc.KindPost, Summary: q.Question, Detail: detail,
		}}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := ipc.Call(ctx, p.Socket, req)
		var de *ipc.DaemonError
		switch {
		case errors.As(err, &de):
			return "", fmt.Errorf("not posted: %v", err)
		case err != nil:
			hook.Spool(p, []ipc.Request{req})
			return "The bridge to the owner's phone is down. The question will reach them when it is back; keep working.", nil
		}
		n := resp.Item
		return fmt.Sprintf("Posted as %s. Keep working; don't wait for the answer. It arrives as a message starting "+
			"\"[answer to %s]\" (from Telegram) or \"%s\" (from the terminal).", n, n, n), nil
	}
	return mcp.Serve(context.Background(), os.Stdin, os.Stdout, version, post)
}

func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
