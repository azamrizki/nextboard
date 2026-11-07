package nodedap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Node processes the program starts, debugged as threads of the same session. Every node
// process of the session loads a bootloader (NODE_OPTIONS --require): in the program it only
// drops the inspector flags, so that what it forks does not take the inspector's port and wait
// forever; in any other node process it opens an inspector on loopback, writes where it is
// into the session's folder, and waits up to 10 s for the adapter to attach and set its
// breakpoints. A child the adapter never answers runs on after that, undebugged.

const (
	bootFile    = "nb-dap-boot.js"
	childDirEnv = "NB_DAP_DIR"
	childPoll   = 100 * time.Millisecond
)

const bootJS = `'use strict';
// NextBoard's Node debugger: report this process's inspector and wait to be attached.
(() => {
  const dir = process.env.` + childDirEnv + `;
  if (!dir) return;
  let inspector;
  try { inspector = require('inspector'); } catch { return; }
  if (inspector.url()) {
    // the program the debugger started: what it forks must not take its inspector port
    try { process.execArgv = process.execArgv.filter((a) => !/^--(inspect|debug)/.test(a)); } catch {}
    return;
  }
  const fs = require('fs');
  const path = require('path');
  try { inspector.open(0, '127.0.0.1', false); } catch { return; }
  const url = inspector.url();
  if (!url) return;
  const id = String(process.pid);
  const title = path.basename(process.argv[1] || process.argv0 || 'node');
  try {
    fs.writeFileSync(path.join(dir, id + '.tmp'), JSON.stringify({ pid: process.pid, url, title }), { mode: 0o600 });
    fs.renameSync(path.join(dir, id + '.tmp'), path.join(dir, id + '.json'));
  } catch { return; }
  const ready = path.join(dir, id + '.go');
  const nap = new Int32Array(new SharedArrayBuffer(4));
  const until = Date.now() + 10000;
  while (Date.now() < until) {
    try { fs.accessSync(ready); break; } catch {}
    Atomics.wait(nap, 0, 0, 20);
  }
})();
`

// childSetup makes the session's folder (0700) with the bootloader in it.
func childSetup() (string, error) {
	dir, err := os.MkdirTemp("", "nb-dap-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, bootFile), []byte(bootJS), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// watchChildren attaches every node process that reports in, until the session ends.
func (a *adapter) watchChildren() {
	seen := map[string]bool{}
	for {
		a.mu.Lock()
		dir, done := a.childDir, a.terminated
		a.mu.Unlock()
		if dir == "" || done {
			return
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(name, ".json") && !seen[name] {
				seen[name] = true
				go a.attachChild(dir, name)
			}
		}
		time.Sleep(childPoll)
	}
}

// attachChild makes a reported process a thread: breakpoints and exceptions as everywhere, then
// it may go on. It goes on in any case.
func (a *adapter) attachChild(dir, name string) {
	defer func() { _ = os.WriteFile(filepath.Join(dir, strings.TrimSuffix(name, ".json")+".go"), nil, 0o600) }()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return
	}
	var c struct {
		Pid   int    `json:"pid"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	// only an inspector on this machine's loopback, as the bootloader opens it
	if json.Unmarshal(b, &c) != nil || !strings.HasPrefix(c.URL, "ws://127.0.0.1:") {
		return
	}
	ws, _, err := websocket.DefaultDialer.Dial(c.URL, nil)
	if err != nil {
		return
	}
	t := a.newTarget(fmt.Sprintf("%s (pid %d)", c.Title, c.Pid), ws)
	go t.readCDP()
	a.event("thread", map[string]any{"reason": "started", "threadId": t.id})
	if err := t.setup(); err != nil {
		return
	}
	a.mu.Lock()
	paths := make(map[string][]srcBP, len(a.srcBPs))
	for p, bps := range a.srcBPs {
		paths[p] = bps
	}
	a.mu.Unlock()
	for p, bps := range paths {
		t.applyPath(p, bps)
	}
}
