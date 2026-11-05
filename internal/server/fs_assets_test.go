package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fsURL(op, p string, extra ...string) string {
	u := "/api/machines/local/fs/" + op + "?path=" + url.QueryEscape(p)
	for _, e := range extra {
		u += "&" + e
	}
	return u
}

func TestLocalFileAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	txt := filepath.Join(dir, "notes.md")
	os.WriteFile(txt, []byte("hello ✓ world"), 0o644)
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2, 3}, 0o644)
	os.WriteFile(filepath.Join(dir, "pic.png"), pngBytes(t, 2, 2), 0o644)
	os.WriteFile(filepath.Join(dir, "page.html"), []byte("<script>alert(1)</script>"), 0o644)

	// stat
	_, st := h.do("GET", fsURL("stat", txt), nil, nil)
	if st["exists"] != true || st["dir"] != false || st["size"].(float64) != float64(len("hello ✓ world")) || st["name"] != "notes.md" || st["mtime"] == nil {
		t.Fatalf("stat %v", st)
	}
	if code, miss := h.do("GET", fsURL("stat", filepath.Join(dir, "nope")), nil, nil); code != 200 || miss["exists"] != false {
		t.Fatalf("stat missing %d %v", code, miss)
	}
	if code, _ := h.do("GET", fsURL("stat", "relative/x"), nil, nil); code != 400 {
		t.Fatalf("relative path %d", code)
	}
	if code, d := h.do("GET", fsURL("stat", "~"), nil, nil); code != 200 || d["dir"] != true || d["path"] != homeDir() {
		t.Fatalf("stat ~ %d %v", code, d)
	}

	// read
	_, rd := h.do("GET", fsURL("read", txt), nil, nil)
	if rd["content"] != "hello ✓ world" || rd["truncated"] != false || rd["binary"] != false {
		t.Fatalf("read %v", rd)
	}
	// Truncation never splits a UTF-8 rune ("hello " + first byte of ✓).
	_, rd = h.do("GET", fsURL("read", txt, "max=7"), nil, nil)
	if rd["content"] != "hello " || rd["truncated"] != true {
		t.Fatalf("read max %q %v", rd["content"], rd)
	}
	if _, rd = h.do("GET", fsURL("read", filepath.Join(dir, "blob.bin")), nil, nil); rd["binary"] != true || rd["content"] != "" {
		t.Fatalf("binary %v", rd)
	}
	if code, _ := h.do("GET", fsURL("read", dir), nil, nil); code != 400 {
		t.Fatalf("read dir %d", code)
	}
	if code, _ := h.do("GET", fsURL("read", filepath.Join(dir, "nope")), nil, nil); code != 404 {
		t.Fatalf("read missing %d", code)
	}

	// raw
	code, body, hdr := h.doRaw("GET", fsURL("raw", filepath.Join(dir, "pic.png")), "", nil)
	if code != 200 || hdr.Get("Content-Type") != "image/png" || hdr.Get("Content-Security-Policy") != "sandbox" || hdr.Get("X-Content-Type-Options") != "nosniff" || !bytes.HasPrefix(body, []byte("\x89PNG")) {
		t.Fatalf("raw png %d %v", code, hdr)
	}
	if _, _, hdr = h.doRaw("GET", fsURL("raw", filepath.Join(dir, "page.html")), "", nil); hdr.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("html must not be served as html: %v", hdr.Get("Content-Type"))
	}
	if _, _, hdr = h.doRaw("GET", fsURL("raw", filepath.Join(dir, "blob.bin")), "", nil); hdr.Get("Content-Type") != "application/octet-stream" || hdr.Get("Content-Disposition") != "attachment" {
		t.Fatalf("binary raw %v", hdr)
	}

	// write: create, ifMtime, unique, base64
	out := filepath.Join(dir, "new.txt")
	code, ws := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": out, "content": "v1"}, nil)
	if code != 200 || ws["path"] != out || ws["size"].(float64) != 2 {
		t.Fatalf("write %d %v", code, ws)
	}
	mtime := ws["mtime"].(string)
	if code, _ := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": out, "content": "v2", "ifMtime": mtime}, nil); code != 200 {
		t.Fatalf("write ifMtime %d", code)
	}
	if code, _ := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": out, "content": "v3", "ifMtime": "2001-01-01T00:00:00Z"}, nil); code != 409 {
		t.Fatalf("stale ifMtime %d", code)
	}
	if b, _ := os.ReadFile(out); string(b) != "v2" {
		t.Fatalf("content %q", b)
	}
	for i, want := range []string{"notes-2.md", "notes-3.md"} {
		_, u := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": txt, "content": "copy", "unique": true}, nil)
		if u["path"] != filepath.Join(dir, want) {
			t.Fatalf("unique #%d %v", i, u)
		}
	}
	if b, _ := os.ReadFile(txt); string(b) != "hello ✓ world" {
		t.Fatal("unique write overwrote the original")
	}
	raw := []byte{0xff, 0x00, 0x10}
	h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": filepath.Join(dir, "b.bin"), "content": base64.StdEncoding.EncodeToString(raw), "encoding": "base64"}, nil)
	if b, _ := os.ReadFile(filepath.Join(dir, "b.bin")); !bytes.Equal(b, raw) {
		t.Fatalf("base64 write %v", b)
	}
	if code, _ := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": out, "content": "x", "encoding": "hex"}, nil); code != 400 {
		t.Fatalf("bad encoding %d", code)
	}
	if code, _ := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": filepath.Join(dir, "no", "such", "dir.txt"), "content": "x"}, nil); code != 404 {
		t.Fatalf("missing parent %d", code)
	}
	big, _ := json.Marshal(map[string]any{"path": out, "content": strings.Repeat("x", 21<<20)})
	if code, _, _ := h.doRaw("PUT", "/api/machines/local/fs/write", "application/json", big); code != 413 {
		t.Fatalf("21 MB write %d", code)
	}

	// The hub's own data directory is off limits (also through a symlink).
	link := filepath.Join(dir, "sneaky")
	os.Symlink(h.srv.st.Dir, link)
	for _, u := range []string{fsURL("read", h.srv.st.Path("secret.key")), fsURL("raw", filepath.Join(link, "auth.json"))} {
		if code, _, _ := h.doRaw("GET", u, "", nil); code != 403 {
			t.Fatalf("%s → %d", u, code)
		}
	}
	if code, _ := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": filepath.Join(link, "auth.json"), "content": "{}"}, nil); code != 403 {
		t.Fatalf("write into data dir %d", code)
	}

	// ls has size and mtime
	var l struct {
		Entries []struct {
			Name  string     `json:"name"`
			Size  int64      `json:"size"`
			Mtime *time.Time `json:"mtime"`
		} `json:"entries"`
	}
	h.getJSON("/api/machines/local/ls?path="+url.QueryEscape(dir), &l)
	found := false
	for _, e := range l.Entries {
		if e.Name == "notes.md" {
			found = e.Size == int64(len("hello ✓ world")) && e.Mtime != nil && !e.Mtime.IsZero()
		}
	}
	if !found {
		t.Fatalf("ls entries %+v", l.Entries)
	}
}

