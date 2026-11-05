package machine

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := NewLocal(Config{ID: LocalID})
	root := t.TempDir()
	write := func(rel, text string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	at := func(rel string) string { return filepath.Join(root, rel) }

	write("a b.bin", "\x00binary\xff", 0o640)
	write("dir/run.sh", "#!/bin/sh\n", 0o755)
	write("dir/sub/x.txt", "x", 0o644)
	if err := os.Symlink("sub/x.txt", at("dir/link")); err != nil {
		t.Fatal(err)
	}

	st, err := Copy(ctx, h, at("a b.bin"), at("c d.bin"), false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Path != at("c d.bin") || read("c d.bin") != "\x00binary\xff" {
		t.Fatalf("salinan salah: %+v", st)
	}
	if fi, _ := os.Stat(at("c d.bin")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode tidak ikut: %v", fi.Mode())
	}

	// never replaces: an error without unique, the next free name with it
	if _, err := Copy(ctx, h, at("a b.bin"), at("c d.bin"), false); !errors.Is(err, ErrExists) {
		t.Fatalf("harus ErrExists, dapat %v", err)
	}
	write("c d.bin", "mine", 0o644)
	st, err = Copy(ctx, h, at("a b.bin"), at("c d.bin"), true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Path != at("c d-2.bin") || read("c d.bin") != "mine" {
		t.Fatalf("menimpa atau salah nama: %+v isi=%q", st, read("c d.bin"))
	}

	// a file onto itself is the unique case too
	if st, err = Copy(ctx, h, at("a b.bin"), at("a b.bin"), true); err != nil || st.Path != at("a b-2.bin") {
		t.Fatalf("salin ke diri sendiri: %+v %v", st, err)
	}

	// a folder with everything in it, the link staying a link
	if st, err = Copy(ctx, h, at("dir"), at("dir2"), false); err != nil {
		t.Fatal(err)
	}
	if !st.Dir || read("dir2/sub/x.txt") != "x" {
		t.Fatalf("folder: %+v", st)
	}
	if fi, _ := os.Stat(at("dir2/run.sh")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode file dalam folder: %v", fi.Mode())
	}
	if l, err := os.Readlink(at("dir2/link")); err != nil || l != "sub/x.txt" {
		t.Fatalf("link: %q %v", l, err)
	}
	// an existing folder is not entered
	if st, err = Copy(ctx, h, at("dir"), at("dir2"), true); err != nil || st.Path != at("dir2-2") {
		t.Fatalf("folder ada: %+v %v", st, err)
	}
	if _, err := os.Stat(at("dir2/dir")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("masuk ke folder yang sudah ada")
	}

	if _, err := Copy(ctx, h, at("dir"), at("dir/sub/dir"), false); !errors.Is(err, ErrCopyIntoItself) {
		t.Fatalf("harus ErrCopyIntoItself, dapat %v", err)
	}
	if _, err := Copy(ctx, h, at("gone"), at("gone2"), false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("harus not exist, dapat %v", err)
	}
	if _, err := Copy(ctx, h, at("a b.bin"), "relative", false); !errors.Is(err, ErrRelativePath) {
		t.Fatalf("harus ErrRelativePath, dapat %v", err)
	}

	// a failed cp leaves nothing: an unreadable file inside a folder
	if os.Getuid() != 0 {
		write("bad/ok.txt", "ok", 0o644)
		write("bad/secret.txt", "no", 0o000)
		if _, err := Copy(ctx, h, at("bad"), at("bad2"), false); err == nil {
			t.Fatal("harus gagal")
		}
		if _, err := os.Stat(at("bad2")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("salinan setengah jadi tertinggal")
		}
	}
}
