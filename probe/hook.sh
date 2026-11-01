#!/usr/bin/env bash
# Step-1 probe hook (throwaway). Logs every call and never returns a decision.
#
#   hook.sh <scratch-dir> <event>
#
# Appends one JSON line per call to <scratch-dir>/log/hooks.jsonl. The
# messaging token is logged by length only.
#
# Control files in <scratch-dir>/ctl:
#   sleep.<event>  seconds to sleep, writing a heartbeat to log/beat.<event>.<pid>
#                  every 0.5 s, so a kill shows up as the last beat
#   post.<event>   one-shot (renamed to .used): text to post to this session's
#                  messaging socket, with the auth line; the socket's reply
#                  goes to log/post.<event>.<pid>
#   reply.<event>  printed to stdout at the end (after any sleep), as hook output
set -u

dir=$1
event=$2
log=$dir/log
mkdir -p "$log"

start=$(date +%s.%N)
input=$(cat)
token=${CLAUDE_CODE_MESSAGING_TOKEN-}
names=$(env | cut -d= -f1 | grep -E '^(CLAUDE|ANTHROPIC|TMUX|AWS)' | sort -u | tr '\n' ' ')

jq -nc \
  --arg ev "$event" --arg t "$start" --argjson pid $$ --argjson ppid "$PPID" \
  --arg sock "${CLAUDE_CODE_MESSAGING_SOCKET-}" \
  --arg tokset "${CLAUDE_CODE_MESSAGING_TOKEN+set}" --argjson toklen "${#token}" \
  --arg names "$names" --arg input "$input" \
  '{ev: $ev, t: ($t | tonumber), pid: $pid, ppid: $ppid,
    sock: $sock, token_set: ($tokset == "set"), token_len: $toklen,
    env: ($names | split(" ") | map(select(. != ""))),
    input: ($input | fromjson? // $input)}' >> "$log/hooks.jsonl"

post=$dir/ctl/post.$event
if mv "$post" "$post.used" 2>/dev/null; then
  PROBE_AUTH=1 python3 -I "$(dirname "$0")/post.py" \
    "${CLAUDE_CODE_MESSAGING_SOCKET-}" "$(<"$post.used")" > "$log/post.$event.$$" 2>&1
fi

ctl=$dir/ctl/sleep.$event
if [[ -f $ctl ]]; then
  secs=$(<"$ctl")
  beat=$log/beat.$event.$$
  for sig in TERM INT HUP PIPE; do
    # shellcheck disable=SC2064 # expand $sig now, one trap per signal
    trap "echo \"signal $sig \$(date +%s.%N)\" >> \"$beat\"; exit 1" "$sig"
  done
  echo "start $start" >> "$beat"
  end=$(($(date +%s) + secs))
  while (($(date +%s) < end)); do
    date +%s.%N >> "$beat"
    sleep 0.5 &
    wait $!
  done
  echo "done $(date +%s.%N)" >> "$beat"
fi

reply=$dir/ctl/reply.$event
[[ -f $reply ]] && cat "$reply"

exit 0
