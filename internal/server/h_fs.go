package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/machine"
)

const fsTimeout = 30 * time.Second

// fsCopyTimeout: copying a folder takes as long as the folder is big.
const fsCopyTimeout = 5 * time.Minute

func fsErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, machine.ErrNotConnected):
		writeErr(w, http.StatusBadGateway, err.Error())
	case errors.Is(err, machine.ErrRelativePath), errors.Is(err, machine.ErrIsDir):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, machine.ErrMtimeMismatch):
		writeErrCode(w, http.StatusConflict, "mtime_mismatch", err.Error())
	case errors.Is(err, machine.ErrNoSFTP):
		writeErrCode(w, http.StatusNotImplemented, "no_sftp", err.Error())
	case errors.Is(err, machine.ErrFileTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, "file tidak ditemukan")
	case errors.Is(err, fs.ErrPermission):
		writeErr(w, http.StatusForbidden, "akses ditolak")
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusGatewayTimeout, "machine tidak merespons")
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

// fsHost resolves {id} and the required ?path= query.
func (s *Server) fsHost(w http.ResponseWriter, r *http.Request) (machine.Host, string, bool) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return nil, "", false
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, http.StatusBadRequest, "path wajib diisi")
		return nil, "", false
	}
	return h, p, true
}

// hubPrivate refuses file API access to the hub's own data directory on
// This PC (secret.key, credentials, auth state), symlinks resolved.
func (s *Server) hubPrivate(w http.ResponseWriter, h machine.Host, p string) bool {
	if h.ID() != machine.LocalID {
		return false
	}
	abs, err := s.machines.Local().Resolve(p)
	if err != nil {
		return false // the machine layer reports the bad path
	}
	if inside(realPath(abs), realPath(s.st.Dir)) {
		writeErr(w, http.StatusForbidden, "folder data hub tidak bisa diakses lewat file API")
		return true
	}
	return false
}

// realPath resolves symlinks of p, or of its nearest existing parent.
func realPath(p string) string {
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func inside(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

func (s *Server) handleFSStat(w http.ResponseWriter, r *http.Request, p *principal) {
	h, fp, ok := s.fsHost(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsTimeout)
	defer cancel()
	st, err := h.Stat(ctx, fp)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleFSRead(w http.ResponseWriter, r *http.Request, p *principal) {
	h, fp, ok := s.fsHost(w, r)
	if !ok || s.hubPrivate(w, h, fp) {
		return
	}
	max := int64(machine.DefaultReadMax)
	if v := r.URL.Query().Get("max"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, "max tidak valid")
			return
		}
		max = min(n, machine.MaxFileBytes)
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsTimeout)
	defer cancel()
	q := r.URL.Query()
	if q.Has("offset") || q.Has("tail") {
		s.fsReadRange(ctx, w, h, fp, q.Get("offset"), q.Get("tail"), max)
		return
	}
	fc, err := h.ReadFile(ctx, fp, max)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fc)
}

// fsReadRange serves fs/read with offset (byte ≥ 0) or tail (last N
// bytes); the response adds "offset", the first byte returned.
func (s *Server) fsReadRange(ctx context.Context, w http.ResponseWriter, h machine.Host, fp, offS, tailS string, max int64) {
	parse := func(v string) (int64, bool) {
		if v == "" {
			return -1, true
		}
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil && n >= 0
	}
	off, ok1 := parse(offS)
	tail, ok2 := parse(tailS)
	if !ok1 || !ok2 || (off >= 0 && tail >= 0) {
		writeErr(w, http.StatusBadRequest, "pakai salah satu dari offset atau tail (byte ≥ 0)")
		return
	}
	st, err := h.Stat(ctx, fp)
	if err != nil {
		fsErr(w, err)
		return
	}
	if !st.Exists {
		fsErr(w, fs.ErrNotExist)
		return
	}
	if st.Dir {
		fsErr(w, machine.ErrIsDir)
		return
	}
	if tail >= 0 {
		off = max64(0, st.Size-min(tail, max))
	}
	data, st2, err := h.ReadAt(ctx, fp, off, max)
	if err != nil {
		fsErr(w, err)
		return
	}
	for i := 0; off > 0 && i < 3 && len(data) > 0 && !utf8.RuneStart(data[0]); i++ {
		data, off = data[1:], off+1 // do not start inside a UTF-8 sequence
	}
	fc := &machine.FileContent{Path: st2.Path, Size: st2.Size, Mtime: st2.Mtime, Truncated: off+int64(len(data)) < st2.Size}
	if machine.IsBinary(data, true) {
		fc.Binary = true
	} else {
		fc.Content = string(data)
	}
	writeJSON(w, http.StatusOK, struct {
		*machine.FileContent
		Offset int64 `json:"offset"`
	}{fc, off})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// rawTypes are served with their own Content-Type; everything else is
// text/plain (if it looks like text) or an octet-stream download. HTML is
// never served as HTML.
var rawTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp", ".ico": "image/x-icon",
	".svg": "image/svg+xml", ".pdf": "application/pdf",
}

