package server

import (
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Uploads come as raw bytes, gzip-compressed by the browser when that makes them smaller
// (docs/API-board.md §4). Each is spooled to a file in the hub's data folder before it goes to
// its machine: the hub holds 32 KB of it at a time instead of the whole file three times over,
// as base64 in JSON did, and an upload cut off halfway never leaves half a file where it was
// going.

// uploadDir holds the spooled uploads. What a hub that stopped mid-upload left is removed when
// the next one starts.
const uploadDir = "uploads"

// spool reads the request body, decompressed, into a temporary file at its start. At most max
// bytes, after decompression: a small body that unpacks into gigabytes stops at max. On failure
// it has answered the request itself.
func (s *Server) spool(w http.ResponseWriter, r *http.Request, max int64) (*os.File, int64, bool) {
	src := io.Reader(r.Body)
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			uploadErr(w, err, max)
			return nil, 0, false
		}
		defer zr.Close()
		src = zr
	default:
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Encoding must be gzip or none")
		return nil, 0, false
	}
	dir := s.st.Path(uploadDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, 0, false
	}
	f, err := os.CreateTemp(dir, "up-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, 0, false
	}
	n, err := io.Copy(f, io.LimitReader(src, max+1))
	if err == nil && n > max {
		err = errUploadTooBig
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		dropSpool(f)
		uploadErr(w, err, max)
		return nil, 0, false
	}
	return f, n, true
}

var errUploadTooBig = errors.New("upload too big")

func uploadErr(w http.ResponseWriter, err error, max int64) {
	var mbe *http.MaxBytesError
	var corrupt flate.CorruptInputError
	switch {
	case errors.Is(err, errUploadTooBig), errors.As(err, &mbe):
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file larger than %d MB", max>>20))
	case errors.Is(err, gzip.ErrHeader), errors.Is(err, gzip.ErrChecksum), errors.As(err, &corrupt):
		writeErr(w, http.StatusBadRequest, "the body is not valid gzip")
	case errors.Is(err, io.ErrUnexpectedEOF):
		writeErr(w, http.StatusBadRequest, "the body ended early")
	default:
		writeErr(w, http.StatusBadRequest, "could not read the body")
	}
}

// dropSpool closes and removes a spooled upload; one moved into place already is left alone.
func dropSpool(f *os.File) {
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}
