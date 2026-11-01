#!/usr/bin/env bash
# Step-1 probe (throwaway): send keys to a probe pane, logging them first.
#
#   keys.sh <name> <tmux send-keys args...>
#   keys.sh <name> -l 'literal text'
set -euo pipefail

name=$1
shift
repo=$(cd "$(dirname "$0")/.." && pwd)
printf '%s %s\n' "$(date +%s.%N)" "$*" >> "$repo/.scratch/$name/log/keys.log"
tmux send-keys -t "=ctg-probe-$name:" "$@"
