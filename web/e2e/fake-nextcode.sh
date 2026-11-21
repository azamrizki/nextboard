#!/bin/sh
# Stands in for Claude Code headless under a NextCode console in the browser tests
# (docs/API-agents.md §30): stream-json in and out, permission requests and questions on the same
# pipe, in the shapes of Claude Code 2.1.329. Never calls anything.
#   "run tests"  asks to run `npm test`, then reports the result
#   "ask"        asks a multiple-choice question, then says the answer
#   anything     streams "Done: <text>" in two parts
# Its arguments go to .fake-nextcode-argv.log in its folder, one start per line.
printf '%s\n' "$*" >> .fake-nextcode-argv.log
sid=-; prev=
for a in "$@"; do
  case "$prev" in --session-id|--resume) sid=$a;; esac
  prev=$a
done
field() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p"; }
ev() { printf '{"type":"stream_event","event":%s,"parent_tool_use_id":null,"session_id":"%s"}\n' "$1" "$sid"; }
inited=0
n=0
while IFS= read -r line; do
  case "$line" in
  *'"subtype":"initialize"'*)
    printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{"commands":[{"name":"review"},{"name":"compact"}]}}}\n' "$(field "$line" request_id)";;
  *'"subtype":"set_permission_mode"'*|*'"subtype":"interrupt"'*)
    printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}\n' "$(field "$line" request_id)";;
  *'"type":"user"'*)
    if [ $inited = 0 ]; then inited=1; printf '{"type":"system","subtype":"init","session_id":"%s","model":"claude-sonnet-5-5","permissionMode":"default"}\n' "$sid"; fi
    n=$((n + 1))
    text=$(field "$line" text)
    msg="msg_$$_$n"
    case "$text" in
    "run tests")
      printf '{"type":"assistant","message":{"id":"%s","content":[{"type":"tool_use","id":"toolu_%s","name":"Bash","input":{"command":"npm test","description":"Run the tests"}}]},"parent_tool_use_id":null}\n' "$msg" "$n"
      printf '{"type":"control_request","request_id":"req_%s","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"npm test","description":"Run the tests"},"tool_use_id":"toolu_%s","permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm test"}],"behavior":"allow","destination":"localSettings"}]}}\n' "$n" "$n"
      IFS= read -r ans
      case "$ans" in
      *'"behavior":"allow"'*)
        printf '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%s","content":"Tests: 3 passed, 3 total"}]},"parent_tool_use_id":null}\n' "$n"
        printf '{"type":"assistant","message":{"id":"%sb","content":[{"type":"text","text":"All **3** tests pass."}]},"parent_tool_use_id":null}\n' "$msg";;
      *)
        printf '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%s","content":"%s","is_error":true}]},"parent_tool_use_id":null}\n' "$n" "$(field "$ans" message)"
        printf '{"type":"assistant","message":{"id":"%sb","content":[{"type":"text","text":"Understood, I did not run it."}]},"parent_tool_use_id":null}\n' "$msg";;
      esac;;
    "ask")
      q='{"questions":[{"question":"Which colour?","header":"Colour","multiSelect":false,"options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}]}]}'
      printf '{"type":"assistant","message":{"id":"%s","content":[{"type":"tool_use","id":"toolu_%s","name":"AskUserQuestion","input":%s}]},"parent_tool_use_id":null}\n' "$msg" "$n" "$q"
      printf '{"type":"control_request","request_id":"req_%s","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":%s,"tool_use_id":"toolu_%s","requires_user_interaction":true}}\n' "$n" "$q" "$n"
      IFS= read -r ans
      choice=$(printf '%s' "$ans" | sed -n 's/.*"Which colour?":"\([^"]*\)".*/\1/p')
      printf '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%s","content":"answered"}]},"parent_tool_use_id":null}\n' "$n"
      printf '{"type":"assistant","message":{"id":"%sb","content":[{"type":"text","text":"You chose %s."}]},"parent_tool_use_id":null}\n' "$msg" "$choice";;
    *)
      ev "{\"type\":\"message_start\",\"message\":{\"id\":\"$msg\"}}"
      ev '{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}'
      ev '{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done: "}}'
      sleep 0.3
      ev "{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"$text\"}}"
      ev '{"type":"content_block_stop","index":0}'
      printf '{"type":"assistant","message":{"id":"%s","content":[{"type":"text","text":"Done: %s"}]},"parent_tool_use_id":null}\n' "$msg" "$text";;
    esac
    printf '{"type":"result","subtype":"success","is_error":false,"result":"","session_id":"%s","total_cost_usd":0.012}\n' "$sid";;
  esac
done
