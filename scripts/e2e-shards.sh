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
