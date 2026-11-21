package settings

import "testing"

// nextcode.provider (docs/API-agents.md §30): the API provider of NextCode's own engine when a
// console picks none. An id of a provider or "" (none); null goes back to none as well.
func TestNextCodeProvider(t *testing.T) {
	s := open(t, t.TempDir())
	if v := s.Get().NextCode.Provider; v != "" {
		t.Fatalf("default %q", v)
	}
	got, err := s.Patch([]byte(`{"nextcode":{"provider":"ap_x1y2z3"}}`))
	if err != nil || got.NextCode.Provider != "ap_x1y2z3" || got.NextCode.PermissionMode != "default" || got.NextCode.IdleExitMin != 30 {
		t.Fatalf("set: %v %+v", err, got.NextCode)
	}
	for _, bad := range []string{`{"nextcode":{"provider":"pf_x1y2z3"}}`, `{"nextcode":{"provider":"ap_X"}}`, `{"nextcode":{"provider":1}}`,
		`{"nextcode":{"provider":"ap_x1y2z3 "}}`} {
		if _, err := s.Patch([]byte(bad)); code(err) != "bad_setting" {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if v := s.Get().NextCode.Provider; v != "ap_x1y2z3" {
		t.Fatalf("after refused patches %q", v)
	}
	// another key of the section leaves it; "" clears it; null too
	if got, err = s.Patch([]byte(`{"nextcode":{"idleExitMin":5}}`)); err != nil || got.NextCode.Provider != "ap_x1y2z3" {
		t.Fatalf("other key: %v %+v", err, got.NextCode)
	}
	if got, err = s.Patch([]byte(`{"nextcode":{"provider":""}}`)); err != nil || got.NextCode.Provider != "" || got.NextCode.IdleExitMin != 5 {
		t.Fatalf("clear: %v %+v", err, got.NextCode)
	}
	if _, err := s.Patch([]byte(`{"nextcode":{"provider":"ap_x1y2z3"}}`)); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Patch([]byte(`{"nextcode":{"provider":null}}`)); err != nil || got.NextCode.Provider != "" {
		t.Fatalf("null: %v %+v", err, got.NextCode)
	}
}

// nextcode.accounts, .account, .model and .effort (docs/API-agents.md §30, One NextCode): what
// NextCode runs on, in its menu's order, and what a new console starts on.
func TestNextCodeAccounts(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if n := s.Get().NextCode; n.Accounts != nil || n.Account != "" {
		t.Fatalf("default %+v", n)
	}
	got, err := s.Patch([]byte(`{"nextcode":{"accounts":["pf_claude1","ap_deep01","pf_codex01"],"account":"ap_deep01","model":"deepseek-chat","effort":"high"}}`))
	if err != nil || len(got.NextCode.Accounts) != 3 || got.NextCode.Accounts[2] != "pf_codex01" || got.NextCode.Account != "ap_deep01" ||
		got.NextCode.Model != "deepseek-chat" || got.NextCode.Effort != "high" {
		t.Fatalf("set: %v %+v", err, got.NextCode)
	}
	for _, bad := range []string{`{"nextcode":{"accounts":["pf_claude1","pf_claude1"]}}`, `{"nextcode":{"accounts":["claude"]}}`,
		`{"nextcode":{"account":"nextcode"}}`, `{"nextcode":{"model":"bad model"}}`, `{"nextcode":{"effort":"HIGH"}}`} {
		if _, err := s.Patch([]byte(bad)); code(err) != "bad_setting" {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// none at all is a choice too, apart from never chosen (null)
	if got, err = s.Patch([]byte(`{"nextcode":{"accounts":[]}}`)); err != nil || got.NextCode.Accounts == nil || len(got.NextCode.Accounts) != 0 {
		t.Fatalf("empty: %v %+v", err, got.NextCode)
	}
	s2 := open(t, dir)
	if n := s2.Get().NextCode; n.Accounts == nil || n.Account != "ap_deep01" {
		t.Fatalf("reloaded %+v", n)
	}
	if got, err = s.Patch([]byte(`{"nextcode":{"accounts":null}}`)); err != nil || got.NextCode.Accounts != nil {
		t.Fatalf("null: %v %+v", err, got.NextCode)
	}

	// a deleted account leaves the section
	n := NextCode{Accounts: []string{"pf_a1b2", "ap_c3d4"}, Account: "ap_c3d4", Model: "m", Effort: "e", Provider: "ap_c3d4"}
	out, changed := n.WithoutAccount("ap_c3d4")
	if !changed || len(out.Accounts) != 1 || out.Accounts[0] != "pf_a1b2" || out.Account != "" || out.Model != "" || out.Provider != "" {
		t.Fatalf("without %+v %v", out, changed)
	}
	if _, changed := out.WithoutAccount("ap_zzzz"); changed {
		t.Fatal("nothing to take out")
	}
}
