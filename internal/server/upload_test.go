package server

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/machine"
)

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// upload sends a raw body, with Content-Encoding when enc is set.
func (h *testHub) upload(method, path, contentType, enc string, body io.Reader) (int, map[string]any) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.ts.URL+path, body)
	req.Header.Set("X-NextBoard", "1")
	req.Header.Set("Content-Type", contentType)
	if enc != "" {
		req.Header.Set("Content-Encoding", enc)
	}
	resp, err := h.c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func writeURL(p string, extra ...string) string {
	u := "/api/machines/local/fs/write?path=" + url.QueryEscape(p)
	for _, e := range extra {
		u += "&" + e
	}
	return u
}

func TestRawUpload(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	put := func(u, enc string, body []byte) (int, map[string]any) {
		t.Helper()
		return h.upload("PUT", u, "application/octet-stream", enc, bytes.NewReader(body))
	}
	files := func() []string {
		t.Helper()
		es, _ := os.ReadDir(dir)
		var out []string
		for _, e := range es {
			out = append(out, e.Name())
		}
		return out
	}

	random := make([]byte, 1<<20)
	rand.Read(random)
	target := filepath.Join(dir, "a.bin")
	if code, st := put(writeURL(target, "unique=1"), "", random); code != 200 || st["path"] != target {
		t.Fatalf("plain %d %v", code, st)
	}
	if b, _ := os.ReadFile(target); !bytes.Equal(b, random) {
		t.Fatal("plain content")
	}
	if code, st := put(writeURL(target, "unique=true"), "", []byte("x")); code != 200 || st["path"] != filepath.Join(dir, "a-2.bin") {
		t.Fatalf("unique %d %v", code, st)
	}

	// gzip: what lands is what was compressed
	text := []byte(strings.Repeat("func main() { println(\"hello\") }\n", 60000))
	packed := gz(t, text)
	if len(packed) > len(text)/20 {
		t.Fatalf("test text does not compress: %d", len(packed))
	}
	code, st := put(writeURL(filepath.Join(dir, "main.go")), "gzip", packed)
	if code != 200 || st["size"].(float64) != float64(len(text)) {
		t.Fatalf("gzip %d %v", code, st)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "main.go")); !bytes.Equal(b, text) {
		t.Fatal("gzip content")
	}

	// in place, with ifMtime
	mtime := st["mtime"].(string)
	if code, _ := put(writeURL(filepath.Join(dir, "main.go"), "ifMtime="+url.QueryEscape(mtime)), "", []byte("v2")); code != 200 {
		t.Fatalf("ifMtime %d", code)
	}
	if code, _ := put(writeURL(filepath.Join(dir, "main.go"), "ifMtime=2001-01-01T00:00:00Z"), "", []byte("v3")); code != 409 {
		t.Fatalf("stale ifMtime %d", code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "main.go")); string(b) != "v2" {
		t.Fatalf("after 409 %q", b)
	}

	before := files()
	// a small body that unpacks past the limit stops at the limit
	bomb := gz(t, make([]byte, machine.MaxFileBytes+1<<20))
	if code, _ := put(writeURL(filepath.Join(dir, "bomb"), "unique=1"), "gzip", bomb); code != 413 {
		t.Fatalf("bomb %d", code)
	}
	if code, _ := put(writeURL(filepath.Join(dir, "big"), "unique=1"), "", make([]byte, machine.MaxFileBytes+1)); code != 413 {
		t.Fatalf("big %d", code)
	}
	if code, _ := put(writeURL(filepath.Join(dir, "bad"), "unique=1"), "gzip", []byte("not gzip at all")); code != 400 {
		t.Fatalf("not gzip %d", code)
	}
	// cut off halfway: nothing is written, not even half
	if code, _ := put(writeURL(filepath.Join(dir, "cut"), "unique=1"), "gzip", packed[:len(packed)/2]); code != 400 {
		t.Fatalf("cut %d", code)
	}
	if code, _ := put(writeURL(filepath.Join(dir, "br"), "unique=1"), "br", []byte("x")); code != 415 {
		t.Fatalf("br %d", code)
	}
	if code, _ := put(writeURL(""), "", []byte("x")); code != 400 {
		t.Fatalf("no path %d", code)
	}
	if code, _ := put(writeURL(h.srv.st.Path("auth.json")), "", []byte("{}")); code != 403 {
		t.Fatalf("hub data %d", code)
	}
	if after := files(); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("a refused upload left %v", after)
	}
	if es, _ := os.ReadDir(h.srv.st.Path(uploadDir)); len(es) != 0 {
		t.Fatalf("spooled uploads left: %d", len(es))
	}

	// the JSON way still works
	if code, _ := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": filepath.Join(dir, "j.txt"), "content": base64.StdEncoding.EncodeToString([]byte("json")), "encoding": "base64"}, nil); code != 200 {
		t.Fatalf("json %d", code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "j.txt")); string(b) != "json" {
		t.Fatalf("json %q", b)
	}
}

