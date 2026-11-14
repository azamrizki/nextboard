package server

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/azamrizki/nextboard/internal/webui"
)

// spaHandler serves the embedded frontend, falling back to index.html for
// client-side routes.
func spaHandler() http.Handler { return spaHandlerFS(webui.FS()) }

func spaHandlerFS(files fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(files))
	gz := &gzipAssets{files: files}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if fi, err := fs.Stat(files, p); err == nil && !fi.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					w.Header().Add("Vary", "Accept-Encoding")
					if gz.serve(w, r, p) {
						return
					}
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		b, err := fs.ReadFile(files, "index.html")
		if err != nil {
			http.Error(w, "This binary has no web UI: the bundle is not in the repository, so `go install` and a plain `go build` leave it out. Clone the repository and run `make build`.", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}

// gzipAssets compresses text assets once, on first request. The editor's
// workers are megabytes of JavaScript, which matters on a slow link to the hub.
type gzipAssets struct {
	files fs.FS
	cache sync.Map // path → *gzipAsset
}

type gzipAsset struct {
	once sync.Once
	body []byte // nil: not worth sending compressed
}

const gzipMinBytes = 1 << 10

func gzippable(p string) bool {
	switch path.Ext(p) {
	case ".js", ".css", ".svg", ".json", ".html", ".map", ".txt":
		return true
	}
	return false
}

// serve answers with the compressed asset when the client accepts it.
func (g *gzipAssets) serve(w http.ResponseWriter, r *http.Request, p string) bool {
	if !gzippable(p) || r.Header.Get("Range") != "" || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}
	v, _ := g.cache.LoadOrStore(p, &gzipAsset{})
	a := v.(*gzipAsset)
	a.once.Do(func() {
		raw, err := fs.ReadFile(g.files, p)
		if err != nil || len(raw) < gzipMinBytes {
			return
		}
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		if _, err := zw.Write(raw); err == nil && zw.Close() == nil {
			a.body = bytes.Clone(b.Bytes()) // kept for the life of the hub: without the buffer's spare room
		}
	})
	if a.body == nil {
		return false
	}
	h := w.Header()
	if ct := mime.TypeByExtension(path.Ext(p)); ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(len(a.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(a.body)
	}
	return true
}
