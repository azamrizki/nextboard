package vault

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/azamrizki/nextboard/internal/store"
)

func b64(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }

func goodKey() KeyMaterial {
	return KeyMaterial{KDF: KDF{Name: "argon2id", M: 64 << 10, T: 3, P: 4}, Salt: b64(16), WrappedKey: b64(wrappedKeyBytes)}
}

func open(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(st), st
}

func ready(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	s, st := open(t)
	if _, err := s.Init(goodKey()); err != nil {
		t.Fatal(err)
	}
	return s, st
}

func TestInitOnlyOnce(t *testing.T) {
	s, _ := open(t)
	v, err := s.Get()
	if err != nil || v.Exists {
		t.Fatalf("fresh vault: %+v %v", v, err)
	}
	if _, err := s.Put("abcdefgh", b64(64), 0); !errors.Is(err, ErrAbsent) {
		t.Fatalf("put before init: %v", err)
	}
	h, err := s.Init(goodKey())
	if err != nil {
		t.Fatal(err)
	}
	if h.ID == "" || h.KeyRev != 1 {
		t.Fatalf("header %+v", h)
	}
	if _, err := s.Init(goodKey()); !errors.Is(err, ErrExists) {
		t.Fatalf("second init: %v", err)
	}
}

func TestKeyMaterialIsChecked(t *testing.T) {
	bad := map[string]func(*KeyMaterial){
		"weak memory":     func(k *KeyMaterial) { k.KDF.M = 1024 },
		"one iteration":   func(k *KeyMaterial) { k.KDF.T = 1 },
		"huge memory":     func(k *KeyMaterial) { k.KDF.M = 8 << 20 },
		"other kdf":       func(k *KeyMaterial) { k.KDF.Name = "pbkdf2" },
		"short salt":      func(k *KeyMaterial) { k.Salt = b64(4) },
		"salt not base64": func(k *KeyMaterial) { k.Salt = "not base64!" },
		"short key":       func(k *KeyMaterial) { k.WrappedKey = b64(32) },
	}
	for name, change := range bad {
		s, _ := open(t)
		k := goodKey()
		change(&k)
		if _, err := s.Init(k); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRevisionsCatchAConflict(t *testing.T) {
	s, _ := ready(t)
	a, err := s.Put("item-0001", b64(64), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("item-0001", b64(64), 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("create over an item: %v", err)
	}
	b, err := s.Put("item-0001", b64(70), a.Rev)
	if err != nil {
		t.Fatal(err)
	}
	if b.Rev <= a.Rev {
		t.Fatalf("revision did not move: %d then %d", a.Rev, b.Rev)
	}
	if _, err := s.Put("item-0001", b64(80), a.Rev); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	if err := s.Delete("item-0001", a.Rev); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := s.Delete("item-0001", b.Rev); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("item-0001", b.Rev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := s.Put("item-0001", b64(64), b.Rev); !errors.Is(err, ErrConflict) {
		t.Fatalf("write to a deleted item: %v", err)
	}
}

func TestItemsAreChecked(t *testing.T) {
	s, _ := ready(t)
	cases := []struct{ name, id, blob string }{
		{"short id", "abc", b64(64)},
		{"path in id", "../../etc", b64(64)},
		{"not base64", "item-0001", "plain text password"},
		{"too short for a nonce and a tag", "item-0001", b64(minBlobBytes - 1)},
		{"too large", "item-0001", strings.Repeat("A", MaxBlobChars+4)},
	}
	for _, c := range cases {
		if _, err := s.Put(c.id, c.blob, 0); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestBatchIsAllOrNothing(t *testing.T) {
	s, _ := ready(t)
	if _, err := s.Put("item-0001", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	_, err := s.Batch([]NewItem{{"item-0002", b64(64)}, {"item-0001", b64(64)}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("batch over an existing item: %v", err)
	}
	v, _ := s.Get()
	if len(v.Items) != 1 {
		t.Fatalf("a failed batch left %d items", len(v.Items))
	}
	out, err := s.Batch([]NewItem{{"item-0002", b64(64)}, {"item-0003", b64(64)}})
	if err != nil || len(out) != 2 {
		t.Fatalf("batch: %v %v", out, err)
	}
}

func TestLimits(t *testing.T) {
	s, _ := ready(t)
	s.d.Items = map[string]Item{}
	for i := range MaxItems {
		id := "item-" + strings.Repeat("0", 4) + itoa(i)
		s.d.Items[id] = Item{ID: id, Blob: b64(32)}
	}
	if _, err := s.Put("one-too-many", b64(64), 0); !errors.Is(err, ErrFull) {
		t.Fatalf("over the item limit: %v", err)
	}
	if _, err := s.Batch([]NewItem{{"one-too-many", b64(64)}}); !errors.Is(err, ErrFull) {
		t.Fatalf("batch over the item limit: %v", err)
	}
	s.d.Items = map[string]Item{"item-0001": {ID: "item-0001", Blob: strings.Repeat("A", MaxTotalChars)}}
	if _, err := s.Put("item-0002", b64(64), 0); !errors.Is(err, ErrFull) {
		t.Fatalf("over the size limit: %v", err)
	}
	if _, err := s.Batch(make([]NewItem, MaxBatch+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("batch too long: %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	out := ""
	for ; i > 0; i /= 10 {
		out = string(rune('0'+i%10)) + out
	}
	return out
}

func TestSurvivesARestart(t *testing.T) {
	s, st := ready(t)
	it, err := s.Put("item-0001", b64(64), 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(st.Path(fileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	again := New(st)
	v, err := again.Get()
	if err != nil || !v.Exists || len(v.Items) != 1 || v.Items[0].Blob != it.Blob || v.Rev != it.Rev {
		t.Fatalf("after restart: %+v %v", v, err)
	}
}

func TestDamagedFileFallsBackToThePreviousCopy(t *testing.T) {
	s, st := ready(t)
	if _, err := s.Put("item-0001", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("item-0002", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.Path(fileName), []byte(`{"version":1,"items":{"oops`), 0o600); err != nil {
		t.Fatal(err)
	}
	again := New(st)
	v, err := again.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !v.Exists || len(v.Items) != 1 {
		t.Fatalf("previous copy has one item: %+v", v)
	}
	// It carries on from there, and the damaged file is still around.
	if _, err := again.Put("item-0003", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	aside, _ := filepath.Glob(st.Path("vault.corrupt-*.json"))
	if len(aside) != 1 {
		t.Fatalf("damaged file kept: %v", aside)
	}
}

func TestUnreadableVaultRefusesToWrite(t *testing.T) {
	s, st := ready(t)
	if _, err := s.Put("item-0001", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	damaged := []byte(`{"version":1,"items":{"oops`)
	for _, name := range []string{fileName, prevName} {
		if err := os.WriteFile(st.Path(name), damaged, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	again := New(st)
	if _, err := again.Get(); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("get: %v", err)
	}
	if _, err := again.Init(goodKey()); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("init: %v", err)
	}
	if _, err := again.Put("item-0002", b64(64), 0); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("put: %v", err)
	}
	if raw, _ := os.ReadFile(st.Path(fileName)); string(raw) != string(damaged) {
		t.Fatalf("the file was written over: %q", raw)
	}
	// Only a reset gets out of it, and it keeps the file.
	kept, err := again.Reset()
	if err != nil || kept == "" {
		t.Fatalf("reset: %q %v", kept, err)
	}
	if raw, _ := os.ReadFile(st.Path(kept)); string(raw) != string(damaged) {
		t.Fatalf("kept file: %q", raw)
	}
	if v, err := again.Get(); err != nil || v.Exists {
		t.Fatalf("after reset: %+v %v", v, err)
	}
}

func TestResetKeepsTheFileAndStartsANewVault(t *testing.T) {
	s, st := ready(t)
	first, _ := s.Get()
	if _, err := s.Put("item-0001", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	var published []map[string]any
	s.Publish = func(v any) { published = append(published, v.(map[string]any)) }
	kept, err := s.Reset()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.Path(kept)); err != nil {
		t.Fatalf("kept file: %v", err)
	}
	if _, err := os.Stat(st.Path(prevName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous copy: %v", err)
	}
	if len(published) != 1 || published[0]["type"] != "vaultChanged" {
		t.Fatalf("event: %v", published)
	}
	if v, _ := s.Get(); v.Exists || len(v.Items) != 0 {
		t.Fatalf("after reset: %+v", v)
	}
	h, err := s.Init(goodKey())
	if err != nil {
		t.Fatal(err)
	}
	if h.ID == first.Header.ID {
		t.Fatal("a new vault must have a new id: open tabs lock on it")
	}
	v, _ := s.Get()
	if v.Rev <= first.Rev {
		t.Fatalf("revision went back: %d then %d", first.Rev, v.Rev)
	}
}

func TestChangingTheKeyLeavesItemsAlone(t *testing.T) {
	s, _ := ready(t)
	it, err := s.Put("item-0001", b64(64), 0)
	if err != nil {
		t.Fatal(err)
	}
	k := goodKey()
	k.Salt = b64(32)
	h, err := s.SetKey(k, 1)
	if err != nil {
		t.Fatal(err)
	}
	if h.KeyRev != 2 || h.Salt != k.Salt {
		t.Fatalf("header %+v", h)
	}
	if _, err := s.SetKey(k, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale change: %v", err)
	}
	v, _ := s.Get()
	if len(v.Items) != 1 || v.Items[0].Rev != it.Rev {
		t.Fatalf("items changed: %+v", v.Items)
	}
}

func TestEventsSayNothingAboutTheContent(t *testing.T) {
	s, _ := ready(t)
	var got map[string]any
	s.Publish = func(v any) { got = v.(map[string]any) }
	if _, err := s.Put("item-0001", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	for k := range got {
		if k != "type" && k != "rev" && k != "vaultId" {
			t.Fatalf("event carries %q: %v", k, got)
		}
	}
}

func TestConcurrentWritesKeepEveryItem(t *testing.T) {
	s, st := ready(t)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Put("item-"+strings.Repeat("0", 4)+itoa(i), b64(64), 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	v, err := New(st).Get()
	if err != nil || len(v.Items) != 40 {
		t.Fatalf("after reload: %d items, %v", len(v.Items), err)
	}
}
