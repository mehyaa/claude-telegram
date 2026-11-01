# Owner authority is keystrokes in the session's terminal

The bridge acts as the owner. Claude Code treats a mailbox message as a peer message: it carries no user authority and is never approval. Answering a dialog from a hook also hides the terminal dialog. Anything that counts as the owner is typed into the session's tmux pane, and only after the screen matches the expected dialog. A session with no pane is read-only and is reached only through the mailbox.

## Considered options

- Post to the messaging socket. Rejected: the model is told the message is not the user.
- Return the answer from the hook. Rejected: the terminal dialog never appears, and both sides have to stay live.
