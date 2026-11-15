package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/store"
)

func appendFile(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func turnTotal(ts []Turn) (n int64) {
	for _, x := range ts {
		n += x.total()
	}
	return n
}

// FileTurns reads only what was appended since its last call for a file, counts a last line
// that is not ended yet without losing it, and reads a file that shrank again.
func TestFileTurnsReadsWhatWasAppended(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	p := filepath.Join(t.TempDir(), "c.jsonl")
	first := claudeLine("msg_1", at, 100, 20) + "\n" + claudeLine("msg_1", at, 100, 20) + "\n"
	if err := os.WriteFile(p, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err := FileTurns(p, "claude")
	if err != nil || len(turns) != 1 || turnTotal(turns) != 135 {
		t.Fatalf("first %+v %v", turns, err)
	}
	// What was read is not read again: the same bytes made unreadable change nothing.
	blank := strings.Repeat(" ", len(first)-1) + "\n"
	last := claudeLine("msg_2", at, 1, 2)
	if err := os.WriteFile(p, []byte(blank+last), 0o600); err != nil { // no newline yet
		t.Fatal(err)
	}
	turns, err = FileTurns(p, "claude")
	if err != nil || len(turns) != 2 || turnTotal(turns) != 135+18 {
		t.Fatalf("with an unended line %+v %v", turns, err)
	}
	appendFile(t, p, "\n"+claudeLine("msg_2", at, 1, 2)+"\n"+claudeLine("msg_3", at, 7, 3)+"\n")
	turns, err = FileTurns(p, "claude")
	if err != nil || len(turns) != 3 || turnTotal(turns) != 135+18+25 {
		t.Fatalf("after the line ended %+v %v", turns, err)
	}
	// rewritten shorter: read again from the start
	if err := os.WriteFile(p, []byte(claudeLine("msg_9", at, 1, 1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if turns, err = FileTurns(p, "claude"); err != nil || len(turns) != 1 || turnTotal(turns) != 17 {
		t.Fatalf("rewritten %+v %v", turns, err)
	}
	// a caller's turns do not change under it
	kept := turns
	appendFile(t, p, claudeLine("msg_10", at, 1, 1)+"\n")
	if more, _ := FileTurns(p, "claude"); len(more) != 2 || len(kept) != 1 {
		t.Fatalf("more %d kept %d", len(more), len(kept))
	}
	turnFiles.Lock()
	n := len(turnFiles.m)
	turnFiles.Unlock()
	if n == 0 || n > turnFilesMax {
		t.Fatalf("%d files remembered", n)
	}
}

// A scan that finds nothing new does not write the index again.
func TestTokenIndexSavedOnlyWhenChanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hub := &fakeHub{local: machine.NewLocal(machine.Config{}), dirs: map[string][]string{}}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cdir := filepath.Join(home, ".claude", "projects", "-work-app")
	_ = os.MkdirAll(cdir, 0o700)
	f := filepath.Join(cdir, "s1.jsonl")
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(f, []byte(claudeLine("msg_1", at, 100, 20)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	x := newTokenIndex(st, hub)
	ctx := context.Background()
	if _, err := x.scan(ctx, machine.LocalID); err != nil {
		t.Fatal(err)
	}
	file := st.Path(indexFile(machine.LocalID))
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("not saved after the first scan: %v", err)
	}
	_ = os.Remove(file)
	if _, err := x.scan(ctx, machine.LocalID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("saved again with nothing new")
	}
	appendFile(t, f, claudeLine("msg_2", at, 1, 2)+"\n")
	if _, err := x.scan(ctx, machine.LocalID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("not saved after new turns: %v", err)
	}
}
