package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/store"
)

func TestProfiles(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Add("local", "claude", " Work ", "", true)
	if err != nil || p.Name != "Work" || !p.CreatedByHub || p.Dir != st.Path("profiles", p.ID) {
		t.Fatalf("%+v %v", p, err)
	}
	if fi, err := os.Stat(p.Dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", fi, err)
	}
	r, err := s.Add("m_x", "codex", "Remote", "/home/u/.codex-work", false)
	if err != nil || r.CreatedByHub {
		t.Fatal(err)
	}
	if _, err := s.Add("m_x", "codex", "Rel", "rel/dir", false); !errors.Is(err, ErrBadDir) {
		t.Fatal(err)
	}
	if _, err := s.Add("local", "claude", "", "", true); !errors.Is(err, ErrBadName) {
		t.Fatal(err)
	}
	if l := s.List("m_x"); len(l) != 1 || l[0].ID != r.ID {
		t.Fatalf("list %+v", l)
	}
	if p2, err := s.Rename(p.ID, "Personal"); err != nil || p2.Name != "Personal" {
		t.Fatal(err)
	}
	// Reload from disk.
	s2, _ := Open(st)
	if got, ok := s2.Get(p.ID); !ok || got.Name != "Personal" {
		t.Fatal("not persisted")
	}
	// Delete without purge keeps the folder; with purge removes it.
	if _, err := s.Delete(r.ID, true); err != nil {
		t.Fatal(err)
	}
	q, _ := s.Add("local", "claude", "Tmp", "", true)
	if _, err := s.Delete(q.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(q.Dir); err != nil {
		t.Fatal("folder removed without purge")
	}
	if _, err := s.Delete(p.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
		t.Fatal("purge did not remove folder")
	}
	if _, err := s.Delete("pf_nope", false); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestEnsureHome(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	p, created, err := s.EnsureHome("local", "claude")
	if err != nil || !created || !p.Home || p.Dir != st.Path("agents", "claude") {
		t.Fatalf("%+v %v %v", p, created, err)
	}
	if fi, err := os.Stat(p.Dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", fi, err)
	}
	if again, created, err := s.EnsureHome("local", "claude"); err != nil || created || again.ID != p.ID {
		t.Fatalf("second call %+v %v %v", again, created, err)
	}
	if _, err := s.Delete(p.ID, true); !errors.Is(err, ErrIsHome) {
		t.Fatalf("delete %v", err)
	}
	if _, err := os.Stat(p.Dir); err != nil {
		t.Fatalf("folder must stay: %v", err)
	}
	// Survives a reload.
	r, _ := Open(st)
	if got, created, _ := r.EnsureHome("local", "claude"); created || got.ID != p.ID {
		t.Fatalf("reload %+v %v", got, created)
	}
}

func TestNoLSPOffer(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".claude.json")
	if err := NoLSPOffer(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), `"lspRecommendationDisabled": true`) {
		t.Fatalf("new file %s", b)
	}
	// What the CLI keeps there stays, large numbers included.
	if err := os.WriteFile(file, []byte(`{"userID":"abc","big":12345678901234567890}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NoLSPOffer(dir); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(file)
	for _, want := range []string{`"userID": "abc"`, `12345678901234567890`, `"lspRecommendationDisabled": true`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("%q missing in %s", want, b)
		}
	}
	// Already off: not written again.
	if err := os.WriteFile(file, []byte(`{"lspRecommendationDisabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NoLSPOffer(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(file); string(b) != `{"lspRecommendationDisabled":true}` {
		t.Fatalf("written again: %s", b)
	}
	// Not JSON: left alone.
	if err := os.WriteFile(file, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NoLSPOffer(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(file); string(b) != `{not json` {
		t.Fatalf("broken file changed: %s", b)
	}
}

func TestTrustFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".claude.json")
	// No file yet.
	if err := TrustFolder(dir, "/data/agents/remote"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), `"hasTrustDialogAccepted": true`) {
		t.Fatalf("new file %s", b)
	}
	// What the CLI keeps there stays, large numbers included.
	old := `{"numStartups":3,"userID":"abc","big":12345678901234567890,"projects":{"/x":{"allowedTools":["Read"]}}}`
	if err := os.WriteFile(file, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := TrustFolder(dir, "/data/agents/remote"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(file)
	for _, want := range []string{`"userID": "abc"`, `12345678901234567890`, `"allowedTools"`, `"/data/agents/remote"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("lost %s: %s", want, b)
		}
	}
	// A file that is not JSON is not ours to repair.
	if err := os.WriteFile(file, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := TrustFolder(dir, "/data/agents/remote"); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(file); string(b) != "{broken" {
		t.Fatalf("rewrote a broken file: %s", b)
	}
}
