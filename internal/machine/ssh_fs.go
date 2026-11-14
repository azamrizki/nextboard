package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// File access on SSH machines goes through the SFTP subsystem of the
// existing connection (stock OpenSSH, nothing installed). One SFTP client
// is shared per connection and recreated after a reconnect. When the
// server refuses the subsystem, reads fall back to exec (`stat`, `head -c`)
// and writes are refused.

// sftpIdle: an unused SFTP client is closed after this long, so its
// channel does not permanently take one of the server's MaxSessions slots
// (OpenSSH default 10) away from terminals.
var sftpIdle = 2 * time.Minute

// sftpState is guarded by SSH.mu; sftpMu serialises client creation.
type sftpState struct {
	client   *sftp.Client
	gen      int       // SSH.gen the client belongs to
	none     int       // SSH.gen for which the subsystem was refused
	home     string    // remote home (Getwd of the SFTP session)
	inflight int       // operations currently using client
	used     time.Time // end of the last operation
}

// sessionWriter closes the whole SSH channel together with the SFTP
// client's write side, so Client.Close never waits on the remote.
type sessionWriter struct {
	io.WriteCloser
	s *ssh.Session
}

func (w sessionWriter) Close() error {
	err := w.WriteCloser.Close()
	_ = w.s.Close()
	return err
}

// sftpClient returns the shared SFTP client, creating it on first use.
// Callers must call done when the operation finishes. ErrNoSFTP means the
// server refused the subsystem on this connection.
func (m *SSH) sftpClient(ctx context.Context) (*sftp.Client, string, func(), error) {
	sc, home, err := m.acquireSFTP(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	var once sync.Once
	return sc, home, func() { once.Do(func() { m.releaseSFTP(sc) }) }, nil
}

func (m *SSH) releaseSFTP(sc *sftp.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sftp.client == sc {
		m.sftp.inflight--
		m.sftp.used = time.Now()
	}
}

func (m *SSH) acquireSFTP(ctx context.Context) (*sftp.Client, string, error) {
	m.sftpMu.Lock()
	defer m.sftpMu.Unlock()
	m.mu.Lock()
	c, gen := m.client, m.gen
	switch {
	case c == nil:
		m.mu.Unlock()
		return nil, "", ErrNotConnected
	case m.sftp.client != nil && m.sftp.gen == gen:
		sc, home := m.sftp.client, m.sftp.home
		m.sftp.inflight++
		m.mu.Unlock()
		return sc, home, nil
	case m.sftp.none == gen:
		m.mu.Unlock()
		return nil, "", ErrNoSFTP
	}
	stale := m.sftp.client
	m.sftp.client = nil
	m.mu.Unlock()
	if stale != nil {
		go stale.Close()
	}

	type result struct {
		c    *sftp.Client
		home string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		sc, err := openSFTP(ctx, c)
		if err != nil {
			ch <- result{err: err}
			return
		}
		home, _ := sc.Getwd()
		ch <- result{c: sc, home: home}
	}()
	var r result
	select {
	case r = <-ch:
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, "", ctx.Err()
	}
	if r.err != nil {
		if errors.Is(r.err, ErrNoSFTP) {
			m.mu.Lock()
			if m.gen == gen {
				m.sftp.none = gen
			}
			m.mu.Unlock()
		}
		return nil, "", r.err
	}
	if r.home == "" {
		if p := m.Info().Probe; p != nil {
			r.home = p.Home
		}
	}
	m.mu.Lock()
	if m.gen != gen || m.client == nil {
		m.mu.Unlock()
		r.c.Close()
		return nil, "", ErrNotConnected
	}
	m.sftp.client, m.sftp.gen, m.sftp.home = r.c, gen, r.home
	m.sftp.inflight, m.sftp.used = 1, time.Now()
	m.mu.Unlock()
	gone := make(chan struct{})
	go func(sc *sftp.Client) {
		_ = sc.Wait()
		m.mu.Lock()
		if m.sftp.client == sc {
			m.sftp.client = nil
		}
		m.mu.Unlock()
		close(gone)
	}(r.c)
	go m.reapIdleSFTP(r.c, gone)
	return r.c, r.home, nil
}

// reapIdleSFTP closes sc once nothing used it for sftpIdle. It ends with the
// client, so a lost connection holds nothing back.
func (m *SSH) reapIdleSFTP(sc *sftp.Client, gone <-chan struct{}) {
	t := time.NewTicker(sftpIdle / 4)
	defer t.Stop()
	for {
		select {
		case <-gone:
			return
		case <-t.C:
		}
		m.mu.Lock()
		if m.sftp.client != sc {
			m.mu.Unlock()
			return
		}
		if m.sftp.inflight > 0 || time.Since(m.sftp.used) < sftpIdle {
			m.mu.Unlock()
			continue
		}
		m.sftp.client = nil
		m.mu.Unlock()
		sc.Close()
		return
	}
}

// doneCloser releases the SFTP client when a streamed file is closed.
type doneCloser struct {
	io.ReadCloser
	done func()
}

func (d doneCloser) Close() error {
	err := d.ReadCloser.Close()
	d.done()
	return err
}

// openSFTP starts the sftp subsystem on a new channel. A refused
// subsystem or failed handshake is reported as ErrNoSFTP.
func openSFTP(ctx context.Context, c *ssh.Client) (*sftp.Client, error) {
	s, err := openSession(ctx, c)
	if err != nil {
		return nil, err
	}
	w, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	r, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	if err := s.RequestSubsystem("sftp"); err != nil {
		s.Close()
		return nil, ErrNoSFTP
	}
	sc, err := sftp.NewClientPipe(r, sessionWriter{w, s})
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("%w (%v)", ErrNoSFTP, err)
	}
	return sc, nil
}

