#!/bin/sh
# Stands in for Claude Code in the browser tests: fires hooks through NB_HOOK_SH (the real marker
# protocol), turns on bracketed paste, draws Claude's input box with the cursor in it, echoes
# what is typed, and logs each line it gets to .fake-claude.log in its folder. Never calls anything.
sid=""
while [ $# -gt 0 ]; do case "$1" in --session-id|--resume) sid="$2"; shift;; esac; shift; done
tp="$PWD/.fake-claude-$sid.jsonl"
hook() { printf '%s' "$2" | sh -c "set -- hook $1; eval \"\$NB_HOOK_SH\""; }
box() { printf '\r\n──────────────────────\r\n❯ \r\n──────────────────────\r\n  ⏸ manual mode on\033[2A\033[3G'; }
printf '\033[?2004h'
hook SessionStart "{\"session_id\":\"$sid\",\"transcript_path\":\"$tp\",\"hook_event_name\":\"SessionStart\"}"
box
while IFS= read -r line; do
  printf '%s\n' "$line" >> .fake-claude.log
  hook UserPromptSubmit '{"hook_event_name":"UserPromptSubmit"}'
  printf '{"type":"user","uuid":"u-%s","timestamp":"2026-10-28T10:00:00.000Z","isSidechain":false,"message":{"role":"user","content":"hi"}}\n' "$$" >> "$tp"
  printf '\r\n● Done\r\n'
  hook Stop '{"hook_event_name":"Stop"}'
  box
done
