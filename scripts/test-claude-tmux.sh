#!/usr/bin/env bash
# Test contrib/claude-tmux.bash: which arguments name a session, and what
# `claude <name>` does with tmux. It runs a tmux server of its own (its own
# TMUX_TMPDIR under .scratch/claude-tmux-test) and a fake claude that only
# records how it was called, so no real session is touched.
#
#   scripts/test-claude-tmux.sh
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
sb=$repo/.scratch/claude-tmux-test
rm -rf "$sb"
mkdir -p "$sb/tmux" "$sb/fake" "$sb/work/alpha" "$sb/work/beta"
# Never reach the owner's tmux server, even when run from inside it.
unset TMUX TMUX_PANE
export TMUX_TMPDIR=$sb/tmux
trap 'tmux kill-server 2> /dev/null || true' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

# The fake claude logs "folder|args" and, in a terminal, stays running the
# way an interactive session would.
cat > "$sb/fake/claude" << EOF
#!/usr/bin/env bash
echo "\$(pwd)|\$*" >> "$sb/calls.log"
[[ -t 0 ]] && exec sleep 600
exit 0
EOF
chmod +x "$sb/fake/claude"
export PATH=$sb/fake:$PATH
touch "$sb/calls.log"

# shellcheck source=contrib/claude-tmux.bash
source "$repo/contrib/claude-tmux.bash"

echo "--- names"
check_name() {
  local got
  got=$(_claude_tmux_name "$1") || got=PROMPT
  [[ $got == "$2" ]] || fail "name of [$1] is $got, want $2"
}
check_name api api
check_name my_app my_app
check_name my-app_v2 my-app_v2
check_name MyApp my-app
check_name My_App my_app
check_name HTTPServer http-server
check_name API-v2 api-v2
check_name v2App v2-app
check_name my--app my-app
check_name _foo- foo
check_name Update update
check_name 1234567890123456789012345678901234567890 PROMPT
check_name abcdefghijabcdefghijabcdefghijabcdefghij abcdefghijabcdefghijabcdefghijabcdefghij
check_name abcdefghijabcdefghijabcdefghijabcdefghijk PROMPT
for word in -c --resume 2fa src/main.go "why?" "fix the bug" "" -- _; do
  check_name "$word" PROMPT
done

echo "--- without a terminal"
last_call() { tail -n 1 "$sb/calls.log"; }
(cd "$sb/work/alpha" && claude MyApp < /dev/null)
[[ $(last_call) == "$sb/work/alpha|--name my-app" ]] || fail "no terminal, name: $(last_call)"
claude "fix the bug" < /dev/null
[[ $(last_call) == *"|fix the bug" ]] || fail "no terminal, prompt: $(last_call)"
claude fix bug < /dev/null
[[ $(last_call) == *"|fix bug" ]] || fail "no terminal, two args: $(last_call)"
claude update < /dev/null
[[ $(last_call) == *"|update" ]] || fail "no terminal, subcommand: $(last_call)"
claude -c < /dev/null
[[ $(last_call) == *"|-c" ]] || fail "no terminal, flag: $(last_call)"
: > "$sb/calls.log"

# Shells with the wrapper loaded: "out" without $TMUX, so its claude attaches
# a nested client, and "in" inside tmux.
cat > "$sb/rc" << EOF
PATH=$sb/fake:\$PATH
PS1='\$ '
source "$repo/contrib/claude-tmux.bash"
EOF
tmux -f /dev/null new-session -d -s ctg-probe-out -x 200 -y 50 -- \
  env -u TMUX -u TMUX_PANE bash --noprofile --rcfile "$sb/rc" -i
tmux new-session -d -s ctg-probe-in -- bash --noprofile --rcfile "$sb/rc" -i

type_in() {
  tmux send-keys -t "=$1:" -l "$2"
  tmux send-keys -t "=$1:" Enter
}
wait_for() {
  local i
  for ((i = 0; i < 50; i++)); do
    "$@" && return
    sleep 0.1
  done
  return 1
}
client_on() { [[ $(tmux list-clients -F '#{client_session}') == "$1" ]]; }
calls() { [[ $(wc -l < "$sb/calls.log") == "$1" ]]; }
tilde() {
  local dir=$1
  [[ $dir == "$HOME"/* ]] && dir="~${dir#"$HOME"}"
  printf '%s\n' "$dir"
}

echo "--- outside tmux, new session"
wait_for tmux has-session -t =ctg-probe-out || fail "driver shell"
sleep 0.5
type_in ctg-probe-out "cd '$sb/work/alpha' && claude MyApp"
wait_for client_on claude-my-app || fail "no client on claude-my-app: $(tmux list-clients -F '#{client_session}')"
wait_for calls 1 || fail "claude not started once: $(cat "$sb/calls.log")"
[[ $(last_call) == "$sb/work/alpha|--name my-app" ]] || fail "started as $(last_call)"
want="my-app: $(tilde "$sb/work/alpha")"
got=$(tmux list-windows -t =claude-my-app -F '#{window_name}')
[[ $got == "$want" ]] || fail "window name [$got], want [$want]"

echo "--- outside tmux, existing session from another folder"
tmux detach-client -s =claude-my-app
wait_for client_on "" || fail "client still attached"
sleep 0.5
type_in ctg-probe-out "cd '$sb/work/beta' && claude my-app"
wait_for client_on claude-my-app || fail "not reattached: $(tmux list-clients -F '#{client_session}')"
sleep 0.5
calls 1 || fail "claude started again: $(cat "$sb/calls.log")"
[[ $(tmux list-sessions -F '#S' | grep -c '^claude-') == 1 ]] || fail "sessions: $(tmux list-sessions -F '#S')"

echo "--- inside tmux, new session"
tmux switch-client -c "$(tmux list-clients -F '#{client_name}')" -t =ctg-probe-in
wait_for client_on ctg-probe-in || fail "client not moved to the inner shell"
type_in ctg-probe-in "cd '$sb/work/beta' && claude Second"
wait_for client_on claude-second || fail "not switched to claude-second: $(tmux list-clients -F '#{client_session}')"
wait_for calls 2 || fail "claude not started: $(cat "$sb/calls.log")"
[[ $(last_call) == "$sb/work/beta|--name second" ]] || fail "started as $(last_call)"
want="second: $(tilde "$sb/work/beta")"
got=$(tmux list-windows -t =claude-second -F '#{window_name}')
[[ $got == "$want" ]] || fail "window name [$got], want [$want]"

echo "--- inside tmux, existing session"
tmux switch-client -c "$(tmux list-clients -F '#{client_name}')" -t =ctg-probe-in
wait_for client_on ctg-probe-in || fail "client not moved back"
type_in ctg-probe-in "claude my-app"
wait_for client_on claude-my-app || fail "not switched to claude-my-app: $(tmux list-clients -F '#{client_session}')"
sleep 0.5
calls 2 || fail "claude started again: $(cat "$sb/calls.log")"

echo "--- outside tmux, a prompt"
tmux detach-client -s =claude-my-app
wait_for client_on "" || fail "client still attached"
sleep 0.5
type_in ctg-probe-out "cd '$sb/work/alpha' && claude 'fix the bug'"
wait_for client_on claude-alpha || fail "no folder session: $(tmux list-sessions -F '#S')"
wait_for calls 3 || fail "claude not started: $(cat "$sb/calls.log")"
[[ $(last_call) == "$sb/work/alpha|fix the bug" ]] || fail "started as $(last_call)"

echo "PASS"
