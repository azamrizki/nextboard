package machine_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/gitsvc"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/preview"
	"github.com/azamrizki/nextboard/internal/store"
)

func sshGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func waitJob(t *testing.T, jm *jobs.Manager, j jobs.Job) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := jm.Get(j.ID); ok && !v.Active() {
			return v
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s not finished", j.ID)
	return jobs.Job{}
}

// TestGitReposOverSSH: the scan and a selected repository work through the
// agentless SSH exec path, with a login shell that is not POSIX sh, for a
// name that needs quoting.
func TestGitReposOverSSH(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
	_, m := machine.ConnectTestSSHD(t)
	root := t.TempDir()
	for _, rel := range []string{"app a", "group/app-b", "node_modules/dep"} {
		d := filepath.Join(root, rel)
		os.MkdirAll(d, 0o755)
		sshGit(t, d, "init", "-q", "-b", "main")
		sshGit(t, d, "config", "user.name", "Remote Tester")
		sshGit(t, d, "config", "user.email", "remote@example.invalid")
		sshGit(t, d, "config", "core.hooksPath", filepath.Join(root, "nohooks"))
		os.WriteFile(filepath.Join(d, "a.txt"), []byte("a\n"), 0o644)
	}
	svc := gitsvc.New()
	svc.Host = func(id string) (machine.Host, bool) {
		if id == m.ID() {
			return m, true
		}
		return nil, false
	}
	r := gitsvc.Repo{ProjectID: "p_ssh", MachineID: m.ID(), Path: root}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	l, err := svc.Repos(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 2 || l.Items[0].Path != "app a" || l.Items[1].Path != "group/app-b" || l.Items[0].Changed != 1 || l.Items[0].Error != "" {
		t.Fatalf("repos %+v", l)
	}
	sub, err := r.In("app a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(ctx, sub, gitsvc.CommitRequest{Message: "Add a", All: true}); err != nil {
		t.Fatal(err)
	}
	if got := sshGit(t, filepath.Join(root, "app a"), "log", "-1", "--format=%s"); got != "Add a" {
		t.Fatalf("subject %q", got)
	}
	if st, err := svc.Status(ctx, sub); err != nil || !st.IsRepo || st.Changed != 0 {
		t.Fatalf("status %+v %v", st, err)
	}
	other, _ := r.In("group/app-b")
	if st, err := svc.Status(ctx, other); err != nil || st.Changed != 1 {
		t.Fatalf("other %+v %v", st, err)
	}
	// Named from the command bar's side too, and no outer repository.
	if p, err := svc.Pick(ctx, r, "app-b"); err != nil || p.Sub != "group/app-b" {
		t.Fatalf("pick %+v %v", p, err)
	}
	if _, err := svc.Init(ctx, r, ""); err == nil {
		t.Fatal("init above repositories was allowed")
	}
}

// TestGitOverSSH runs the git service through the agentless SSH exec
// path: status, commit, push/pull with a bare remote, discard to trash,
// branches; the git environment on the remote side is non-interactive
// (BatchMode, no prompts, no askpass) and nothing outside the repo and
// its remote changes.
func TestGitOverSSH(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
	reg, m := machine.ConnectTestSSHD(t)
	_ = reg
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	bare := filepath.Join(root, "remote.git")
	os.Mkdir(repo, 0o755)
	sentinel := filepath.Join(root, "outside.txt")
	os.WriteFile(sentinel, []byte("untouched\n"), 0o644)
	sshGit(t, root, "init", "-q", "--bare", "-b", "main", bare)
	sshGit(t, repo, "init", "-q", "-b", "main")
	// Repo-local identity and an empty hooks folder: the remote login
	// shell reads the real user's global config, the test must not
	// depend on it.
	hooks := filepath.Join(root, "nohooks")
	os.Mkdir(hooks, 0o755)
	sshGit(t, repo, "config", "user.name", "Remote Tester")
	sshGit(t, repo, "config", "user.email", "remote@example.invalid")
	sshGit(t, repo, "config", "core.hooksPath", hooks)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	sshGit(t, repo, "remote", "add", "origin", bare)

	st, _ := store.Open(t.TempDir())
	host := func(id string) (machine.Host, bool) {
		if id == m.ID() {
			return m, true
		}
		return nil, false
	}
	jm := jobs.NewManager(st, host)
	t.Cleanup(jm.Close)
	svc := gitsvc.New()
	svc.Host, svc.Jobs = host, jm
	r := gitsvc.Repo{ProjectID: "p_ssh", MachineID: m.ID(), Path: repo}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s, err := svc.Status(ctx, r)
	if err != nil || !s.IsRepo || s.Branch != "main" || len(s.Files) != 1 || s.Files[0].Status != "untracked" || s.Files[0].Added != 1 {
		t.Fatalf("status %+v %v", s, err)
	}
	res, err := svc.Commit(ctx, r, gitsvc.CommitRequest{Message: "Remote commit", All: true})
	if err != nil || res.Commit.Subject != "Remote commit" {
		t.Fatalf("commit %+v %v", res, err)
	}
	if j := waitJob(t, jm, must(svc.Push(r, false, nil))); j.State != jobs.StateDone {
		t.Fatalf("push %+v", j)
	}
	if got := sshGit(t, bare, "log", "-1", "--format=%s", "main"); got != "Remote commit" {
		t.Fatalf("remote log %q", got)
	}
	if j := waitJob(t, jm, must(svc.Pull(r, nil))); j.State != jobs.StateDone {
		t.Fatalf("pull %+v", j)
	}
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("mine\n"), 0o644)
	d, err := svc.Discard(ctx, r, []string{"a.txt"})
	if err != nil || !strings.HasPrefix(d.TrashedTo, ".git/nextboard-trash/") {
		t.Fatalf("discard %+v %v", d, err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, d.TrashedTo, "a.txt")); string(b) != "mine\n" {
		t.Fatalf("trash copy %q", b)
	}
	if _, err := svc.CreateBranch(ctx, r, "feature/ssh", "", true); err != nil {
		t.Fatal(err)
	}
	if b, err := svc.Branches(ctx, r); err != nil || b.Current != "feature/ssh" || len(b.Items) != 2 {
		t.Fatalf("branches %+v %v", b, err)
	}
	if lg, err := svc.Log(ctx, r, 10); err != nil || len(lg) != 1 || !lg[0].Pushed {
		t.Fatalf("log %+v %v", lg, err)
	}

	// The environment git runs with on the remote: an ext:: transport
	// records it (repo-local opt-in, only in this throwaway repo).
	envOut := filepath.Join(root, "env.txt")
	dump := filepath.Join(root, "dump-env.sh")
	os.WriteFile(dump, []byte("#!/bin/sh\nenv > "+envOut+"\nexit 1\n"), 0o755)
	sshGit(t, repo, "config", "protocol.ext.allow", "always")
	sshGit(t, repo, "remote", "add", "probe", "ext::"+dump)
	sshGit(t, repo, "config", "branch.feature/ssh.remote", "probe")
	if j := waitJob(t, jm, must(svc.Push(r, true, nil))); j.State != jobs.StateFailed {
		t.Fatalf("probe push %+v", j)
	}
	env, _ := os.ReadFile(envOut)
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "SSH_ASKPASS_REQUIRE=never", "GCM_INTERACTIVE=never"} {
		if !strings.Contains(string(env), want+"\n") {
			t.Fatalf("remote env lacks %s:\n%s", want, env)
		}
	}
	if strings.Contains(string(env), "GIT_ASKPASS=") {
		t.Fatal("askpass reached git")
	}
	if b, _ := os.ReadFile(sentinel); string(b) != "untouched\n" {
		t.Fatal("file outside the repo changed")
	}
	ents, _ := os.ReadDir(root)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "dump-env.sh,env.txt,nohooks,outside.txt,remote.git,repo" {
		t.Fatalf("unexpected files next to the repo: %v", names)
	}
}

