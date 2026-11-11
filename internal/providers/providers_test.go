package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.Open(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st, box)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func ptr[T any](v T) *T { return &v }

func TestStore(t *testing.T) {
	s, dir := open(t)
	p, err := s.Add(Input{Name: ptr("Z"), Kind: ptr("anthropic"), BaseURL: ptr("https://api.z.ai/api/anthropic/v1/"), Model: ptr("glm-4.6"), Key: ptr("sk-SECRET-123")})
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseURL != "https://api.z.ai/api/anthropic" || p.Auth != AuthBearer || !p.HasKey || !ValidID(p.ID) {
		t.Fatalf("%+v", p)
	}
	b, _ := os.ReadFile(filepath.Join(dir, storeName))
	if strings.Contains(string(b), "sk-SECRET-123") || !strings.Contains(string(b), `"key"`) {
		t.Fatal("the key is not sealed on disk")
	}
	if k, err := s.Key(p.ID); err != nil || k != "sk-SECRET-123" {
		t.Fatalf("key %q %v", k, err)
	}
	if _, err := s.Add(Input{Name: ptr("z"), Kind: ptr("openai"), BaseURL: ptr("http://localhost:11434/v1"), Model: ptr("qwen2.5-coder:7b")}); err == nil {
		t.Fatal("two providers of the same name")
	}
	// a server on this machine may do without a key; chat is the default style
	o, err := s.Add(Input{Name: ptr("Ollama"), Kind: ptr("openai"), BaseURL: ptr("http://localhost:11434/v1"), Model: ptr("qwen2.5-coder:7b")})
	if err != nil || o.API != APIChat || o.HasKey {
		t.Fatalf("%+v %v", o, err)
	}
	// an empty key keeps the key
	p, err = s.Update(p.ID, Input{Key: ptr(""), Budget: ptr(12.345)})
	if err != nil || !p.HasKey || p.Budget != 12.35 {
		t.Fatalf("%+v %v", p, err)
	}
	for name, in := range map[string]Input{
		"control in name":  {Name: ptr("a\tb")},
		"quote in address": {BaseURL: ptr(`https://a.com/"`)},
		"model with space": {Model: ptr("glm 4")},
		"negative price":   {Prices: &map[string]Price{"glm-4.6": {Input: -1}}},
		"key with newline": {Key: ptr("a\nb")},
	} {
		if _, err := s.Update(p.ID, in); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
	if err := s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Key(p.ID); err != ErrNotFound {
		t.Fatal("the key outlived its provider")
	}
}

func TestCompatible(t *testing.T) {
	a := Provider{Kind: KindAnthropic}
	chat := Provider{Kind: KindOpenAI, API: APIChat}
	resp := Provider{Kind: KindOpenAI, API: APIResponses}
	for _, c := range []struct {
		base string
		p    Provider
		want bool
	}{
		{"claude", a, true}, {"claude", chat, false}, {"codex", resp, true}, {"codex", chat, false}, {"codex", a, false},
		{"opencode", a, true}, {"opencode", chat, true}, {"gemini", a, false},
	} {
		if got := Compatible(c.base, c.p); got != c.want {
			t.Errorf("%s on %+v: %v", c.base, c.p, got)
		}
	}
	if c := (Price{Input: 2, Output: 8, CacheRead: 0.5}).Cost(1_000_000, 500_000, 2_000_000, 0); c != 7 {
		t.Fatalf("cost %v", c)
	}
}
