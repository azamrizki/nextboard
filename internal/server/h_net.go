package server

import (
	crand "crypto/rand"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// The speed between a browser and the hub (docs/API.md, Connection to the hub). The latency is
// measured over /api/events (a ping the hub answers with a pong); these are the speed test,
// which a browser runs only when asked, and what the hub can tell of the route.
const (
	netDownDefault = 10 << 20
	netDownMax     = 64 << 20
	netUpMax       = 32 << 20
	netChunk       = 64 << 10
)

// handleNet says whether the request came through a reverse proxy: one sets these headers.
func (s *Server) handleNet(w http.ResponseWriter, r *http.Request, _ *principal) {
	proxied := false
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded", "Via"} {
		if r.Header.Get(h) != "" {
			proxied = true
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxied": proxied})
}

// handleNetDownload sends ?bytes= bytes (10 MB unless asked, at most 64 MB) that no proxy can
// compress or cache, so the browser times the link itself.
func (s *Server) handleNetDownload(w http.ResponseWriter, r *http.Request, _ *principal) {
	n := int64(netDownDefault)
	if v := r.URL.Query().Get("bytes"); v != "" {
		x, err := strconv.ParseInt(v, 10, 64)
		if err != nil || x < 1 || x > netDownMax {
			writeErr(w, http.StatusBadRequest, "bytes: from 1 to "+strconv.Itoa(netDownMax))
			return
		}
		n = x
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(n, 10))
	h.Set("Cache-Control", "no-store")
	// nginx passes it on as it comes instead of holding it first: the test times the whole route
	h.Set("X-Accel-Buffering", "no")
	var seed [32]byte
	_, _ = crand.Read(seed[:])
	src := rand.NewChaCha8(seed)
	buf := make([]byte, netChunk)
	for n > 0 {
		k := min(n, int64(len(buf)))
		_, _ = src.Read(buf[:k])
		if _, err := w.Write(buf[:k]); err != nil {
			return
		}
		n -= k
	}
}

// handleNetUpload reads the body (at most 32 MB, bodyLimit) and throws it away. The browser
// times the upload itself: a proxy that holds the whole body before passing it on would make the
// hub's own time the proxy's, not the browser's.
func (s *Server) handleNetUpload(w http.ResponseWriter, r *http.Request, _ *principal) {
	start := time.Now()
	n, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "the speed test sends at most "+strconv.Itoa(netUpMax>>20)+" MB")
			return
		}
		writeErr(w, http.StatusBadRequest, "the upload broke off")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bytes": n, "ms": time.Since(start).Milliseconds()})
}
