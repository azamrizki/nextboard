#!/usr/bin/env bash
# The agent of the product demo (web/demo): a scripted console, no model, no account, no network.
# It only prints, and answers what is typed with canned work, so that the board shows each state
# an agent can be in: working, waiting for an answer, ready.
role=${1:-tester}
B=$'\033[1m' D=$'\033[2m' R=$'\033[0m' G=$'\033[32m' Y=$'\033[33m' C=$'\033[36m' M=$'\033[35m'
SPIN='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'

banner() {
  printf '%s◆ demo agent%s %s· %s · acme-shop%s\n\n' "$B$M" "$R" "$D" "$1" "$R"
}

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
    room=$(($(stty size 2>/dev/null | cut -d' ' -f2 || echo 80) - 22))
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

reply() {
  printf '\n'
  work 6 'Working on it…' \
    "${D}read${R}  src/checkout.ts, src/cart.ts" \
    "${D}plan${R}  validate the code, apply it to the total, show it in the summary" \
    "${Y}edit${R}  src/checkout.ts ${G}+14${R} ${D}-3${R}" \
    "${Y}add${R}   src/coupons.ts ${G}+17${R}" \
    "${C}run${R}   npm test  ${G}✓ 46 passed${R}"
  [ -d "$DEMO_PATCH" ] && cp -r "$DEMO_PATCH"/. .
  printf '%s●%s Done. Coupons work at checkout, with a test for an expired code.\n' "$G" "$R"
  printf '  %s2 files changed · review them in the Code card%s\n' "$D" "$R"
}

case $role in
builder)
  banner 'builder'
  printf '%s›%s Add order history to the account page\n\n' "$D" "$R"
  printf '  %sread%s  src/account/page.tsx, src/api/orders.ts\n' "$D" "$R"
  printf '  %splan%s  1 list the orders · 2 paginate · 3 empty state · 4 tests\n' "$D" "$R"
  printf '  %sedit%s  src/api/orders.ts %s+42%s\n' "$Y" "$R" "$G" "$R"
  while :; do
    work 14 'Writing the order list…' \
      "${Y}edit${R}  src/account/OrderList.tsx ${G}+87${R}" \
      "${Y}edit${R}  src/account/page.tsx ${G}+9${R} ${D}-1${R}" \
      "${C}run${R}   npm run typecheck  ${G}✓${R}"
    work 14 'Adding pagination…' \
      "${Y}edit${R}  src/api/orders.ts ${G}+16${R} ${D}-4${R}" \
      "${C}run${R}   npm test -- orders  ${G}✓ 12 passed${R}"
  done
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
  printf '\033[3B\r\033[K\n%s●%s Applied. The tests use $50 now.\n' "$G" "$R"
  prompt
  exec cat >/dev/null
  ;;
*)
  banner 'tester'
  printf '%s●%s Ready. All 44 tests pass on main.\n' "$G" "$R"
  prompt
  while IFS= read -r line; do
    [ -z "$line" ] && { prompt; continue; }
    reply
    prompt
  done
  ;;
esac
