#!/bin/sh
[ -f "$0.env" ] && . "$0.env"
log() { [ -n "$FAKE_LOG" ] && printf '%s\n' "$*" >> "$FAKE_LOG"; }
log "start cwd=$PWD args=$* cfg=${CLAUDE_CONFIG_DIR:-} codexhome=${CODEX_HOME:-} noupdate=${DISABLE_AUTOUPDATER:-}"
stty raw -echo min 0 time 2 2>/dev/null
ESC=$(printf '\033'); CR=$(printf '\r')
kind=${FAKE_KIND:-claude}
sel=1; buf=; mode=prompt; statuses=0
[ "${FAKE_TRUST:-0}" = 1 ] && mode=trust
[ "${FAKE_MODE:-ok}" = update ] && mode=update
cls() { printf '\033[2J\033[H'; }
draw() {
  cls
  case "$mode" in
  trust)
    printf ' Accessing workspace:\r\n\r\n %s\r\n\r\n Quick safety check: Is this a project you created or one you trust?\r\n\r\n' "$PWD"
    if [ "$sel" = 1 ]; then printf ' \342\235\257 No, exit\r\n   Yes, I trust this folder\r\n'; else printf '   No, exit\r\n \342\235\257 Yes, I trust this folder\r\n'; fi
    printf '\r\n Enter to confirm \302\267 Esc to cancel\r\n' ;;
  update)
    printf '  Update available \302\267 0.163.0 -> 0.165.0\r\n\r\n\342\200\272 1. Update now\r\n  2. Skip\r\n\r\n  enter continue \302\267 esc skip\r\n' ;;
  login)
    printf ' Select login method:\r\n\r\n \342\235\257 1. Claude account with subscription\r\n' ;;
  prompt)
    if [ "$kind" = codex ]; then
      printf '  >_ OpenAI Codex (v0.165.0)\r\n\r\n'
      printf '\342\200\272 %s\r\n\r\n  gpt default\r\n' "${buf:-Ask Codex to do anything}"
      printf '\033[3;%dH' $(( ${#buf} + 3 ))
    else
      printf ' Claude Code fake\r\n\r\n'
      printf '\342\224\200\342\224\200\342\224\200\342\224\200\342\224\200\342\224\200\342\224\200\342\224\200\r\n'
      printf '\342\235\257 %s\r\n' "$buf"
      printf '\342\224\200\342\224\200\342\224\200\342\224\200\342\224\200\342\224\200\342\224\200\342\224\200\r\n'
      printf '\033[4;%dH' $(( ${#buf} + 3 ))
    fi ;;
  usage)
    if [ "${FAKE_MODE:-ok}" = unknown ]; then
      printf ' Something completely different\r\n nothing to see\r\n'
    elif [ "$kind" = codex ] && [ "${FAKE_MODE:-ok}" = apikey ]; then
      printf '  Model:          gpt-6.1\r\n  Account:        API key configured (run codex login to use ChatGPT)\r\n  Limits:         not available for this account\r\n'
    elif [ "$kind" = codex ] && { [ "${FAKE_MODE:-ok}" = pending ] || { [ "${FAKE_MODE:-ok}" = pending1 ] && [ "$statuses" -lt 2 ]; }; }; then
      printf '  Model:          gpt-6.1\r\n  Account:        me@example.com (Plus)\r\n  Limits:         refresh requested; run /status again\r\n'
    elif [ "$kind" = codex ]; then
      printf '\342\225\255\342\224\200\342\224\200\342\225\256\r\n'
      printf '\342\224\202  Model:          gpt-6.1 (reasoning high)        \342\224\202\r\n'
      printf '\342\224\202  Account:        me@example.com (Plus)            \342\224\202\r\n'
      printf '\342\224\202  5h limit:     [\342\226\210\342\226\210\342\226\210\342\226\210\342\226\221\342\226\221\342\226\221\342\226\221] 58%% left (resets 18:30) \342\224\202\r\n'
      printf '\342\224\202  Weekly limit: [\342\226\210\342\226\221\342\226\221\342\226\221\342\226\221\342\226\221\342\226\221\342\226\221] 90%% left (resets 09:00 on 3 Nov) \342\224\202\r\n'
      printf '\342\225\260\342\224\200\342\224\200\342\225\257\r\n'
    else
      printf ' Settings:  Status   Config   Usage\r\n\r\n'
      printf ' Current session\r\n \342\226\210\342\226\210\342\226\210\342\226\210\342\226\214                       45%% used\r\n Resets 6pm (Asia/Jakarta)\r\n\r\n'
      printf ' Current week (all models)\r\n \342\226\210\342\226\214                           100%% used\r\n Resets Nov 3, 10am (Asia/Jakarta)\r\n\r\n Esc to cancel\r\n'
    fi ;;
  esac
}
[ "${FAKE_MODE:-ok}" = login ] && mode=login
[ "${FAKE_MODE:-ok}" = hang ] && { while :; do sleep 1; done; }
[ "${FAKE_MODE:-ok}" = die ] && { printf 'Error: account/read failed during TUI bootstrap\r\n'; exit 1; }
sleep 0.2
draw
while :; do
  c=$(dd bs=1 count=1 2>/dev/null)
  [ -z "$c" ] && continue
  if [ "$c" = "$ESC" ]; then
    n=$(dd bs=2 count=1 2>/dev/null)
    case "$mode:$n" in
      trust:'[B') sel=2; draw; continue;;
      trust:'[A') sel=1; draw; continue;;
      trust:*) log "trust-cancel"; exit 1;;
      update:*) log "update-skipped"; mode=prompt; draw; continue;;
      usage:*) mode=prompt; draw; continue;;
      *) continue;;
    esac
  fi
  if [ "$c" = "$CR" ]; then
    case "$mode" in
      trust) if [ "$sel" = 2 ]; then log "trusted"; mode=prompt; draw; else log "trust-no"; exit 1; fi;;
      update) log "UNEXPECTED update-now"; exit 4;;
      prompt)
        case "$buf" in
          /usage|/status|/stats) log "cmd $buf"; statuses=$((statuses + 1)); buf=; mode=usage; draw;;
          /exit|/quit) log "exit $buf"; exit 0;;
          *) log "UNEXPECTED prompt: $buf"; exit 3;;
        esac;;
      usage)
        # codex prints /status into the history; the prompt stays live
        case "$kind:$buf" in
          codex:/status) log "cmd $buf"; statuses=$((statuses + 1)); buf=; draw;;
          codex:/exit|codex:/quit) log "exit $buf"; exit 0;;
          codex:*) log "UNEXPECTED prompt: $buf"; exit 3;;
        esac;;
    esac
    continue
  fi
  [ "$mode" = prompt ] && { buf="$buf$c"; draw; }
  [ "$mode:$kind" = usage:codex ] && buf="$buf$c"
done
