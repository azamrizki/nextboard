#!/usr/bin/env bash
# The release workflow's split of one slow Go package (internal/server, internal/machine) over
# several runners, so its tests run side by side instead of one after another.
#
#   scripts/ci-shard.sh PACKAGE SHARDS [SHARD] [-- BUILD FLAGS]
#
# With SHARD (1..SHARDS) it prints that shard's -run pattern, ^(TestA|TestB|…)$; without it, every
# shard's tests. Either way it first builds the test binary and checks the split against it: each
# shard's pattern, given to the binary's own -test.list, must select exactly that shard's names,
# and the shards together every test, example and fuzz target of the package, each once. The
# split is by name (sorted, dealt out in turn), so every runner works it out the same, and a test
# added later lands in a shard by itself. BUILD FLAGS (such as -race) should match the go test
# that follows, so its build comes from the same cache.
set -euo pipefail

usage() { echo "usage: $0 PACKAGE SHARDS [SHARD] [-- BUILD FLAGS]" >&2; exit 2; }
[ $# -ge 2 ] || usage
pkg=$1 shards=$2
shift 2
shard=
if [ $# -gt 0 ] && [ "$1" != -- ]; then shard=$1; shift; fi
if [ $# -gt 0 ] && [ "$1" = -- ]; then shift; fi
[[ $shards =~ ^[1-9][0-9]*$ ]] || usage
if [ -n "$shard" ] && { ! [[ $shard =~ ^[1-9][0-9]*$ ]] || [ "$shard" -gt "$shards" ]; }; then usage; fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
dir=$(go list -f '{{.Dir}}' "$pkg")
go test "$@" -c -o "$tmp/pkg.test" "$pkg" >&2
[ -x "$tmp/pkg.test" ] || { echo "ci-shard: $pkg has no tests" >&2; exit 1; }

# Names as the binary lists them, from the package folder like go test would. Benchmarks run only
# with -bench, so they are not part of any shard; -run selects tests, examples and fuzz targets.
list() {
	if ! (cd "$dir" && "$tmp/pkg.test" -test.list "$1") >"$tmp/out" 2>"$tmp/err"; then
		echo "ci-shard: listing the tests of $pkg failed:" >&2
		cat "$tmp/out" "$tmp/err" >&2
		exit 1
	fi
	grep -E '^(Test|Example|Fuzz)' "$tmp/out" | LC_ALL=C sort || true # grep finds none: empty
}
list '.*' >"$tmp/all"
[ -s "$tmp/all" ] || { echo "ci-shard: $pkg lists no tests" >&2; exit 1; }
if grep -vqE '^[[:alnum:]_]+$' "$tmp/all"; then
	echo "ci-shard: a name that is not a plain identifier:" >&2
	grep -vE '^[[:alnum:]_]+$' "$tmp/all" >&2
	exit 1
fi

pattern() { # the -run pattern of a file of names; ^$ selects nothing
	if [ -s "$1" ]; then echo "^($(paste -sd'|' "$1"))$"; else echo '^$'; fi
}
for ((k = 1; k <= shards; k++)); do
	awk -v k="$k" -v n="$shards" '(NR - 1) % n == k - 1' "$tmp/all" >"$tmp/shard$k"
	list "$(pattern "$tmp/shard$k")" >"$tmp/got$k"
	if ! cmp -s "$tmp/shard$k" "$tmp/got$k"; then
		echo "ci-shard: shard $k/$shards of $pkg selects other tests than it should:" >&2
		diff "$tmp/shard$k" "$tmp/got$k" >&2 || true
		exit 1
	fi
done
cat "$tmp"/got* | LC_ALL=C sort >"$tmp/union"
if ! cmp -s "$tmp/all" "$tmp/union"; then
	echo "ci-shard: the shards of $pkg do not cover each test exactly once:" >&2
	diff "$tmp/all" "$tmp/union" >&2 || true
	exit 1
fi

if [ -n "$shard" ]; then
	pattern "$tmp/shard$shard"
	exit 0
fi
for ((k = 1; k <= shards; k++)); do
	echo "shard $k/$shards: $(wc -l <"$tmp/shard$k") tests"
	sed 's/^/  /' "$tmp/shard$k"
done
echo "ok: $(wc -l <"$tmp/all") tests of $pkg in $shards shards, each once"
