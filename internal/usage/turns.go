package usage

import (
	"bufio"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Turn is the tokens of one model turn of a transcript, with its model.
type Turn struct {
	At    time.Time
	Model string
	Tokens
}

// turnFiles is how far FileTurns read each transcript, so that one that grew is read from
// there on, as the token index does, and not again from its start: a conversation's transcript
// can be tens of MB and changes with every turn.
var turnFiles = struct {
	sync.Mutex
	m map[string]*turnFile
}{m: map[string]*turnFile{}}

type turnFile struct {
	agent string
	off   int64     // the end of the last complete line read
	st    fileState // what the lines so far left: dedupe ids, the model
	turns []Turn
	used  time.Time
}

// turnFilesMax bounds the transcripts remembered; the one used longest ago goes first.
const turnFilesMax = 64

// FileTurns reads every turn of a local transcript of claude or codex, as the token index does,
// whole: what an API provider costs (docs/API-agents.md §10.2) needs all of it, not the tail.
// Only what was appended since the last call for path is read; a file that shrank is read again.
func FileTurns(path, agent string) ([]Turn, error) {
	turnFiles.Lock()
	defer turnFiles.Unlock()
	f, err := os.Open(path)
	if err != nil {
		delete(turnFiles.m, path)
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	tf := turnFiles.m[path]
	if tf == nil || tf.agent != agent || fi.Size() < tf.off {
		tf = &turnFile{agent: agent, st: fileState{Agent: agent}}
	}
	if _, err := f.Seek(tf.off, io.SeekStart); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	read, last := int64(0), false
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			read += int64(i + 1)
			return i + 1, dropCR(data[:i]), nil
		}
		if atEOF && len(data) > 0 {
			last = true // a line not ended yet: counted now, read again once it is
			return len(data), dropCR(data), bufio.ErrFinalToken
		}
		return 0, nil, nil
	})
	turns := tf.turns
	var pending *Turn
	for sc.Scan() {
		st := &tf.st
		if last {
			tmp := tf.st
			tmp.IDs = slices.Clone(tf.st.IDs)
			st = &tmp
		}
		if at, t, ok := parseTurn(st, sc.Bytes()); ok {
			if last {
				pending = &Turn{At: at, Model: st.Model, Tokens: t}
			} else {
				turns = append(turns, Turn{At: at, Model: st.Model, Tokens: t})
			}
		}
	}
	if err := sc.Err(); err != nil {
		delete(turnFiles.m, path)
		return turns, err
	}
	tf.turns, tf.off, tf.used = turns, tf.off+read, time.Now()
	turnFiles.m[path] = tf
	if len(turnFiles.m) > turnFilesMax {
		var oldest string
		for p, x := range turnFiles.m {
			if oldest == "" || x.used.Before(turnFiles.m[oldest].used) {
				oldest = p
			}
		}
		delete(turnFiles.m, oldest)
	}
	out := slices.Clip(tf.turns) // the caller's: a later append here never shows in it
	if pending != nil {
		out = append(out, *pending)
	}
	return out, nil
}

func dropCR(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\r' {
		return b[:len(b)-1]
	}
	return b
}

// TranscriptFiles lists the transcripts of claude or codex in one config folder (CLAUDE_CONFIG_DIR
// or CODEX_HOME), with their size and time, for a caller that keeps its own cache.
func TranscriptFiles(dir, agent string) []fs.FileInfo {
	var out []fs.FileInfo
	paths := map[string]fs.FileInfo{}
	switch agent {
	case "claude":
		matches, _ := filepath.Glob(filepath.Join(dir, "projects", "*", "*.jsonl"))
		for _, p := range matches {
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				paths[p] = fi
			}
		}
	case "codex":
		_ = filepath.WalkDir(filepath.Join(dir, "sessions"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl") {
				if fi, err := d.Info(); err == nil {
					paths[p] = fi
				}
			}
			return nil
		})
	}
	for p, fi := range paths {
		out = append(out, namedInfo{FileInfo: fi, path: p})
	}
	return out
}

// namedInfo carries the full path of a file.
type namedInfo struct {
	fs.FileInfo
	path string
}

// PathOf is the full path of an entry of TranscriptFiles.
func PathOf(fi fs.FileInfo) string {
	if n, ok := fi.(namedInfo); ok {
		return n.path
	}
	return fi.Name()
}
