#!/usr/bin/env bash
# Stands in for an agent that works, in the browser tests: each message typed into it is
# answered after a short run, with the hint a TUI shows while it works, then its prompt again,
# so that the hub sees a turn start and end. No model, no network.
printf '> '
while IFS= read -r line; do
  # a message of several lines (an answer carries a quote) is one turn
  while IFS= read -r -t 0.5 _; do :; done
  [ -z "$line" ] && { printf '> '; continue; }
  i=0
  while [ "$i" -lt 20 ]; do
    printf '\r\033[K. working on it (esc to interrupt)'
    sleep 0.1
    i=$((i + 1))
  done
  printf '\r\033[KDone: %s\n> ' "$line"
done
