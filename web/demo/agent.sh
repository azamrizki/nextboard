#!/usr/bin/env bash
# The agent of the product demo (web/demo): a scripted console, no model, no account, no network.
# It only prints, and answers what is typed with canned work, so that the board shows each state
# an agent can be in: working, waiting for an answer, ready, asleep.
#
# The tester and the writer are registered on Claude Code's base (setup.ts), so the hub starts
# them with a conversation id. They then speak to the hub as Claude Code does: its input box, its
# hooks (NB_HOOK_SH) and a transcript, from which the hub takes the report it posts to the thread
# and learns which files the agent wrote (Review). The conversation id is also what lets a console
# sleep and resume. The others are plain CLIs, read from their screen.
role=${1:-tester}
shift
sid=""
while [ $# -gt 0 ]; do
  case "$1" in --session-id | --resume)
    sid="$2"
    shift
    ;;
  esac
  shift
done
B=$'\033[1m' D=$'\033[2m' R=$'\033[0m' G=$'\033[32m' Y=$'\033[33m' C=$'\033[36m' M=$'\033[35m'
SPIN='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'
project=$(basename "$PWD")
# how long the builder and QA keep working before they report (seconds since they started)
BUSY=${DEMO_BUSY:-100}

banner() {
  printf '%s◆ demo agent%s %s· %s · %s%s\n\n' "$B$M" "$R" "$D" "$1" "$project" "$R"
}

cols() { stty size 2>/dev/null | cut -d' ' -f2 || echo 80; }

