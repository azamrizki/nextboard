package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/vault"
)

func vaultKey() map[string]any {
	return map[string]any{
		"kdf":        map[string]any{"name": "argon2id", "m": 65536, "t": 3, "p": 4},
		"salt":       base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"wrappedKey": base64.StdEncoding.EncodeToString(make([]byte, 60)),
	}
}

func vaultBlob(b byte) string {
	raw := make([]byte, 64)
	raw[0] = b
	return base64.StdEncoding.EncodeToString(raw)
}

func TestVaultNeedsLogin(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	anon := &testHub{t: t, srv: h.srv, ts: h.ts, c: h.ts.Client()}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/vault"},
		{"POST", "/api/vault"},
		{"PUT", "/api/vault/key"},
		{"POST", "/api/vault/reset"},
		{"POST", "/api/vault/batch"},
		{"PUT", "/api/vault/items/item-0001"},
		{"DELETE", "/api/vault/items/item-0001?baseRev=1"},
		{"GET", "/api/vault/agents"},
		{"PUT", "/api/vault/agents/item-0001"},
		{"GET", "/api/vault/requests"},
		{"POST", "/api/vault/requests/cr_x/approve"},
		{"POST", "/api/vault/requests/cr_x/deny"},
		{"GET", "/api/vault/agent-log"},
	} {
		if st, _ := anon.do(c.method, c.path, map[string]any{}, nil); st != 401 {
			t.Errorf("%s %s without a login: %d", c.method, c.path, st)
		}
	}
	if st, _ := h.do("POST", "/api/vault", vaultKey(), map[string]string{"X-NextBoard": ""}); st != 403 {
		t.Fatalf("no csrf header: %d", st)
	}
}

