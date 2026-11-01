#!/usr/bin/env bash
# Step-1 probe (throwaway): capture a probe pane to .scratch/<name>/cap and print it.
#
#   cap.sh <name> <label>
set -euo pipefail

name=$1
label=$2
repo=$(cd "$(dirname "$0")/.." && pwd)
dir=$repo/.scratch/$name/cap
mkdir -p "$dir"
file=$dir/$(date +%H%M%S.%N | cut -c1-10)-$label.txt
tmux capture-pane -p -t "=ctg-probe-$name:" > "$file"
# Trailing blank lines carry no information.
sed -e :a -e '/^\n*$/{$d;N;ba' -e '}' "$file"
