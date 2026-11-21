package nextcode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// fakeClaude plays Claude Code's headless protocol for the tests (the test binary runs itself
// with NEXTCODE_FAKE=claude): the shapes of the real CLI's lines (2.1.329), a few scripted
// conversations chosen by the message's text.
func fakeClaude() {
	if f := os.Getenv("NEXTCODE_FAKE_ARGS"); f != "" {
		if fh, err := os.OpenFile(f, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			fmt.Fprintln(fh, strings.Join(os.Args[1:], " "))
			fh.Close()
		}
	}
	conv := "conv-1"
	for i, a := range os.Args {
		if (a == "--session-id" || a == "--resume") && i+1 < len(os.Args) {
			conv = os.Args[i+1]
		}
	}
	mode := "default"
	if i := slices.Index(os.Args, "--permission-mode"); i > 0 && i+1 < len(os.Args) {
		mode = os.Args[i+1]
	}
	out := bufio.NewWriter(os.Stdout)
	emit := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	in := make(chan map[string]any, 64)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				in <- m
			}
		}
		close(in)
	}()
	// response waits for the control_response to request id
	response := func(id string) map[string]any {
		for m := range in {
			if m["type"] == "control_response" {
				r, _ := m["response"].(map[string]any)
				if r["request_id"] == id {
					return r
				}
			}
		}
		os.Exit(0)
		return nil
	}
	inited := false
	n := 0
	result := func(subtype string, isError bool, text string) {
		emit(map[string]any{"type": "result", "subtype": subtype, "is_error": isError, "result": text, "session_id": conv,
			"total_cost_usd": 0.01, "num_turns": 1, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}})
	}
	text := func(msg, s string) {
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": msg, "content": []any{map[string]any{"type": "text", "text": s}}},
			"parent_tool_use_id": nil, "session_id": conv})
	}
	stream := func(ev map[string]any) {
		emit(map[string]any{"type": "stream_event", "event": ev, "parent_tool_use_id": nil, "session_id": conv})
	}
	toolUse := func(msg, id, name string, input any, parent any) {
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": msg, "content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}, "parent_tool_use_id": parent, "session_id": conv})
	}
	toolResult := func(id, content string, isError bool, parent any) {
		emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": content, "is_error": isError}}}, "parent_tool_use_id": parent, "session_id": conv})
	}
	for m := range in {
		switch m["type"] {
		case "control_request":
			r, _ := m["request"].(map[string]any)
			if r["subtype"] == "initialize" {
				emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": m["request_id"],
					"response": map[string]any{"commands": []any{map[string]any{"name": "review"}, map[string]any{"name": "compact"}}}}})
			}
			continue
		case "user":
		default:
			continue
		}
		msgObj, _ := m["message"].(map[string]any)
		content, _ := msgObj["content"].([]any)
		said := ""
		if len(content) > 0 {
			c, _ := content[0].(map[string]any)
			said, _ = c["text"].(string)
		}
		if !inited {
			inited = true
			emit(map[string]any{"type": "system", "subtype": "init", "session_id": conv, "model": "fake-model", "permissionMode": mode, "tools": []any{}})
		}
		n++
		msg := fmt.Sprintf("msg_%d", n)
		switch said {
		case "hello":
			stream(map[string]any{"type": "message_start", "message": map[string]any{"id": msg}})
			stream(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Hi"}})
			stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": " there"}})
			stream(map[string]any{"type": "content_block_stop", "index": 0})
			text(msg, "Hi there")
			emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "allowed"}})
			result("success", false, "Hi there")
		case "write":
			stream(map[string]any{"type": "message_start", "message": map[string]any{"id": msg}})
			stream(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_w", "name": "Write"}})
			stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"file_path":`}})
			toolUse(msg, "toolu_w", "Write", map[string]any{"file_path": "/tmp/x.txt", "content": "hi"}, nil)
			emit(map[string]any{"type": "control_request", "request_id": "req_w", "request": map[string]any{"subtype": "can_use_tool",
				"tool_name": "Write", "input": map[string]any{"file_path": "/tmp/x.txt", "content": "hi"}, "tool_use_id": "toolu_w",
				"display_name": "Write", "permission_suggestions": []any{map[string]any{"type": "setMode", "mode": "acceptEdits", "destination": "session"}}}})
			r := response("req_w")
			resp, _ := r["response"].(map[string]any)
			if resp["behavior"] == "allow" {
				out := "File created"
				if resp["updatedPermissions"] != nil {
					out += " (remember)"
				}
				toolResult("toolu_w", out, false, nil)
				text(msg+"b", "done")
			} else {
				toolResult("toolu_w", fmt.Sprint(resp["message"]), true, nil)
				if resp["interrupt"] == true {
					result("error_during_execution", true, "")
					continue
				}
				text(msg+"b", "denied")
			}
			result("success", false, "")
		case "ask":
			q := map[string]any{"questions": []any{map[string]any{"question": "Colour?", "header": "Colour", "multiSelect": false,
				"options": []any{map[string]any{"label": "Red", "description": "red"}, map[string]any{"label": "Blue", "description": "blue"}}}}}
			toolUse(msg, "toolu_q", "AskUserQuestion", q, nil)
			emit(map[string]any{"type": "control_request", "request_id": "req_q", "request": map[string]any{"subtype": "can_use_tool",
				"tool_name": "AskUserQuestion", "input": q, "tool_use_id": "toolu_q", "requires_user_interaction": true}})
			r := response("req_q")
			resp, _ := r["response"].(map[string]any)
			upd, _ := resp["updatedInput"].(map[string]any)
			b, _ := json.Marshal(upd["answers"])
			toolResult("toolu_q", "answers "+string(b), false, nil)
			text(msg+"b", "You chose "+string(b))
			result("success", false, "")
		case "long":
			stream(map[string]any{"type": "message_start", "message": map[string]any{"id": msg}})
			stream(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			tick := time.NewTicker(20 * time.Millisecond)
		loop:
			for {
				select {
				case c, ok := <-in:
					if !ok {
						os.Exit(0)
					}
					if c["type"] == "control_request" {
						if r, _ := c["request"].(map[string]any); r["subtype"] == "interrupt" {
							emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": c["request_id"],
								"response": map[string]any{"still_queued": []any{}}}})
							break loop
						}
					}
				case <-tick.C:
					stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "tick "}})
				}
			}
			tick.Stop()
			text(msg, "tick tick")
			result("error_during_execution", true, "")
		case "fail":
			result("error_max_turns", true, "")
		case "nologin":
			text(msg, "Not logged in · Please run /login")
			result("success", true, "Not logged in · Please run /login")
		case "crash":
			fmt.Fprintln(os.Stderr, "boom: fatal")
			os.Exit(3)
		case "sub":
			toolUse(msg, "toolu_t", "Task", map[string]any{"description": "look around", "prompt": "do the thing"}, nil)
			emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "do the thing"}, "parent_tool_use_id": "toolu_t", "session_id": conv})
			toolUse("sub_1", "toolu_b", "Bash", map[string]any{"command": "ls"}, "toolu_t")
			toolResult("toolu_b", "a\nb", false, "toolu_t")
			toolResult("toolu_t", "found a and b", false, nil)
			text(msg+"b", "the sub-agent found a and b")
			result("success", false, "")
		case "hook":
			emit(map[string]any{"type": "control_request", "request_id": "req_h", "request": map[string]any{"subtype": "hook_callback", "callback_id": "x"}})
			r := response("req_h")
			text(msg, "hook answered "+fmt.Sprint(r["subtype"]))
			result("success", false, "")
		default:
			text(msg, "echo "+said)
			result("success", false, "")
		}
	}
}
