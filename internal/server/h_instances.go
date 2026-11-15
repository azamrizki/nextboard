package server

// The instances of This PC as the info panel sees them (docs: multi-instance design §3, §4):
// what GET /api/hub adds about this instance (its folders, its service, its siblings, whether
// its binary was replaced since it started), the web manifest named after the instance, and
// New instance… (GET/POST /api/instances), which makes a new instance with instance.Create, the
// path `nextboard install --instance` takes too.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/instance"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/update"
	"github.com/azamrizki/nextboard/internal/webui"
)

// instancesState is the Server's part for this file.
type instancesState struct {
	// create: one POST /api/instances at a time, so two cannot pick the same name or ports
	create sync.Mutex

	mu    sync.Mutex
	svcAt time.Time
	svc   hubService
}

// hubService is the service that runs this instance, as /api/hub shows it.
type hubService struct {
	// Kind is "systemd", "launchd", or "none": no service of this instance serves this folder.
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	// UnitPath is for admins only.
	UnitPath string `json:"unitPath,omitempty"`
	// Managed: this process is the one the service runs (update.Managed); false for a hub
	// started by hand while its unit is there.
	Managed bool `json:"managed"`
}

// binaryStamp is the hub's program as it was when the hub started.
type binaryStamp struct {
	path string
	fi   os.FileInfo
}

// startBinary is read when the package loads, that is when the hub starts.
var startBinary = stampSelf()

