# Claude Telegram Bot

Two-way control of local Claude Code sessions from Telegram: read and follow
sessions, answer their questions and permission prompts, and send them
messages, while the terminal stays live.

The terminal and the phone stay live together, and whichever side answers
first wins. The sections under **Using it** are the features. Vocabulary is
in [`GLOSSARY.md`](GLOSSARY.md). Decisions that are hard to undo are in
[`docs/adr/`](docs/adr/). Released under the [MIT license](LICENSE).

## Requirements

### To run

- Linux, amd64 or arm64, with a systemd user session (`systemctl --user`).
  The service step installs and starts `ctg` there.
- A Telegram account, and a bot token from
  [@BotFather](https://t.me/BotFather).
- Claude Code, as `claude` on `PATH`. The mcp step calls it. Automatic
  answers need a version the probe has checked; see [Versions](#versions).
- tmux. The bot types into a session through it.
- bash. The shell step sources the tmux wrapper from `~/.bashrc`.
- Outbound HTTPS to `api.telegram.org`.

### To develop

- Go 1.26 or later, the version in `go.mod`.
- tmux. The dialog tests and `scripts/test-claude-tmux.sh` start a tmux
  server of their own. A missing tmux skips those tests.
- bash.
- shellcheck. CI runs it on `scripts/*.sh`, `probe/*.sh`, and
  `contrib/*.bash`.
- jq, for the shell scripts in `probe/`.

## Setup

1. Create a bot with [@BotFather](https://t.me/BotFather).
2. Put its token in `.env` in this repo (it's gitignored):

   ```dotenv
   TELEGRAM_BOT_TOKEN=123456:ABC...
   TELEGRAM_BOT_NAME=my_ctg_bot
   TELEGRAM_USER_ID=12345678
   ```

   The name and user ID are optional: the pairing step names the bot to open,
   and checks the paired chat against your user ID. Without a `.env`, the
   token step asks for the token instead. `ctg install` reads `.env` from the
   current directory, or `$CTG_ENV_FILE`.
3. Get a `ctg` binary and run the installer. From a [GitHub
   release](https://github.com/mehyaa/claude-telegram/releases/latest),
   download `ctg-linux-amd64` or `ctg-linux-arm64`. There is no `curl | sh`:

   ```sh
   chmod +x ctg-linux-amd64
   ./ctg-linux-amd64 install
   ```

   From this repo instead: `go build -o ctg ./cmd/ctg && ./ctg install`.

   The installer goes through every step, asking before each. The `binary`
   step copies **this** executable to `~/.local/bin/ctg`; everything else
   runs that copy, not the file you started from.

   | Step | What it does |
   |---|---|
   | `binary` | copies this executable to `~/.local/bin/ctg` |
   | `token` | stores the token in `~/.config/claude-telegram/token`, mode 0600, without printing it, and offers to make `.env` 0600 |
   | `pair` | shows a 6-digit code to send to the bot from your Telegram account, waits for it, and checks the paired chat against `TELEGRAM_USER_ID` |
   | `service` | installs and starts the systemd user service `ctg` |
   | `hooks` | backs up `~/.claude/settings.json` and adds ctg's hooks; other hooks stay |
   | `mcp` | adds the `post_question` MCP server for all projects |
   | `shell` | copies the tmux wrapper to `~/.local/share/claude-telegram/` and sources it from `~/.bashrc` |

   One step at a time: `ctg install pair`. `--yes` skips the questions.
   Every step can be run again: after `git pull`, `go build -o ctg ./cmd/ctg`
   and `./ctg install --yes binary service` puts the new build in place.

Turn on two-step verification for the Telegram account you pair: whoever holds
it controls your sessions. Claude Code sessions join when they start, so
restart the ones already running. To pair a different chat later, run
`ctg install pair` and send the code from that chat.

`ctg uninstall` undoes the steps, last first, asking before each: shell, mcp,
hooks, service, config, binary. `--yes` answers yes, including deleting the
token, the paired chat and the audit log.

With the `shell` step, `claude` in a terminal starts in a tmux session of its
own, `claude-<folder>`, so the bot can type into it. `claude <name>`, with one
word as the only argument, names the session instead: it attaches to tmux
session `claude-<name>` if there is one, from any folder, and otherwise starts
`claude --name <name>` there, in a window called `<name>: ~/folder`. Inside
tmux it switches to that session. Words that aren't lowercase kebab-case or
snake_case are converted (`MyApp` → `my-app`); a quoted prompt such as
`claude "fix the bug"` still starts a session with that prompt.

## Commands

| Command | What it does |
|---|---|
| `ctg daemon` | run the Telegram bot (the systemd user service runs this) |
| `ctg pair` | print a new pairing code; the running daemon accepts it for 15 minutes |
| `ctg hook <event>` | called by Claude Code's hooks; reads the hook input on stdin |
| `ctg mcp` | the `post_question` MCP server, run by Claude Code |
| `ctg install [--yes] [step…]` | install, asking before each step |
| `ctg uninstall [--yes] [step…]` | undo the install, last first |
| `ctg update` | download the latest signed release for this CPU, check `SHA256SUMS` and its ed25519 signature, replace `~/.local/bin/ctg`, re-run hooks/service/shell if those embedded files changed, restart the service |
| `ctg keygen [file]` | generate an ed25519 key pair for signing releases; writes the private key (mode 0600) and prints the public hex to compile in |
| `ctg sign <SHA256SUMS> <key>` | write `SHA256SUMS.sig` (the release workflow) |
| `ctg version` | print the version |
| `ctg help` | list commands |

`ctg update` refuses a release whose signature doesn't verify, and this build
refuses to update at all until a public key is compiled in. Before the first
tag: `ctg keygen`, paste the public hex as `update.PublicKeyHex` in
`internal/update/pubkey.go`, and store the private hex as the GitHub Actions
secret `CTG_RELEASE_KEY`. The builder never sees it. A key change ships inside
a signed update. Tag `v*` (for example `v0.3.1`) to build `linux/amd64` and
`linux/arm64`, stamp `ctg version`, and attach `SHA256SUMS` plus
`SHA256SUMS.sig`.

## Using it

Every command is in the Telegram menu. Typed commands and buttons do the
same thing. The list and the session menu keep the common actions on the
message and put the rest behind ⋯, editing that message in place. There is
no reply keyboard: those taps would be typed into the session as text.

### Sessions

`/list` shows every interactive session. One joins when it starts and keeps
a number until it ends. Items under it are numbered `2.1`, `2.2`. Tap a row
to select it. The label is the session's own name, otherwise its folder and
branch.

| | |
|---|---|
| ⚙️ working | a turn is running |
| ❓ waiting | a dialog is open |
| 🐚 shell | a background shell is still running |
| 📡 monitor | a background monitor is still running |
| 🤖 agent | a background agent is still running |
| 💤 idle | the turn has ended and nothing is still running |
| 💤⏰ | idle, and the session will wake itself |

🐚, 📡, and 🤖 also sit beside ⚙️ during a turn, and they combine when more
than one kind is left. The session view counts the active ones on their own
line. `/legend` lists every icon.

A session that is not in tmux is read-only. Text and files still reach it,
through Claude Code's mailbox, as a message that is not you: it carries no
authority and is never an approval.

### Pings

A one-line ping arrives when a session needs you. Everything else waits
until you ask for it.

A session pings once when it becomes idle, including a turn that was waiting
on background work. That ping is skipped when you typed the turn at the
terminal and that tmux client was used in the last minute. A session that
will wake itself gets that first ping and no reminders.

Reminders are off until `/settings` turns them on. A session's ⋯ menu can
override that. Once on, they fire at 5 minutes, 10, 15, 30, 1 hour, 2, 4,
8, 12, and 16 hours, then one last notice that reminders have stopped. The
schedule restarts when the session runs again or you open it.

`/mute [n] [30m|2h|1d]` silences pings for every session or for one, idle
pings and reminders included. With no duration it lasts until `/unmute`.
Plain `/unmute` ends every mute. Muted items still show in `/pending`. The
header shows 🔕 while any mute is on.

### Answering

`/pending [n]` lists what is waiting, for every session or for one. Five
kinds of item ping you:

- a question dialog, including multi-select and a free-text Other
- a permission prompt
- a plan approval
- a posted question, which returns at once so the model keeps working
- a closing question: a ❓ block in the model's reply, with its ➡️ recommendation

The dialog stays on the terminal. The phone answers it by keystrokes.
Whichever side answers first wins, and the phone says who did.

From the phone you can Allow once, or Deny with a reason. On a plan you can
Approve, keeping manual approval, or send it back with Change. Allow always,
and approving a plan into auto mode, stay on the terminal. A tap here does
not change the session's permission mode for good.

Several questions are collected, then sent in one go. If the terminal has
already started on that dialog, the phone presses nothing.

Before any key the bot reads the screen. When it does not show the expected
dialog, nothing is pressed and the item says to answer in the terminal. You
can retry, or finish it from 📺 Screen. A Claude Code version that has not
been checked is treated the same way. See [Versions](#versions).

An answer to a posted question is typed as `[answer to 2.3] …`. A terminal
message that starts with the item number closes it too. The item closes
itself when a later turn does not ask it again, or when you submit a
free-form prompt at the terminal.

A closing question offers ✅ Accept (the recommendation) and ✍️ Reply.
📤 Send types the collected answers as one message, one line each
(`Q16: …`). Typing that line at the terminal answers it too. `ctg mcp`
teaches the model this shape.

`/dismiss [num|n|all]` closes items without answering. ✖ Dismiss is on each
card, and Dismiss all is on the pending list whenever something is waiting.
`/cancel` drops a half-typed answer.

### Reading

`/last [N] [agent|chat|full]` shows the selected session's last messages.
10 by default, 50 at most.

| Mode | What you see |
|---|---|
| 💬 Chat | your prompts and the model's text. This is the default. |
| 🤖 Agent | the model's text only |
| 🧰 Full | chat, plus tool calls, their results, and messages to and from other sessions |

📜 Last asks with buttons. A number only moves the mark, a mode sends, and
✍️ asks for a number. Messages are Telegram HTML. A long one is cut into
pages that keep a code block whole. ⏬ More, or `/more`, shows the next
page. Each message names its session, its role, and the time on one line.

`/follow [n] [agent|chat|full]` streams new messages until
`/unfollow [n|all]`. Several sessions can be followed at once, each in its
own mode. A followed row shows 👁 and the mode. 👁 Following lists them.
A streamed message can unfollow that session or open it.

🧰 Full shows a tool as a card: the path, the arguments, and the result.
🧾 Diff is on an edit, a write, and a shell command that changed files, and
on the matching permission card, so a change can be read before Allow once.
A short diff is inline. A long one, or 📄 As file, arrives as a document.

`/grep [n] <text>` searches transcripts, ignoring case. A session number
searches that session. From the list, 🔎 Search covers every session from
the last 30 days. Up to 10 hits. 📜 Around shows the five messages around
one.

The session view shows `ctx` as a percentage of the model's window. The
list does not. There is no cost figure.

### Sending

Text that is not a command goes to the selected session, named in the
header. A busy session holds it until it can take it. You do not have to
interrupt just to queue a follow-up.

`/interrupt <n> [text]` presses Esc, waits until the turn has stopped, then
types the text.

📺 Screen (`/screen`) shows the pane with `↑ ↓ ⏎ Esc Tab` and `1–9`, one
key a tap, and ⌨️ Type for text, slash commands included. The screen is
read again after each key. If it changed before the tap, nothing is pressed.
This works for any dialog, including a Claude Code version the probe has
not checked.

🔒 Secret, on that keyboard, types the next message, deletes it from the
chat at once, and logs only its length. What the bot shows is masked as
`••••` for the next hour. Do not use it for a long-lived secret: the
message still passes through Telegram's servers. A request for text lapses
after five minutes. `/cancel` drops it sooner.

A photo or a document is saved under the selected session's
`.ctg-uploads/`, and that directory is added to `.git/info/exclude`, not to
`.gitignore`. The bot can download up to 20 MB. A caption is the prompt,
with the saved path attached. With no caption the file waits for the next
message, and the header shows it waiting. A read-only session receives the
file through the mailbox.

🛑 End (`/end`) asks first, and says when a turn or background work is
running. It types `/exit`, with Esc first when the session is busy, waits
for the process to exit, and closes a `claude-*` tmux session that has no
other windows. Otherwise it closes only that pane. A read-only session is
removed from the list. It is not killed.

🔀 Mode (`/mode`) offers Manual, Accept edits, and Plan. Auto and bypass
are never offered. A switch that would pass through auto waits until the
session is idle.

### Starting a session

🆕 New and `/new <name> <folder> [-- <prompt>]` start a fresh tmux session
`claude-<name>` in that folder, in a login shell, so it has the same
environment as a terminal. The name is required: lowercase, at most 40
characters, `my-app` or `my_app`. `MyApp` becomes `my-app`. If that tmux
session already exists, it is selected instead of started again.

The folder stays under your home directory. An exact path starts. A partial
path such as `proj/claude-tel` searches from home, a few levels deep,
skipping hidden directories and dependency directories unless the query
itself has the dot. One match starts. Two to eight become buttons. More
than that asks you to narrow it. A missing folder needs a tap on Create.
A path that resolves outside home is refused. Nothing runs as root.

⏪ Resume and `/resume [text]` list past sessions, newest first, ten a
page, across projects. Text filters the label, the branch, and the folder.
The list leaves out the same sessions the in-terminal picker hides:
subagents, unattended runs, and sessions that were continued elsewhere.
One that is already running is selected. Resume uses the session's original
folder, anywhere that folder still exists and can be read, including
outside home. A missing folder is refused. If the name already belongs to
a different conversation, the bot asks for another.

The shell wrapper, installed by the `shell` step above, is what puts a
terminal `claude` into tmux. One bare word is a name. A quoted prompt, an
unattended `claude -p`, and a real subcommand keep their old meaning. The
wrapper never resumes. Resume is only from the phone.

### Trust

Only the paired chat is accepted. Every other update is dropped. A second
machine uses its own bot token.

A hook that cannot reach the daemon gives up after about two seconds, and
the session continues on the terminal alone. After a restart the bot
reports the gap: when it was down, and that items waited.

Every remote action is appended to
`~/.local/state/claude-telegram/audit.jsonl` and kept for 90 days. That is
what was sent, to which session, what was typed, which dialog was answered,
and which keystroke was refused. Secret input is recorded as a length.
Copies of outbound messages are kept for 14 days, with secrets masked.

🩺 Status (`/status`) shows how long the daemon has been up, its version, a
newer release when there is one, which Claude Code versions in use are not
verified, whether the hooks, the posted-question server, the service, and
the shell wrapper are installed and current, and the last Telegram or tmux
error. ⬆️ Update from that view runs `ctg update`.

### Versions

Keys are pressed only for a Claude Code version the probe has checked
(2.1.308 so far). After an update, re-run the probe in `probe/` and add the
version to `~/.config/claude-telegram/verified-versions`, one per line.
Until then that version's dialogs are answered in the terminal. 📺 Screen
still works.

## Specs

The same features, written for an agent, are closed issues. They record
shipped behavior. They are not a to-do list.

- [#1 Pair the bridge to one chat](https://github.com/mehyaa/claude-telegram/issues/1)
- [#2 List sessions and ping when one needs the owner](https://github.com/mehyaa/claude-telegram/issues/2)
- [#3 Answer pending items from the paired chat](https://github.com/mehyaa/claude-telegram/issues/3)
- [#4 Read and follow a session](https://github.com/mehyaa/claude-telegram/issues/4)
- [#5 Instruct a session and drive its screen](https://github.com/mehyaa/claude-telegram/issues/5)
- [#6 Start and resume sessions](https://github.com/mehyaa/claude-telegram/issues/6)
- [#7 Install, update, and remove the bridge](https://github.com/mehyaa/claude-telegram/issues/7)

## Development

```sh
scripts/test.sh              # gofmt check, go vet, go test
scripts/test.sh -race
scripts/test-claude-tmux.sh  # the tmux wrapper, on a tmux server of its own
```

CI runs that on every push and PR (tmux and shellcheck included). A `v*` tag
runs the release workflow.

Tests run against a fake Bot API (`internal/telegram/fake`) and, for
the dialogs, a stub Claude Code screen (`internal/drive/stubtui`) in a tmux
server of their own. They keep temporary files in `.scratch/tmp`. The installer
is tested against a sandbox `HOME`. `probe/` holds the probes: `start.sh` and
its helpers for the step-1 dialog checks, and `live.sh` for a full run against
a real Claude Code session with a fake phone (`probe/faketg`).
