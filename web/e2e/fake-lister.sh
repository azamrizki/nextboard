#!/bin/sh
# Stands in for an agent at the project root in the browser tests: like fake-agent.sh it leaves
# its hook token in its folder, and it writes what it finds there to .nb-e2e-root, one line per
# entry: a link with the files it leads to, a part's note folder with its note, or a file.
[ -n "$NB_TOKEN" ] && printf '%s\n%s\n' "$NB_TOKEN" "$NB_HOOK_URL" > .nb-e2e-console
{
  for d in *; do
    if [ -L "$d" ]; then
      printf 'link %s: %s\n' "$d" "$(ls -1 "$d/" | tr '\n' ' ')"
    elif [ -f "$d/NEXTBOARD-PART.md" ]; then
      printf 'note %s: %s\n' "$d" "$(tr '\n' ' ' < "$d/NEXTBOARD-PART.md")"
    else
      printf 'file %s\n' "$d"
    fi
  done
} > .nb-e2e-root.tmp && mv .nb-e2e-root.tmp .nb-e2e-root
printf '> '
exec sleep 600
