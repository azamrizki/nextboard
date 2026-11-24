#!/usr/bin/env bash
# NextCode's engine in the product demo (web/demo): a stand-in for Claude Code headless, stream-json
# in and out, as web/e2e/fake-nextcode.sh. No model, no account, no network. Its first message gets
# one scripted piece of work on acme-shop: it reads, plans, waits for the owner's decision on the
# plan, edits, runs the tests and reports. Anything after that gets "Done: <text>".
sid=-
prev=
for a in "$@"; do
  case "$prev" in --session-id | --resume) sid=$a ;; esac
  prev=$a
done
here=$PWD
field() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p"; }
ok() {
  local r='{}'
  [ -n "$2" ] && r=$2
  printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":%s}}\n' "$(field "$1" request_id)" "$r"
}
# the hub's control requests, whenever they come
ctl() {
  case "$1" in
  *'"subtype":"initialize"'*) ok "$1" '{"commands":[{"name":"review"},{"name":"compact"}]}' ;;
  *'"subtype":"set_permission_mode"'* | *'"subtype":"interrupt"'* | *'"subtype":"set_model"'*) ok "$1" ;;
  esac
}
# a pause that still answers the hub meanwhile
pause() { while IFS= read -r -t "$1" l; do ctl "$l"; done; }
ev() { printf '{"type":"stream_event","event":%s,"parent_tool_use_id":null,"session_id":"%s"}\n' "$1" "$sid"; }
tool() { printf '{"type":"assistant","message":{"id":"m_%s","content":[{"type":"tool_use","id":"%s","name":"%s","input":%s}]},"parent_tool_use_id":null}\n' "$1" "$1" "$2" "$3"; }
result() { printf '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"%s","content":%s}]},"parent_tool_use_id":null}\n' "$1" "$2"; }
text() { printf '{"type":"assistant","message":{"id":"%s","content":[{"type":"text","text":%s}]},"parent_tool_use_id":null}\n' "$1" "$2"; }
# a text streamed in a few parts, then whole: $1 id, $2 the whole as JSON, $3... the parts as JSON
stream() {
  local id=$1 whole=$2
  shift 2
  ev "{\"type\":\"message_start\",\"message\":{\"id\":\"$id\"}}"
  ev '{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}'
  for part in "$@"; do
    ev "{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":$part}}"
    pause 0.25
  done
  ev '{"type":"content_block_stop","index":0}'
  text "$id" "$whole"
}

PLAN=$(cat <<'EOF'
{"plan":"## One use per customer for coupons\n\n1. Read the codes a customer already used from their orders (`src/api/orders.ts`).\n2. Add `alreadyUsed()` to `src/coupons.ts`; checkout gives no discount for a code used before.\n3. The order summary says *Already used* instead of *Invalid code*.\n4. Add `src/coupons.test.ts`: a second use of `SPRING10` is refused.\n\n**Check:** `npm test` and `npm run typecheck`"}
EOF
)
READ=$(cat <<'EOF'
"     1\texport interface Coupon {\n     2\t  code: string;\n     3\t  kind: 'percent' | 'amount';\n     4\t  value: number;\n     5\t  expires: string;\n     6\t}\n     7\t\n     8\tconst COUPONS: Coupon[] = [\n     9\t  { code: 'SPRING10', kind: 'percent', value: 10, expires: '2027-05-31' },\n    10\t  { code: 'WELCOME5', kind: 'amount', value: 500, expires: '2027-12-31' },\n    11\t];\n    12\t\n    13\texport function findCoupon(code: string, now: Date): Coupon | undefined {\n    14\t  const c = COUPONS.find((c) => c.code === code.trim().toUpperCase());\n    15\t  return c && new Date(c.expires) >= now ? c : undefined;\n    16\t}\n"
EOF
)
GREP=$(cat <<'EOF'
"src/checkout.ts:3:import { Coupon, findCoupon } from './coupons';\nsrc/checkout.ts:16:  const coupon = code ? findCoupon(code, now) : undefined;\nsrc/api/orders.ts:4:  coupon?: string;"
EOF
)
EDIT1=$(cat <<EOF
{"file_path":"$here/src/coupons.ts","old_string":"  return c && new Date(c.expires) >= now ? c : undefined;\n}","new_string":"  return c && new Date(c.expires) >= now ? c : undefined;\n}\n\n/** A code this customer used before: it works once per customer. */\nexport function alreadyUsed(code: string, usedCodes: string[]): boolean {\n  return usedCodes.includes(code.trim().toUpperCase());\n}"}
EOF
)
EDIT2=$(cat <<EOF
{"file_path":"$here/src/checkout.ts","old_string":"export function summarize(cart: Cart, code?: string, now = new Date()): Summary {\n  const sub = subtotal(cart);\n  const coupon = code ? findCoupon(code, now) : undefined;","new_string":"export function summarize(cart: Cart, code?: string, now = new Date(), usedCodes: string[] = []): Summary {\n  const sub = subtotal(cart);\n  const used = code ? alreadyUsed(code, usedCodes) : false;\n  const coupon = code && !used ? findCoupon(code, now) : undefined;"}
EOF
)
WRITE=$(cat <<EOF
{"file_path":"$here/src/coupons.test.ts","content":"import { expect, test } from 'vitest';\nimport { summarize } from './checkout';\n\nconst cart = { lines: [{ sku: 'pour-over', name: 'Pour-over set', price: 3400, qty: 1 }] };\n\ntest('a coupon works once per customer', () => {\n  expect(summarize(cart, 'SPRING10').discount).toBe(340);\n  expect(summarize(cart, 'SPRING10', new Date(), ['SPRING10']).discount).toBe(0);\n});\n"}
EOF
)
TESTS=$(cat <<'EOF'
"\n RUN  v3.2.4\n\n ✓ src/coupons.test.ts (1 test) 3ms\n ✓ src/checkout.test.ts (6 tests) 5ms\n ✓ src/cart.test.ts (8 tests) 4ms\n ✓ src/shipping/rates.test.ts (2 tests) 2ms\n ✓ src/account/orders.test.ts (12 tests) 9ms\n\n Test Files  9 passed (9)\n      Tests  47 passed (47)\n   Duration  1.21s\n\n> acme-shop@ typecheck\n> tsc --noEmit\n"
EOF
)
todos() { # the three steps, each "pending", "in_progress" or "completed"
  printf '{"todos":[{"content":"alreadyUsed() in coupons.ts","activeForm":"Adding alreadyUsed()","status":"%s"},{"content":"Checkout refuses a used code","activeForm":"Changing checkout","status":"%s"},{"content":"A test for a second use","activeForm":"Writing the test","status":"%s"}]}' "$1" "$2" "$3"
}

