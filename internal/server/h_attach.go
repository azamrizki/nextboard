package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"nextboard/internal/machine"
	"nextboard/internal/session"
)

// Files pasted into a console or an order (docs/API-studio.md §7.9). A console
// gets them on the machine it runs on, an order on This PC, where agents run.
// The hub picks the place: nobody chooses a path here, so there is nothing to
// aim at the hub's private files or at the project. On This PC they stay a week
// in the hub's folder, because a resumed conversation may still point at them.
// On a server the folder lives in /tmp and goes with its console (owner decision
// #2): one fixed path per console, created only as a real folder of the user's,
// so that nothing has to be remembered for a hub that crashed.

const (
	attachKeep    = 7 * 24 * time.Hour
	attachNameMax = 80
	attachTimeout = 2 * time.Minute
	attachSweep   = 20 * time.Second
)

// attachRoot is the parent of a server's paste folders (tests use a scratch one).
var attachRoot = "/tmp"

// attachID limits what a fixed folder name may hold: it goes into a script.
var attachID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// attachName is the last part of what the browser calls the file, in characters
// no shell or CLI needs quoted, never starting like a dotfile or an option.
func attachName(name string) string {
	var b strings.Builder
	for _, r := range path.Base(strings.ReplaceAll(name, `\`, "/")) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := []rune(strings.TrimLeft(b.String(), ".-"))
	if len(out) > attachNameMax {
		ext := []rune(path.Ext(string(out)))
		if len(ext) >= attachNameMax/2 {
			ext = nil
		}
		out = append(out[:attachNameMax-len(ext):attachNameMax-len(ext)], ext...)
	}
	if len(out) == 0 {
		return "pasted"
	}
	return string(out)
}

// attachMakeScript prints the console's folder, made private when it is new. A
// link or someone else's folder at that path is refused.
func attachMakeScript(root, id string) string {
	return `u=$(id -u) || exit 125; d=` + machine.Quote(root) + `/nb-paste-$u-` + id + `; ` +
		`if [ -L "$d" ]; then exit 124; fi; ` +
		`if [ -e "$d" ]; then { [ -d "$d" ] && [ -O "$d" ]; } || exit 124; else mkdir -m 700 -- "$d" || exit 125; fi; ` +
		`echo "$d"`
}

// attachSweepScript removes the user's real paste folders of consoles not in live.
func attachSweepScript(root string, live []string) string {
	// the first name is no console: an empty pattern is no valid shell
	pattern := strings.Join(append([]string{"nb-none"}, live...), "|")
	return `u=$(id -u) || exit 125; for d in ` + machine.Quote(root) + `/nb-paste-$u-*; do ` +
		`if [ -L "$d" ] || [ ! -d "$d" ] || [ ! -O "$d" ]; then continue; fi; ` +
		`case "${d##*/nb-paste-$u-}" in ` + pattern + `) ;; *) rm -rf -- "$d";; esac; done`
}

// attachDir is the folder for key on h, made when needed.
func (s *Server) attachDir(ctx context.Context, h machine.Host, key string) (string, error) {
	if !attachID.MatchString(key) {
		return "", errors.New("id tidak valid")
	}
	if h.ID() == machine.LocalID {
		dir := s.st.Path("attachments", key)
		return dir, os.MkdirAll(dir, 0o700)
	}
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Script: attachMakeScript(attachRoot, key), MaxOutput: 4 << 10})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("folder sementara tidak bisa dibuat di %s (kode %d)", h.Info().Name, res.ExitCode)
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// attachTo stores the request body in key's folder on h.
func (s *Server) attachTo(w http.ResponseWriter, r *http.Request, h machine.Host, key string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "file lebih dari 20 MB")
			return
		}
		writeErr(w, http.StatusBadRequest, "gagal membaca body")
		return
	}
	if len(data) == 0 {
		writeErr(w, http.StatusBadRequest, "file kosong")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), attachTimeout)
	defer cancel()
	dir, err := s.attachDir(ctx, h, key)
	if err != nil {
		fsErr(w, err)
		return
	}
	st, err := h.WriteFile(ctx, path.Join(dir, attachName(r.URL.Query().Get("name"))), data, machine.WriteOptions{Unique: true})
	if err != nil {
		fsErr(w, err)
		return
	}
	s.sweepAttachments(time.Now())
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleSessionAttach(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	in, ok := s.sessions.Info(id)
	if !ok {
		writeErr(w, http.StatusNotFound, session.ErrNotFound.Error())
		return
	}
	h, ok := s.machines.Host(in.MachineID)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	s.attachTo(w, r, h, id)
}

func (s *Server) handleProjectAttach(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	if _, ok := s.projects.Get(id); !ok {
		writeErrCode(w, http.StatusNotFound, "project_not_found", "project tidak ditemukan")
		return
	}
	h, ok := s.machines.Host(machine.LocalID)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	s.attachTo(w, r, h, "project-"+id)
}

// handleProjectAttachRaw serves a file pasted into an order, for the thread. The file
// API stays closed to the hub's folder, so this opens one name in one project's paste
// folder and nothing else. It holds what the user pasted, so like fs/raw it never runs
// on the hub's origin.
func (s *Server) handleProjectAttachRaw(w http.ResponseWriter, r *http.Request, p *principal) {
	id, name := r.PathValue("id"), r.PathValue("name")
	if _, ok := s.projects.Get(id); !ok {
		writeErrCode(w, http.StatusNotFound, "project_not_found", "project tidak ditemukan")
		return
	}
	notFound := func() { writeErr(w, http.StatusNotFound, "file tidak ditemukan") }
	if !attachID.MatchString(id) || name != attachName(name) {
		notFound()
		return
	}
	f, err := os.OpenInRoot(s.st.Path("attachments", "project-"+id), name)
	if err != nil {
		notFound()
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		notFound()
		return
	}
	head := make([]byte, 8<<10)
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		notFound()
		return
	}
	ct := rawContentType(name, head[:n])
	hd := w.Header()
	hd.Set("Content-Type", ct)
	hd.Set("Content-Security-Policy", "sandbox")
	if ct == "application/octet-stream" {
		hd.Set("Content-Disposition", "attachment")
	}
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// sweepAttachments removes what This PC kept long enough.
func (s *Server) sweepAttachments(now time.Time) {
	root := s.st.Path("attachments")
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, d := range dirs {
		dir := filepath.Join(root, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		left := len(files)
		for _, f := range files {
			if fi, err := f.Info(); err == nil && now.Sub(fi.ModTime()) > attachKeep && os.Remove(filepath.Join(dir, f.Name())) == nil {
				left--
			}
		}
		if left == 0 {
			_ = os.Remove(dir)
		}
	}
}

// sweepRemoteAttachments removes the paste folders on a server whose console is
// gone, also those of a console deleted while the server was out of reach.
func (s *Server) sweepRemoteAttachments(machineID string) {
	h, ok := s.machines.Host(machineID)
	if !ok || machineID == machine.LocalID || !h.Connected() {
		return
	}
	var live []string
	for _, in := range s.sessions.List() {
		if in.MachineID == machineID && attachID.MatchString(in.ID) {
			live = append(live, in.ID)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), attachSweep)
	defer cancel()
	if res, err := machine.Exec(ctx, h, machine.ExecSpec{Script: attachSweepScript(attachRoot, live), MaxOutput: 4 << 10}); err != nil || res.ExitCode > 1 {
		log.Printf("folder paste di %s: %v", machineID, err)
	}
}

// setupAttachments hooks the cleanup up to consoles going and servers coming.
func (s *Server) setupAttachments() {
	s.sweepAttachments(time.Now())
	onRemove := s.sessions.OnRemove
	s.sessions.OnRemove = func(sp session.Spec) {
		if onRemove != nil {
			onRemove(sp)
		}
		if sp.MachineID == machine.LocalID {
			_ = os.RemoveAll(s.st.Path("attachments", sp.ID))
			return
		}
		go s.sweepRemoteAttachments(sp.MachineID)
	}
	onConnect := s.machines.OnConnect
	s.machines.OnConnect = func(id string) {
		if onConnect != nil {
			onConnect(id)
		}
		go s.sweepRemoteAttachments(id)
	}
}
