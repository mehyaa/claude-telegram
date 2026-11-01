#!/usr/bin/env bash
# Step-1 probe (throwaway): start a scratch Claude Code session in tmux.
#
#   start.sh <name> [claude args...]
#
# Creates .scratch/<name> with probe hooks in its own .claude/settings.json,
# then starts `claude --permission-mode default` there in the detached tmux
# session ctg-probe-<name>, 120x45. The pane stays after claude exits.
set -euo pipefail

name=$1
shift
repo=$(cd "$(dirname "$0")/.." && pwd)
dir=$repo/.scratch/$name
session=ctg-probe-$name
hook=$repo/probe/hook.sh

if tmux has-session -t "=$session" 2>/dev/null; then
  echo "$session already exists" >&2
  exit 1
fi

mkdir -p "$dir/.claude" "$dir/log" "$dir/ctl" "$dir/cap"

events=(SessionStart SessionEnd UserPromptSubmit PreToolUse PermissionRequest
  PostToolUse PostToolUseFailure Notification Stop)
jq -n --arg hook "$hook" --arg dir "$dir" '
  {hooks: ($ARGS.positional | map({key: ., value: [{matcher: "*", hooks: [
    {type: "command", command: "\($hook) \($dir) \(.)"}]}]}) | from_entries)}
' --args "${events[@]}" > "$dir/.claude/settings.json"

# Drop this (controlling) session's identity so the probe is a fresh session.
unset_args=()
while IFS= read -r var; do
  [[ $var == CLAUDE_CODE_USE_BEDROCK ]] || unset_args+=(-u "$var")
done < <(env | cut -d= -f1 | grep -E '^CLAUDE')

tmux new-session -d -s "$session" -x 120 -y 45 -c "$dir" \
  env "${unset_args[@]}" claude --permission-mode default "$@"
tmux set-option -w -t "=$session:" remain-on-exit on
echo "started $session in $dir"
