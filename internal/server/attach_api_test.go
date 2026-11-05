package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nextboard/internal/machine"
)

func TestAttachName(t *testing.T) {
	for in, want := range map[string]string{
		"image.png":                      "image.png",
		"My Screen Shot (2).png":         "My-Screen-Shot--2-.png",
		"../../etc/passwd":               "passwd",
		`C:\Users\me\file.txt`:           "file.txt",
		"-rf.txt":                        "rf.txt",
		".bashrc":                        "bashrc",
		"":                               "pasted",
		"...":                            "pasted",
		"laporan akhir.pdf":              "laporan-akhir.pdf",
		"foto-日本語.jpg":                   "foto-日本語.jpg",
		strings.Repeat("a", 200) + ".md": strings.Repeat("a", 77) + ".md",
	} {
		if got := attachName(in); got != want {
			t.Errorf("attachName(%q) = %q, mau %q", in, got, want)
		}
	}
}

func TestProjectAttachRaw(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "raw"}, nil)
	if st != 200 && st != 201 {
		t.Fatalf("buat project: %d %v", st, pr)
	}
	id := pr["id"].(string)
	png := []byte("\x89PNG\r\n\x1a\nnot really")
	code, raw, _ := h.doRaw("POST", "/api/projects/"+id+"/attach?name=image.png", "image/png", png)
	if code != 200 {
		t.Fatalf("attach: %d %s", code, raw)
	}
	folder := filepath.Join(h.srv.st.Dir, "attachments", "project-"+id)

	code, raw, hd := h.doRaw("GET", "/api/projects/"+id+"/attach/image.png", "", nil)
	if code != 200 || string(raw) != string(png) {
		t.Fatalf("baca: %d %q", code, raw)
	}
	if hd.Get("Content-Type") != "image/png" || hd.Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("header: %v", hd)
	}

	// what a page could use to run on the hub's origin stays sandboxed
	if code, raw, _ := h.doRaw("POST", "/api/projects/"+id+"/attach?name=x.html", "text/html", []byte("<script>alert(1)</script>")); code != 200 {
		t.Fatalf("attach html: %d %s", code, raw)
	}
	if _, _, hd := h.doRaw("GET", "/api/projects/"+id+"/attach/x.html", "", nil); hd.Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("html tanpa sandbox: %v", hd)
	}

	// only a name the hub could have given, in this project's folder
	secret := filepath.Join(h.srv.st.Dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("rahasia"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(folder, "link.txt")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link.txt", "tidak-ada.png", "..%2F..%2Fsecret.txt", ".env", "a%20b.png"} {
		code, raw, _ := h.doRaw("GET", "/api/projects/"+id+"/attach/"+name, "", nil)
		if code == 200 || strings.Contains(string(raw), "rahasia") {
			t.Errorf("%s: %d %q", name, code, raw)
		}
	}
	if code, _, _ := h.doRaw("GET", "/api/projects/tidak-ada/attach/image.png", "", nil); code != 404 {
		t.Errorf("project tidak ada: %d", code)
	}

	// the sign-in applies
	anon := &testHub{t: t, srv: h.srv, ts: h.ts, c: h.ts.Client()}
	if code, _, _ := anon.doRaw("GET", "/api/projects/"+id+"/attach/image.png", "", nil); code != 401 {
		t.Errorf("tanpa login: %d", code)
	}
}