func stampSelf() binaryStamp {
	p, err := os.Executable()
	if err != nil {
		return binaryStamp{}
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	fi, err := os.Stat(p)
	if err != nil {
		return binaryStamp{path: p}
	}
	return binaryStamp{path: p, fi: fi}
}

// replaced reports whether another program is at the path now (an update of this instance or of
// a sibling on the same binary swapped it in): the hub runs the new one only once it restarts.
// A program that is gone is not a replacement.
func (b binaryStamp) replaced() bool {
	if b.fi == nil {
		return false
	}
	now, err := os.Stat(b.path)
	if err != nil {
		return false
	}
	return !os.SameFile(b.fi, now) || !now.ModTime().Equal(b.fi.ModTime()) || now.Size() != b.fi.Size()
}

// instanceHealthWait is how long POST /api/instances waits for the new hub to answer.
var instanceHealthWait = instance.DefaultHealthWait

// hubInstanceInfo adds this instance's fields to GET /api/hub. Paths and the other instances are
// for admins only: a paired device sees only its own hub.
func (s *Server) hubInstanceInfo(out map[string]any, admin bool) {
	name := s.instanceLabel()
	out["instance"] = map[string]any{"name": name, "default": name == instance.Default}
	if id := s.hubID(); id != "" {
		out["hubId"] = id
	}
	out["statusListen"] = s.statusListen
	replaced := startBinary.replaced()
	out["replacedSinceStart"] = replaced
	svc := s.serviceInfo()
	if !admin {
		svc.UnitPath = ""
		out["service"] = svc
		return
	}
	out["service"] = svc
	dir := s.dataDirAbs()
	out["dataDir"] = dir
	out["configFile"] = filepath.Join(dir, config.File)
	out["binary"] = map[string]any{"path": startBinary.path, "replacedSinceStart": replaced}
	out["siblings"] = s.siblingViews()
}

func (s *Server) dataDirAbs() string {
	if abs, err := filepath.Abs(s.st.Dir); err == nil {
		return abs
	}
	return s.st.Dir
}

// serviceInfo is this instance's service, asked again at most every siblingsFresh.
func (s *Server) serviceInfo() hubService {
	c := &s.insts
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.svcAt.IsZero() || time.Since(c.svcAt) > siblingsFresh {
		c.svc, c.svcAt = s.findOwnService(), time.Now()
	}
	return c.svc
}

// findOwnService is the unit (or LaunchAgent) of this instance's name, when it serves this hub's
// folder; a unit of the same name on another folder is not this hub's.
func (s *Server) findOwnService() hubService {
	name := s.instanceLabel()
	svc, dir, err := instance.FindService(name)
	if err != nil || svc == nil || samePath(dir) != samePath(s.st.Dir) {
		return hubService{Kind: "none"}
	}
	return hubService{Kind: svc.Kind, Name: svc.Name, UnitPath: svc.UnitPath, Managed: update.Managed(name, startBinary.path) == nil}
}

// selfInstance is this hub as instance.List would find it.
func (s *Server) selfInstance() instance.Instance {
	dir := s.dataDirAbs()
	return instance.Instance{
		Name: s.instanceLabel(), HubID: s.hubID(), DataDir: dir, ConfigFile: filepath.Join(dir, config.File), Configured: true,
		Listen: s.listen, StatusListen: s.statusListen, PreviewRange: s.settings.View().Previews.PortRange, Running: true, PID: os.Getpid(),
	}
}

// instancesHere are the instances of This PC (instance.List, read only) with this hub among
// them, and its index. List finds the folders ~/.nextboard and ~/.nextboard-* and the services,
// so a hub on another folder (make dev's .devdata, a test's) is added as it runs: its ports are
// then claimed too.
func (s *Server) instancesHere() ([]instance.Instance, int) {
	list, err := instance.List()
	if err != nil {
		log.Printf("instances: %v", err)
	}
	me, id := samePath(s.st.Dir), s.hubID()
	for i, in := range list {
		if samePath(in.DataDir) == me {
			// what this hub runs on, which its config.json may not say (a --listen flag)
			list[i].Listen, list[i].StatusListen, list[i].Running = s.listen, s.statusListen, true
			return list, i
		}
	}
	for i, in := range list {
		if id != "" && in.HubID == id {
			return list, i
		}
	}
	return append(list, s.selfInstance()), len(list)
}

// siblingView is another instance of This PC in the info panel.
type siblingView struct {
	Name    string `json:"name"`
	HubName string `json:"hubName,omitempty"`
	Listen  string `json:"listen"`
	Port    int    `json:"port,omitempty"`
	// URLs are where its hub is reached (HubURLs of its listen and extraUrls).
	URLs []string `json:"urls"`
	// Running: its hub holds its hub.lock. State is "running" or "stopped".
	Running bool   `json:"running"`
	State   string `json:"state"`
	// Service is its unit's or LaunchAgent's name, "" when it has none.
	Service  string   `json:"service,omitempty"`
	Problems []string `json:"problems,omitempty"`
}

func (s *Server) siblingViews() []siblingView {
	all, self := s.instancesHere()
	out := []siblingView{}
	for i, in := range all {
		if i == self {
			continue
		}
		var cfg struct {
			HubName   string   `json:"hubName"`
			ExtraURLs []string `json:"extraUrls"`
		}
		_, _ = (&store.Store{Dir: in.DataDir}).Load(config.File, &cfg)
		v := siblingView{Name: in.Name, HubName: cfg.HubName, Listen: in.Listen, Port: listenPort(in.Listen), Running: in.Running, State: "stopped", Problems: in.Problems}
		if v.Running {
			v.State = "running"
		}
		if v.Port > 0 {
			v.URLs = AdvertisedURLs(HubURLs(in.Listen, cfg.ExtraURLs))
		}
		if v.URLs == nil {
			v.URLs = []string{}
		}
		if in.Service != nil {
			v.Service = in.Service.Name
		}
		out = append(out, v)
	}
	return out
}

// handleManifest serves the web manifest named after this hub, so two installed instances do
// not look the same on a dock or a home screen: "NextBoard · <hub name>", short name the hub name;
// before the hub has a name, a named instance's ("NextBoard · work"), else the bundle's.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m := map[string]any{}
	if raw, err := fs.ReadFile(webui.FS(), "manifest.json"); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	if len(m) == 0 {
		m = map[string]any{"id": "/", "name": "NextBoard", "short_name": "NextBoard", "start_url": "/", "scope": "/", "display": "standalone"}
	}
	label := strings.TrimSpace(s.hubName())
	if label == "" {
		label = s.instanceName()
	}
	if label != "" {
		m["name"] = "NextBoard · " + label
		m["short_name"] = label
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(m)
}

// ownListenHost is the address this hub listens on, "0.0.0.0" for every address.
func (s *Server) ownListenHost() string {
	host, _, err := net.SplitHostPort(s.listen)
	if err != nil || host == "" {
		return "0.0.0.0"
	}
	return host
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// handleInstances: GET /api/instances (admin) lists the instances of This PC, this one too
// (self), and suggests the next free block of ports and the host for a new one: the same host as
// this hub's, so it is reached the same way (loopback is the other choice).
func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request, p *principal) {
	all, _ := s.instancesHere()
	host := s.ownListenHost()
	hostname, _ := os.Hostname()
	suggest := map[string]any{"host": host, "loopback": loopbackHost(host), "hostname": hostname}
	out := map[string]any{"self": s.instanceLabel(), "items": all, "suggest": suggest}
	if b, err := instance.PickBlock(all, 0); err != nil {
		out["noFreeBlock"] = err.Error()
	} else {
		suggest["port"], suggest["previewRange"] = b.HubPort, b.PreviewRange
	}
	writeJSON(w, http.StatusOK, out)
}

// createInstanceRequest is the body of POST /api/instances. The web never names a folder: the
// new instance's is ~/.nextboard-<name>.
type createInstanceRequest struct {
	Name    string `json:"name"`
	HubName string `json:"hubName"`
	// ListenHost is "127.0.0.1" (also "") or this hub's own host; anything else is refused.
	ListenHost string `json:"listenHost"`
	// Port 0 (or the suggested one) takes the next free block.
	Port int `json:"port"`
	// AllowNetwork must be true for a host beyond loopback: plain HTTP there can be read on the
	// network, which the dialog says.
	AllowNetwork bool   `json:"allowNetwork"`
	Password     string `json:"password"`
}

// createdView is the 201 answer: what instance.Create made (the setup code only inside
// setupUrls), and why its service did not start, when it did not.
type createdView struct {
	*instance.Created
	StartError string `json:"startError,omitempty"`
}

// handleInstanceCreate: POST /api/instances (admin) makes a new instance on This PC with its own
// folder, ports and service, and answers its single-use setup link (30 minutes), only here: the
// link is in no event and no log. Besides X-NextBoard it needs an Origin that is this hub's own
// (sibling instances and preview ports are same-site, so SameSite does not stop them) and this
// instance's admin password again, rate limited like a login.
func (s *Server) handleInstanceCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	if r.Header.Get("Origin") == "" || !s.checkOrigin(r, p) {
		writeErrCode(w, http.StatusForbidden, "origin", "request from another origin")
		return
	}
	var req createInstanceRequest
	if !decode(w, r, &req) {
		return
	}
	key := "instances:" + clientIP(r)
	if s.limiter.Blocked(key) {
		writeErrCode(w, http.StatusTooManyRequests, "too_many_attempts", "too many wrong passwords, try again in 10 minutes")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := instance.ValidName(req.Name); err != nil {
		writeErrCode(w, http.StatusBadRequest, "bad_instance_name", err.Error())
		return
	}
	if req.Name == instance.Default {
		writeErrCode(w, http.StatusBadRequest, "bad_instance_name", "default is the instance in ~/.nextboard: set it up with `nextboard install`")
		return
	}
	req.HubName = strings.TrimSpace(req.HubName)
	if len(req.HubName) > 64 || strings.ContainsAny(req.HubName, "\r\n\x00") {
		writeErrCode(w, http.StatusBadRequest, "bad_hub_name", "the hub name is one line of at most 64 characters")
		return
	}
	own := s.ownListenHost()
	host := strings.Trim(strings.TrimSpace(req.ListenHost), "[]")
	switch {
	case host == "" || loopbackHost(host):
		host = "127.0.0.1"
	case host != own:
		writeErrCode(w, http.StatusBadRequest, "bad_listen", fmt.Sprintf("a new instance listens on this machine only (127.0.0.1) or on this hub's own address (%s)", own))
		return
	}
	if !loopbackHost(host) && !req.AllowNetwork {
		writeErrCode(w, http.StatusConflict, "network_not_allowed", fmt.Sprintf("listening on %s reaches beyond this machine over plain HTTP: confirm it (allowNetwork)", host))
		return
	}
	if req.Port != 0 && (req.Port < 1024 || req.Port > 65535) {
		writeErrCode(w, http.StatusBadRequest, "bad_listen", "the port must be 1024 to 65535")
		return
	}
	ok, err := auth.CheckPasswordIn(s.st, req.Password)
	switch {
	case errors.Is(err, auth.ErrNotSetup):
		writeErrCode(w, http.StatusConflict, "not_set_up", err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	case !ok:
		s.limiter.Fail(key)
		writeErrCode(w, http.StatusForbidden, "bad_password", "wrong admin password")
		return
	}

	s.insts.create.Lock()
	defer s.insts.create.Unlock()
	all, _ := s.instancesHere() // this hub too: its own ports are claimed as well
	opts := instance.CreateOptions{
		Name: req.Name, Host: host, HubName: req.HubName, Shell: machine.LoginShell(), Siblings: all, URLs: HubURLs,
		HealthWait: instanceHealthWait,
	}
	if req.Port != 0 {
		// the suggested port takes its whole block; another one only its preview range
		if b, err := instance.PickBlock(all, 0); err != nil || b.HubPort != req.Port {
			opts.Listen = net.JoinHostPort(host, strconv.Itoa(req.Port))
		}
	}
	c, err := instance.Create(opts)
	if c == nil {
		writeCreateErr(w, err)
		return
	}
	out := createdView{Created: c}
	if err != nil {
		out.StartError = err.Error()
	}
	log.Printf("instance %s created on %s by instance %s (started %v)", c.Instance, c.Listen, s.instanceLabel(), c.Started)
	writeJSON(w, http.StatusCreated, out)
}

func writeCreateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, instance.ErrBadName):
		writeErrCode(w, http.StatusBadRequest, "bad_instance_name", err.Error())
	case errors.Is(err, instance.ErrBadListen):
		writeErrCode(w, http.StatusBadRequest, "bad_listen", err.Error())
	case errors.Is(err, instance.ErrExists):
		writeErrCode(w, http.StatusConflict, "instance_exists", err.Error())
	case errors.Is(err, instance.ErrPortInUse):
		writeErrCode(w, http.StatusConflict, "port_in_use", err.Error())
	case errors.Is(err, instance.ErrNoFreeBlock):
		writeErrCode(w, http.StatusConflict, "no_free_block", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}
