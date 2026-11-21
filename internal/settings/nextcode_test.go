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
