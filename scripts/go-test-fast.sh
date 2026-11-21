#!/usr/bin/env bash
# `make test`'s Go tests side by side, as the release workflow runs them: internal/server took
# ~12 min of the race run on its own, so it (and internal/machine) is split by test name
# (scripts/ci-shard.sh) and its shards run at the same time as the other packages.
#
#   scripts/go-test-fast.sh [SERVER_SHARDS]
#
# Everything is compiled first, so the tests do not share the CPUs with the compiler (a few of
# them time a debounced save or a timeout). Each run's output goes to
# $TMPDIR/nextboard-go-<run>.log; a failed run's failures are printed at the end, and the exit
# status is 1 when any failed.
set -uo pipefail
cd "$(dirname "$0")/.."

shards=${1:-4}
mod=$(go list -m)
tmp=${TMPDIR:-/tmp}
start=$(date +%s)

go vet ./... || exit 1
# every test binary into the build cache, running none
go test -race -count=1 -run '^$' ./... >"$tmp/nextboard-go-build.log" 2>&1 || { cat "$tmp/nextboard-go-build.log"; exit 1; }

names=() pids=()
run() { # NAME ARGS…: one go test in the background
	local name=$1
	shift
	go test -race -count=1 -timeout 30m "$@" >"$tmp/nextboard-go-$name.log" 2>&1 &
	names+=("$name") pids+=($!)
}
shard() { # PACKAGE N: its N shards
	local pkg=$1 n=$2 k pattern
	for ((k = 1; k <= n; k++)); do
		pattern=$(scripts/ci-shard.sh "$pkg" "$n" "$k" -- -race) || exit 1
		run "$(basename "$pkg")-$k" -run "$pattern" "$pkg"
	done
}

others=$(go list ./... | grep -vxF -e "$mod/internal/server" -e "$mod/internal/machine")
# shellcheck disable=SC2086 # one word per package
run others -p 4 $others
shard ./internal/server "$shards"
shard ./internal/machine 2

failed=()
for i in "${!pids[@]}"; do
	if wait "${pids[$i]}"; then
		echo "go test: ${names[$i]} ok"
		continue
	fi
	# A few tests time a delivery or a debounce and can miss it under this load: the failed tests
	# of a run go once more, alone. Passing then, they are named as flaky; a build error, a panic
	# or a test failing again fails the run.
	log="$tmp/nextboard-go-${names[$i]}.log"
	tests=$(grep -oE '^--- FAIL: [A-Za-z0-9_]+' "$log" | awk '{print $3}' | sort -u | paste -sd'|')
	pkgs=$(grep -oE "^FAIL[[:space:]]+$mod/[^[:space:]]+" "$log" | awk '{print $2}' | sort -u)
	# one package only, so the names are its own; and the second try must have run them
	if [ -n "$tests" ] && [ "$(wc -w <<<"$pkgs")" -eq 1 ] && ! grep -q '^panic:' "$log" &&
		go test -race -count=1 -timeout 30m -run "^($tests)\$" "$pkgs" >"$log.again" 2>&1 &&
		! grep -q 'no tests to run' "$log.again"; then
		echo "go test: ${names[$i]} ok on a second try of $tests alone (flaky under load)"
	else
		failed+=("${names[$i]}")
	fi
done
for name in "${failed[@]}"; do
	echo
	echo "go test: $name FAILED ($tmp/nextboard-go-$name.log):"
	# each failed test with what it said, a panic, or a build error
	grep -E -A12 '^\s*--- FAIL|^panic:|^FAIL|cannot|undefined' "$tmp/nextboard-go-$name.log" | grep -vE '^[0-9]{4}/[0-9]{2}/[0-9]{2} ' | head -80
done
echo "go test: $(( ($(date +%s) - start) / 60 )) min $(( ($(date +%s) - start) % 60 )) s"
[ ${#failed[@]} -eq 0 ]
