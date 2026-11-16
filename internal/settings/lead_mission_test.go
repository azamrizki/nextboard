package settings

import (
	"testing"

	"github.com/azamrizki/nextboard/internal/store"
)

// lead.language and the mission limits (docs/API-orchestration.md §3.9, §22): checked, and
// merged field by field like the other lead settings.
func TestLeadLanguageAndMissionLimits(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	if l := s.Get().Lead; l.Language != "" || l.MissionRounds != 30 || l.MissionHours != 4 {
		t.Fatalf("defaults %+v", l)
	}
	got, err := s.Patch([]byte(`{"lead":{"language":"id","missionRounds":12}}`))
	if err != nil || got.Lead.Language != "id" || got.Lead.MissionRounds != 12 || got.Lead.MissionHours != 4 || got.Lead.Name != DefaultLeadName || !got.Lead.TalkFirst {
		t.Fatalf("%v %+v", err, got.Lead)
	}
	got, err = s.Patch([]byte(`{"lead":{"missionHours":8}}`))
	if err != nil || got.Lead.Language != "id" || got.Lead.MissionRounds != 12 || got.Lead.MissionHours != 8 {
		t.Fatalf("%v %+v", err, got.Lead)
	}
	for _, bad := range []string{`{"lead":{"language":"fr"}}`, `{"lead":{"language":"Indonesian"}}`, `{"lead":{"missionRounds":0}}`,
		`{"lead":{"missionRounds":501}}`, `{"lead":{"missionHours":0}}`, `{"lead":{"missionHours":73}}`} {
		if _, err := s.Patch([]byte(bad)); code(err) != "bad_setting" {
			t.Errorf("%s: %v", bad, err)
		}
	}
	got, err = s.Patch([]byte(`{"lead":{"language":"en"}}`))
	if err != nil || got.Lead.Language != "en" {
		t.Fatalf("%v %+v", err, got.Lead)
	}
	got, err = s.Patch([]byte(`{"lead":{"language":""}}`))
	if err != nil || got.Lead.Language != "" || got.Lead.MissionHours != 8 {
		t.Fatalf("%v %+v", err, got.Lead)
	}
	// a document saved before these settings existed takes their defaults
	raw := map[string]any{"schemaVersion": 1, "lead": map[string]any{"name": "Sekar", "talkFirst": true}}
	if err := st.Save(File, raw); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(st); err != nil {
		t.Fatal(err)
	}
	if l := s.Get().Lead; l.Name != "Sekar" || l.MissionRounds != 30 || l.MissionHours != 4 {
		t.Fatalf("old document %+v", l)
	}
}
