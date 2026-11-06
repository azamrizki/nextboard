#!/bin/sh
# Stands in for an agent CLI in the browser tests: shows a prompt, never calls anything. It leaves
# its hook token in its folder, so that a test can call the hub's tools as this console would.
[ -n "$NB_TOKEN" ] && printf '%s\n%s\n' "$NB_TOKEN" "$NB_HOOK_URL" > .nb-e2e-console
printf '> '
exec sleep 600