// takeSFTPLocked detaches the SFTP client (connection lost or machine
// closed). Caller holds m.mu and closes the result outside the lock.
func (m *SSH) takeSFTPLocked() *sftp.Client {
	sc := m.sftp.client
	m.sftp.client = nil
	return sc
}

// Stat reports a remote path; a missing path is Exists:false.
func (m *SSH) Stat(ctx context.Context, p string) (*FileStat, error) {
	sc, home, done, err := m.sftpClient(ctx)
	if errors.Is(err, ErrNoSFTP) {
		return m.execStat(ctx, p)
	}
	if err != nil {
		return nil, err
	}
	defer done()
	abs, err := cleanAbs(p, home)
	if err != nil {
		return nil, err
	}
	fi, err := sc.Stat(abs)
	if notExist(err) {
		return &FileStat{Path: abs, Name: path.Base(abs)}, nil
	}
	if err != nil {
		return nil, err
	}
	return statOf(abs, fi), nil
}

// ReadFile reads at most max bytes of a remote file.
func (m *SSH) ReadFile(ctx context.Context, p string, max int64) (*FileContent, error) {
	sc, home, done, err := m.sftpClient(ctx)
	if errors.Is(err, ErrNoSFTP) {
		st, data, err := m.execRead(ctx, p, max+1)
		if err != nil {
			return nil, err
		}
		return makeContent(st, data, max), nil
	}
	if err != nil {
		return nil, err
	}
	defer done()
	f, st, err := openRegularSFTP(sc, home, p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	return makeContent(st, data, max), nil
}

// ReadAt reads up to n bytes of a remote file starting at off (SFTP, or
// `tail -c | head -c` through the read-only exec fallback).
func (m *SSH) ReadAt(ctx context.Context, p string, off, n int64) ([]byte, *FileStat, error) {
	if off < 0 || n < 0 {
		return nil, nil, errors.New("invalid offset")
	}
	sc, home, done, err := m.sftpClient(ctx)
	if errors.Is(err, ErrNoSFTP) {
		return m.execReadAt(ctx, p, off, n)
	}
	if err != nil {
		return nil, nil, err
	}
	defer done()
	f, st, err := openRegularSFTP(sc, home, p)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return readRange(f, st, off, n)
}

// OpenRaw opens a remote file for streaming. The exec fallback buffers at
// most MaxFileBytes+1 bytes.
func (m *SSH) OpenRaw(ctx context.Context, p string) (io.ReadCloser, *FileStat, error) {
	sc, home, done, err := m.sftpClient(ctx)
	if errors.Is(err, ErrNoSFTP) {
		st, data, err := m.execRead(ctx, p, MaxFileBytes+1)
		if err != nil {
			return nil, nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), st, nil
	}
	if err != nil {
		return nil, nil, err
	}
	f, st, err := openRegularSFTP(sc, home, p)
	if err != nil {
		done()
		return nil, nil, err
	}
	return doneCloser{f, done}, st, nil
}

func openRegularSFTP(sc *sftp.Client, home, p string) (*sftp.File, *FileStat, error) {
	abs, err := cleanAbs(p, home)
	if err != nil {
		return nil, nil, err
	}
	fi, err := sc.Stat(abs)
	if err != nil {
		return nil, nil, err
	}
	if err := checkRegular(fi); err != nil {
		return nil, nil, err
	}
	f, err := sc.Open(abs)
	if err != nil {
		return nil, nil, err
	}
	return f, statOf(abs, fi), nil
}

// WriteFile writes a remote file over SFTP. This is the only remote write
// the hub performs, and only on an explicit user action (save, copy into
// project). There is no exec fallback for writes.
func (m *SSH) WriteFile(ctx context.Context, p string, data []byte, opt WriteOptions) (*FileStat, error) {
	sc, home, done, err := m.sftpClient(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	abs, err := cleanAbs(p, home)
	if err != nil {
		return nil, err
	}
	if opt.Unique {
		for n := 1; n <= maxUniqueTries; n++ {
			cand := abs
			if n > 1 {
				cand = uniqueName(abs, n)
			}
			f, err := sc.OpenFile(cand, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
			if err != nil {
				// OpenSSH reports EEXIST as a generic failure: check.
				if _, serr := sc.Lstat(cand); serr == nil {
					continue
				}
				return nil, err
			}
			return finishSFTPWrite(sc, f, cand, data)
		}
		return nil, fmt.Errorf("no free name for %s", path.Base(abs))
	}
	fi, err := sc.Stat(abs)
	if err != nil && !notExist(err) {
		return nil, err
	}
	if err == nil {
		if err := checkRegular(fi); err != nil {
			return nil, err
		}
	}
	if opt.IfMtime != nil && (fi == nil || !mtimeMatches(fi.ModTime(), *opt.IfMtime)) {
		return nil, ErrMtimeMismatch
	}
	f, err := sc.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return nil, err
	}
	return finishSFTPWrite(sc, f, abs, data)
}

// Mkdir creates one remote folder over SFTP (explicit user action only;
// there is no exec fallback for writes).
func (m *SSH) Mkdir(ctx context.Context, p string) error {
	sc, home, done, err := m.sftpClient(ctx)
	if err != nil {
		return err
	}
	defer done()
	abs, err := cleanAbs(p, home)
	if err != nil {
		return err
	}
	if fi, err := sc.Stat(abs); err == nil {
		if fi.IsDir() {
			return nil
		}
		return errors.New("path already exists and is not a folder")
	}
	return sc.Mkdir(abs)
}

func finishSFTPWrite(sc *sftp.Client, f *sftp.File, p string, data []byte) (*FileStat, error) {
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	fi, err := sc.Stat(p)
	if err != nil {
		return nil, err
	}
	return statOf(p, fi), nil
}

// listDirSFTP lists a folder with sizes and mtimes; symlinks report their
// target's kind.
func listDirSFTP(sc *sftp.Client, home, p string) (*DirListing, error) {
	switch {
	case p == "":
		p = "~"
	case !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~"):
		p = "~/" + p
	}
	dir, err := cleanAbs(p, home)
	if err != nil {
		return nil, err
	}
	fis, err := sc.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	l := &DirListing{Path: dir, Entries: []DirEntry{}}
	if dir != "/" {
		l.Parent = path.Dir(dir)
	}
	for _, fi := range fis {
		if fi.Mode()&os.ModeSymlink != 0 {
			if t, err := sc.Stat(path.Join(dir, fi.Name())); err == nil {
				fi = t
			}
		}
		de := DirEntry{Name: fi.Name(), Dir: fi.IsDir(), Mtime: fi.ModTime().UTC()}
		if !de.Dir {
			de.Size = fi.Size()
		}
		l.Entries = append(l.Entries, de)
	}
	sortEntries(l.Entries)
	return l, nil
}

// ---- read-only exec fallback ----

const (
	statMarker = "__NB_STAT__"
	dataMarker = "__NB_DATA__"
)

// shellPath returns p as a shell word, expanding ~ through $HOME.
func shellPath(p string) (string, error) {
	if strings.ContainsAny(p, "\x00\n") || len(p) > 4096 {
		return "", ErrRelativePath
	}
	switch {
	case p == "~":
		return `"$HOME"`, nil
	case strings.HasPrefix(p, "~/"):
		return `"$HOME"/` + Quote(path.Clean("/" + p[2:])[1:]), nil
	case strings.HasPrefix(p, "/"):
		return Quote(path.Clean(p)), nil
	}
	return "", ErrRelativePath
}

// statScript prints "<marker> <abs path>" then "missing" or
// "<d|f|o> <size> <mtime>" (GNU stat, then BSD stat).
func statScript(expr string) string {
	return `p=` + expr + `; printf '%s %s\n' ` + statMarker + ` "$p"; ` +
		`if [ ! -e "$p" ]; then echo missing; exit 0; fi; ` +
		`if [ -d "$p" ]; then t=d; elif [ -f "$p" ]; then t=f; else t=o; fi; ` +
		`x=$(stat -L -c '%s %Y' -- "$p" 2>/dev/null || stat -L -f '%z %m' -- "$p" 2>/dev/null); ` +
		`echo "$t $x"`
}

// parseStat parses statScript output (the lines after the marker).
func parseStat(out string) (*FileStat, string, error) {
	i := strings.Index(out, statMarker+" ")
	if i < 0 {
		return nil, "", errors.New("unexpected stat output")
	}
	lines := strings.SplitN(out[i+len(statMarker)+1:], "\n", 3)
	if len(lines) < 2 {
		return nil, "", errors.New("unexpected stat output")
	}
	abs := path.Clean(lines[0])
	st := &FileStat{Path: abs, Name: path.Base(abs)}
	f := strings.Fields(lines[1])
	if len(f) == 0 || f[0] == "missing" {
		return st, "", nil
	}
	st.Exists = true
	st.Dir = f[0] == "d"
	if len(f) >= 3 {
		if n, err := strconv.ParseInt(f[1], 10, 64); err == nil && !st.Dir {
			st.Size = n
		}
		if sec, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			st.Mtime = time.Unix(sec, 0).UTC()
		}
	}
	return st, f[0], nil
}

func (m *SSH) execStat(ctx context.Context, p string) (*FileStat, error) {
	expr, err := shellPath(p)
	if err != nil {
		return nil, err
	}
	out, err := m.execStdout(ctx, "/bin/sh -c "+Quote(statScript(expr)), 64<<10)
	if err != nil {
		return nil, err
	}
	st, _, err := parseStat(string(out))
	return st, err
}

// execRead returns the stat and the first n bytes of a regular file.
func (m *SSH) execRead(ctx context.Context, p string, n int64) (*FileStat, []byte, error) {
	expr, err := shellPath(p)
	if err != nil {
		return nil, nil, err
	}
	script := statScript(expr) + `; [ "$t" = f ] && echo ` + dataMarker + ` && exec head -c ` + strconv.FormatInt(n, 10) + ` -- "$p"`
	out, err := m.execStdout(ctx, "/bin/sh -c "+Quote(script), n+64<<10)
	if err != nil && !bytes.Contains(out, []byte(statMarker)) {
		return nil, nil, err
	}
	st, kind, perr := parseStat(string(out[:min(len(out), 8<<10)]))
	if perr != nil {
		return nil, nil, perr
	}
	switch {
	case !st.Exists:
		return nil, nil, os.ErrNotExist
	case kind == "d":
		return nil, nil, ErrIsDir
	case kind != "f":
		return nil, nil, errors.New("not a regular file")
	}
	i := bytes.Index(out, []byte(dataMarker+"\n"))
	if i < 0 {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("unexpected head output")
	}
	return st, out[i+len(dataMarker)+1:], nil
}

// execReadAt is ReadAt over exec: stat, then the byte range.
func (m *SSH) execReadAt(ctx context.Context, p string, off, n int64) ([]byte, *FileStat, error) {
	expr, err := shellPath(p)
	if err != nil {
		return nil, nil, err
	}
	script := statScript(expr) + `; [ "$t" = f ] && echo ` + dataMarker + ` && tail -c +` + strconv.FormatInt(off+1, 10) +
		` -- "$p" | head -c ` + strconv.FormatInt(n, 10)
	out, err := m.execStdout(ctx, "/bin/sh -c "+Quote(script), n+64<<10)
	if err != nil && !bytes.Contains(out, []byte(statMarker)) {
		return nil, nil, err
	}
	st, kind, perr := parseStat(string(out[:min(len(out), 8<<10)]))
	if perr != nil {
		return nil, nil, perr
	}
	switch {
	case !st.Exists:
		return nil, nil, os.ErrNotExist
	case kind == "d":
		return nil, nil, ErrIsDir
	case kind != "f":
		return nil, nil, errors.New("not a regular file")
	}
	i := bytes.Index(out, []byte(dataMarker+"\n"))
	if i < 0 {
		return nil, nil, errors.New("unexpected tail output")
	}
	data := out[i+len(dataMarker)+1:]
	if int64(len(data)) > n {
		data = data[:n]
	}
	return data, st, nil
}

// execStdout runs cmd and returns stdout only (binary-safe, at most limit
// bytes kept). A failing command reports its stderr.
func (m *SSH) execStdout(ctx context.Context, cmd string, limit int64) ([]byte, error) {
	c := m.currentClient()
	if c == nil {
		return nil, ErrNotConnected
	}
	s, err := openSession(ctx, c)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	out := &limitBuffer{max: limit}
	var errb lockedBuffer
	s.Stdout = out
	s.Stderr = &errb
	if err := s.Start(cmd); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return out.Bytes(), errors.New(msg)
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// limitBuffer keeps the first max bytes and silently drops the rest.
type limitBuffer struct {
	mu   sync.Mutex
	max  int64
	b    bytes.Buffer
	drop bool
}

func (l *limitBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	room := l.max - int64(l.b.Len())
	if room > 0 {
		l.b.Write(p[:min(int64(len(p)), room)])
	}
	if int64(len(p)) > room {
		l.drop = true
	}
	return len(p), nil
}

// dropped reports whether output beyond max was discarded.
func (l *limitBuffer) dropped() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.drop
}

func (l *limitBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}

// take hands over what was written without copying it, for when the writer is done: a copy
// of output of up to tens of MB doubled what an exec held. Anything written later is dropped.
func (l *limitBuffer) take() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.b.Bytes()
	l.b, l.max = bytes.Buffer{}, 0
	return b
}
