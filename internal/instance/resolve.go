package instance

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Source is the rule Resolve chose the data folder by.
type Source string

const (
	FromDataDir     Source = "--data-dir"
	FromInstance    Source = "--instance"
	FromEnvHome     Source = "$NEXTBOARD_HOME"
	FromEnvInstance Source = "$NEXTBOARD_INSTANCE"
	FromDefault     Source = "default"
)

// Request holds a command's --data-dir and --instance flags, "" for a flag not given.
type Request struct {
	DataDir  string
	Instance string
}

// Resolved is the instance a command acts on.
type Resolved struct {
	Name    string
	DataDir string // absolute
	From    Source
	// Recorded: config.json already records Name. When it does not, Name is what the hub
	// writes there at its next start (EnsureIdentity with this Name).
	Recorded bool
	// HubID is config.json's, "" before a hub of this version ran on the folder.
	HubID string
	// Exists: the folder holds a config.json.
	Exists bool
}

// Resolve picks the instance a command acts on, first rule that applies:
//
//  1. --data-dir D. With --instance N too, D must be N's: its config.json records N, or
//     records no instance yet (it is claimed as N), and its path does not commit it to
//     another one (~/.nextboard is the default instance's, .nextboard-<x> is x's).
//  2. --instance N: the data folder N's service runs (its --data-dir), else DirFor(N).
//  3. $NEXTBOARD_HOME (as before), checked against $NEXTBOARD_INSTANCE when both are set;
//     then $NEXTBOARD_INSTANCE alone, as --instance.
//  4. ~/.nextboard.
//
// A folder that belongs to another instance than the one named is a *MismatchError; a bad
// name wraps ErrBadName. Resolve writes nothing; the folder need not exist (Exists says).
func Resolve(req Request) (Resolved, error) {
	h, err := home()
	if err != nil && req.DataDir == "" && os.Getenv("NEXTBOARD_HOME") == "" {
		return Resolved{}, err
	}
	return resolve(req, h, runtime.GOOS, os.Getenv)
}

func resolve(req Request, home, goos string, getenv func(string) string) (Resolved, error) {
	if req.Instance != "" {
		if err := ValidName(req.Instance); err != nil {
			return Resolved{}, fmt.Errorf("--instance: %w", err)
		}
	}
	switch {
	case req.DataDir != "":
		dir, err := filepath.Abs(req.DataDir)
		if err != nil {
			return Resolved{}, err
		}
		return check(home, dir, req.Instance, FromDataDir)
	case req.Instance != "":
		return byName(home, goos, req.Instance, FromInstance)
	}
	envHome, envInst := getenv("NEXTBOARD_HOME"), getenv("NEXTBOARD_INSTANCE")
	if envInst != "" {
		if err := ValidName(envInst); err != nil {
			return Resolved{}, fmt.Errorf("$NEXTBOARD_INSTANCE: %w", err)
		}
	}
	switch {
	case envHome != "":
		dir, err := filepath.Abs(envHome)
		if err != nil {
			return Resolved{}, err
		}
		r, err := check(home, dir, "", FromEnvHome)
		if err != nil {
			return Resolved{}, err
		}
		if envInst != "" && envInst != r.Name {
			return Resolved{}, fmt.Errorf("$NEXTBOARD_HOME and $NEXTBOARD_INSTANCE disagree: %w", &MismatchError{DataDir: dir, Has: r.Name, Want: envInst})
		}
		return r, nil
	case envInst != "":
		return byName(home, goos, envInst, FromEnvInstance)
	}
	return check(home, dirIn(home, Default), "", FromDefault)
}

// byName is the folder of instance name: the one its service runs, else DirFor's.
func byName(home, goos, name string, from Source) (Resolved, error) {
	dir := dirIn(home, name)
	for _, u := range listUnits(home, goos) {
		if u.name == name {
			dir = u.dataDir
			break
		}
	}
	return check(home, dir, name, from)
}

// check reads the folder's identity; want, when set, must be (or may become) its name.
func check(home, dir, want string, from Source) (Resolved, error) {
	id, err := ReadIdentity(dir)
	if err != nil {
		return Resolved{}, err
	}
	r := Resolved{Name: id.Name, DataDir: dir, From: from, Recorded: id.Recorded, HubID: id.HubID, Exists: id.Exists}
	if want == "" {
		return r, nil
	}
	if id.Recorded && id.Name != want {
		return Resolved{}, &MismatchError{DataDir: dir, Has: id.Name, Want: want}
	}
	if !id.Recorded {
		if bound, ok := boundName(home, dir); ok && bound != want {
			return Resolved{}, &MismatchError{DataDir: dir, Has: bound, Want: want}
		}
	}
	r.Name = want
	return r, nil
}
