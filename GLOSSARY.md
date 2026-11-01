# Claude Telegram

The bridge between the Claude Code sessions on this workstation and one paired Telegram chat. The terminal and the phone stay live together, and whichever side answers first is the owner.

## Language

**Session**:
An interactive Claude Code conversation registered with the bridge. It keeps one number until it ends.
_Avoid_: Chat, thread

**Read-only session**:
A session with no tmux pane. It can be listed and reached through the mailbox, and it cannot be typed into.
_Avoid_: Headless session, editor session

**Mailbox**:
The cross-session channel. A message on it is a peer message and is not the owner.
_Avoid_: Socket, bridge note

**Owner**:
The person who can approve and instruct a session, at its terminal or from the paired chat.
_Avoid_: User, admin

**Paired chat**:
The single Telegram chat the bridge accepts.
_Avoid_: Allowlist, login

**Pending item**:
An open ask from a session: a dialog, a posted question, or a closing question.
_Avoid_: Notification, alert

**Dialog**:
A question, permission prompt, or plan approval drawn in the terminal.
_Avoid_: Modal, popup

**Posted question**:
A question the model asks through the bridge without stopping its turn.
_Avoid_: Async question

**Closing question**:
A question written in the model's reply, which becomes a pending item when the turn ends.
_Avoid_: Inline question, question block

**Follow**:
A subscription that streams a session's new messages to the paired chat. Several sessions can be followed at once.
_Avoid_: Watch, tail

**Mute**:
A silence on pings, for every session or for one. Pending items are still recorded.
_Avoid_: Snooze

**Remote keyboard**:
The paired chat's view of a session's screen, and the keys sent to that screen.
_Avoid_: Screen share, remote control

**Background work**:
A shell, monitor, or agent that keeps running after the turn that started it.
_Avoid_: Job, task

**Idle**:
A session whose turn has ended and that has no background work running.
_Avoid_: Done, finished

**Answer in the terminal**:
The outcome when the screen does not match the expected dialog, so the bridge presses nothing.
_Avoid_: Failed answer, mismatch
