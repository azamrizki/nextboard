#!/bin/sh
# The service manager every e2e hub finds first on its PATH, as `systemctl` and `loginctl`: no
# hub under test, nor anything it starts, ever reaches the real user's systemd. `systemctl
# --user restart <unit>` runs that unit's ExecStart from $HOME/.config/systemd/user (the
# scratch home), under `timeout` so that a run killed halfway leaves nothing behind for long;
# one pid file per unit in $HOME/.fake-systemd, which the teardown ends. Every call is noted in
# $HOME/.fake-systemd/calls.log.
state="$HOME/.fake-systemd"
mkdir -p "$state"
echo "$(basename "$0") $*" >> "$state/calls.log"
[ "$(basename "$0")" = systemctl ] || exit 0

verb=""
units=""
skip=0
for a in "$@"; do
  if [ "$skip" = 1 ]; then skip=0; continue; fi
  case "$a" in
    -p|--property) skip=1 ;;
    -*) ;;
    *) if [ -z "$verb" ]; then verb=$a; else units="$units $a"; fi ;;
  esac
done

alive() { [ -f "$state/$1.pid" ] && kill -0 "$(cat "$state/$1.pid")" 2>/dev/null; }
stop() {
  if alive "$1"; then
    pid=$(cat "$state/$1.pid")
    kill "$pid" 2>/dev/null
    i=0
    while kill -0 "$pid" 2>/dev/null && [ $i -lt 80 ]; do sleep 0.1; i=$((i + 1)); done
  fi
  rm -f "$state/$1.pid"
}
start() {
  n=$1
  file="$HOME/.config/systemd/user/$n.service"
  [ -f "$file" ] || { echo "Unit $n.service not found." >&2; return 5; }
  line=$(sed -n 's/^ExecStart=//p' "$file" | head -n1 | sed "s|%h|$HOME|g")
  eval "set -- $line"
  nohup timeout 900 "$@" >> "$state/$n.log" 2>&1 &
  echo $! > "$state/$n.pid"
}

code=0
for u in $units; do
  name=${u%.service}
  case "$verb" in
    start|restart|reload-or-restart|try-restart)
      stop "$name"
      start "$name" || code=$?
      ;;
    stop) stop "$name" ;;
    disable)
      case " $* " in *" --now "*) stop "$name" ;; esac
      ;;
    is-active)
      if alive "$name"; then echo active; else echo inactive; code=3; fi
      ;;
    is-enabled)
      if [ -f "$HOME/.config/systemd/user/$name.service" ]; then echo enabled; else echo disabled; code=1; fi
      ;;
    show)
      if alive "$name"; then cat "$state/$name.pid"; else echo 0; fi
      ;;
  esac
done
exit $code
