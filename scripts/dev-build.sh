#!/usr/bin/env bash
# Air's build step in `make dev`. Air runs dev-hub.sh again after every build, failed or not;
# the hub only restarts when the binary it finds has changed.
set -u

pidfile=bin/air/build.pid
out=bin/air/nextboard-dev.$$
echo $$ >"$pidfile"

# Every save links a new binary; /tmp may be small or shared.
export GOTMPDIR=$PWD/bin/air/tmp
mkdir -p "$GOTMPDIR"

tree() { find cmd internal -name '*.go' -not -name '*_test.go' -printf '%T@ %p\n' | sort | cksum; }

# Several files are usually saved together.
before=$(tree)
sleep 2
while [ "$before" != "$(tree)" ]; do
	before=$(tree)
	sleep 2
done

# The commit the hub reports (About this hub). -buildvcs=false keeps a commit out of the binary
# otherwise; this is read only when air builds, so a commit alone rebuilds nothing. No build
# time: it would change the binary on every build, and dev-hub.sh restarts the hub whenever the
# binary changes. The hub takes the binary's file time instead.
commit=$(git rev-parse --short HEAD 2>/dev/null) && { git diff --quiet HEAD -- 2>/dev/null || commit="$commit-dirty"; }
if ! go build -buildvcs=false -ldflags "-X github.com/azamrizki/nextboard/internal/version.Commit=$commit" -o "$out" ./cmd/nextboard; then
	rm -f "$out"
	echo "dev: build gagal; hub tetap jalan dengan kode yang terakhir berhasil dibuild." >&2
	exit 1
fi
# A later save may have built already; older code must not replace it.
if [ "$(cat "$pidfile")" != "$$" ]; then
	rm -f "$out"
	exit 1
fi
mv -f "$out" bin/nextboard-dev