func TestAttachToConsoleAndProject(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	st, s := h.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": "shell", "cols": 80, "rows": 24, "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("buat session: %d %v", st, s)
	}
	id := s["id"].(string)
	t.Cleanup(func() { h.do("DELETE", "/api/sessions/"+id, nil, nil) })

	post := func(path, name string, body []byte) (int, map[string]any) {
		t.Helper()
		code, raw, _ := h.doRaw("POST", path+"?name="+url.QueryEscape(name), "image/png", body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return code, out
	}

	code, a := post("/api/sessions/"+id+"/attach", "my shot.png", []byte("one"))
	if code != 200 {
		t.Fatalf("attach: %d %v", code, a)
	}
	first := a["path"].(string)
	if want := h.srv.st.Path("attachments", id, "my-shot.png"); first != want {
		t.Fatalf("path %q, mau %q", first, want)
	}
	if b, _ := os.ReadFile(first); string(b) != "one" {
		t.Fatalf("isi %q", b)
	}

	// the same name again never replaces
	_, a = post("/api/sessions/"+id+"/attach", "my shot.png", []byte("two"))
	if a["path"].(string) != h.srv.st.Path("attachments", id, "my-shot-2.png") {
		t.Fatalf("nama kedua: %v", a)
	}
	if b, _ := os.ReadFile(first); string(b) != "one" {
		t.Fatal("file pertama tertimpa")
	}

	// nobody aims it elsewhere
	_, a = post("/api/sessions/"+id+"/attach", "../../auth.json", []byte("x"))
	if filepath.Dir(a["path"].(string)) != h.srv.st.Path("attachments", id) {
		t.Fatalf("keluar folder: %v", a)
	}

	if code, _ := post("/api/sessions/"+id+"/attach", "empty", nil); code != 400 {
		t.Fatalf("file kosong: %d", code)
	}
	if code, _ := post("/api/sessions/nope/attach", "x", []byte("x")); code != 404 {
		t.Fatalf("session tidak ada: %d", code)
	}
	if code, _ := post("/api/projects/nope/attach", "x", []byte("x")); code != 404 {
		t.Fatalf("project tidak ada: %d", code)
	}
	if code, _ := post("/api/sessions/"+id+"/attach", "big", make([]byte, 20<<20+1)); code != 413 {
		t.Fatalf("lebih dari 20 MB: %d", code)
	}

	pid, _ := h.homeWorkspace()
	_, a = post("/api/projects/"+pid+"/attach", "order.txt", []byte("hi"))
	order := a["path"].(string)
	if order != h.srv.st.Path("attachments", "project-"+pid, "order.txt") {
		t.Fatalf("path order: %v", a)
	}

	// a week later it goes, the folder with it
	old := time.Now().Add(-attachKeep - time.Hour)
	if err := os.Chtimes(order, old, old); err != nil {
		t.Fatal(err)
	}
	h.srv.sweepAttachments(time.Now())
	if _, err := os.Stat(filepath.Dir(order)); !os.IsNotExist(err) {
		t.Fatalf("folder lama masih ada: %v", err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("file baru ikut hilang: %v", err)
	}

	// and it goes with its console
	if st, _ := h.do("DELETE", "/api/sessions/"+id, nil, nil); st != 200 {
		t.Fatalf("hapus session: %d", st)
	}
	if _, err := os.Stat(filepath.Dir(first)); !os.IsNotExist(err) {
		t.Fatalf("folder console masih ada: %v", err)
	}
}

func TestFSCopyAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	copyTo := func(body map[string]any) (int, map[string]any) {
		t.Helper()
		return h.do("POST", "/api/machines/local/fs/copy", body, nil)
	}
	st, out := copyTo(map[string]any{"from": src, "to": filepath.Join(dir, "b.txt")})
	if st != 200 || out["path"] != filepath.Join(dir, "b.txt") {
		t.Fatalf("salin: %d %v", st, out)
	}
	if st, out = copyTo(map[string]any{"from": src, "to": filepath.Join(dir, "b.txt")}); st != 409 || out["code"] != "exists" {
		t.Fatalf("sudah ada: %d %v", st, out)
	}
	if st, out = copyTo(map[string]any{"from": src, "to": src, "unique": true}); st != 200 || out["path"] != filepath.Join(dir, "a-2.txt") {
		t.Fatalf("unique: %d %v", st, out)
	}
	if st, _ = copyTo(map[string]any{"from": filepath.Join(dir, "gone"), "to": filepath.Join(dir, "x")}); st != 404 {
		t.Fatalf("sumber hilang: %d", st)
	}
	// the hub's own files stay out of reach, both ways
	if st, _ = copyTo(map[string]any{"from": h.srv.st.Path("config.json"), "to": filepath.Join(dir, "c")}); st != 403 {
		t.Fatalf("baca file hub: %d", st)
	}
	if st, _ = copyTo(map[string]any{"from": src, "to": h.srv.st.Path("x.txt")}); st != 403 {
		t.Fatalf("tulis file hub: %d", st)
	}
}