func TestVaultFlow(t *testing.T) {
	h := newTestHub(t)
	h.setup()

	st, v := h.do("GET", "/api/vault", nil, nil)
	if st != 200 || v["exists"] != false {
		t.Fatalf("fresh vault %d %v", st, v)
	}
	st, hd := h.do("POST", "/api/vault", vaultKey(), nil)
	if st != 201 || hd["id"] == "" {
		t.Fatalf("init %d %v", st, hd)
	}
	if st, b := h.do("POST", "/api/vault", vaultKey(), nil); st != 409 || b["code"] != "vault_exists" {
		t.Fatalf("second init %d %v", st, b)
	}

	conn, ev := h.eventsCh("vaultChanged")
	defer conn.Close()

	st, it := h.do("PUT", "/api/vault/items/item-0001", map[string]any{"blob": vaultBlob(1), "baseRev": 0}, nil)
	if st != 200 {
		t.Fatalf("put %d %v", st, it)
	}
	deadline := time.After(5 * time.Second)
	for seen := false; !seen; {
		select {
		case e := <-ev:
			if e["type"] != "vaultChanged" {
				continue // a stream opens with a snapshot
			}
			if e["vaultId"] != hd["id"] || e["blob"] != nil {
				t.Fatalf("event %v", e)
			}
			seen = true
		case <-deadline:
			t.Fatal("no event")
		}
	}
	rev := int64(it["rev"].(float64))

	if st, b := h.do("PUT", "/api/vault/items/item-0001", map[string]any{"blob": vaultBlob(2), "baseRev": rev - 1}, nil); st != 409 || b["code"] != "conflict" {
		t.Fatalf("stale put %d %v", st, b)
	}
	if st, b := h.do("PUT", "/api/vault/items/item-0002", map[string]any{"blob": "my password in the clear", "baseRev": 0}, nil); st != 400 {
		t.Fatalf("a blob that is not ciphertext %d %v", st, b)
	}
	if st, _ := h.do("DELETE", "/api/vault/items/item-0001", nil, nil); st != 400 {
		t.Fatalf("delete without baseRev %d", st)
	}
	st, batch := h.do("POST", "/api/vault/batch", map[string]any{"items": []map[string]any{
		{"id": "item-0002", "blob": vaultBlob(3)},
		{"id": "item-0003", "blob": vaultBlob(4)},
	}}, nil)
	if st != 200 || len(batch["items"].([]any)) != 2 {
		t.Fatalf("batch %d %v", st, batch)
	}

	key := vaultKey()
	key["salt"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	key["baseKeyRev"] = 1
	if st, b := h.do("PUT", "/api/vault/key", key, nil); st != 200 || b["keyRev"] != float64(2) {
		t.Fatalf("change key %d %v", st, b)
	}
	if st, b := h.do("PUT", "/api/vault/key", key, nil); st != 409 {
		t.Fatalf("stale change of key %d %v", st, b)
	}

	if st, _ := h.do("DELETE", "/api/vault/items/item-0001?baseRev="+itoa64(rev), nil, nil); st != 200 {
		t.Fatalf("delete %d", st)
	}
	st, v = h.do("GET", "/api/vault", nil, nil)
	if st != 200 || len(v["items"].([]any)) != 2 {
		t.Fatalf("after delete %d %v", st, v)
	}

	// The vault file is the one thing a hub backup must carry, and it is private.
	info, err := os.Stat(filepath.Join(h.srv.st.Dir, "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("vault.json mode %v", info.Mode())
	}

	if st, _ := h.do("POST", "/api/vault/reset", map[string]any{"confirm": "yes"}, nil); st != 400 {
		t.Fatalf("reset without the word %d", st)
	}
	st, r := h.do("POST", "/api/vault/reset", map[string]any{"confirm": "RESET"}, nil)
	if st != 200 || !strings.HasPrefix(r["kept"].(string), "vault.reset-") {
		t.Fatalf("reset %d %v", st, r)
	}
	if _, err := os.Stat(filepath.Join(h.srv.st.Dir, r["kept"].(string))); err != nil {
		t.Fatalf("reset keeps the file: %v", err)
	}
	if st, v = h.do("GET", "/api/vault", nil, nil); st != 200 || v["exists"] != false {
		t.Fatalf("after reset %d %v", st, v)
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for ; n > 0; n /= 10 {
		out = string(rune('0'+n%10)) + out
	}
	return out
}

// A device works with items, but a stolen device token must not be able to lock the owner out.
func TestVaultDeviceCannotReplaceTheKey(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	if st, _ := h.do("POST", "/api/vault", vaultKey(), nil); st != 201 {
		t.Fatalf("init %d", st)
	}
	_, ps := h.do("POST", "/api/pair/start", nil, nil)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	anon := &testHub{t: t, srv: h.srv, ts: h.ts, c: h.ts.Client()}
	st, pc := anon.do("POST", "/api/pair/complete", map[string]any{
		"code": ps["code"], "deviceName": "Pixel 8", "platform": "android", "publicKey": base64.StdEncoding.EncodeToString(pub),
	}, nil)
	if st != 200 {
		t.Fatalf("pair %d %v", st, pc)
	}
	devID := pc["deviceId"].(string)
	_, ch := anon.do("POST", "/api/auth/challenge", map[string]any{"deviceId": devID}, nil)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(ch["nonce"].(string))))
	_, tok := anon.do("POST", "/api/auth/device", map[string]any{"deviceId": devID, "nonce": ch["nonce"], "sig": sig}, nil)
	bearer := map[string]string{"Authorization": "Bearer " + tok["accessToken"].(string)}

	if st, _ := anon.do("GET", "/api/vault", nil, bearer); st != 200 {
		t.Fatalf("device read %d", st)
	}
	if st, _ := anon.do("PUT", "/api/vault/items/item-0001", map[string]any{"blob": vaultBlob(1), "baseRev": 0}, bearer); st != 200 {
		t.Fatalf("device put %d", st)
	}
	key := vaultKey()
	key["baseKeyRev"] = 1
	if st, _ := anon.do("PUT", "/api/vault/key", key, bearer); st != 403 {
		t.Fatalf("device change key %d", st)
	}
	if st, _ := anon.do("POST", "/api/vault/reset", map[string]any{"confirm": "RESET"}, bearer); st != 403 {
		t.Fatalf("device reset %d", st)
	}
}

// The hub's file API refuses its data folder, so no tile or tool can fetch the file through it.
func TestVaultFileIsNotServedByTheFileAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	if st, _ := h.do("POST", "/api/vault", vaultKey(), nil); st != 201 {
		t.Fatalf("init %d", st)
	}
	p := filepath.Join(h.srv.st.Dir, "vault.json")
	for _, path := range []string{"/api/machines/local/fs/read?path=" + p, "/api/machines/local/fs/raw?path=" + p} {
		if st, b := h.do("GET", path, nil, nil); st == 200 {
			t.Fatalf("%s served the vault: %v", path, b)
		}
	}
}

// A big import fits in one request, and an item alone does not need the room.
func TestVaultBodyLimits(t *testing.T) {
	if got := bodyLimit("/api/vault/batch"); got < vault.MaxBatch*vault.MaxBlobChars/4 {
		t.Fatalf("batch limit %d", got)
	}
	if got := bodyLimit("/api/vault/items/item-0001"); got <= vault.MaxBlobChars {
		t.Fatalf("item limit %d", got)
	}
}

func vaultProof(b byte) string {
	raw := make([]byte, 32)
	raw[0] = b
	return base64.StdEncoding.EncodeToString(raw)
}