func must(j jobs.Job, err error) jobs.Job {
	if err != nil {
		panic(err)
	}
	return j
}

// TestPreviewOverSSH proxies a web server through a direct-tcpip channel
// of the machine's SSH connection; with AllowTcpForwarding no the preview
// reports an error state instead of hanging.
func TestPreviewOverSSH(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
	_, m := machine.ConnectTestSSHD(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		io.WriteString(w, "<html><head><title>remote</title></head><body>"+r.Host+"</body></html>")
	}))
	defer up.Close()
	port := up.Listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !machine.ProbePort(ctx, m, port, time.Second) {
		t.Fatal("direct-tcpip probe failed")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if machine.ProbePort(ctx, m, closed, time.Second) {
		t.Fatal("closed port reported open")
	}
	probe := preview.ProbePorts(ctx, func(ctx context.Context, p int) (net.Conn, error) { return machine.DialLoopback(ctx, m, p) }, []int{port, closed})
	if !probe[0].Open && !probe[1].Open {
		t.Fatalf("probe %+v", probe)
	}
	for _, pp := range probe {
		if pp.Port == port && (!pp.Open || !pp.HTTP) || pp.Port == closed && pp.Open {
			t.Fatalf("probe %+v", probe)
		}
	}

	lo := freePort(t)
	mgr := preview.New(preview.Config{
		BindHost: "127.0.0.1", PortMin: lo, PortMax: lo, LocalID: machine.LocalID,
		Dial: func(ctx context.Context, mid string, p int) (net.Conn, error) { return machine.DialLoopback(ctx, m, p) },
	})
	defer mgr.Close()
	pv, lp, _, err := mgr.Create(preview.CreateRequest{MachineID: m.ID(), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + strconv.Itoa(pv.ListenPort) + lp)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Frame-Options") != "" || !strings.Contains(string(body), "/__nb/bridge.js") ||
		!strings.Contains(string(body), "localhost:"+strconv.Itoa(port)) {
		t.Fatalf("ssh proxied %d %v %s", resp.StatusCode, resp.Header, body)
	}

	// sshd without TCP forwarding: an error state, not a hang.
	_, m2 := machine.ConnectTestSSHD(t, "Subsystem sftp internal-sftp", "AllowTcpForwarding no")
	_, err = machine.DialLoopback(ctx, m2, port)
	if err != machine.ErrForwardingDisabled {
		t.Fatalf("forwarding disabled: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 100; i++ {
		p := 40000 + int(time.Now().UnixNano()%20000)
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			l.Close()
			return p
		}
	}
	t.Fatal("no free port")
	return 0
}
