# shellcheck shell=bash
# Source this from ~/.bashrc. It makes every interactive `claude` start in a
# tmux session of its own, named after the folder, so ctg can answer its
# dialogs from Telegram (Q13). Inside tmux, for `claude -p`, for subcommands
# such as `claude mcp`, and without a terminal it just runs claude.
#
# `claude <name>`, with one word as the only argument, names the session
# (Q36–Q46): it attaches to tmux session claude-<name> if there is one, from
# any folder, and otherwise starts `claude --name <name>` in it, in a window
# called `<name>: <folder>`. Inside tmux it switches to that session. A word
# that isn't lowercase kebab-case or snake_case is converted (MyApp → my-app);
# anything else, such as "fix the bug" or src/main.go, is still a prompt.

# _claude_tmux_name WORD prints the session name WORD stands for, or fails
# if WORD is a prompt.
_claude_tmux_name() {
  local word=$1 re='^[a-z][a-z0-9]*([-_][a-z0-9]+)*$'
  [[ $word =~ ^[A-Za-z0-9_-]+$ && $word != -* ]] || return 1
  if ! [[ $word =~ $re && ${#word} -le 40 ]]; then
    # myApp → my-App, HTTPServer → HTTP-Server; then squeeze and trim the
    # separators.
    word=$(sed -E 's/([a-z0-9])([A-Z])/\1-\2/g; s/([A-Z])([A-Z][a-z])/\1-\2/g;
      s/([-_])[-_]+/\1/g; s/^[-_]+|[-_]+$//g' <<< "$word")
    word=${word,,}
  fi
  [[ $word =~ $re && ${#word} -le 40 ]] || return 1
  printf '%s\n' "$word"
}

# _claude_tmux_named NAME attaches or switches to tmux session claude-NAME,
# starting it first if there is none.
_claude_tmux_named() {
  local name=$1 session=claude-$1 dir=$PWD
  if [[ $dir == "$HOME" || $dir == "$HOME"/* ]]; then
    dir="~${dir#"$HOME"}"
  fi
  if [[ -z ${TMUX-} ]]; then
    tmux new-session -A -s "$session" -n "$name: $dir" -c "$PWD" -- claude --name "$name"
    return
  fi
  if ! tmux has-session -t "=$session" 2> /dev/null; then
    tmux new-session -d -s "$session" -n "$name: $dir" -c "$PWD" -- claude --name "$name" || return
  fi
  tmux switch-client -t "=$session"
}

claude() {
  local arg
  for arg in "$@"; do
    case $arg in
      -p | --print | -h | --help | -v | --version) command claude "$@"; return ;;
    esac
  done
  case ${1-} in
    agents | attach | auth | auto-mode | doctor | gateway | import | install | logs | mcp | \
      plugin | plugins | purge | respawn | rm | setup-token | stop | kill | ultrareview | update | upgrade)
      command claude "$@"
      return
      ;;
  esac
  local name=
  if [[ $# == 1 ]]; then
    name=$(_claude_tmux_name "$1") || name=
  fi
  if [[ ! -t 0 || ! -t 1 ]] || ! command -v tmux > /dev/null; then
    if [[ -n $name ]]; then
      command claude --name "$name"
    else
      command claude "$@"
    fi
    return
  fi
  if [[ -n $name ]]; then
    _claude_tmux_named "$name"
    return
  fi
  if [[ -n ${TMUX-} ]]; then
    command claude "$@"
    return
  fi
  name=${PWD##*/}
  name=${name//[^A-Za-z0-9_-]/_}
  local session=claude-$name n=2
  while tmux has-session -t "=$session" 2> /dev/null; do
    session=claude-$name-$n
    n=$((n + 1))
  done
  tmux new-session -s "$session" -n "$name" -c "$PWD" -- claude "$@"
}
