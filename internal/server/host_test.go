package server

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/hostres"
)

// GET /api/host/resources answers with This PC's reading, the hub itself among its consoles,
// and the last hour when asked; the host's thresholds are settings.
func TestHostResources(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("This PC is read on Linux")
	}
	h := newTestHub(t)
	h.setup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.srv.host.Run(ctx)

	var host map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, out := h.do("GET", "/api/host/resources?history=1", nil, nil)
		if st != 200 {
			t.Fatalf("%d %v", st, out)
		}
		host = out["host"].(map[string]any)
		if _, ok := out["history"].([]any); !ok {
			t.Fatalf("no history %v", out)
		}
		if cs, _ := host["consoles"].([]any); len(cs) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if host["supported"] != true || host["cpu"].(map[string]any)["threads"].(float64) < 1 || host["memory"].(map[string]any)["total"].(float64) <= 0 {
		t.Fatalf("reading %v", host)
	}
	found := false
	for _, c := range host["consoles"].([]any) {
		if c.(map[string]any)["sessionId"] == "hub" && c.(map[string]any)["name"] == "NextBoard" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the hub is not among the consoles: %v", host["consoles"])
	}

	if st, _ := h.do("PATCH", "/api/settings", map[string]any{"host": map[string]any{"warnTempC": 95, "hotTempC": 90}}, nil); st != 400 {
		t.Fatalf("warn above hot accepted: %d", st)
	}
	if st, out := h.do("PATCH", "/api/settings", map[string]any{"host": map[string]any{"warnTempC": 70, "hotTempC": 85}}, nil); st != 200 {
		t.Fatalf("thresholds %d %v", st, out)
	}
	if th := h.srv.host.Thresholds(); th.WarnTempC != 70 || th.HotTempC != 85 || th.HotMemPct != 95 {
		t.Fatalf("thresholds the sampler gets %+v", th)
	}
}

// This PC hot for 30 s is told once, as an attention item; again only after 5 min out of it.
func TestHostAlert(t *testing.T) {
	h := newTestHub(t)
	t0 := time.Unix(1_800_000_000, 0)
	hot := func(at time.Duration) hostres.Snapshot {
		return hostres.Snapshot{At: t0.Add(at), Hostname: "devbox", Level: "hot", Why: "CPU 93 °C"}
	}
	ok := func(at time.Duration) hostres.Snapshot { return hostres.Snapshot{At: t0.Add(at), Level: "ok"} }
	count := func() int {
		n := 0
		items, _ := h.srv.attn.since(0, 100)
		for _, a := range items {
			if a.Kind == "hostHot" {
				n++
				if a.Title != "devbox runs hot" || a.Body != "CPU 93 °C" {
					t.Fatalf("alert %+v", a)
				}
			}
		}
		return n
	}
	for _, s := range []hostres.Snapshot{hot(0), hot(20 * time.Second)} {
		h.srv.hostAlert(s)
	}
	if count() != 0 {
		t.Fatal("told before 30 s")
	}
	h.srv.hostAlert(hot(30 * time.Second))
	h.srv.hostAlert(hot(90 * time.Second))
	h.srv.hostAlert(ok(100 * time.Second))
	h.srv.hostAlert(hot(110 * time.Second)) // back within 5 min: no second alert
	h.srv.hostAlert(hot(150 * time.Second))
	if count() != 1 {
		t.Fatalf("alerts %d, want 1", count())
	}
	h.srv.hostAlert(ok(200 * time.Second))
	h.srv.hostAlert(ok(200*time.Second + hostRearm))
	h.srv.hostAlert(hot(600 * time.Second))
	h.srv.hostAlert(hot(631 * time.Second))
	if count() != 2 {
		t.Fatalf("after cooling down for 5 min: %d alerts, want 2", count())
	}
	// turned off in Settings › Notifications
	if _, err := h.srv.settings.Patch([]byte(`{"notifications":{"host":false}}`)); err != nil {
		t.Fatal(err)
	}
	h.srv.hostAlert(ok(2000 * time.Second))
	h.srv.hostAlert(ok(2000*time.Second + hostRearm))
	h.srv.hostAlert(hot(3000 * time.Second))
	h.srv.hostAlert(hot(3031 * time.Second))
	if count() != 2 {
		t.Fatal("told while off")
	}
}