func TestVaultPinFlow(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	if st, _ := h.do("POST", "/api/vault", vaultKey(), nil); st != 201 {
		t.Fatalf("init %d", st)
	}
	anon := &testHub{t: t, srv: h.srv, ts: h.ts, c: h.ts.Client()}
	if st, _ := anon.do("POST", "/api/vault/pins", map[string]any{"proof": vaultProof(1)}, nil); st != 401 {
		t.Fatalf("no login %d", st)
	}
	if st, _ := h.do("POST", "/api/vault/pins", map[string]any{"proof": "123456"}, nil); st != 400 {
		t.Fatalf("a PIN itself must be refused %d", st)
	}
	st, made := h.do("POST", "/api/vault/pins", map[string]any{"proof": vaultProof(1)}, nil)
	if st != 201 || made["secret"] == nil {
		t.Fatalf("create %d %v", st, made)
	}
	id := made["id"].(string)
	open := "/api/vault/pins/" + id + "/open"
	if st, _ := anon.do("POST", open, map[string]any{"proof": vaultProof(1)}, nil); st != 401 {
		t.Fatalf("open without a login %d", st)
	}
	st, got := h.do("POST", open, map[string]any{"proof": vaultProof(1)}, nil)
	if st != 200 || got["secret"] != made["secret"] {
		t.Fatalf("right proof %d %v", st, got)
	}
	for left := 4; left >= 1; left-- {
		st, b := h.do("POST", open, map[string]any{"proof": vaultProof(2)}, nil)
		if st != 403 || b["code"] != "wrong_pin" || b["left"] != float64(left) || b["secret"] != nil {
			t.Fatalf("wrong proof, %d left: %d %v", left, st, b)
		}
	}
	if st, b := h.do("POST", open, map[string]any{"proof": vaultProof(2)}, nil); st != 410 || b["code"] != "pin_gone" {
		t.Fatalf("out of tries %d %v", st, b)
	}
	if st, b := h.do("POST", open, map[string]any{"proof": vaultProof(1)}, nil); st != 410 {
		t.Fatalf("right proof after that %d %v", st, b)
	}

	// What a client can read never shows a PIN.
	st, made = h.do("POST", "/api/vault/pins", map[string]any{"proof": vaultProof(3)}, nil)
	if st != 201 {
		t.Fatalf("create %d", st)
	}
	_, v := h.do("GET", "/api/vault", nil, nil)
	if v["pins"] != nil {
		t.Fatalf("view %v", v)
	}
	raw, _ := os.ReadFile(filepath.Join(h.srv.st.Dir, "vault.json"))
	if strings.Contains(string(raw), made["secret"].(string)) {
		t.Fatal("the secret is on disk")
	}
	if st, _ := h.do("DELETE", "/api/vault/pins/"+made["id"].(string), nil, nil); st != 200 {
		t.Fatalf("delete %d", st)
	}
	if st, _ := h.do("POST", "/api/vault/pins/"+made["id"].(string)+"/open", map[string]any{"proof": vaultProof(3)}, nil); st != 410 {
		t.Fatalf("after delete %d", st)
	}
	if st, _ := h.do("POST", "/api/vault/pins", map[string]any{"proof": vaultProof(4)}, nil); st != 201 {
		t.Fatalf("create %d", st)
	}
	if st, _ := h.do("DELETE", "/api/vault/pins", nil, nil); st != 200 {
		t.Fatalf("clear %d", st)
	}
}

// The breach check passes 5 hex characters on, and nothing else: no cookie, no other path.
func TestVaultPwnedRelay(t *testing.T) {
	var got []*http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r)
		_, _ = w.Write([]byte("0018A45C4D1DEF81644B54AB7F969B88D65:3\r\n00000000000000000000000000000000000:0\r\n"))
	}))
	defer upstream.Close()
	old := pwnedURL
	pwnedURL = upstream.URL + "/range/"
	defer func() { pwnedURL = old }()

	h := newTestHub(t)
	if st, _ := h.do("GET", "/api/vault/pwned/21BD1", nil, nil); st != 401 {
		t.Fatalf("no login: %d", st)
	}
	h.setup()
	st, out := h.do("GET", "/api/vault/pwned/21BD1", nil, nil)
	if st != 200 || !strings.Contains(out["_raw"].(string), "0018A45C4D1DEF81644B54AB7F969B88D65:3") {
		t.Fatalf("%d %v", st, out)
	}
	if len(got) != 1 || got[0].URL.Path != "/range/21BD1" || got[0].Header.Get("Add-Padding") != "true" || got[0].Header.Get("Cookie") != "" {
		t.Fatalf("upstream got %+v", got)
	}
	for _, bad := range []string{"21bd1", "21BD", "21BD12", "..%2F..", "ZZZZZ"} {
		if st, _ := h.do("GET", "/api/vault/pwned/"+bad, nil, nil); st != 400 && st != 404 {
			t.Errorf("%q: %d", bad, st)
		}
	}
	if len(got) != 1 {
		t.Fatalf("bad prefixes reached upstream: %d", len(got))
	}
}
