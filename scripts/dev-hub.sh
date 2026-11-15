#!/usr/bin/env bash
# What air runs in `make dev`, again after every build. It restarts the hub only when the
# binary changed, so a failed build leaves the hub and its consoles alone. The hub runs
# outside air's process tree: air signals that whole tree, but consoles are the hub's to stop
# and GUI apps outlive the hub (docs/API-gui.md). dev.sh stops the hub when `make dev` ends.
set -u

pidfile=bin/air/hub.pid
sumfile=bin/air/hub.sum
trap 'exit 0' INT TERM HUP

is_hub() {
	case "$(ps -o args= -p "$1" 2>/dev/null)" in
	*nextboard-dev*) return 0 ;;
	esac
	return 1
}

hub=$(cat "$pidfile" 2>/dev/null)
sum=$(cksum <bin/nextboard-dev)

if [ -n "$hub" ] && is_hub "$hub" && [ "$(cat "$sumfile" 2>/dev/null)" != "$sum" ]; then
	echo "dev: kode berubah; restart hub."
	kill -TERM "$hub" 2>/dev/null
	for _ in {1..120}; do
		is_hub "$hub" || break
		sleep 0.1
	done
	is_hub "$hub" && kill -KILL "$hub" 2>/dev/null
fi

if [ -z "$hub" ] || ! is_hub "$hub"; then
	echo "$sum" >"$sumfile"
	# The dev hub is instance "dev": cookies and browser profiles of its own, so it never logs
	# the browser out of an installed hub on this host. Never fatal: the hub starts anyway.
	mkdir -p bin/air/tmp
	GOTMPDIR=$PWD/bin/air/tmp go run ./scripts/devinstance "$DATA" ||
		echo "dev: data folder not set to instance dev; the hub starts as it is." >&2
	setsid=$(command -v setsid)
	(
		$setsid bin/nextboard-dev serve --dev --dev-web-port "$WEB_PORT" --data-dir "$DATA" --listen "$LISTEN" &
		echo $! >"$pidfile"
	)
	hub=$(cat "$pidfile")
fi

while kill -0 "$hub" 2>/dev/null; do
	if ! kill -0 "$NEXTBOARD_DEV_PID" 2>/dev/null; then
		kill -TERM "$hub" 2>/dev/null
		exit 0
	fi
	sleep 0.5 &
	wait $!
done
echo "dev: hub berhenti; simpan file Go untuk menjalankannya lagi." >&2
