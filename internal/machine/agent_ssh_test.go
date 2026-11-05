package machine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/marker"
	"github.com/azamrizki/nextboard/internal/transcript"
)

func builtin(id string) agent.Agent {
	for _, a := range agent.Builtins() {
		if a.ID == id {
			return a
		}
	}
	panic(id)
}

func gate(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
}

type outBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *outBuf) Write(p []byte) { o.mu.Lock(); o.b.Write(p); o.mu.Unlock() }
func (o *outBuf) Bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.b.Bytes()...)
}

func readAll(p machine.Proc) (*outBuf, chan struct{}) {
	o := &outBuf{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 8192)
		for {
			n, err := p.Read(buf)
			o.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()
	return o, done
}

func waitOut(t *testing.T, o *outBuf, s string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(o.Bytes(), []byte(s)) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %q in %q", s, o.Bytes())
}

// TestSSHWrapperMarkersForwardAndForeground runs the agent wrapper over a
// real SSH PTY (direct mode): markers through /dev/tty, a hook without a
// controlling terminal through the reverse port-forward, and the remote
// ps foreground check.
func TestSSHWrapperMarkersForwardAndForeground(t *testing.T) {
	gate(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl tidak ada")
	}
	reg, m := machine.ConnectTestSSHD(t)
	got := make(chan marker.Marker, 8)
	reg.HookHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if mk, err := marker.Parse(b); err == nil && r.URL.Path == "/nb/hook" {
			got <- mk
		}
		w.WriteHeader(204)
	})
	m.RefreshForward()
	url := m.HookURL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") || m.Info().HookForward == nil || m.Info().HookForward.Port == 0 {
		t.Fatalf("forward %q %+v", url, m.Info().HookForward)
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakeagent")
	os.WriteFile(fake, []byte(`#!/bin/sh
echo "AGENT-ARGS $*"
printf '{"hook_event_name":"SessionStart","session_id":"x"}' | sh -c 'set -- hook SessionStart; eval "$NB_HOOK_SH"'
printf '{"hook_event_name":"UserPromptSubmit"}' | setsid sh -c 'set -- hook UserPromptSubmit; eval "$NB_HOOK_SH"'
printf '{"hook_event_name":"Stop"}' | NB_HOOK_CH=http setsid sh -c 'set -- hook Stop; eval "$NB_HOOK_SH"'
read line
exit 5
`), 0o755)
	a := builtin("claude")
	plan, err := agent.Build(a, agent.Options{Mode: agent.ModeFresh, LiveStatus: true, Token: "tokR", Command: fake, HookURL: url})
	if err != nil {
		t.Fatal(err)
	}
	proc, err := m.Start(machine.StartSpec{SessionID: "s_w", Shell: plan.Shell, Env: append(plan.Env, "SHELL=/bin/sh"), Cwd: dir, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	out, done := readAll(proc)
	waitOut(t, out, "AGENT-ARGS --session-id "+plan.AgentSessionID+" --settings")
	waitOut(t, out, "777;nb;hook;")
	select {
	case mk := <-got:
		if mk.Type != "hook" || mk.Payload.K != "tokR" || mk.Payload.Ev != "Stop" {
			t.Fatalf("forwarded %+v", mk)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hook without tty did not arrive over the reverse forward")
	}
	waitOut(t, out, "AGENT-ARGS")
	time.Sleep(300 * time.Millisecond)
	_, ms := (&marker.Stripper{Token: "tokR"}).Feed(out.Bytes(), time.Now())
	// SessionStart via /dev/tty; UserPromptSubmit without a controlling
	// terminal via the parent's terminal; Stop (NB_HOOK_CH=http) only via HTTP.
	if len(ms) != 3 || ms[0].Type != marker.TypeAgentStart || ms[1].Payload.Ev != "SessionStart" || ms[2].Payload.Ev != "UserPromptSubmit" {
		t.Fatalf("markers %+v", ms)
	}
	pid := ms[0].Payload.Pid
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fg := proc.(machine.Foregrounder)
	if pg, _, err := fg.Foreground(ctx, pid); err != nil || pg != pid {
		t.Fatalf("agent foreground %d (wrapper %d) %v", pg, pid, err)
	}
	proc.Write([]byte("bye\r"))
	waitOut(t, out, "777;nb;agent-exit;")
	time.Sleep(500 * time.Millisecond)
	if pg, _, err := fg.Foreground(ctx, pid); err != nil || pg != pid {
		t.Fatalf("idle shell %d %v", pg, err)
	}
	proc.Write([]byte("sleep 20\r"))
	time.Sleep(700 * time.Millisecond)
	pg, comm, err := fg.Foreground(ctx, pid)
	if err != nil || pg == pid || comm != "sleep" {
		t.Fatalf("job foreground %d %q %v", pg, comm, err)
	}
	proc.Write([]byte("\x03exit\r"))
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shell did not exit")
	}
	if code, _ := proc.Wait(); code != 0 && code != 130 {
		t.Logf("exit code %d", code)
	}
	_, ms = (&marker.Stripper{Token: "tokR"}).Feed(out.Bytes(), time.Now())
	if last := ms[len(ms)-1]; last.Type != marker.TypeAgentExit || *last.Payload.Rc != 5 {
		t.Fatalf("exit marker %+v", last)
	}
}

func TestSSHLaunchLineLength(t *testing.T) {
	reg := machine.ConnectlessRegistryForTest(t)
	m, err := reg.Add(machine.AddRequest{Host: "127.0.0.1", Port: 22, User: "u", AuthMethod: "sshconfig", PersistMode: "tmux"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"claude", "codex"} {
		a := builtin(id)
		plan, _ := agent.Build(a, agent.Options{Mode: agent.ModeFresh, LiveStatus: true, Token: "0123456789abcdefghijkl", HookURL: "http://127.0.0.1:41234/nb/hook", ProfileDir: "/home/u/.claude-work", Yolo: true})
		cmd := machine.BuildCommandForTest(m, machine.StartSpec{SessionID: "s_k3j2h1q9z8", Shell: plan.Shell, Env: plan.Env, Cwd: "/home/u/projects/some app", Cols: 120, Rows: 32, TmuxName: "nb-s_k3j2h1q9z8"})
		if len(cmd) > agent.MaxCommandLine {
			t.Errorf("%s: tmux launch line %d bytes > %d", id, len(cmd), agent.MaxCommandLine)
		}
		if !strings.Contains(cmd, `set-option -wq allow-passthrough on`) {
			t.Errorf("%s: no passthrough option", id)
		}
		t.Logf("%s: %d bytes", id, len(cmd))
	}
}

// TestTranscriptOverSSH reads fixture transcripts through SFTP and through
// the exec fallback of a server without the SFTP subsystem.
func TestTranscriptOverSSH(t *testing.T) {
	gate(t)
	fx := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "transcript", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	home := t.TempDir()
	cwd := "/work/app"
	cfg := filepath.Join(home, ".claude")
	cdir := filepath.Join(cfg, "projects", transcript.ClaudeSlug(cwd))
	os.MkdirAll(cdir, 0o755)
	cid := "0b5e0000-1111-2222-3333-444455556666"
	cp := filepath.Join(cdir, cid+".jsonl")
	os.WriteFile(cp, fx("claude-text-tool-text.jsonl"), 0o644)
	now := time.Now().UTC()
	day := filepath.Join(home, ".codex", "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	os.MkdirAll(day, 0o755)
	xid := "01a16161-3072-7200-901b-c1b06d92ca7e"
	xp := filepath.Join(day, "rollout-"+now.Format("2006-01-02T15-04-05")+"-"+xid+".jsonl")
	os.WriteFile(xp, fx("codex-rollout.jsonl"), 0o644)

	for _, name := range []string{"sftp", "exec"} {
		t.Run(name, func(t *testing.T) {
			var m *machine.SSH
			if name == "sftp" {
				_, m = machine.ConnectTestSSHD(t)
			} else {
				_, m = machine.ConnectTestSSHD(t, "# no sftp subsystem")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			r, err := transcript.ReadReply(ctx, m, transcript.KindClaude, cp)
			if err != nil || r.Text == "" || r.Chars == 0 {
				t.Fatalf("claude reply %+v %v", r, err)
			}
			local, _ := transcript.ReadReply(ctx, machine.NewLocal(machine.Config{}), transcript.KindClaude, cp)
			if r.Text != local.Text || r.MessageID != local.MessageID {
				t.Fatalf("remote %q != local %q", r.Text, local.Text)
			}
			toks, err := transcript.ReadTokens(ctx, m, transcript.KindClaude, cp, time.Time{})
			if err != nil || len(toks) == 0 {
				t.Fatalf("tokens %v %v", toks, err)
			}
			if p, err := transcript.FindClaude(ctx, m, cfg, cwd, cid); err != nil || p != cp {
				t.Fatalf("find claude %q %v", p, err)
			}
			xr, err := transcript.ReadReply(ctx, m, transcript.KindCodex, xp)
			if err != nil || xr.Text == "" {
				t.Fatalf("codex reply %+v %v", xr, err)
			}
			if p, err := transcript.CodexPathByID(ctx, m, filepath.Join(home, ".codex"), xid, now); err != nil || p != xp {
				t.Fatalf("codex by id %q %v", p, err)
			}
			if name == "sftp" { // listing needs sizes/mtimes (SFTP)
				l, err := transcript.ListConversations(ctx, m, transcript.KindClaude, cfg, cwd, 10, func(string) string { return "" })
				if err != nil || len(l.Items) != 1 || l.Items[0].AgentSessionID != cid {
					b, _ := json.Marshal(l)
					t.Fatalf("conversations %s %v", b, err)
				}
			}
		})
	}
}

// TestSSHForwardRefused: a server with AllowTcpForwarding no reports the
// reason in hookForward and NB_HOOK_URL stays empty (tty only).
func TestSSHForwardRefused(t *testing.T) {
	gate(t)
	reg, m := machine.ConnectTestSSHD(t, "Subsystem sftp internal-sftp", "AllowTcpForwarding no")
	reg.HookHandler = http.NotFoundHandler()
	m.RefreshForward()
	if hf := m.Info().HookForward; hf == nil || hf.Error == "" || hf.Port != 0 || m.HookURL() != "" {
		t.Fatalf("forward %+v %q", hf, m.HookURL())
	}
}
