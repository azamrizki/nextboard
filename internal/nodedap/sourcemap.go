package nodedap

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Source maps (version 3): a script compiled from TypeScript or bundled says where its code
// came from, so that a breakpoint in the source lands in the script, and a paused script shows
// the source.

const maxSourceMap = 32 << 20

// mapping is one segment: a place in the generated script and the place in a source it came from.
type mapping struct {
	genLine, genCol int // 0-based
	src             int // index into sources
	line, col       int // 0-based
}

type sourceMap struct {
	sources []string  // absolute paths, or the source's own name when it has none
	byGen   []mapping // sorted by generated position
}

// loadSourceMap reads the map a script names: a data: URL inside it, or a file next to it.
// scriptPath is the generated script's absolute path.
func loadSourceMap(scriptPath, ref string) (*sourceMap, error) {
	var raw []byte
	mapPath := ""
	switch {
	case strings.HasPrefix(ref, "data:"):
		meta, data, ok := strings.Cut(strings.TrimPrefix(ref, "data:"), ",")
		if !ok {
			return nil, errors.New("bad data URL")
		}
		var err error
		if strings.HasSuffix(meta, ";base64") {
			raw, err = base64.StdEncoding.DecodeString(data)
		} else {
			var s string
			s, err = url.PathUnescape(data)
			raw = []byte(s)
		}
		if err != nil {
			return nil, err
		}
	default:
		p := ref
		if strings.HasPrefix(p, "file://") {
			p = pathOf(p)
		} else if !filepath.IsAbs(p) {
			u, err := url.PathUnescape(p)
			if err != nil {
				return nil, err
			}
			p = filepath.Join(filepath.Dir(scriptPath), u)
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if st.Size() > maxSourceMap {
			return nil, errors.New("source map too large")
		}
		if raw, err = os.ReadFile(p); err != nil {
			return nil, err
		}
		mapPath = p
	}
	base := filepath.Dir(scriptPath)
	if mapPath != "" {
		base = filepath.Dir(mapPath)
	}
	return parseSourceMap(raw, base)
}

func parseSourceMap(raw []byte, base string) (*sourceMap, error) {
	var m struct {
		Version    int      `json:"version"`
		SourceRoot string   `json:"sourceRoot"`
		Sources    []string `json:"sources"`
		Mappings   string   `json:"mappings"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.Version != 3 {
		return nil, errors.New("not a version 3 source map")
	}
	sm := &sourceMap{}
	for _, s := range m.Sources {
		sm.sources = append(sm.sources, resolveSource(base, m.SourceRoot, s))
	}
	segs, err := decodeMappings(m.Mappings, len(m.Sources))
	if err != nil {
		return nil, err
	}
	sm.byGen = segs
	return sm, nil
}

// resolveSource makes a source's path absolute: against the map's folder and its sourceRoot.
func resolveSource(base, root, s string) string {
	if strings.HasPrefix(s, "webpack://") || strings.HasPrefix(s, "node:") {
		return s
	}
	if p := pathOf(s); p != "" {
		return filepath.Clean(p)
	}
	if root != "" {
		if p := pathOf(root); p != "" {
			return filepath.Join(p, s)
		}
		s = strings.TrimSuffix(root, "/") + "/" + s
	}
	if filepath.IsAbs(s) {
		return filepath.Clean(s)
	}
	return filepath.Join(base, s)
}

const b64chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var b64index = func() (t [128]int8) {
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(b64chars); i++ {
		t[b64chars[i]] = int8(i)
	}
	return
}()

// decodeMappings reads the Base64 VLQ of "mappings". Segments with one field (no source) are
// left out: they say where code is, not where it came from.
func decodeMappings(s string, nSources int) ([]mapping, error) {
	var out []mapping
	src, line, col := 0, 0, 0
	genLine := 0
	for _, group := range strings.Split(s, ";") {
		genCol := 0
		for _, seg := range strings.Split(group, ",") {
			if seg == "" {
				continue
			}
			var f [5]int
			n := 0
			val, shift := 0, 0
			for i := 0; i < len(seg); i++ {
				c := seg[i]
				if c >= 128 || b64index[c] < 0 {
					return nil, errors.New("bad mappings")
				}
				d := int(b64index[c])
				val += (d & 31) << shift
				if d&32 != 0 {
					shift += 5
					if shift > 30 {
						return nil, errors.New("bad mappings")
					}
					continue
				}
				if n == len(f) {
					return nil, errors.New("bad mappings")
				}
				if val&1 != 0 {
					f[n] = -(val >> 1)
				} else {
					f[n] = val >> 1
				}
				n++
				val, shift = 0, 0
			}
			if shift != 0 || n == 0 {
				return nil, errors.New("bad mappings")
			}
			genCol += f[0]
			if n >= 4 {
				src += f[1]
				line += f[2]
				col += f[3]
				if src < 0 || src >= nSources || line < 0 || col < 0 || genCol < 0 {
					return nil, errors.New("bad mappings")
				}
				out = append(out, mapping{genLine: genLine, genCol: genCol, src: src, line: line, col: col})
			}
		}
		genLine++
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].genLine != out[j].genLine {
			return out[i].genLine < out[j].genLine
		}
		return out[i].genCol < out[j].genCol
	})
	return out, nil
}

// original is where a place in the generated script came from: the last segment at or before
// it on its line.
func (sm *sourceMap) original(genLine, genCol int) (string, int, int, bool) {
	i := sort.Search(len(sm.byGen), func(i int) bool {
		m := sm.byGen[i]
		return m.genLine > genLine || (m.genLine == genLine && m.genCol > genCol)
	})
	if i == 0 || sm.byGen[i-1].genLine != genLine {
		// nothing at or before it on its line: the first segment of the line
		if i < len(sm.byGen) && sm.byGen[i].genLine == genLine {
			m := sm.byGen[i]
			return sm.sources[m.src], m.line, m.col, true
		}
		return "", 0, 0, false
	}
	m := sm.byGen[i-1]
	return sm.sources[m.src], m.line, m.col, true
}

// generated is where a line of a source landed: its first place in the script. A line with no
// code of its own takes the next one that has some, as editors expect of a breakpoint.
func (sm *sourceMap) generated(source string, line int) (int, int, int, bool) {
	src := sm.sourceIndex(source)
	if src < 0 {
		return 0, 0, 0, false
	}
	best := -1
	bestLine := -1
	for i, m := range sm.byGen {
		if m.src != src || m.line < line {
			continue
		}
		if best < 0 || m.line < bestLine || (m.line == bestLine && (m.genLine < sm.byGen[best].genLine || (m.genLine == sm.byGen[best].genLine && m.genCol < sm.byGen[best].genCol))) {
			best, bestLine = i, m.line
		}
	}
	if best < 0 || bestLine > line+20 {
		return 0, 0, 0, false
	}
	m := sm.byGen[best]
	return m.genLine, m.genCol, m.line, true
}

func (sm *sourceMap) sourceIndex(source string) int {
	for i, s := range sm.sources {
		if s == source {
			return i
		}
	}
	return -1
}

func (sm *sourceMap) has(source string) bool { return sm.sourceIndex(source) >= 0 }

// srcBP is a breakpoint the client set in a source.
type srcBP struct {
	dapID int
	line  int // 1-based
	cond  string
}

type setBP struct {
	dapID int
	line  int // 1-based, where it landed in the source
}

// loadMap reads the source map of a script the first time it is seen.
func (a *adapter) loadMap(scriptID, scriptURL, ref string) {
	if scriptID == "" || ref == "" {
		return
	}
	a.mu.Lock()
	_, done := a.maps[scriptID]
	a.mu.Unlock()
	path := pathOf(scriptURL)
	if done || path == "" {
		return
	}
	sm, err := loadSourceMap(path, ref)
	if err != nil {
		return
	}
	a.mu.Lock()
	a.maps[scriptID] = sm
	a.mu.Unlock()
}

// applyMapped sets the breakpoints of every source of a script's map.
func (a *adapter) applyMapped(scriptID string) {
	a.mu.Lock()
	sm := a.maps[scriptID]
	type job struct {
		source string
		bps    []srcBP
	}
	var jobs []job
	if sm != nil {
		for _, src := range sm.sources {
			if bps := a.srcBPs[src]; len(bps) > 0 {
				jobs = append(jobs, job{src, bps})
			}
		}
	}
	a.mu.Unlock()
	for _, j := range jobs {
		for _, got := range a.setMapped(scriptID, j.source, j.bps) {
			a.event("breakpoint", map[string]any{"reason": "changed", "breakpoint": map[string]any{"id": got.dapID, "verified": true, "line": got.line}})
		}
	}
}

// setMapped sets breakpoints of a source in one script, where its map puts them.
func (a *adapter) setMapped(scriptID, source string, bps []srcBP) []setBP {
	a.mu.Lock()
	sm := a.maps[scriptID]
	a.mu.Unlock()
	if sm == nil {
		return nil
	}
	var out []setBP
	for _, b := range bps {
		gl, gc, landed, ok := sm.generated(source, b.line-1)
		if !ok {
			continue
		}
		params := map[string]any{"location": map[string]any{"scriptId": scriptID, "lineNumber": gl, "columnNumber": gc}}
		if b.cond != "" {
			params["condition"] = b.cond
		}
		res, err := a.cdp("Debugger.setBreakpoint", params)
		if err != nil {
			continue
		}
		var r struct {
			BreakpointID string `json:"breakpointId"`
		}
		if json.Unmarshal(res, &r) != nil || r.BreakpointID == "" {
			continue
		}
		a.mu.Lock()
		a.cdpToDap[r.BreakpointID] = b.dapID
		a.mappedBPs[source] = append(a.mappedBPs[source], r.BreakpointID)
		a.mu.Unlock()
		out = append(out, setBP{dapID: b.dapID, line: landed + 1})
	}
	return out
}

// applyAllMapped sets the breakpoints of every script with a map loaded so far.
func (a *adapter) applyAllMapped() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.maps))
	for id := range a.maps {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.applyMapped(id)
	}
}

// ---- compiled scripts found before they load ----

const (
	outScanMax  = 5000     // files looked at
	outFileMax  = 20 << 20 // a script bigger than this is not read for its map
	outTailRead = 4096     // where the sourceMappingURL comment is
)

type outFile struct {
	path string
	sm   *sourceMap
}

// outRoots are the folders compiled scripts are looked for in: the fixed part of each outFiles
// glob, or the program's folder when there are none.
func outRoots(cwd string, globs []string) []string {
	var roots []string
	for _, g := range globs {
		g = strings.ReplaceAll(g, "${workspaceFolder}", cwd)
		if !filepath.IsAbs(g) {
			g = filepath.Join(cwd, g)
		}
		if i := strings.IndexAny(g, "*?[{"); i >= 0 {
			g = filepath.Dir(g[:i] + "x")
		}
		roots = append(roots, filepath.Clean(g))
	}
	if len(roots) == 0 && cwd != "" {
		roots = []string{cwd}
	}
	return roots
}

var mapComment = []byte("sourceMappingURL=")

// indexOutFiles reads which sources the compiled scripts under the roots came from, once.
func (a *adapter) indexOutFiles() map[string][]outFile {
	a.mu.Lock()
	if a.indexed {
		idx := a.outIndex
		a.mu.Unlock()
		return idx
	}
	roots := a.outRoots
	a.mu.Unlock()
	idx := map[string][]outFile{}
	seen := 0
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if n := d.Name(); n == "node_modules" || n == ".git" || (strings.HasPrefix(n, ".") && p != root) {
					return filepath.SkipDir
				}
				return nil
			}
			if seen++; seen > outScanMax {
				return filepath.SkipAll
			}
			ext := filepath.Ext(p)
			if ext != ".js" && ext != ".mjs" && ext != ".cjs" {
				return nil
			}
			ref := mapRef(p)
			if ref == "" {
				return nil
			}
			sm, err := loadSourceMap(p, ref)
			if err != nil {
				return nil
			}
			for _, s := range sm.sources {
				idx[s] = append(idx[s], outFile{path: p, sm: sm})
			}
			return nil
		})
	}
	a.mu.Lock()
	a.outIndex, a.indexed = idx, true
	a.mu.Unlock()
	return idx
}

// mapRef is the sourceMappingURL a script ends with, if any.
func mapRef(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > outFileMax {
		return ""
	}
	off := st.Size() - outTailRead
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return ""
	}
	i := bytes.LastIndex(buf, mapComment)
	if i < 0 {
		return ""
	}
	ref := buf[i+len(mapComment):]
	if j := bytes.IndexAny(ref, " \t\r\n*"); j >= 0 {
		ref = ref[:j]
	}
	return string(ref)
}

// setByOutFile sets a source's breakpoints in the compiled scripts that came from it, by their
// URL, so that they are in place whenever those scripts load.
func (a *adapter) setByOutFile(source string, bps []srcBP) []setBP {
	files := a.indexOutFiles()[source]
	var out []setBP
	for _, f := range files {
		for _, b := range bps {
			gl, gc, landed, ok := f.sm.generated(source, b.line-1)
			if !ok {
				continue
			}
			params := map[string]any{"lineNumber": gl, "columnNumber": gc, "urlRegex": urlRegexFor(f.path)}
			if b.cond != "" {
				params["condition"] = b.cond
			}
			res, err := a.cdp("Debugger.setBreakpointByUrl", params)
			if err != nil {
				continue
			}
			var r struct {
				BreakpointID string `json:"breakpointId"`
			}
			if json.Unmarshal(res, &r) != nil || r.BreakpointID == "" {
				continue
			}
			a.mu.Lock()
			a.cdpToDap[r.BreakpointID] = b.dapID
			a.bpLine[r.BreakpointID] = landed + 1
			a.mappedBPs[source] = append(a.mappedBPs[source], r.BreakpointID)
			a.mu.Unlock()
			out = append(out, setBP{dapID: b.dapID, line: landed + 1})
		}
	}
	return out
}