# work <seconds> <label> <line>...: prints the lines one by one under a spinner.
work() {
  local secs=$1 label=$2
  shift 2
  local lines=("$@") n=${#lines[@]} t=0 i=0 steps
  steps=$((secs * 10))
  while [ "$t" -lt "$steps" ]; do
    if [ "$n" -gt 0 ] && [ $((t % (steps / (n + 1) + 1))) -eq 0 ] && [ "$i" -lt "$n" ] && [ "$t" -gt 0 ]; then
      printf '\r\033[K  %s\n' "${lines[$i]}"
      i=$((i + 1))
    fi
    # one row, however narrow the screen (a phone resizes the console): the label gives way
    room=$(($(cols) - 22))
    [ "$room" -lt 4 ] && room=4
    printf '\r\033[K%s%s%s %s %s(esc to interrupt)%s' "$C" "${SPIN:$((t % 10)):1}" "$R" "${label:0:$room}" "$D" "$R"
    sleep 0.1
    t=$((t + 1))
  done
  while [ "$i" -lt "$n" ]; do
    printf '\r\033[K  %s\n' "${lines[$i]}"
    i=$((i + 1))
  done
  printf '\r\033[K'
}

prompt() { printf '\n%s>%s ' "$B" "$R"; }

# ---- as Claude Code: hooks, a transcript, the input box

transcript="$HOME/.claude/projects/demo/$sid.jsonl"
json() {
  local s=${1//\\/\\\\}
  printf '%s' "${s//\"/\\\"}"
}
now() { date -u +%Y-%m-%dT%H:%M:%S.000Z; }
hook() { [ -n "$NB_HOOK_SH" ] && printf '%s' "$2" | sh -c "set -- hook $1; eval \"\$NB_HOOK_SH\""; }
said() { # said <user|assistant> <text>: a line of the transcript
  local id
  id=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
  if [ "$1" = user ]; then
    printf '{"type":"user","uuid":"u-%s","sessionId":"%s","timestamp":"%s","isSidechain":false,"message":{"role":"user","content":"%s"}}\n' "$id" "$sid" "$(now)" "$(json "$2")"
  else
    printf '{"type":"assistant","uuid":"a-%s","sessionId":"%s","timestamp":"%s","isSidechain":false,"message":{"id":"msg_%s","role":"assistant","content":[{"type":"text","text":"%s"}]}}\n' "$id" "$sid" "$(now)" "$id" "$(json "$2")"
  fi >>"$transcript"
}
edited() { # edited <file>: the hooks Claude Code fires around an edit, which name the file
  local f="{\"file_path\":\"$PWD/$1\"}"
  hook PreToolUse "{\"hook_event_name\":\"PreToolUse\",\"session_id\":\"$sid\",\"tool_name\":\"Edit\",\"tool_input\":$f}"
  hook PostToolUse "{\"hook_event_name\":\"PostToolUse\",\"session_id\":\"$sid\",\"tool_name\":\"Edit\",\"tool_input\":$f}"
}
# not the screen's width, as Claude Code's: a tile is sized after the console starts
rule() {
  local n=$(($(cols) - 1))
  [ "$n" -gt 60 ] && n=60
  printf '%s%s%s' "$D" "$(printf '─%.0s' $(seq 1 "$n"))" "$R"
}
# the box with the cursor in it, "❯ " between two rules: the prompt the hub knows Claude Code by
box() { printf '\r\n%s\r\n❯ \r\n%s\r\n  %s⏵⏵ accept edits on%s\033[2A\033[3G' "$(rule)" "$(rule)" "$D" "$R"; }
# what was typed leaves the box for the conversation
unbox() { printf '\033[%dA\r\033[J%s> %s%s\n\n' $((2 + (${#1} + 2) / $(cols))) "$D" "$1" "$R"; }

turn() { # turn <text>: an order, its work, its report
  hook UserPromptSubmit "{\"hook_event_name\":\"UserPromptSubmit\",\"session_id\":\"$sid\"}"
  said user "$1"
  work 3 'Reading the checkout…' \
    "${D}read${R}  src/checkout.ts, src/cart.ts" \
    "${D}plan${R}  validate the code, apply it to the total, show it in the summary"
  [ -d "$DEMO_PATCH" ] && cp -r "$DEMO_PATCH"/. .
  edited src/checkout.ts
  edited src/coupons.ts
  work 4 'Writing coupons…' \
    "${Y}edit${R}  src/checkout.ts ${G}+14${R} ${D}-3${R}" \
    "${Y}add${R}   src/coupons.ts ${G}+17${R}" \
    "${C}run${R}   npm test  ${G}✓ 46 passed${R}"
  said assistant 'Coupon codes work at checkout: SPRING10 takes 10% off, and an expired code is refused, with a test for it. All 46 tests pass. Two files changed, src/checkout.ts and src/coupons.ts, ready for review.'
  printf '%s●%s Coupon codes work at checkout, with a test for an expired code.\n' "$G" "$R"
  printf '  %s2 files changed · 46 tests pass · review them in the Code card%s\n' "$D" "$R"
  hook Stop "{\"hook_event_name\":\"Stop\",\"session_id\":\"$sid\"}"
}

if [ -n "$sid" ]; then
  mkdir -p "$(dirname "$transcript")"
  touch "$transcript"
  hook SessionStart "{\"hook_event_name\":\"SessionStart\",\"session_id\":\"$sid\",\"transcript_path\":\"$transcript\",\"source\":\"startup\"}"
  banner "$role"
  case $role in
  writer) printf '%s●%s Ready. The help pages match the storefront: free shipping from $50.\n' "$G" "$R" ;;
  *) printf '%s●%s Ready. All 44 tests pass on main.\n' "$G" "$R" ;;
  esac
  box
  while IFS= read -r line; do
    if [ -z "$line" ]; then
      printf '\033[1A'
      box
      continue
    fi
    unbox "$line"
    turn "$line"
    box
  done
  exit 0
fi

# ---- plain CLIs, read from their screen

case $role in
builder)
  banner 'builder'
  printf '%s›%s Add order history to the account page\n\n' "$D" "$R"
  printf '  %sread%s  src/account/page.tsx, src/api/orders.ts\n' "$D" "$R"
  printf '  %splan%s  1 list the orders · 2 paginate · 3 empty state · 4 tests\n' "$D" "$R"
  printf '  %sedit%s  src/api/orders.ts %s+42%s\n' "$Y" "$R" "$G" "$R"
  while [ "$SECONDS" -lt "$BUSY" ]; do
    work 14 'Writing the order list…' \
      "${Y}edit${R}  src/account/OrderList.tsx ${G}+87${R}" \
      "${Y}edit${R}  src/account/page.tsx ${G}+9${R} ${D}-1${R}" \
      "${C}run${R}   npm run typecheck  ${G}✓${R}"
    work 14 'Adding pagination…' \
      "${Y}edit${R}  src/api/orders.ts ${G}+16${R} ${D}-4${R}" \
      "${C}run${R}   npm test -- orders  ${G}✓ 12 passed${R}"
  done
  printf '%s●%s Done. Order history is on the account page, 20 orders a page.\n' "$G" "$R"
  prompt
  exec cat >/dev/null
  ;;
reviewer)
  banner 'reviewer'
  printf '%s›%s Review the shipping rates change\n\n' "$D" "$R"
  printf '  %sread%s  src/shipping/rates.ts, src/shipping/rates.test.ts\n' "$D" "$R"
  printf '  %snote%s  free shipping starts at $50, the tests still say $40\n\n' "$Y" "$R"
  printf '  Edit src/shipping/rates.test.ts %s(+3 -3)%s\n' "$D" "$R"
  printf '  Do you want to apply this change?\n'
  printf '  %s❯ 1. Yes%s\n' "$C" "$R"
  printf '    2. No, and tell the agent what to do instead\n'
  printf '\n  %sEnter to confirm · Esc to cancel%s' "$D" "$R"
  printf '\033[3A\r  '
  read -r -s -n1 _
  # the question leaves the screen once answered, as a TUI's dialog does
  printf '\033[2A\r\033[J  %sEdit%s  src/shipping/rates.test.ts %s+3%s %s-3%s\n' "$Y" "$R" "$G" "$R" "$D" "$R"
  printf '%s●%s Applied. The tests use $50 now.\n' "$G" "$R"
  prompt
  exec cat >/dev/null
  ;;
qa)
  banner 'qa'
  printf '%s›%s Run the release checks on the checkout\n\n' "$D" "$R"
  printf '  %splan%s  1 desktop browsers · 2 coupons · 3 shipping · 4 payment sandbox\n' "$D" "$R"
  while [ "$SECONDS" -lt "$BUSY" ]; do
    work 12 'Running the checkout suite…' \
      "${C}run${R}   e2e checkout on Chrome  ${G}✓ 18 passed${R}" \
      "${C}run${R}   e2e checkout on Firefox  ${G}✓ 18 passed${R}"
    work 12 'Checking coupons and shipping…' \
      "${C}run${R}   e2e coupons  ${G}✓ 6 passed${R}" \
      "${C}run${R}   e2e shipping  ${G}✓ 4 passed${R}"
  done
  printf '%s●%s Done. The checkout passes on Chrome and Firefox.\n' "$G" "$R"
  prompt
  exec cat >/dev/null
  ;;
mobile)
  banner 'mobile qa'
  printf '%s›%s Check the release on small screens\n\n' "$D" "$R"
  printf '  %srun%s   e2e on a 375 px phone  %s✓ 31 passed%s\n' "$C" "$R" "$G" "$R"
  printf '  %srun%s   e2e on a tablet  %s✓ 31 passed%s\n' "$C" "$R" "$G" "$R"
  printf '%s●%s Done. Checkout works on phones and tablets.\n' "$G" "$R"
  prompt
  exec cat >/dev/null
  ;;
changelog)
  banner 'changelog'
  printf '%s›%s Draft the changelog for 2.4\n\n' "$D" "$R"
  printf '  %sread%s  git log v2.3.0..main\n' "$D" "$R"
  printf '  %sedit%s  CHANGELOG.md %s+24%s\n' "$Y" "$R" "$G" "$R"
  printf '%s●%s Drafted: coupon codes, order history, free shipping from $50, 3 fixes.\n' "$G" "$R"
  prompt
  exec cat >/dev/null
  ;;
*)
  banner "$role"
  printf '%s●%s Ready.\n' "$G" "$R"
  prompt
  exec cat >/dev/null
  ;;
esac
