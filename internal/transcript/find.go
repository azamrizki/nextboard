package transcript

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// FindClaude locates <id>.jsonl: the slug folder of cwd first, then any
// <cfg>/projects/*/ folder (Claude shortens very long slugs). "" when the
// transcript does not exist (yet).
func FindClaude(ctx context.Context, h machine.Host, cfgDir, cwd, id string) (string, error) {
	if !validID(id) {
		return "", ErrBadID
	}
	p := ClaudePath(cfgDir, cwd, id)
	if st, err := h.Stat(ctx, p); err != nil {
		return "", err
	} else if st.Exists && !st.Dir {
		return p, nil
	}
	root := path.Join(cfgDir, "projects")
	if st, err := h.Stat(ctx, root); err != nil || !st.Exists || !st.Dir {
		return "", err
	}
	l, err := h.ListDir(ctx, root)
	if err != nil {
		return "", err
	}
	for _, e := range l.Entries {
		if !e.Dir {
			continue
		}
		cand := path.Join(root, e.Name, id+".jsonl")
		st, err := h.Stat(ctx, cand)
		if err != nil {
			return "", err
		}
		if st.Exists && !st.Dir {
			return cand, nil
		}
	}
	return "", nil
}

// codexDayDir is <home>/sessions/YYYY/MM/DD for t (UTC).
func codexDayDir(home string, t time.Time) string {
	t = t.UTC()
	return path.Join(home, "sessions", t.Format("2006"), t.Format("01"), t.Format("02"))
}

// listFiles lists regular files of dir with size/mtime (stat'ed when the
// listing lacks them, i.e. the SSH exec fallback). A missing dir is empty.
func listFiles(ctx context.Context, h machine.Host, dir string, keep func(name string) bool) ([]machine.DirEntry, error) {
	st, err := h.Stat(ctx, dir)
	if err != nil {
		return nil, err
	}
	if !st.Exists || !st.Dir {
		return nil, nil
	}
	l, err := h.ListDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	var out []machine.DirEntry
	for _, e := range l.Entries {
		if e.Dir || !keep(e.Name) {
			continue
		}
		if e.Mtime.IsZero() {
			if fst, err := h.Stat(ctx, path.Join(dir, e.Name)); err == nil && fst.Exists {
				e.Size, e.Mtime = fst.Size, fst.Mtime
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func isRollout(name string) bool {
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

// readHeader returns the first HeaderBytes of p.
func readHeader(ctx context.Context, h machine.Host, p string) ([]byte, error) {
	b, _, err := h.ReadAt(ctx, p, 0, HeaderBytes)
	return b, err
}

// FindCodex discovers the rollout a Codex console just started (§8.1):
// day folders around startedAt, mtime ≥ startedAt−5 s, session_meta.cwd ==
// cwd and not claimed by another console. Exactly one candidate wins;
// none or several (ambiguous) → "" until notify names the thread.
func FindCodex(ctx context.Context, h machine.Host, home, cwd string, startedAt time.Time, claimed func(path string) bool) (string, string, error) {
	cwd = path.Clean(cwd)
	seen := map[string]bool{}
	var hitPath, hitID string
	hits := 0
	for _, d := range []time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
		dir := codexDayDir(home, startedAt.Add(d))
		if seen[dir] {
			continue
		}
		seen[dir] = true
		files, err := listFiles(ctx, h, dir, isRollout)
		if err != nil {
			return "", "", err
		}
		for _, f := range files {
			if f.Mtime.Before(startedAt.Add(-5 * time.Second)) {
				continue
			}
			p := path.Join(dir, f.Name)
			if claimed != nil && claimed(p) {
				continue
			}
			hdr, err := readHeader(ctx, h, p)
			if err != nil {
				continue
			}
			id, mcwd, _, ok := codexMeta(hdr)
			if !ok || path.Clean(mcwd) != cwd {
				continue
			}
			hits++
			hitPath, hitID = p, id
		}
	}
	if hits != 1 {
		return "", "", nil
	}
	return hitPath, hitID, nil
}

// codexDayDirs lists up to max day folders under <home>/sessions, newest
// first.
func codexDayDirs(ctx context.Context, h machine.Host, home string, max int) ([]string, error) {
	numeric := func(ctx context.Context, dir string) ([]string, error) {
		st, err := h.Stat(ctx, dir)
		if err != nil || !st.Exists || !st.Dir {
			return nil, err
		}
		l, err := h.ListDir(ctx, dir)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, e := range l.Entries {
			if _, err := strconv.Atoi(e.Name); err == nil && e.Dir {
				names = append(names, e.Name)
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		return names, nil
	}
	root := path.Join(home, "sessions")
	years, err := numeric(ctx, root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, y := range years {
		months, err := numeric(ctx, path.Join(root, y))
		if err != nil {
			return nil, err
		}
		for _, m := range months {
			days, err := numeric(ctx, path.Join(root, y, m))
			if err != nil {
				return nil, err
			}
			for _, d := range days {
				out = append(out, path.Join(root, y, m, d))
				if len(out) >= max {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

// CodexPathByID finds rollout-*-<id>.jsonl in the day folders around near
// (±1 day), then in the newest 30 day folders. "" when not found.
func CodexPathByID(ctx context.Context, h machine.Host, home, id string, near time.Time) (string, error) {
	if !validID(id) {
		return "", ErrBadID
	}
	suffix := "-" + id + ".jsonl"
	match := func(name string) bool { return isRollout(name) && strings.HasSuffix(name, suffix) }
	seen := map[string]bool{}
	search := func(dir string) (string, error) {
		if seen[dir] {
			return "", nil
		}
		seen[dir] = true
		files, err := listFiles(ctx, h, dir, match)
		if err != nil || len(files) == 0 {
			return "", err
		}
		return path.Join(dir, files[0].Name), nil
	}
	if !near.IsZero() {
		for _, d := range []time.Duration{0, -24 * time.Hour, 24 * time.Hour} {
			if p, err := search(codexDayDir(home, near.Add(d))); p != "" || err != nil {
				return p, err
			}
		}
	}
	dirs, err := codexDayDirs(ctx, h, home, 30)
	if err != nil {
		return "", err
	}
	for _, dir := range dirs {
		if p, err := search(dir); p != "" || err != nil {
			return p, err
		}
	}
	return "", nil
}
