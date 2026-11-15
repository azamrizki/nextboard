package server

// The hub's own update (docs/API.md "Hub update"). GET /api/hub/update says whether a newer
// release of the running build's channel is out, asking GitHub at most once an hour (a failure
// is asked again after 10 minutes); a dev build asks nothing. POST /api/hub/update installs it
// as a job (download, checksum, swap; internal/update) and then restarts the hub through the
// service manager that `nextboard install` set up, as `nextboard update` does.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/update"
	"github.com/azamrizki/nextboard/internal/version"
)

const (
	updateCheckTTL   = time.Hour
	updateFailTTL    = 10 * time.Minute
	updateCheckWait  = 15 * time.Second
	updateInstallMax = 10 * time.Minute
	jobKindHubUpdate = "hubUpdate"
	// restartDelay lets the job's last event reach the browsers before the hub stops.
	restartDelay = 1500 * time.Millisecond
)

// hubUpdater is the hub's update state. The func fields replace the real thing in tests.
type hubUpdater struct {
	start sync.Mutex // held by one POST at a time

	mu         sync.Mutex
	check      update.Check
	rel        *update.Release
	at         time.Time     // when check was made; zero = never
	inflight   chan struct{} // closed when the check being made ends
	jobID      string
	installed  string // the tag swapped in, not running yet
	restartErr string

	client  func(timeout time.Duration) *update.Client
	binPath func() (string, error)
	managed func(bin string) error
	restart func() error
	current func() string
}

func (u *hubUpdater) currentVersion() string {
	if u.current != nil {
		return u.current()
	}
	return version.Info().Version
}

func (u *hubUpdater) newClient(timeout time.Duration) *update.Client {
	if u.client != nil {
		return u.client(timeout)
	}
	return update.NewClient(version.Repo, timeout)
}

func (u *hubUpdater) bin() (string, error) {
	if u.binPath != nil {
		return u.binPath()
	}
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func (u *hubUpdater) isManaged(bin string) error {
	if u.managed != nil {
		return u.managed(bin)
	}
	return update.Managed(bin)
}

func (u *hubUpdater) restartSelf() error {
	if u.restart != nil {
		return u.restart()
	}
	return update.RestartSelf()
}

// get is the last check while it is fresh, else a new one; fresh asks GitHub again whatever the
// last check said. Callers waiting on the same check share it.
func (u *hubUpdater) get(ctx context.Context, fresh bool) (update.Check, *update.Release) {
	cur := u.currentVersion()
	if version.Channel(cur) == version.ChannelDev {
		return update.Check{Status: update.StatusDev, Current: cur, Channel: version.ChannelDev, CheckedAt: time.Now().UTC()}, nil
	}
	u.mu.Lock()
	for {
		ttl := updateCheckTTL
		if u.check.Status == update.StatusUnknown {
			ttl = updateFailTTL
		}
		if !fresh && !u.at.IsZero() && time.Since(u.at) < ttl {
			c, rel := u.check, u.rel
			u.mu.Unlock()
			return c, rel
		}
		if u.inflight == nil {
			break
		}
		wait := u.inflight
		u.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return update.Check{Status: update.StatusUnknown, Current: cur, Channel: version.Channel(cur), Error: ctx.Err().Error(), CheckedAt: time.Now().UTC()}, nil
		}
		fresh = false // the one just made is fresh
		u.mu.Lock()
	}
	done := make(chan struct{})
	u.inflight = done
	u.mu.Unlock()

	// not the request's context: a browser that goes away does not fail a check others share
	cctx, cancel := context.WithTimeout(context.Background(), updateCheckWait)
	c, rel := u.newClient(updateCheckWait).CheckFor(cctx, cur)
	cancel()

	u.mu.Lock()
	u.check, u.rel, u.at, u.inflight = c, rel, time.Now(), nil
	close(done)
	u.mu.Unlock()
	return c, rel
}

// hubUpdateView is the body of GET /api/hub/update.
type hubUpdateView struct {
	update.Check
	// Blocked says why this hub cannot install an update from the web ("" = it can).
	Blocked      string `json:"blocked,omitempty"`
	BlockedCode  string `json:"blockedCode,omitempty"`
	JobID        string `json:"jobId,omitempty"`        // the update job running
	Installed    string `json:"installed,omitempty"`    // swapped in, the hub has not restarted into it yet
	RestartError string `json:"restartError,omitempty"` // why that restart failed
}

// refusal is why an update cannot start, as a code and a message.
type refusal struct {
	status    int
	code, msg string
}

// blocked is what stops an install on this hub, whatever the release: a dev build, a hub not
// run by its service, a folder it cannot write.
func (u *hubUpdater) blocked() (*refusal, string) {
	cur := u.currentVersion()
	if version.Channel(cur) == version.ChannelDev {
		return &refusal{http.StatusConflict, "dev_build", fmt.Sprintf("This is a dev build (%s): update it with git pull and a rebuild.", cur)}, ""
	}
	bin, err := u.bin()
	if err != nil {
		return &refusal{http.StatusInternalServerError, "no_binary", "The hub cannot find its own binary: " + err.Error()}, ""
	}
	if err := u.isManaged(bin); err != nil {
		msg := err.Error()
		if errors.Is(err, update.ErrNotService) {
			msg = "This hub was not started by the service that `nextboard install` sets up, so it cannot restart itself. Update it with `nextboard update` where it runs."
		}
		return &refusal{http.StatusConflict, "not_service", msg}, bin
	}
	if err := update.Writable(filepath.Dir(bin)); err != nil {
		return &refusal{http.StatusConflict, "not_writable", fmt.Sprintf("The hub cannot write to %s, so it cannot replace its binary there. Run `nextboard update` as a user who can.", filepath.Dir(bin))}, bin
	}
	return nil, bin
}

