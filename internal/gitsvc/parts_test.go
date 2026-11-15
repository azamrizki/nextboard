package gitsvc

import (
	"fmt"
	"testing"
)

// TestPartEvents: every part of a project sends gitPart, keyed by its id, so a reorder changes no
// key; the first part in display order (Legacy) also sends the old git/gitRepo events, and a part
// that is not first sends no old event (v0.9.0-beta.2, docs/API-studio.md §3.6).
func TestPartEvents(t *testing.T) {
	var got []map[string]any
	s := New()
	s.Publish = func(v any) { got = append(got, v.(map[string]any)) }
	types := func() string {
		out := ""
		for _, e := range got {
			out += fmt.Sprintf("%s:%v:%v ", e["type"], e["part"], e["repo"])
		}
		got = nil
		return out
	}
	st := &Status{IsRepo: true, Branch: "main"}

	first := Repo{ProjectID: "p_shop", Part: "pt_api", Legacy: true, MachineID: "local", Path: "/w/api"}
	s.publishStatus(first, st, true)
	if ts := types(); ts != "gitPart:pt_api: git:<nil>:<nil> " {
		t.Fatalf("first part %s", ts)
	}
	sub, _ := first.In("libs/inner")
	s.publishStatus(sub, st, true)
	if ts := types(); ts != "gitPart:pt_api:libs/inner gitRepo:<nil>:libs/inner " {
		t.Fatalf("first part, nested repository %s", ts)
	}
	other := Repo{ProjectID: "p_shop", Part: "pt_web", MachineID: "devbox", Path: "/srv/web"}
	s.publishStatus(other, st, true)
	if ts := types(); ts != "gitPart:pt_web: " {
		t.Fatalf("another part %s", ts)
	}
	// reordered: the same part, now not first, keeps its key and its state
	moved := first
	moved.Legacy = false
	if moved.key() != first.key() || moved.key() != "p_shop#pt_api" {
		t.Fatalf("key %s %s", moved.key(), first.key())
	}
	s.publishStatus(moved, st, false)
	if ts := types(); ts != "" {
		t.Fatalf("the same summary again %s", ts)
	}
	// a repository not resolved through a part: the old event only
	s.publishStatus(Repo{ProjectID: "p_old", MachineID: "local", Path: "/w/old"}, st, true)
	if ts := types(); ts != "git:<nil>:<nil> " {
		t.Fatalf("no part %s", ts)
	}
}
