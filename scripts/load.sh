#!/usr/bin/env bash
# Sustained load for the dashboard: a burst of N requests every second, until Ctrl+C.
N=${1:-12}
DIR="$(cd "$(dirname "$0")" && pwd)"
while true; do
  "$DIR/burst.sh" "$N" > /dev/null &
  sleep 1
done
