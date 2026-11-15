package server

import (
	"encoding/json"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/instance"
)

// This hub among its sibling instances on one machine (several hubs of one OS user, each with
// its own data folder). What two hubs would otherwise share is named after the instance or the
// hubId: the session cookies (api.go), the tmux sessions and paste folders on servers
// (h_machines.go, h_attach.go), the usage folder, the packaged browsers' profiles and the
// AppArmor profile of NextBoard Chromium, and NEXTBOARD_INSTANCE in consoles of This PC.

// instanceIdent is what config.json records about the instance (instance.EnsureIdentity wrote
// and checked both in New, before config.Load: a bad name or hubId stops the hub there).
type instanceIdent struct {
	// Instance is "" for the default instance: today's names everywhere.
	Instance string
	// HubID marks what the hub leaves on servers and in /tmp.
	HubID string
}

// setupInstance runs before the components that name things after the instance.
func (s *Server) setupInstance() {
	s.cfgMu.Lock()
	s.ident = instanceIdent{Instance: s.cfg.Instance, HubID: s.cfg.HubID}
	s.cfgMu.Unlock()
	if s.ident.Instance == instance.Default {
		s.ident.Instance = ""
	}
	s.sessions.HubID = s.ident.HubID
	s.sessions.LocalEnv = []string{"NEXTBOARD_INSTANCE=" + s.instanceLabel()}
	s.previewsOnOwnBlock()
}

// previewsOnOwnBlock moves the previews of a named instance on block k (its hub on 12312+100k)
// to that block's range, 12320+100k…12339+100k, when its settings still have the default
// instance's 12320–12339: the two hubs would otherwise compete for the same 20 ports, and a site
// would lose the port it keeps. A new instance gets its range written when it is made; this is
// for one whose settings.json never had one. It runs before the preview proxy starts, which
// reads the range once.
func (s *Server) previewsOnOwnBlock() {
	if s.ident.Instance == "" || s.settings.View().Previews.PortRange != instance.DefaultPreviewRange {
		return
	}
	port := listenPort(s.listen)
	k := (port - instance.BlockAt(0).HubPort) / instance.BlockSize
	if k < 1 || k > instance.MaxBlock || instance.BlockAt(k).HubPort != port {
		return // not on a block: the instances list reports the shared range
	}
	r := instance.BlockAt(k).PreviewRange
	patch, _ := json.Marshal(map[string]any{"previews": map[string]any{"portRange": r}})
	if _, err := s.settings.Patch(patch); err != nil {
		log.Printf("previews of instance %s stay on %v: %v", s.ident.Instance, instance.DefaultPreviewRange, err)
		return
	}
	log.Printf("previews of instance %s moved to its own ports %d–%d", s.ident.Instance, r[0], r[1])
}

// siblingsFresh is how long the other instances found on disk are trusted.
const siblingsFresh = 30 * time.Second

// siblingPorts caches the hub and status ports of the other instances on This PC.
type siblingPorts struct {
	mu    sync.Mutex
	at    time.Time
	ports map[int]bool
}

// siblingPort reports whether port is another instance's hub or status port on This PC: a
// preview there would open that hub's own pages in a tile of this one. Its preview ports are
// not refused: a dev server may use one that hub does not.
func (s *Server) siblingPort(port int) bool {
	c := &s.siblings
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ports == nil || time.Since(c.at) > siblingsFresh {
		c.ports, c.at = s.findSiblingPorts(), time.Now()
	}
	return c.ports[port]
}

func (s *Server) findSiblingPorts() map[int]bool {
	ports := map[int]bool{}
	list, err := instance.List()
	if err != nil {
		return ports
	}
	self := s.st.Dir
	if p, err := filepath.EvalSymlinks(self); err == nil {
		self = p
	}
	for _, in := range list {
		if in.DataDir == self || in.DataDir == s.st.Dir || (in.HubID != "" && in.HubID == s.ident.HubID) {
			continue
		}
		for _, l := range []string{in.Listen, in.StatusListen} {
			if p := listenPort(l); p > 0 {
				ports[p] = true
			}
		}
	}
	return ports
}

// instanceName is the instance's name, "" for the default instance.
func (s *Server) instanceName() string { return s.ident.Instance }

// instanceLabel is the instance's name with "default" for the default instance.
func (s *Server) instanceLabel() string {
	if s.ident.Instance == "" {
		return instance.Default
	}
	return s.ident.Instance
}

// hubID is the hub's id, "" when it has none (the legacy names apply).
func (s *Server) hubID() string { return s.ident.HubID }
