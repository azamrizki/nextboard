package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// `go install` builds without the bundle, which is not in the repository.
func TestSPAWithoutBundleSaysWhatToDo(t *testing.T) {
	w := httptest.NewRecorder()
	spaHandlerFS(fstest.MapFS{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "make build") {
		t.Errorf("status %d, body %q", w.Code, w.Body.String())
	}
}

func TestSPAGzipsTextAssets(t *testing.T) {
	big := strings.Repeat("console.log('x');\n", 1000)
	files := fstest.MapFS{
		"index.html":      {Data: []byte("<html>")},
		"assets/a.js":     {Data: []byte(big)},
		"assets/small.js": {Data: []byte("1")},
		"assets/i.png":    {Data: bytes.Repeat([]byte{1}, 5000)},
	}
	h := spaHandlerFS(files)
	get := func(path, enc string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if enc != "" {
			r.Header.Set("Accept-Encoding", enc)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/assets/a.js", "gzip, br")
	if w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(w.Header().Get("Content-Type"), "javascript") || w.Body.Len() >= len(big) {
		t.Fatalf("not compressed: %v %d", w.Header(), w.Body.Len())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != big {
		t.Fatal("body differs")
	}
	if w := get("/assets/a.js", ""); w.Header().Get("Content-Encoding") != "" || w.Body.String() != big {
		t.Fatalf("plain client got %v", w.Header())
	}
	if w := get("/assets/small.js", "gzip"); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("tiny asset compressed")
	}
	if w := get("/assets/i.png", "gzip"); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("png compressed")
	}
	if w := get("/some/route", "gzip"); w.Body.String() != "<html>" {
		t.Fatalf("spa fallback: %q", w.Body.String())
	}
}
