// Command devinstance makes the dev hub's data folder instance "dev" before `make dev` starts the
// hub (scripts/dev.sh, the Makefile's dev-hub). A named instance has cookies, browser profiles
// and an AppArmor profile of its own, so the dev hub no longer logs the browser out of an
// installed hub on the same host, and its consoles say NEXTBOARD_INSTANCE=dev.
//
//	go run ./scripts/devinstance <data folder>
//
// It writes only config.json's "instance", keeping every other field, and only when:
//   - the folder records no instance yet (a new folder, or one from before instances): the hub
//     would record "default" at its first start; or
//   - it records "default" and is the dev folder (.devdata), which a hub built between the
//     instances foundations and this script labelled so. Relabelled once, it stays "dev".
//
// It leaves the folder alone while a hub holds its lock, when a service (systemd unit,
// LaunchAgent) runs it, when its path belongs to an instance (~/.nextboard, ~/.nextboard-<x>),
// and when it records another name. Then it only says so: the hub starts as what the folder
// records, so `make dev` never fails because of this.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/instance"
	"github.com/azamrizki/nextboard/internal/store"
)

const (
	devName   = "dev"
	devFolder = ".devdata"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/devinstance <data folder>")
		os.Exit(2)
	}
	msg, err := label(os.Args[1])
	if msg != "" {
		fmt.Println("dev: " + msg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dev: "+err.Error())
		os.Exit(1)
	}
}

// label makes dir instance dev when it may, and says what it did or why not.
func label(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if running, _ := instance.Running(abs); running {
		return "", nil // the running hub's name stays; dev.sh checks the port before this anyway
	}
	r, err := instance.Resolve(instance.Request{DataDir: abs, Instance: devName})
	var mm *instance.MismatchError
	switch {
	case err == nil && r.Recorded:
		return "", nil
	case err == nil:
		// no name recorded yet: what `serve --instance dev` would claim it as
	case errors.As(err, &mm) && mm.Has == instance.Default && filepath.Base(abs) == devFolder:
		// labelled "default" by a hub of this version that ran without this script
	case errors.As(err, &mm):
		return fmt.Sprintf("%s is instance %s; the dev hub starts as that", abs, mm.Has), nil
	default:
		return "", err
	}
	if served(abs) {
		return fmt.Sprintf("%s is run by a service; its instance is left as it is", abs), nil
	}
	if err := writeInstance(abs, devName); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s is instance %s (its own cookies and browser profiles)", abs, devName), nil
}

// served reports whether a NextBoard service runs the folder.
func served(abs string) bool {
	list, _ := instance.List()
	real := abs
	if p, err := filepath.EvalSymlinks(abs); err == nil {
		real = p
	}
	for _, in := range list {
		if in.Service != nil && (in.DataDir == abs || in.DataDir == real) {
			return true
		}
	}
	return false
}

// writeInstance sets config.json's instance, every other field as it was.
func writeInstance(dir, name string) error {
	path := filepath.Join(dir, config.File)
	m := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if m == nil {
			m = map[string]json.RawMessage{}
		}
	}
	m["instance"], _ = json.Marshal(name)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return store.WriteFileAtomic(path, append(b, '\n'))
}
