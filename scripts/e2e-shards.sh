#!/usr/bin/env bash
# The browser tests split over several throwaway hubs side by side, instead of one after another
# on one hub (the suite took ~30 min that way). Each shard is its own Playwright run with
# --shard=i/N and its own hub: its port (NEXTBOARD_E2E_PORT), so its own data folder, login state,
# results folder and hub log (e2e/global-setup.ts). The tests of one shard still run one at a
# time on its hub, as they share its settings.
#
#   scripts/e2e-shards.sh [SHARDS] [-- PLAYWRIGHT ARGS]
#
# SHARDS defaults to a third of the CPUs, at most 6: more makes the tests flaky (they time
# animations and debounced saves). NEXTBOARD_E2E_BIN is the hub to test (`make test-e2e-fast`
# builds it). The output of every shard goes to $TMPDIR/nextboard-e2e-shard-<i>.log; a failed
# shard's summary is printed at the end, and the exit status is 1 when any shard failed.
set -uo pipefail

cpus=$(nproc 2>/dev/null || echo 4)
shards=$(( cpus / 3 ))
if [ $# -gt 0 ] && [ "$1" != -- ]; then shards=$1; shift; fi
if [ $# -gt 0 ] && [ "$1" = -- ]; then shift; fi
[ "$shards" -ge 1 ] || shards=1
[ "$shards" -le 6 ] || shards=6
: "${NEXTBOARD_E2E_BIN:?the hub binary to test; make test-e2e-fast sets it}"
base=${NEXTBOARD_E2E_PORT:-12398}
tmp=${TMPDIR:-/tmp}
cd "$(dirname "$0")/../web"

# The database browser's spec needs a PostgreSQL and a Redis (web/e2e/db-browser.spec.ts):
# throwaway containers started here, before any shard's browser runs (a container started while
# a page loads fails it with ERR_NETWORK_CHANGED), and removed at the end. Only these two.
db_pg_image=postgres:18.6-alpine
db_redis_image=redis:8.4.8-alpine
db_name="nb-dbtest-e2e-shards-$$"
db_started=
db_stop() { [ -n "$db_started" ] && docker rm -f "$db_name-pg" "$db_name-rd" >/dev/null 2>&1; }
trap db_stop EXIT
if command -v docker >/dev/null 2>&1 && docker image inspect "$db_pg_image" "$db_redis_image" >/dev/null 2>&1; then
	db_pw="e2e-only-$RANDOM$RANDOM"
	if docker run -d --rm --name "$db_name-pg" -e POSTGRES_PASSWORD="$db_pw" -p 127.0.0.1::5432 "$db_pg_image" >/dev/null &&
		docker run -d --rm --name "$db_name-rd" -p 127.0.0.1::6379 "$db_redis_image" >/dev/null; then
		db_started=1
		db_pg_port=$(docker port "$db_name-pg" 5432/tcp | head -1 | sed 's/.*://')
		db_redis_port=$(docker port "$db_name-rd" 6379/tcp | head -1 | sed 's/.*://')
		for _ in $(seq 1 120); do
			[ "$(docker exec "$db_name-pg" psql -h 127.0.0.1 -U postgres -tAc 'SELECT 1' 2>/dev/null)" = 1 ] && break
			sleep 0.5
		done
		export NB_E2E_DB="{\"pg\":\"$db_name-pg\",\"pgPort\":$db_pg_port,\"redis\":\"$db_name-rd\",\"redisPort\":$db_redis_port,\"pw\":\"$db_pw\"}"
		sleep 3
		echo "e2e: PostgreSQL and Redis for the database browser in containers $db_name-*"
	fi
else
	echo "e2e: no Docker with $db_pg_image and $db_redis_image: the database browser's spec is skipped"
fi

echo "e2e: $shards shards side by side"
start=$(date +%s)
pids=()
for i in $(seq 1 "$shards"); do
	port=$(( base + 10 * i ))
	log="$tmp/nextboard-e2e-shard-$i.log"
	NEXTBOARD_E2E_PORT=$port npx playwright test -c e2e/playwright.config.ts --shard="$i/$shards" "$@" >"$log" 2>&1 &
	pids+=($!)
done

failed=()
for i in $(seq 1 "$shards"); do
	if wait "${pids[$((i - 1))]}"; then
		echo "e2e: shard $i/$shards passed ($(grep -oE '[0-9]+ passed' "$tmp/nextboard-e2e-shard-$i.log" | tail -1))"
	else
		failed+=("$i")
	fi
done
for i in "${failed[@]}"; do
	echo
	echo "e2e: shard $i/$shards FAILED ($tmp/nextboard-e2e-shard-$i.log):"
	# Playwright's summary: the failed tests and the counts
	sed -n '/^  [0-9]* failed/,$p' "$tmp/nextboard-e2e-shard-$i.log"
done
echo "e2e: $(( ($(date +%s) - start) / 60 )) min $(( ($(date +%s) - start) % 60 )) s"
[ ${#failed[@]} -eq 0 ]
