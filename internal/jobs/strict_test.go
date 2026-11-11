package jobs_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/studio"
)

// strictProblems lists what OpenAI's strict structured outputs refuse in a schema: an object that
// is not closed or does not require every property, and keywords strict mode does not know.
func strictProblems(path string, s map[string]any) []string {
	var out []string
	for _, k := range []string{"maxLength", "minLength"} {
		if _, ok := s[k]; ok {
			out = append(out, path+": "+k)
		}
	}
	if props, ok := s["properties"].(map[string]any); ok {
		if s["additionalProperties"] != false {
			out = append(out, path+": additionalProperties is not false")
		}
		var keys, req []string
		for k := range props {
			keys = append(keys, k)
		}
		for _, r := range s["required"].([]any) {
			req = append(req, r.(string))
		}
		sort.Strings(keys)
		sort.Strings(req)
		if !reflect.DeepEqual(keys, req) {
			out = append(out, fmt.Sprintf("%s: required %v, properties %v", path, req, keys))
		}
		for k, p := range props {
			out = append(out, strictProblems(path+"."+k, p.(map[string]any))...)
		}
	} else if t, _ := s["type"].(string); t == "object" && path != "$" {
		out = append(out, path+": object without properties")
	}
	if it, ok := s["items"].(map[string]any); ok {
		out = append(out, strictProblems(path+"[]", it)...)
	}
	return out
}

// TestStrictSchemas: every schema a hidden run can hand to Codex passes strict mode once rewritten.
func TestStrictSchemas(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{"plan": orch.PlanSchema, "split": orch.SplitSchema, "goal": orch.GoalSchema,
		"init": orch.InitSchema, "scan": orch.ScanSchema, "refresh": orch.RefreshSchema, "diagram": studio.DiagramJSONSchema} {
		var s map[string]any
		if err := json.Unmarshal(jobs.StrictSchema(raw), &s); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p := strictProblems("$", s); len(p) > 0 {
			t.Errorf("%s: %v", name, p)
		}
	}
	// a free-form answer is left alone; so is anything that is not JSON
	if got := string(jobs.StrictSchema(json.RawMessage(`{"type":"object"}`))); got != `{"type":"object"}` {
		t.Fatalf("free-form %s", got)
	}
	if got := string(jobs.StrictSchema(json.RawMessage(`nope`))); got != `nope` {
		t.Fatalf("not JSON %s", got)
	}
}

// TestFromStrict: the nulls strict mode asks for are dropped and free-form objects decoded, so a
// Codex answer reads like a Claude Code one.
func TestFromStrict(t *testing.T) {
	var s map[string]any
	_ = json.Unmarshal(jobs.StrictSchema(orch.PlanSchema), &s)
	steps := s["properties"].(map[string]any)["steps"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if got := steps["count"].(map[string]any)["type"]; !reflect.DeepEqual(got, []any{"integer", "null"}) {
		t.Fatalf("count %v", got)
	}
	if got := steps["mode"].(map[string]any)["enum"].([]any); got[len(got)-1] != nil {
		t.Fatalf("enum without null %v", got)
	}
	if got := steps["data"].(map[string]any)["type"]; !reflect.DeepEqual(got, []any{"string", "null"}) {
		t.Fatalf("data %v", got)
	}
	answer := `{"reply":"ok","danger":null,"steps":[{"op":"node.add","kind":"checklist","data":"{\"items\":[{\"text\":\"a\"}]}","text":null,"count":null,"targets":null},` +
		`{"op":"send","targets":["@a"],"text":"go","data":null},{"op":"node.update","data":"not json"}]}`
	got := string(jobs.FromStrict(orch.PlanSchema, json.RawMessage(answer)))
	want := `{"reply":"ok","steps":[{"data":{"items":[{"text":"a"}]},"kind":"checklist","op":"node.add"},{"op":"send","targets":["@a"],"text":"go"},{"op":"node.update"}]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// required fields stay, even null
	if got := string(jobs.FromStrict(orch.PlanSchema, json.RawMessage(`{"reply":null,"steps":[]}`))); got != `{"reply":null,"steps":[]}` {
		t.Fatalf("required %s", got)
	}
}