work() {
  local m=$1
  pause 0.6
  text "${m}a" '"I will look at how a code is checked now, and where checkout uses it."'
  tool r1 Read "{\"file_path\":\"$here/src/coupons.ts\"}"
  pause 0.5
  result r1 "$READ"
  tool g1 Grep '{"pattern":"findCoupon|coupon\\?","path":"src","output_mode":"content","-n":true}'
  pause 0.5
  result g1 "$GREP"
  pause 0.7
  tool p1 ExitPlanMode "$PLAN"
  printf '{"type":"control_request","request_id":"req_p1","request":{"subtype":"can_use_tool","tool_name":"ExitPlanMode","input":%s,"tool_use_id":"p1"}}\n' "$PLAN"
  local ans
  while IFS= read -r ans; do
    case "$ans" in *'"type":"control_response"'*) break ;; *) ctl "$ans" ;; esac
  done
  case "$ans" in
  *'"behavior":"allow"'*) ;;
  *)
    result p1 "\"$(field "$ans" message)\""
    text "${m}b" '"Understood. I will wait for your note before changing anything."'
    return
    ;;
  esac
  result p1 '"User has approved your plan. You can now start coding."'
  pause 0.5
  tool t1 TodoWrite "$(todos in_progress pending pending)"
  result t1 '"Todos have been modified successfully."'
  pause 0.6
  tool e1 Edit "$EDIT1"
  pause 0.5
  result e1 "\"The file $here/src/coupons.ts has been updated.\""
  pause 0.6
  tool e2 Edit "$EDIT2"
  pause 0.5
  result e2 "\"The file $here/src/checkout.ts has been updated.\""
  pause 0.6
  tool w1 Write "$WRITE"
  pause 0.4
  result w1 "\"File created successfully at: $here/src/coupons.test.ts\""
  pause 0.4
  tool b1 Bash '{"command":"npm test && npm run typecheck","description":"Run the tests and the type check"}'
  pause 1.6
  result b1 "$TESTS"
  tool t4 TodoWrite "$(todos completed completed completed)"
  result t4 '"Todos have been modified successfully."'
  pause 0.4
  stream "${m}c" \
    '"Done: **a coupon now works once per customer.**\n\n- `alreadyUsed()` in `src/coupons.ts` checks the codes a customer used before.\n- `summarize()` takes those codes, and a used one gives no discount.\n- `src/coupons.test.ts` covers a second use of `SPRING10`.\n\nAll **47** tests pass and the types check."' \
    '"Done: **a coupon now works once per customer.**\n\n"' \
    '"- `alreadyUsed()` in `src/coupons.ts` checks the codes a customer used before.\n"' \
    '"- `summarize()` takes those codes, and a used one gives no discount.\n"' \
    '"- `src/coupons.test.ts` covers a second use of `SPRING10`.\n\n"' \
    '"All **47** tests pass and the types check."'
}

inited=0
n=0
while IFS= read -r line; do
  case "$line" in
  *'"type":"user"'*)
    if [ $inited = 0 ]; then
      inited=1
      printf '{"type":"system","subtype":"init","session_id":"%s","model":"claude-sonnet-5-5","permissionMode":"default"}\n' "$sid"
    fi
    n=$((n + 1))
    msg="msg_$$_$n"
    if [ $n = 1 ]; then
      work "$msg"
      cost=0.184
    else
      t=$(field "$line" text)
      stream "$msg" "\"Done: $t\"" '"Done: "' "\"$t\""
      cost=0.012
    fi
    printf '{"type":"result","subtype":"success","is_error":false,"result":"","session_id":"%s","total_cost_usd":%s}\n' "$sid" "$cost"
    ;;
  *) ctl "$line" ;;
  esac
done
