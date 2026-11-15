#!/bin/sh
# Stands in for the Codex CLI on the browser tests' hub (global-setup puts it first on PATH), so
# that no test ever reaches the real one or an account. `codex features list` says that image
# generation is on; `codex exec` "makes" an image as Codex does: a small PNG under
# $CODEX_HOME/generated_images (~/.codex by default), whose path it prints. Everything else does
# nothing.
#
# FAKE_CODEX_IMAGES=false: the image_generation feature is off.
# FAKE_CODEX_DELAY: seconds `exec` takes (default 2), so that a test sees the job running.
PNG='iVBORw0KGgoAAAANSUhEUgAAAGAAAABACAIAAABqVuVZAAABJUlEQVR42u3QWRHCQBAFwMhBBN9oihzUoSNYyO7MuyZVraCP1+cc5fd933I8O0/Q+s6goLWdKUHLOyOCdnbygzZ3woP2d5KDSnZig6p2MoMKdwKDanfSgsp3ooI6dnKCmnZCgvp2EoJad+yDune8gwA7xkGYHdcg2I5lEHLHLwi8YxaE33EKouzYBLF2PIKIOwZB3B31IPqOdJDCjm6QyI5okM6OYpDUjlyQ2o5WkOCOUJDmjkqQ7I5EkPIOP0h8hxykv8MMstihBbnscIKMdghBXjvoILsdaJDjDi7IdAcU5LuDCLLeaQ9y3+kNCthpDMrY6QqK2WkJStqpDwrbKQ7K26kMitwpC0rdqQkK3ikIyt7ZDYrf2QqasLMeNGRnMWjOzt8FmWDGUt6CZ6MAAAAASUVORK5CYII='

case "$1" in
--version | -V)
  echo 'codex-cli 0.0.0-e2e'
  ;;
features)
  printf '%-28s %-12s %s\n' undo stable false
  printf '%-28s %-12s %s\n' image_generation stable "${FAKE_CODEX_IMAGES:-true}"
  printf '%-28s %-12s %s\n' web_search_request stable true
  ;;
exec)
  shift
  out=''
  while [ $# -gt 0 ]; do
    case "$1" in
    --help | -h)
      echo 'Usage: codex exec [OPTIONS] [PROMPT]  (e2e stand-in: --color, -o, -C)'
      exit 0
      ;;
    -o) out="$2" && shift ;;
    esac
    shift
  done
  # a prompt on stdin is read and dropped; one in the arguments is never looked at
  [ -t 0 ] || timeout 5 cat >/dev/null
  dir="${CODEX_HOME:-$HOME/.codex}/generated_images/e2e-$$"
  mkdir -p "$dir"
  printf '%s' "$PNG" | base64 -d >"$dir/ig_e2e$$.png"
  sleep "${FAKE_CODEX_DELAY:-2}"
  [ -n "$out" ] && echo "Made the image: $dir/ig_e2e$$.png" >"$out"
  echo "Made the image: $dir/ig_e2e$$.png"
  ;;
esac
exit 0