// A raw upload costs the hub a few buffers; the same file as base64 JSON cost it several copies.
func TestRawUploadHoldsLittleMemory(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	data := make([]byte, 16<<20)
	rand.Read(data)
	b64, _ := json.Marshal(map[string]any{"path": filepath.Join(dir, "j.bin"), "content": base64.StdEncoding.EncodeToString(data), "encoding": "base64"})

	measure := func(send func()) uint64 {
		runtime.GC()
		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)
		send()
		runtime.ReadMemStats(&b)
		return b.TotalAlloc - a.TotalAlloc
	}
	raw := measure(func() {
		if code, _ := h.upload("PUT", writeURL(filepath.Join(dir, "r.bin")), "application/octet-stream", "", bytes.NewReader(data)); code != 200 {
			t.Fatalf("raw %d", code)
		}
	})
	jsonWay := measure(func() {
		if code, _, _ := h.doRaw("PUT", "/api/machines/local/fs/write", "application/json", b64); code != 200 {
			t.Fatalf("json %d", code)
		}
	})
	t.Logf("16 MB upload allocated: raw %.1f MB, base64 JSON %.1f MB", float64(raw)/(1<<20), float64(jsonWay)/(1<<20))
	if raw > 4<<20 {
		t.Fatalf("raw upload allocated %.1f MB", float64(raw)/(1<<20))
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "r.bin")); !bytes.Equal(b, data) {
		t.Fatal("raw content")
	}
}

func TestCompressedAssetAndAttach(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	img := pngBytes(t, 40, 30)
	code, a := h.upload("POST", "/api/assets", "image/png", "gzip", bytes.NewReader(gz(t, img)))
	if code != 201 || a["size"].(float64) != float64(len(img)) || a["w"].(float64) != 40 {
		t.Fatalf("asset %d %v", code, a)
	}
	if b, err := os.ReadFile(h.srv.st.Path("assets", a["id"].(string))); err != nil || !bytes.Equal(b, img) {
		t.Fatalf("asset file %v", err)
	}
	if fi, _ := os.Stat(h.srv.st.Path("assets", a["id"].(string))); fi.Mode().Perm() != 0o600 {
		t.Fatalf("asset mode %v", fi.Mode())
	}
	if code, _ := h.upload("POST", "/api/assets", "image/png", "gzip", bytes.NewReader(gz(t, []byte("<svg/>")))); code != 415 {
		t.Fatalf("not an image %d", code)
	}

	dir := t.TempDir()
	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "up"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, pr)
	}
	log := []byte(strings.Repeat("2026-11-15 12:00:00 INFO request served\n", 20000))
	code, f := h.upload("POST", "/api/projects/"+pr["id"].(string)+"/attach?name=server.log", "text/plain", "gzip", bytes.NewReader(gz(t, log)))
	if code != 200 {
		t.Fatalf("attach %d %v", code, f)
	}
	if b, _ := os.ReadFile(f["path"].(string)); !bytes.Equal(b, log) {
		t.Fatal("attach content")
	}
	if code, _ := h.upload("POST", "/api/projects/"+pr["id"].(string)+"/attach?name=e", "text/plain", "", bytes.NewReader(nil)); code != 400 {
		t.Fatalf("empty %d", code)
	}
}
