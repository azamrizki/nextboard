package tools

import "testing"

func TestTodoWrite(t *testing.T) {
	item := func(content, status, active string) map[string]any {
		return map[string]any{"content": content, "status": status, "activeForm": active}
	}
	cases := []struct {
		name    string
		in      any
		isError bool
		want    string
	}{
		{"counts", map[string]any{"todos": []any{
			item("Read the code", "completed", "Reading the code"),
			item("Fix the bug", "in_progress", "Fixing the bug"),
			item("Run the tests", "pending", "Running the tests"),
			item("Write the report", "pending", "Writing the report"),
		}}, false, "Todos updated: 4 (1 done, 1 in progress, 2 pending)"},
		{"empty list", map[string]any{"todos": []any{}}, false, "Todos updated: 0 (0 done, 0 in progress, 0 pending)"},
		{"bad status", map[string]any{"todos": []any{item("a", "done", "doing a")}}, true,
			`todo 1: status must be pending, in_progress or completed, not "done"`},
		{"no content", map[string]any{"todos": []any{item("a", "pending", "doing a"), item(" ", "pending", "x")}}, true, "todo 2 has no content"},
		{"no activeForm", map[string]any{"todos": []any{map[string]any{"content": "a", "status": "pending"}}}, true,
			`todo 1 has no activeForm (the item as it is being done: "Running the tests")`},
		{"no todos", map[string]any{}, true, "TodoWrite needs todos: the whole list, each item with content, status and activeForm"},
		{"not a list", map[string]any{"todos": "x"}, true, "invalid input for TodoWrite: todos has the wrong type (string given)"},
	}
	for _, c := range cases {
		r := call(t, "TodoWrite", &Env{}, c.in)
		if r.IsError != c.isError || r.Output != c.want {
			t.Errorf("%s: got %+v, want %q", c.name, r, c.want)
		}
	}
}
