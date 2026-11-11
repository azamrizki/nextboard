package usage

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Turn is the tokens of one model turn of a transcript, with its model.
type Turn struct {
	At    time.Time
	Model string
	Tokens
}

// FileTurns reads every turn of a local transcript of claude or codex, as the token index does,
// whole: what an API provider costs (docs/API-agents.md §10.2) needs all of it, not the tail.
func FileTurns(path, agent string) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st := &fileState{Agent: agent}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	var out []Turn
	for sc.Scan() {
		if at, t, ok := parseTurn(st, sc.Bytes()); ok {
			out = append(out, Turn{At: at, Model: st.Model, Tokens: t})
		}
	}
	return out, sc.Err()
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