func (u *hubUpdater) runningJob(s *Server) string {
	u.mu.Lock()
	id := u.jobID
	u.mu.Unlock()
	if id == "" {
		return ""
	}
	if j, ok := s.jobs.Get(id); ok && j.Active() {
		return id
	}
	return ""
}

func (s *Server) handleHubUpdateCheck(w http.ResponseWriter, r *http.Request, p *principal) {
	u := &s.hubUpdate
	c, _ := u.get(r.Context(), false)
	v := hubUpdateView{Check: c, JobID: u.runningJob(s)}
	if c.Status == update.StatusAvailable {
		if why, _ := u.blocked(); why != nil {
			v.Blocked, v.BlockedCode = why.msg, why.code
		}
	}
	u.mu.Lock()
	v.Installed, v.RestartError = u.installed, u.restartErr
	u.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleHubUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Version string `json:"version"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	u := &s.hubUpdate
	if !u.start.TryLock() {
		writeErrCode(w, http.StatusConflict, "update_running", "An update is already starting.")
		return
	}
	defer u.start.Unlock()
	if id := u.runningJob(s); id != "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "An update is already running.", "code": "update_running", "jobId": id})
		return
	}
	why, bin := u.blocked()
	if why != nil {
		writeErrCode(w, why.status, why.code, why.msg)
		return
	}
	u.mu.Lock()
	installed := u.installed
	u.mu.Unlock()
	if installed != "" {
		writeErrCode(w, http.StatusConflict, "restart_pending", fmt.Sprintf("%s is already in place; the hub runs it once it restarts.", installed))
		return
	}
	cur := u.currentVersion()
	c, rel := u.get(r.Context(), true)
	switch {
	case c.Status == update.StatusUnknown || rel == nil:
		writeErrCode(w, http.StatusBadGateway, "check_failed", "Could not check GitHub for a release: "+c.Error)
		return
	case c.Status != update.StatusAvailable || !version.Newer(rel.TagName, cur):
		writeErrCode(w, http.StatusConflict, "up_to_date", fmt.Sprintf("This hub already runs %s, the newest %s release.", cur, c.Channel))
		return
	case req.Version != "" && strings.TrimPrefix(req.Version, "v") != strings.TrimPrefix(rel.TagName, "v"):
		writeErrCode(w, http.StatusConflict, "version_changed", fmt.Sprintf("The newest release is now %s, not %s. Check again.", rel.TagName, req.Version))
		return
	}
	target := *rel
	j, err := s.jobs.SubmitFunc(jobs.FuncJob{
		Kind: jobKindHubUpdate, Purpose: target.TagName,
		Run: func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error) {
			return s.runHubUpdate(ctx, rep, &target, cur, bin)
		},
		OnFinish: func(j jobs.Job) {
			if j.State == jobs.StateDone {
				time.AfterFunc(restartDelay, s.restartAfterUpdate)
			}
		},
	})
	if err != nil {
		runnerErr(w, err)
		return
	}
	u.mu.Lock()
	u.jobID = j.ID
	u.mu.Unlock()
	writeJSON(w, http.StatusAccepted, j)
}

// runHubUpdate is the update job: download, checksum, swap. The restart follows once it is done.
func (s *Server) runHubUpdate(ctx context.Context, rep *jobs.Reporter, rel *update.Release, from, bin string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, updateInstallMax)
	defer cancel()
	client := s.hubUpdate.newClient(updateInstallMax)
	err := client.Install(ctx, rel, bin, func(phase, text string, pct float64) {
		if pct >= 0 {
			rep.ProgressPct(phase, text, pct)
		} else {
			rep.Progress(phase, text)
		}
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, &jobs.FuncError{Code: "update_failed", Msg: err.Error()}
	}
	u := &s.hubUpdate
	u.mu.Lock()
	u.installed, u.restartErr = rel.TagName, ""
	u.mu.Unlock()
	log.Printf("update: %s replaced by %s at %s (the old binary is %s); the hub restarts", from, rel.TagName, bin, update.OldPath(bin))
	rep.Progress("restarting", "Restarting the hub…")
	out, _ := json.Marshal(map[string]any{"from": from, "to": rel.TagName, "binary": bin, "old": update.OldPath(bin), "restart": true})
	return out, nil
}

func (s *Server) restartAfterUpdate() {
	u := &s.hubUpdate
	if err := u.restartSelf(); err != nil {
		log.Printf("update: the restart failed: %v", err)
		u.mu.Lock()
		u.restartErr = err.Error()
		u.mu.Unlock()
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