func TestAssets(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	img := pngBytes(t, 3, 2)
	code, body, _ := h.doRaw("POST", "/api/assets", "image/png", img)
	var a map[string]any
	json.Unmarshal(body, &a)
	if code != 201 || a["type"] != "image/png" || a["w"].(float64) != 3 || a["h"].(float64) != 2 || a["size"].(float64) != float64(len(img)) {
		t.Fatalf("upload %d %s", code, body)
	}
	id := a["id"].(string)
	if a["url"] != "/api/assets/"+id || !strings.HasPrefix(id, "a_") {
		t.Fatalf("id/url %v", a)
	}
	if fi, err := os.Stat(h.srv.st.Path("assets", id)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("asset file %v %v", fi, err)
	}
	code, got, hdr := h.doRaw("GET", "/api/assets/"+id, "", nil)
	if code != 200 || !bytes.Equal(got, img) || hdr.Get("Content-Type") != "image/png" ||
		hdr.Get("Cache-Control") != "private, max-age=31536000, immutable" ||
		hdr.Get("Content-Security-Policy") != "sandbox" || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("serve %d %v", code, hdr)
	}

	// GIF works too; the sniffed type wins over the declared one.
	var gb bytes.Buffer
	gif.Encode(&gb, image.NewPaletted(image.Rect(0, 0, 5, 4), []color.Color{color.Black, color.White}), nil)
	code, body, _ = h.doRaw("POST", "/api/assets", "image/png", gb.Bytes())
	json.Unmarshal(body, &a)
	if code != 201 || a["type"] != "image/gif" || a["w"].(float64) != 5 {
		t.Fatalf("gif %d %s", code, body)
	}

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if code, _, _ := h.doRaw("POST", "/api/assets", "image/svg+xml", svg); code != 415 {
		t.Fatalf("svg declared %d", code)
	}
	if code, _, _ := h.doRaw("POST", "/api/assets", "image/png", svg); code != 415 {
		t.Fatalf("svg disguised as png %d", code)
	}
	if code, _, _ := h.doRaw("POST", "/api/assets", "image/png", []byte("\x89PNG\r\n\x1a\nbroken")); code != 415 {
		t.Fatalf("corrupt png %d", code)
	}
	huge := append(append([]byte{}, img...), make([]byte, 20<<20)...)
	if code, _, _ := h.doRaw("POST", "/api/assets", "image/png", huge); code != 413 {
		t.Fatalf("over 20 MB %d", code)
	}
	for _, bad := range []string{"a_nope0000", "..%2F..%2Fconfig.json", "a_..", "config.json"} {
		if code, _, _ := h.doRaw("GET", "/api/assets/"+bad, "", nil); code != 404 {
			t.Fatalf("asset %q → %d", bad, code)
		}
	}
	// Unauthenticated access is refused.
	anon := &testHub{t: t, srv: h.srv, ts: h.ts, c: h.ts.Client()}
	if code, _, _ := anon.doRaw("GET", "/api/assets/"+id, "", nil); code != 401 {
		t.Fatalf("anon asset %d", code)
	}
}
