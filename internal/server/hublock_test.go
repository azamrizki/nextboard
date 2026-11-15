package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/instance"
)

// One hub per data folder: a second New on it fails with a clear error, the first one's
// shutdown lets it go, and the folder's config.json records the instance and a hub id.
func TestOneHubPerDataFolder(t *testing.T) {
	dir := hubDataDir(t)
	srv1, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv1.review.Close) // a snapshot still running would write the folder back
	_, err = New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if !errors.Is(err, instance.ErrLocked) || !strings.Contains(err.Error(), "already running on the data folder "+dir) {
		t.Fatalf("second hub on one folder: %v", err)
	}
	if run, pid := instance.Running(dir); !run || pid != os.Getpid() {
		t.Fatalf("Running %v %d", run, pid)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var conf map[string]any
	_ = json.Unmarshal(raw, &conf)
	hubID, _ := conf["hubId"].(string)
	if conf["instance"] != "default" || len(hubID) != 10 || srv1.cfg.Instance != "default" || srv1.cfg.HubID != hubID {
		t.Fatalf("config.json %s, cfg %+v", raw, srv1.cfg)
	}
	srv1.shutdown()
	if run, _ := instance.Running(dir); run {
		t.Fatal("still locked after shutdown")
	}

	// an instance the folder is not: refused, and the lock goes with the failed start
	_, err = New(Options{DataDir: dir, Instance: "work", Listen: "127.0.0.1:0"})
	var mm *instance.MismatchError
	if !errors.As(err, &mm) || mm.Has != "default" {
		t.Fatalf("wrong instance: %v", err)
	}
	if run, _ := instance.Running(dir); run {
		t.Fatal("a failed start kept the lock")
	}

	srv2, err := New(Options{DataDir: dir, Instance: "default", Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("after the first hub stopped: %v", err)
	}
	t.Cleanup(srv2.review.Close)
	defer srv2.shutdown()
	if srv2.cfg.HubID != hubID {
		t.Fatalf("hub id changed %q → %q", hubID, srv2.cfg.HubID)
	}
}