// The scripts that run on servers: a real folder of the user's is made once, a
// link there is refused, and only paste folders of gone consoles are removed.
func TestAttachServerScripts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := machine.NewLocal(machine.Config{ID: machine.LocalID})
	root := t.TempDir()
	run := func(script string) *machine.ExecResult {
		t.Helper()
		res, err := machine.Exec(ctx, h, machine.ExecSpec{Script: script})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := run(attachMakeScript(root, "s_one"))
	if res.ExitCode != 0 {
		t.Fatalf("buat: %+v", res)
	}
	one := strings.TrimSpace(string(res.Stdout))
	fi, err := os.Stat(one)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder: %v %v", fi, err)
	}
	if again := run(attachMakeScript(root, "s_one")); again.ExitCode != 0 || strings.TrimSpace(string(again.Stdout)) != one {
		t.Fatalf("dipakai lagi: %+v", again)
	}

	prefix := strings.TrimSuffix(one, "s_one")
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, prefix+"s_link"); err != nil {
		t.Fatal(err)
	}
	if bad := run(attachMakeScript(root, "s_link")); bad.ExitCode != 124 {
		t.Fatalf("symlink harus ditolak: %+v", bad)
	}

	if res := run(attachMakeScript(root, "s_two")); res.ExitCode != 0 {
		t.Fatalf("buat kedua: %+v", res)
	}
	other := filepath.Join(root, "nb-usage-1000")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if res := run(attachSweepScript(root, []string{"s_two"})); res.ExitCode != 0 {
		t.Fatalf("sapu: %+v", res)
	}
	if _, err := os.Stat(one); !os.IsNotExist(err) {
		t.Fatal("folder console yang hilang masih ada")
	}
	if _, err := os.Stat(prefix + "s_two"); err != nil {
		t.Fatal("folder console hidup terhapus")
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatal("isi di balik symlink terhapus")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("folder lain di /tmp terhapus")
	}
	// nothing live: every paste folder goes, and a folder without any is no error
	run(attachSweepScript(root, nil))
	if _, err := os.Stat(prefix + "s_two"); !os.IsNotExist(err) {
		t.Fatal("folder masih ada tanpa console hidup")
	}
	if res := run(attachSweepScript(root, nil)); res.ExitCode != 0 {
		t.Fatalf("sapu kosong: %+v", res)
	}
}

// On a real server: the file lands in the console's private folder, the same
// name never replaces, the folder goes with the console, and one left behind by
// a console deleted out of reach goes when the server connects. Copy works there too.
func TestAttachAndCopyOverSSH(t *testing.T) {
	port, key := startSSHServer(t)
	h := newTestHub(t)
	h.setup()
	old := attachRoot
	attachRoot = t.TempDir()
	t.Cleanup(func() { attachRoot = old })
	mid := h.connectMachine(port, key)

	dir := t.TempDir()
	st, s := h.do("POST", "/api/sessions", map[string]any{"machineId": mid, "preset": "shell", "cols": 80, "rows": 24, "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("buat session: %d %v", st, s)
	}
	id := s["id"].(string)
	code, raw, _ := h.doRaw("POST", "/api/sessions/"+id+"/attach?name=shot.png", "image/png", []byte("one"))
	if code != 200 {
		t.Fatalf("attach: %d %s", code, raw)
	}
	var a map[string]any
	_ = json.Unmarshal(raw, &a)
	first := a["path"].(string)
	if filepath.Dir(first) != filepath.Join(attachRoot, fmt.Sprintf("nb-paste-%d-%s", os.Getuid(), id)) || filepath.Base(first) != "shot.png" {
		t.Fatalf("path %q", first)
	}
	if b, _ := os.ReadFile(first); string(b) != "one" {
		t.Fatalf("isi %q", b)
	}
	if fi, _ := os.Stat(filepath.Dir(first)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder tidak privat: %v", fi.Mode())
	}
	code, raw, _ = h.doRaw("POST", "/api/sessions/"+id+"/attach?name=shot.png", "image/png", []byte("two"))
	_ = json.Unmarshal(raw, &a)
	if code != 200 || filepath.Base(a["path"].(string)) != "shot-2.png" {
		t.Fatalf("kedua: %d %s", code, raw)
	}

	stc, out := h.do("POST", "/api/machines/"+mid+"/fs/copy", map[string]any{"from": first, "to": filepath.Join(dir, "copy.png")}, nil)
	if stc != 200 || out["path"] != filepath.Join(dir, "copy.png") {
		t.Fatalf("salin: %d %v", stc, out)
	}
	if stc, _ = h.do("POST", "/api/machines/"+mid+"/fs/copy", map[string]any{"from": first, "to": filepath.Join(dir, "copy.png")}, nil); stc != 409 {
		t.Fatalf("salin ke yang ada: %d", stc)
	}

	waitGone := func(p string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(p); os.IsNotExist(err) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s masih ada", p)
	}
	if st, _ := h.do("DELETE", "/api/sessions/"+id, nil, nil); st != 200 {
		t.Fatalf("hapus session: %d", st)
	}
	waitGone(filepath.Dir(first))

	// left behind while the server was away
	stale := filepath.Join(attachRoot, fmt.Sprintf("nb-paste-%d-s_gone", os.Getuid()))
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	h.srv.machines.OnConnect(mid)
	waitGone(stale)
}
