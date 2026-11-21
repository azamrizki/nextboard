package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const todoDescription = `Writes your to-do list for the current task, which the owner sees in the chat. Each call replaces the whole list.

- Use it for work of three steps or more, or when the owner asks for several things; a single simple step needs no list.
- Each item has content (what to do, as an instruction: "Run the tests"), activeForm (the same while it is being done: "Running the tests") and status: pending, in_progress or completed.
- Keep exactly one item in_progress while you work. Mark an item completed as soon as it is done, not in a batch at the end, and only when it is fully done: tests pass and nothing is left half-finished.
- Add items you discover on the way, and remove those that no longer apply.`

const todoSchema = `{
  "type": "object",
  "properties": {
    "todos": {
      "type": "array",
      "description": "The whole updated to-do list",
      "items": {
        "type": "object",
        "properties": {
          "content": {"type": "string", "minLength": 1},
          "status": {"type": "string", "enum": ["pending", "in_progress", "completed"]},
          "activeForm": {"type": "string", "minLength": 1}
        },
        "required": ["content", "status", "activeForm"],
        "additionalProperties": false
      }
    }
  },
  "required": ["todos"],
  "additionalProperties": false
}`

func todoTool() Tool { return newTool("TodoWrite", KindMeta, todoDescription, todoSchema, runTodo) }

type todoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

// runTodo checks the list and counts it; the list itself lives in the chat (the call's input),
// so the tool keeps nothing.
func runTodo(_ context.Context, _ *Env, input json.RawMessage) Result {
	var in struct {
		Todos *[]todoItem `json:"todos"`
	}
	if err := decode("TodoWrite", input, &in); err != nil {
		return fail("%v", err)
	}
	if in.Todos == nil {
		return fail("TodoWrite needs todos: the whole list, each item with content, status and activeForm")
	}
	count := map[string]int{}
	for i, t := range *in.Todos {
		switch {
		case strings.TrimSpace(t.Content) == "":
			return fail("todo %d has no content", i+1)
		case strings.TrimSpace(t.ActiveForm) == "":
			return fail("todo %d has no activeForm (the item as it is being done: \"Running the tests\")", i+1)
		case t.Status != "pending" && t.Status != "in_progress" && t.Status != "completed":
			return fail("todo %d: status must be pending, in_progress or completed, not %q", i+1, t.Status)
		}
		count[t.Status]++
	}
	return ok(fmt.Sprintf("Todos updated: %d (%d done, %d in progress, %d pending)",
		len(*in.Todos), count["completed"], count["in_progress"], count["pending"]))
}
