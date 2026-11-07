package uptime

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// jsonStep is one step of a JSON query: a key of an object, or an index of an array.
type jsonStep struct {
	key   string
	index int // -1 for a key
}

// parseJSONPath reads "data.items[0].status", with or without a leading "$." and with
// ["key with.dots"] for keys a dot cannot name.
func parseJSONPath(p string) ([]jsonStep, error) {
	bad := fmt.Errorf("JSON query %q: use keys and [n], as in data.items[0].status", p)
	p = strings.TrimPrefix(strings.TrimPrefix(p, "$"), ".")
	var steps []jsonStep
	for p != "" {
		switch {
		case strings.HasPrefix(p, "[\""):
			end := strings.Index(p, "\"]")
			if end < 2 {
				return nil, bad
			}
			steps = append(steps, jsonStep{key: p[2:end], index: -1})
			p = p[end+2:]
		case p[0] == '[':
			end := strings.IndexByte(p, ']')
			if end < 0 {
				return nil, bad
			}
			n, err := strconv.Atoi(p[1:end])
			if err != nil || n < 0 {
				return nil, bad
			}
			steps = append(steps, jsonStep{index: n})
			p = p[end+1:]
		default:
			end := strings.IndexAny(p, ".[")
			if end < 0 {
				end = len(p)
			}
			// a key with spaces is written ["like this"]
			if end == 0 || strings.ContainsAny(p[:end], " \t") {
				return nil, bad
			}
			steps = append(steps, jsonStep{key: p[:end], index: -1})
			p = p[end:]
		}
		if strings.HasPrefix(p, ".") {
			p = p[1:]
			if p == "" || p[0] == '.' || p[0] == '[' {
				return nil, bad
			}
		} else if p != "" && p[0] != '[' {
			return nil, bad
		}
	}
	if len(steps) == 0 {
		return nil, bad
	}
	return steps, nil
}

var errNoValue = errors.New("no value")

// jsonPick follows the steps through a decoded JSON document.
func jsonPick(doc any, steps []jsonStep) (any, error) {
	v := doc
	for _, s := range steps {
		if s.index >= 0 {
			arr, ok := v.([]any)
			if !ok || s.index >= len(arr) {
				return nil, errNoValue
			}
			v = arr[s.index]
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, errNoValue
		}
		if v, ok = obj[s.key]; !ok {
			return nil, errNoValue
		}
	}
	return v, nil
}

// jsonText is a value as people write it: a string without quotes, numbers and the rest
// as JSON.
func jsonText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case nil:
		return "null"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// jsonQuery answers whether the body's value at path meets the condition, and says what
// it found for the message of the check.
func jsonQuery(body []byte, path, op, want string) (bool, string) {
	steps, err := parseJSONPath(path)
	if err != nil {
		return false, err.Error()
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return false, "The answer is no JSON"
	}
	v, err := jsonPick(doc, steps)
	if err != nil {
		return false, clip(fmt.Sprintf("%s: no such value", path))
	}
	got := jsonText(v)
	ok := false
	switch op {
	case "==":
		ok = got == want
	case "!=":
		ok = got != want
	case "contains":
		ok = strings.Contains(got, want)
	case "<", ">", "<=", ">=":
		a, errA := strconv.ParseFloat(got, 64)
		b, errB := strconv.ParseFloat(strings.TrimSpace(want), 64)
		if errA != nil || errB != nil || math.IsNaN(a) || math.IsNaN(b) {
			return false, clip(fmt.Sprintf("%s is %q, not a number to compare", path, got))
		}
		switch op {
		case "<":
			ok = a < b
		case ">":
			ok = a > b
		case "<=":
			ok = a <= b
		default:
			ok = a >= b
		}
	}
	if ok {
		return true, clip(fmt.Sprintf("%s is %q", path, got))
	}
	return false, clip(fmt.Sprintf("%s is %q, want %s %q", path, got, op, want))
}