func rawContentType(name string, head []byte) string {
	if t, ok := rawTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	if !machine.IsBinary(head, true) {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

func (s *Server) handleFSRaw(w http.ResponseWriter, r *http.Request, p *principal) {
	h, fp, ok := s.fsHost(w, r)
	if !ok || s.hubPrivate(w, h, fp) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	rc, st, err := h.OpenRaw(ctx, fp)
	if err != nil {
		fsErr(w, err)
		return
	}
	defer rc.Close()
	if st.Size > machine.MaxFileBytes {
		fsErr(w, machine.ErrFileTooLarge)
		return
	}
	br := bufio.NewReaderSize(rc, 8<<10)
	head, _ := br.Peek(8 << 10)
	ct := rawContentType(st.Name, head)
	hd := w.Header()
	hd.Set("Content-Type", ct)
	hd.Set("Content-Security-Policy", "sandbox")
	if ct == "application/octet-stream" {
		hd.Set("Content-Disposition", "attachment")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(br, machine.MaxFileBytes))
}

// handleFSWrite writes a file on the machine. Only ever called on an
// explicit user action (save, copy into project).
func (s *Server) handleFSWrite(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	var req struct {
		Path     string  `json:"path"`
		Content  string  `json:"content"`
		Encoding string  `json:"encoding"`
		IfMtime  *string `json:"ifMtime"`
		Unique   bool    `json:"unique"`
		// Code studio (API-studio §7): with projectId, a file whose real
		// path is outside the project needs allowOutside; writes into a
		// Claude config folder of an SSH machine need confirm (§9.2).
		ProjectID    string       `json:"projectId"`
		AllowOutside bool         `json:"allowOutside"`
		Confirm      *confirmBody `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeErr(w, http.StatusBadRequest, "path wajib diisi")
		return
	}
	if s.hubPrivate(w, h, req.Path) {
		return
	}
	var data []byte
	switch req.Encoding {
	case "", "utf8":
		data = []byte(req.Content)
	case "base64":
		var err error
		if data, err = base64.StdEncoding.DecodeString(req.Content); err != nil {
			writeErr(w, http.StatusBadRequest, "content base64 tidak valid")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, `encoding harus "utf8" atau "base64"`)
		return
	}
	if len(data) > machine.MaxFileBytes {
		fsErr(w, machine.ErrFileTooLarge)
		return
	}
	opt := machine.WriteOptions{Unique: req.Unique}
	if req.IfMtime != nil && *req.IfMtime != "" {
		t, err := time.Parse(time.RFC3339Nano, *req.IfMtime)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "ifMtime harus RFC 3339")
			return
		}
		opt.IfMtime = &t
	}
	if !s.writeAllowed(w, r, h, req.Path, req.ProjectID, req.AllowOutside, req.Confirm) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if req.ProjectID != "" {
		s.review.UserWrote(req.Path)
	}
	st, err := h.WriteFile(ctx, req.Path, data, opt)
	if err != nil {
		fsErr(w, err)
		return
	}
	if req.ProjectID != "" {
		s.review.Soon(req.ProjectID)
	}
	writeJSON(w, http.StatusOK, st)
}
