package machine_test

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/marker"
)

// TestSSHTmuxAgentPassthrough needs a disposable sshd with tmux (as for
// TestSSHTmuxPersistence):
//
//	NEXTBOARD_TMUX_SSH=127.0.0.1:2299 NEXTBOARD_TMUX_USER=root NEXTBOARD_TMUX_KEY=/path/key
//
// It checks that wrapper and hook markers written inside a tmux pane reach
// the hub through allow-passthrough (set on the session window only), that
// the global option is untouched, and that a re-attach does not relaunch.
func TestSSHTmuxAgentPassthrough(t *testing.T) {
	addr := os.Getenv("NEXTBOARD_TMUX_SSH")
	if addr == "" {
		t.Skip("set NEXTBOARD_TMUX_SSH/USER/KEY untuk uji tmux")
	}
	host, portS, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portS)
	key, err := os.ReadFile(os.Getenv("NEXTBOARD_TMUX_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	reg := machine.ConnectlessRegistryForTest(t)
	m, err := reg.Add(machine.AddRequest{Host: host, Port: port, User: os.Getenv("NEXTBOARD_TMUX_USER"), AuthMethod: "key", PrivateKey: string(key)})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_ = m.Connect(ctx)
	if hk := m.Info().PendingHostKey; hk != nil {
		if err := m.AcceptHostKey(ctx, hk.Fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	p := m.Info().Probe
	if p == nil || !p.Tmux {
		t.Fatalf("tmux not detected: %+v", p)
	}
	t.Logf("tmux %s, curl %q", p.TmuxVersion, p.Commands["curl"])
	globalBefore, _ := m.Exec(ctx, "tmux start-server \\; show -gv allow-passthrough 2>&1; true")

	// The fake agent lives on the remote: written with a plain exec.
	script := `#!/bin/sh
echo "TMUX-AGENT $TMUX_PANE"
printf '{"hook_event_name":"SessionStart","session_id":"abc"}' | sh -c 'set -- hook SessionStart; eval "$NB_HOOK_SH"'
read line
exit 4
`
	if out, err := m.Exec(ctx, "printf %s "+machine.Quote(script)+" > /tmp/nb-fakeagent && chmod +x /tmp/nb-fakeagent"); err != nil {
		t.Fatalf("write agent: %v %s", err, out)
	}
	a := builtin("claude")
	plan, err := agent.Build(a, agent.Options{Mode: agent.ModeFresh, LiveStatus: true, Token: "tokT", Command: "/tmp/nb-fakeagent"})
	if err != nil {
		t.Fatal(err)
	}
	spec := machine.StartSpec{SessionID: "s_tmxa", Shell: plan.Shell, Env: plan.Env, Cwd: "~", Cols: 100, Rows: 30, TmuxName: "nb-s_tmxa"}
	proc, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := readAll(proc)
	defer func() {
		c, cl := context.WithTimeout(context.Background(), 10*time.Second)
		defer cl()
		_ = m.KillTmux(c, "nb-s_tmxa")
	}()
	waitOut(t, out, "TMUX-AGENT %")
	passthrough := p.TmuxVersion >= "3.3"
	if !passthrough {
		// tmux < 3.3: the launch must not fail (-q); markers do not pass.
		time.Sleep(2 * time.Second)
		_, ms := (&marker.Stripper{Token: "tokT"}).Feed(out.Bytes(), time.Now())
		t.Logf("tmux %s: launch ok; %d marker(s) passed (passthrough is always on before 3.3)", p.TmuxVersion, len(ms))
		return
	}
	waitOut(t, out, "777;nb;hook;")
	_, ms := (&marker.Stripper{Token: "tokT"}).Feed(out.Bytes(), time.Now())
	if len(ms) != 2 || ms[0].Type != marker.TypeAgentStart || ms[0].Payload.Mode != "fresh" || ms[1].Payload.Ev != "SessionStart" {
		t.Fatalf("markers through tmux %+v\n%q", ms, out.Bytes())
	}
	if strings.Contains(string(out.Bytes()), "\x1bPtmux;") {
		t.Fatal("tmux forwarded the DCS wrapper instead of the plain OSC")
	}
	win, _ := m.Exec(ctx, "tmux show -wv -t nb-s_tmxa allow-passthrough")
	globalAfter, _ := m.Exec(ctx, "tmux show -gv allow-passthrough 2>&1; true")
	if strings.TrimSpace(win) != "on" || globalAfter != globalBefore {
		t.Fatalf("window %q, global before %q after %q", win, globalBefore, globalAfter)
	}
	t.Logf("allow-passthrough: window %q, global %q (unchanged)", strings.TrimSpace(win), strings.TrimSpace(globalAfter))
	// Re-attach after a dropped connection: no second agent-start.
	if ok, err := m.TmuxHas(ctx, "nb-s_tmxa"); err != nil || !ok {
		t.Fatalf("has-session %v %v", ok, err)
	}
	proc.Close()
	proc.Wait()
	proc2, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	out2, done2 := readAll(proc2)
	waitOut(t, out2, "TMUX-AGENT") // tmux repaints the pane
	time.Sleep(500 * time.Millisecond)
	if _, ms := (&marker.Stripper{Token: "tokT"}).Feed(out2.Bytes(), time.Now()); len(ms) != 0 {
		t.Fatalf("re-attach replayed markers %+v", ms)
	}
	proc2.Write([]byte("bye\r"))
	waitOut(t, out2, "777;nb;agent-exit;")
	_, ms = (&marker.Stripper{Token: "tokT"}).Feed(out2.Bytes(), time.Now())
	if len(ms) != 1 || *ms[0].Payload.Rc != 4 {
		t.Fatalf("exit through tmux %+v", ms)
	}
	proc2.Write([]byte("exit\r"))
	select {
	case <-done2:
	case <-time.After(10 * time.Second):
		t.Fatal("pane did not end")
	}
}
