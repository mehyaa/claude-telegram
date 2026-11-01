#!/usr/bin/env bash
# Step-3 live probe (throwaway): a ctg daemon, a fake phone, and a real
# Claude Code session in the tmux session ctg-probe-live, wired together with
# contrib/hooks.json and ctg mcp. Everything lives in .scratch/live.
#
#   probe/live.sh start   build, start the phone and the daemon, pair, start claude
#   probe/live.sh daemon  rebuild ctg and restart the daemon only
#   probe/live.sh stop    stop all three
#   probe/live.sh phone   print what the bot sent, one line each
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
live=$repo/.scratch/live
addr=127.0.0.1:18765
session=ctg-probe-live
owner=42

start() {
  if tmux has-session -t "=$session" 2> /dev/null; then
    echo "$session already exists" >&2
    exit 1
  fi
  mkdir -p "$live/bin" "$live/home/config" "$live/session/.claude"
  (cd "$repo" && go build -o "$live/bin/ctg" ./cmd/ctg && go build -o "$live/bin/faketg" ./probe/faketg)
  (umask 077 && printf '123456:fake-token\n' > "$live/home/config/token")

  : > "$live/phone.jsonl"
  setsid "$live/bin/faketg" "$addr" "$live/phone.jsonl" > "$live/faketg.log" 2>&1 < /dev/null &
  echo $! > "$live/faketg.pid"
  sleep 0.5
  : > "$live/daemon.log"
  # The fake numbers its updates from 1 again; old state would skip them.
  rm -f "$live/home/state/state.json"
  run_daemon

  local code=""
  for _ in $(seq 50); do
    code=$(grep -o 'Send this code to the bot from your Telegram account: [0-9]\{6\}' "$live/daemon.log" | grep -o '[0-9]\{6\}$' || true)
    [[ -n $code ]] && break
    sleep 0.1
  done
  if [[ -n $code ]]; then
    curl -s -d "$code" "http://$addr/ctl/send?chat=$owner"
    echo "paired chat $owner"
  else
    echo "no pairing code (already paired?)"
  fi

  sed "s|\\\$HOME/.local/bin/ctg|$live/bin/ctg|g" "$repo/contrib/hooks.json" > "$live/session/.claude/settings.json"
  jq -n --arg ctg "$live/bin/ctg" '{mcpServers: {ctg: {command: $ctg, args: ["mcp"]}}}' > "$live/mcp.json"

  local unset_args=()
  while IFS= read -r var; do
    [[ $var == CLAUDE_CODE_USE_BEDROCK ]] || unset_args+=(-u "$var")
  done < <(env | cut -d= -f1 | grep -E '^CLAUDE')
  tmux new-session -d -s "$session" -x 120 -y 45 -c "$live/session" \
    env "${unset_args[@]}" CTG_HOME="$live/home" \
    claude --permission-mode default --name live-probe --mcp-config "$live/mcp.json"
  tmux set-option -w -t "=$session:" remain-on-exit on
  echo "started $session"
}

run_daemon() {
  CTG_HOME=$live/home CTG_TELEGRAM_API=http://$addr/api \
    setsid "$live/bin/ctg" daemon >> "$live/daemon.log" 2>&1 < /dev/null &
  echo $! > "$live/daemon.pid"
}

restart_daemon() {
  kill "$(cat "$live/daemon.pid")" 2> /dev/null || true
  sleep 0.5
  (cd "$repo" && go build -o "$live/bin/ctg" ./cmd/ctg)
  run_daemon
  echo "daemon restarted"
}

stop() {
  tmux kill-session -t "=$session" 2> /dev/null || true
  for p in daemon faketg; do
    if [[ -f $live/$p.pid ]]; then
      kill "$(cat "$live/$p.pid")" 2> /dev/null || true
      rm -f "$live/$p.pid"
    fi
  done
  echo stopped
}

phone() {
  jq -r '[.t, (.method // (if .toast then "toast" elif .phone_sent then "PHONE>" else "TAP>" end)), (.msg // .on // "" | tostring), (.text // .toast // .phone_sent // .phone_tapped | gsub("\n"; " ⏎ ")), (if .buttons and (.buttons | length) > 0 then (.buttons | tostring) else "" end)] | join("  ")' "$live/phone.jsonl"
}

case ${1-} in
  start) start ;;
  stop) stop ;;
  daemon) restart_daemon ;;
  phone) phone ;;
  *)
    echo "usage: $0 start|stop|daemon|phone" >&2
    exit 2
    ;;
esac
