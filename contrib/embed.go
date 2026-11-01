// Package contrib holds the files ctg install embeds: the tmux wrapper, the
// systemd user unit, and the Claude Code hooks (Q60).
package contrib

import _ "embed"

//go:embed claude-tmux.bash
var ClaudeTmux []byte

//go:embed ctg.service
var ServiceUnit []byte

//go:embed hooks.json
var Hooks []byte
