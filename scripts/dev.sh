#!/usr/bin/env bash
# `make dev`: the hub and the Vite dev server in one terminal. Go changes rebuild and restart
# the hub through air (.air.toml); RELOAD=0 runs it without air. Ctrl-C stops both, and if
# either one dies the other is stopped too.
set -u
cd "$(dirname "$0")/.."

DATA=${DATA:-$PWD/.devdata}
LISTEN=${LISTEN:-0.0.0.0:12312}
WEB_PORT=${WEB_PORT:-15173}
RELOAD=${RELOAD:-1}
hub_port=${LISTEN##*:}

in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

for port in "$hub_port" "$WEB_PORT"; do
	if in_use "$port"; then
		echo "dev: port $port sudah dipakai proses lain:" >&2
		ss -ltnp 2>/dev/null | grep -E ":$port\b" >&2
		echo "dev: hentikan proses itu, atau pakai port lain: make dev LISTEN=0.0.0.0:<port> WEB_PORT=<port>" >&2
		exit 1
	fi
done

[ -d web/node_modules ] || (cd web && npm ci) || exit 1

# go:embed needs at least one file in dist/ before the first web build.
mkdir -p internal/webui/dist bin/air
touch internal/webui/dist/.gitkeep
rm -f bin/air/hub.pid # the port check above found no hub

if [ "$RELOAD" = 0 ]; then
	go build -o bin/nextboard-dev ./cmd/nextboard || exit 1
	hub=(bin/nextboard-dev serve --dev --dev-web-port "$WEB_PORT" --data-dir "$DATA" --listen "$LISTEN")
	go_reload="mati"
else
	air=$(command -v air || echo "$(go env GOPATH)/bin/air")
	if [ ! -x "$air" ]; then
		echo "dev: air tidak ditemukan. Pasang: go install github.com/air-verse/air@latest" >&2
		echo "dev: atau jalankan tanpa hot reload Go: make dev RELOAD=0" >&2
		exit 1
	fi
	export DATA LISTEN WEB_PORT NEXTBOARD_DEV_PID=$$
	hub=("$air" -c .air.toml)
	go_reload="air; tiap restart hub, konsol lokal ikut restart (make dev RELOAD=0 untuk mematikan)"
fi

pids=()
stop() {
	trap '' INT TERM # a second Ctrl-C must not abandon the hub's graceful shutdown
	kill -TERM "${pids[@]}" 2>/dev/null
	for _ in {1..100}; do
		kill -0 "${pids[@]}" 2>/dev/null || break
		sleep 0.1
	done
	kill -KILL "${pids[@]}" 2>/dev/null
	wait 2>/dev/null
	# With air the hub runs on its own (scripts/dev-hub.sh).
	if [ "$RELOAD" != 0 ] && [ -s bin/air/hub.pid ]; then
		hub_pid=$(cat bin/air/hub.pid)
		kill -TERM "$hub_pid" 2>/dev/null
		for _ in {1..150}; do
			kill -0 "$hub_pid" 2>/dev/null || break
			sleep 0.1
		done
		kill -KILL "$hub_pid" 2>/dev/null
	fi
}
trap 'stop; exit 0' INT TERM

echo "NextBoard dev"
echo "  Web (Vite, hot reload): http://localhost:$WEB_PORT"
echo "  Hub:                    http://localhost:$hub_port  (listen $LISTEN)"
echo "  Hot reload Go:          $go_reload"
echo "  Data:                   $DATA"
echo "  Ctrl-C untuk berhenti."
echo

"${hub[@]}" &
pids+=($!)
(cd web && NEXTBOARD_HUB="http://localhost:$hub_port" exec node_modules/.bin/vite --port "$WEB_PORT" --clearScreen false) &
pids+=($!)

while kill -0 "${pids[0]}" 2>/dev/null && kill -0 "${pids[1]}" 2>/dev/null; do
	sleep 0.5
done
echo "dev: hub atau Vite berhenti; menghentikan yang lain." >&2
stop
exit 1
