package machine_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/skills"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/studio"
	"github.com/azamrizki/nextboard/internal/usage"
)

// Phase 7 part B over a real SSH connection (throwaway sshd on loopback,
// SFTP subsystem): the skills 428 protocol and hub backups, Code studio
// file operations, and a usage check in a hidden remote PTY with the
// fixed temp folder. Personal config folders are temp folders; ~/.claude
// is never touched.

func needSSHD(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
}

func put(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSSHSkillsConfirmProtocol(t *testing.T) {
	needSSHD(t)
	_, m := machine.ConnectTestSSHD(t)
	ctx := context.Background()
	cfg, repo := t.TempDir(), t.TempDir()
	put(t, cfg+"/skills/existing/SKILL.md", "---\nname: existing\ndescription: old text\n---\nbody\n")
	plug := cfg + "/plugins/cache/m/p/1"
	put(t, plug+"/skills/ro/SKILL.md", "---\nname: ro\ndescription: r\n---\nx\n")
	inst, _ := json.Marshal(map[string]any{"plugins": map[string]any{"p@m": []any{map[string]any{"installPath": plug}}}})
	put(t, cfg+"/plugins/installed_plugins.json", string(inst))
	st, _ := store.Open(t.TempDir())
	mg := skills.New(st)
	n := 0
	mg.Now = func() time.Time { n++; return time.Date(2026, 10, 28, 14, 0, n, 0, time.UTC) }
	loc := skills.Loc{MachineID: m.ID(), MachineName: "devbox", Remote: true, Cfg: cfg, Repo: repo}

	items, _, err := mg.List(ctx, m, loc)
	if err != nil || len(items) != 2 {
		t.Fatalf("list over SFTP %+v %v", items, err)
	}
	// create without confirm → 428 with the exact paths; nothing written
	req := skills.CreateRequest{Scope: "personal", Name: "remote-skill", Description: "R", Body: "b"}
	_, err = mg.Create(ctx, m, loc, req, nil)
	var ce *skills.ConfirmError
	dir := cfg + "/skills/remote-skill"
	if !errors.As(err, &ce) || ce.Machine != "devbox" || strings.Join(sorted(ce.Paths), ",") != dir+","+dir+"/SKILL.md" {
		t.Fatalf("428 %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("written before confirmation")
	}
	// confirm differing by one path → 428 again
	if _, err := mg.Create(ctx, m, loc, req, []string{dir, dir + "/OTHER.md"}); !errors.As(err, &ce) {
		t.Fatalf("one path off: %v", err)
	}
	if _, err := mg.Create(ctx, m, loc, req, ce.Paths); err != nil {
		t.Fatal(err)
	}
	// overwrite: confirm + backup on the hub before the write
	file := cfg + "/skills/existing/SKILL.md"
	if _, err := mg.Write(ctx, m, loc, file, "---\nname: existing\ndescription: new\n---\nnew body\n", nil, nil); !errors.As(err, &ce) {
		t.Fatalf("write 428: %v", err)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "old text") {
		t.Fatal("overwritten without confirmation")
	}
	if _, err := mg.Write(ctx, m, loc, file, "---\nname: existing\ndescription: new\n---\nnew body\n", nil, []string{file}); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(st.Path("backups", "skills", m.ID(), "*", "skills", "existing", "SKILL.md"))
	if len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
	if b, _ := os.ReadFile(backups[0]); !strings.Contains(string(b), "old text") {
		t.Fatalf("backup content %q", b)
	}
	// delete: 428, then backup, then gone
	if _, err := mg.Delete(ctx, m, loc, file, nil); !errors.As(err, &ce) || strings.Join(sorted(ce.Paths), ",") != cfg+"/skills/existing,"+file {
		t.Fatalf("delete 428 %v", err)
	}
	b, err := mg.Delete(ctx, m, loc, file, ce.Paths)
	if err != nil || !strings.HasPrefix(b, st.Path("backups", "skills", m.ID())) {
		t.Fatalf("delete %q %v", b, err)
	}
	if _, err := os.Stat(cfg + "/skills/existing"); !os.IsNotExist(err) {
		t.Fatal("skill still there")
	}
	if data, err := os.ReadFile(filepath.Join(b, "skills", "existing", "SKILL.md")); err != nil || !strings.Contains(string(data), "new body") {
		t.Fatalf("backup before delete %q %v", data, err)
	}
	// project writes need no confirmation; plugins are read-only
	if _, err := mg.Create(ctx, m, loc, skills.CreateRequest{Scope: "project", Kind: "command", Name: "ship", Description: "S", Body: "b"}, nil); err != nil {
		t.Fatalf("project write %v", err)
	}
	if _, err := mg.Write(ctx, m, loc, plug+"/skills/ro/SKILL.md", "x", nil, []string{plug + "/skills/ro/SKILL.md"}); !errors.Is(err, skills.ErrReadOnly) {
		t.Fatalf("plugin %v", err)
	}
	// a missing config folder is never created remotely (decision #4)
	gone := skills.Loc{MachineID: m.ID(), Remote: true, Cfg: cfg + "/does-not-exist"}
	if _, err := mg.Create(ctx, m, gone, req, []string{gone.Cfg + "/skills"}); err == nil {
		t.Fatal("created a config folder remotely")
	}
}

func sorted(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}

func TestSSHCodeStudioFileOps(t *testing.T) {
	needSSHD(t)
	_, m := machine.ConnectTestSSHD(t)
	ctx := context.Background()
	root := t.TempDir()
	put(t, root+"/src/a.go", "package a")
	put(t, root+"/README.md", "# r")
	put(t, root+"/node_modules/x.js", "x")
	_ = os.MkdirAll(root+"/.git", 0o755)
	tr, err := studio.Tree(ctx, m, root, false)
	if err != nil || len(tr.Entries) != 2 || tr.Entries[0].Name != "src" || tr.Entries[1].Mtime.IsZero() {
		t.Fatalf("tree %+v %v", tr, err)
	}
	if f, _, err := studio.Find(ctx, m, root, "a.go", 5); err != nil || len(f) != 1 {
		t.Fatalf("find %+v %v", f, err)
	}
	if _, err := machine.Rename(ctx, m, root+"/README.md", root+"/src/a.go"); !errors.Is(err, machine.ErrExists) {
		t.Fatalf("rename overwrite over SFTP: %v", err)
	}
	if st, err := machine.Rename(ctx, m, root+"/README.md", root+"/DOCS.md"); err != nil || !st.Exists {
		t.Fatalf("rename %+v %v", st, err)
	}
	to, err := studio.MoveToTrash(ctx, m, root, root+"/src", time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC))
	if err != nil || to != root+"/.git/nextboard-trash/20261028-010203/src" {
		t.Fatalf("trash %q %v", to, err)
	}
	if _, err := os.Stat(to + "/a.go"); err != nil {
		t.Fatal("trash content")
	}
	other := t.TempDir()
	put(t, other+"/o.txt", "o")
	_ = os.Symlink(other+"/o.txt", root+"/link.txt")
	if out, err := studio.OutsideProject(ctx, m, root, root+"/link.txt"); err != nil || !out {
		t.Fatalf("outside over SFTP %v %v", out, err)
	}
	if st, link, err := machine.Lstat(ctx, m, root+"/link.txt"); err != nil || !link || !st.Exists {
		t.Fatalf("lstat %+v %v %v", st, link, err)
	}
	if err := machine.Remove(ctx, m, root+"/link.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other + "/o.txt"); err != nil {
		t.Fatal("remove followed the link")
	}
}

// usageHub serves one SSH machine to the usage service.
type usageHub struct {
	m       machine.Host
	command string
	mu      sync.Mutex
	events  []map[string]any
}

func (u *usageHub) Host(id string) (machine.Host, bool) {
	if id == u.m.ID() {
		return u.m, true
	}
	return nil, false
}
func (u *usageHub) Settings() usage.Settings { return usage.Settings{Background: true} }
func (u *usageHub) Publish(v any) {
	u.mu.Lock()
	u.events = append(u.events, v.(map[string]any))
	u.mu.Unlock()
}
func (u *usageHub) Profile(m, a, p string) (usage.Profile, error) {
	return usage.Profile{Env: "CLAUDE_CONFIG_DIR", Dir: "/tmp/nb-test-profile"}, nil
}
func (u *usageHub) Command(m, a string) string             { return u.command }
func (u *usageHub) ProfileDirs(string) map[string][]string { return nil }
func (u *usageHub) MachineIDs() []string                   { return []string{u.m.ID()} }

// TestSSHUsageCheck runs a usage check in a hidden PTY on the SSH machine
// with the fake CLI: the fixed folder is made remotely, used as cwd, and
// removed; a recorded leftover is cleaned when the machine connects.
func TestSSHWorkbenchFilesAndPipe(t *testing.T) {
	needSSHD(t)
	_, m := machine.ConnectTestSSHD(t)
	ctx := context.Background()
	root := t.TempDir()
	put(t, root+"/src/a.go", "package a // needle\n")
	put(t, root+"/README.md", "needle\n")
	put(t, root+"/node_modules/x.js", "needle\n")
	fl, err := studio.ListFiles(ctx, m, root)
	if err != nil || fl.Source != "walk" || len(fl.Items) != 2 {
		t.Fatalf("list %+v %v", fl, err)
	}
	res, err := studio.Search(ctx, m, root, studio.SearchOptions{Query: "needle"})
	if err != nil || res.Count != 2 {
		t.Fatalf("search %+v %v", res, err)
	}
	rep, err := studio.Replace(ctx, m, root, studio.SearchOptions{Query: "needle", MatchCase: true}, "pin", []string{"README.md"}, nil)
	if err != nil || rep.Count != 1 {
		t.Fatalf("replace %+v %v", rep, err)
	}
	if b, _ := os.ReadFile(root + "/README.md"); string(b) != "pin\n" {
		t.Fatalf("replaced %q", b)
	}
	runs, err := studio.RunOnSave(ctx, m, root, "README.md", []studio.OnSaveRule{{Pattern: "*.md", Command: "printf ran > marker"}})
	if err != nil || len(runs) != 1 || runs[0].ExitCode != 0 {
		t.Fatalf("on save %+v %v", runs, err)
	}
	if b, _ := os.ReadFile(root + "/marker"); string(b) != "ran" {
		t.Fatalf("marker %q", b)
	}
	if _, err := studio.LangServerStatus(ctx, m, root); err != nil {
		t.Fatal(err)
	}
	// a framed exchange over a non-PTY channel, then a stop that ends the program
	p, err := machine.StartPipe(m, machine.ExecSpec{Cwd: root, Script: `head -c 21 >/dev/null; printf 'Content-Length: 2\r\n\r\n{}'; cat >/dev/null`})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	if err := studio.WriteLSPFrame(p.Stdin, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	body, err := studio.ReadLSPFrame(bufio.NewReader(p.Stdout))
	if err != nil || string(body) != "{}" {
		t.Fatalf("frame %q %v", body, err)
	}
	p.Stop()
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not stop")
	}
}

func TestSSHUsageCheck(t *testing.T) {
	needSSHD(t)
	_, m := machine.ConnectTestSSHD(t)
	root, err := os.MkdirTemp("", "nbu-ssh")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	old := usage.Root
	usage.Root = root
	defer func() { usage.Root = old }()
	dir := t.TempDir()
	script, err := os.ReadFile("../usage/testdata/fake-tui.sh")
	if err != nil {
		t.Fatal(err)
	}
	fake := dir + "/fake-cli"
	put(t, fake, string(script))
	_ = os.Chmod(fake, 0o755)
	logf := dir + "/fake.log"
	put(t, fake+".env", fmt.Sprintf("FAKE_LOG=%s\nFAKE_KIND=claude\nFAKE_TRUST=1\n", logf))
	st, _ := store.Open(t.TempDir())
	hub := &usageHub{m: m, command: fake}
	jm := jobs.NewManager(st, hub.Host)
	defer jm.Close()
	svc := usage.New(st, jm, hub, nil)
	defer svc.Close()
	zero := 0
	_, j, err := svc.Check(usage.CheckRequest{MachineID: m.ID(), Agent: "claude", ProfileID: "pf_x", MaxAgeSec: &zero, Trigger: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var v jobs.Job
	for i := 0; i < 600; i++ {
		if v, _ = jm.Get(j.ID); !v.Active() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	var rep usage.Report
	_ = json.Unmarshal(v.Result, &rep)
	lg, _ := os.ReadFile(logf)
	if v.State != jobs.StateDone || rep.State != "ok" || len(rep.Windows) != 2 || rep.Windows[0].UsedPct != 45 {
		t.Fatalf("job %+v report %+v\nlog %s", v, rep, lg)
	}
	want := fmt.Sprintf("start cwd=%s/nb-usage-%d", root, os.Getuid())
	if !strings.Contains(string(lg), want) || !strings.Contains(string(lg), "cfg=/tmp/nb-test-profile") ||
		!strings.Contains(string(lg), "trusted") || strings.Contains(string(lg), "UNEXPECTED") {
		t.Fatalf("log %s", lg)
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 || len(svc.Pending()) != 0 {
		t.Fatalf("left behind %v %v", ents, svc.Pending())
	}
	// A hub killed mid-check: the leftover folder is recorded; on the
	// next connect only that folder is removed.
	left := fmt.Sprintf("%s/nb-usage-%d", root, os.Getuid())
	put(t, left+"/junk", "x")
	keep := root + "/keep-me"
	put(t, keep, "x")
	b, _ := json.Marshal([]map[string]any{{"machineId": m.ID(), "path": left, "at": time.Now()}})
	_ = os.WriteFile(st.Path("usage", "pending-cleanup.json"), b, 0o600)
	svc2 := usage.New(st, jm, hub, nil)
	svc2.MachineConnected(m.ID())
	for i := 0; i < 100 && len(svc2.Pending()) > 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(left); !os.IsNotExist(err) || len(svc2.Pending()) != 0 {
		t.Fatalf("not cleaned: %v %v", err, svc2.Pending())
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("cleanup touched another file")
	}
}
