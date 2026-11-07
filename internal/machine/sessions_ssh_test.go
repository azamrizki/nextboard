package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// sshd allows MaxSessions channels per connection (OpenSSH default 10) and
// refuses the next one. A burst of commands, as the code studio sends when it
// opens, must wait for a free channel instead of failing.
func TestSSHBurstWaitsForSessions(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
	reg := newTestRegistry(t)
	m := connectTestSSHD(t, reg, startSSHD(t, "Subsystem sftp internal-sftp", "MaxSessions 3"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// a console holds one channel the whole time
	hold, err := StartPipe(m, ExecSpec{Script: "sleep 20"})
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Stop()

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := Exec(ctx, m, ExecSpec{Script: fmt.Sprintf("sleep 0.4; echo %d", i), Timeout: 10 * time.Second})
			if err == nil && string(res.Stdout) != fmt.Sprintf("%d\n", i) {
				err = fmt.Errorf("stdout %q", res.Stdout)
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("command %d: %v", i, err)
		}
	}
	if _, err := m.Exec(ctx, "true"); err != nil {
		t.Errorf("Exec after the burst: %v", err)
	}

	// every channel held for longer than the hub waits: refused, the link stays
	for i := 0; i < 2; i++ {
		p, err := StartPipe(m, ExecSpec{Script: "sleep 20"})
		if err != nil {
			t.Fatal(err)
		}
		defer p.Stop()
	}
	old := sessionWait
	sessionWait = 300 * time.Millisecond
	defer func() { sessionWait = old }()
	if _, err := Exec(ctx, m, ExecSpec{Script: "true"}); !errors.Is(err, ErrSessionsFull) || LinkLost(err) {
		t.Fatalf("full server: %v", err)
	}
	// a caller that gives up first stops asking
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, err := Exec(short, m, ExecSpec{Script: "true"}); !errors.Is(err, ErrSessionsFull) || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("cancelled wait: %v after %v", err, time.Since(start))
	}
}
