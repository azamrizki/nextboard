package jobs

import (
	"encoding/json"
	"sort"
)

// Codex hands --output-schema to the API with "strict": true (measured with Codex 0.165 against a
// loopback API). Strict mode wants every property listed in "required", every object closed, and it
// knows no maxLength or minLength, so the schemas written for Claude Code are refused there.
// StrictSchema rewrites a schema that way for a Codex run, and FromStrict turns the answer back into
// what the original schema describes: callers get one shape whichever CLI ran.

// StrictSchema makes optional properties nullable and turns a nested object without properties
// (free-form, which strict mode cannot describe) into a string that holds the JSON.
func StrictSchema(raw json.RawMessage) json.RawMessage {
	var s map[string]any
	if json.Unmarshal(raw, &s) != nil {
		return raw
	}
	if schemaType(s) == "object" && len(propsOf(s)) == 0 {
		return raw // a free-form answer: nothing strict mode can say about it
	}
	b, err := json.Marshal(strictNode(s, false))
	if err != nil {
		return raw
	}
	return b
}

func strictNode(s map[string]any, nullable bool) map[string]any {
	out := map[string]any{}
	for k, v := range s {
		switch k {
		case "maxLength", "minLength", "properties", "items", "required", "additionalProperties":
		default:
			out[k] = v
		}
	}
	switch schemaType(s) {
	case "object":
		props := propsOf(s)
		if len(props) == 0 {
			desc := "A JSON object, written as a string."
			if d, ok := s["description"].(string); ok && d != "" {
				desc = d + " " + desc
			}
			return nullableNode(map[string]any{"type": "string", "description": desc}, nullable)
		}
		req := requiredOf(s)
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		np := map[string]any{}
		for _, k := range keys {
			ps, _ := props[k].(map[string]any)
			np[k] = strictNode(ps, !req[k])
		}
		out["type"] = "object"
		out["properties"] = np
		out["required"] = keys
		out["additionalProperties"] = false
	case "array":
		if it, ok := s["items"].(map[string]any); ok {
			out["items"] = strictNode(it, false)
		}
	}
	return nullableNode(out, nullable)
}

func nullableNode(n map[string]any, nullable bool) map[string]any {
	if !nullable {
		return n
	}
	if t, ok := n["type"].(string); ok {
		n["type"] = []any{t, "null"}
	}
	if e, ok := n["enum"].([]any); ok {
		n["enum"] = append(append([]any{}, e...), nil)
	}
	return n
}

// FromStrict drops the nulls strict mode put in place of optional properties and decodes the
// free-form objects it had to send as strings.
func FromStrict(schema, val json.RawMessage) json.RawMessage {
	var s map[string]any
	var v any
	if json.Unmarshal(schema, &s) != nil || json.Unmarshal(val, &v) != nil {
		return val
	}
	b, err := json.Marshal(unstrict(s, v, true))
	if err != nil {
		return val
	}
	return b
}

func unstrict(s map[string]any, v any, root bool) any {
	switch schemaType(s) {
	case "object":
		props := propsOf(s)
		if len(props) == 0 {
			str, ok := v.(string)
			if root || !ok {
				return v
			}
			var o map[string]any
			if json.Unmarshal([]byte(str), &o) != nil {
				return nil
			}
			return o
		}
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		req := requiredOf(s)
		for k, x := range m {
			ps, _ := props[k].(map[string]any)
			if ps == nil {
				continue
			}
			if x != nil {
				x = unstrict(ps, x, false)
			}
			if x == nil && !req[k] {
				delete(m, k)
				continue
			}
			m[k] = x
		}
		return m
	case "array":
		it, _ := s["items"].(map[string]any)
		l, ok := v.([]any)
		if !ok || it == nil {
			return v
		}
		for i := range l {
			l[i] = unstrict(it, l[i], false)
		}
		return l
	}
	return v
}

func schemaType(s map[string]any) string {
	switch t := s["type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if x, ok := x.(string); ok && x != "null" {
				return x
			}
		}
	}
	if _, ok := s["properties"]; ok {
		return "object"
	}
	return ""
}

func propsOf(s map[string]any) map[string]any {
	p, _ := s["properties"].(map[string]any)
	return p
}

func requiredOf(s map[string]any) map[string]bool {
	out := map[string]bool{}
	l, _ := s["required"].([]any)
	for _, x := range l {
		if k, ok := x.(string); ok {
			out[k] = true
		}
	}
	return out
}
