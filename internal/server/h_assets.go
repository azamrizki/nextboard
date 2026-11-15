package server

import (
	"bufio"
	"image"
	_ "image/gif" // DecodeConfig formats
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"

	_ "golang.org/x/image/webp"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/store"
)

const maxAssetBytes = 20 << 20

var assetIDRe = regexp.MustCompile(`^a_[a-z0-9]{6,24}$`)

// assetTypes are the only accepted blobs. SVG is refused (it can carry
// scripts).
var assetTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

func (s *Server) assetPath(id string) string { return s.st.Path("assets", id) }

// isImageAsset reports whether id names a stored image.
func (s *Server) isImageAsset(id string) bool {
	if !assetIDRe.MatchString(id) {
		return false
	}
	f, err := os.Open(s.assetPath(id))
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	return assetTypes[http.DetectContentType(head[:n])]
}

// storeAsset keeps an image made by the hub (the art job) in the asset store.
func (s *Server) storeAsset(data []byte) (string, error) {
	if err := os.MkdirAll(s.st.Path("assets"), 0o700); err != nil {
		return "", err
	}
	id := auth.RandomID("a_")
	return id, store.WriteFileAtomic(s.assetPath(id), data)
}

func (s *Server) handleAssetUpload(w http.ResponseWriter, r *http.Request, p *principal) {
	declared, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !assetTypes[declared] {
		writeErr(w, http.StatusUnsupportedMediaType, "image type not supported (png, jpeg, gif, webp)")
		return
	}
	f, size, ok := s.spool(w, r, maxAssetBytes)
	if !ok {
		return
	}
	defer dropSpool(f)
	if size == 0 {
		writeErr(w, http.StatusBadRequest, "empty body")
		return
	}
	// Trust the bytes, not the header.
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	typ := http.DetectContentType(head[:n])
	if !assetTypes[typ] {
		writeErr(w, http.StatusUnsupportedMediaType, "the file content is not a png, jpeg, gif or webp image")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg, _, err := image.DecodeConfig(bufio.NewReader(f))
	if err != nil {
		writeErr(w, http.StatusUnsupportedMediaType, "the image cannot be read")
		return
	}
	if err := os.MkdirAll(s.st.Path("assets"), 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := auth.RandomID("a_")
	// the spooled file is in the same data folder: it moves into place whole
	if err := f.Sync(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(f.Name(), s.assetPath(id)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "url": "/api/assets/" + id, "type": typ, "size": size, "w": cfg.Width, "h": cfg.Height,
	})
}

func (s *Server) handleAssetGet(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	if !assetIDRe.MatchString(id) {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	f, err := os.Open(s.assetPath(id))
	if err != nil {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	typ := http.DetectContentType(head[:n])
	if !assetTypes[typ] {
		typ = "application/octet-stream"
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", typ)
	hd.Set("Cache-Control", "private, max-age=31536000, immutable")
	hd.Set("Content-Security-Policy", "sandbox")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}
