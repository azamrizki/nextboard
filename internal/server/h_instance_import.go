package server

// The setup wizard's view of instances (docs: design §5): what /api/setup/status says about this
// instance, and copying from another instance of this machine (POST /api/instances/import).

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/instance"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/settings"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/vault"
)

// wizardInstance is this hub's instance name ("default" for ~/.nextboard) and hubId, as
// instance.EnsureIdentity recorded them in config.json before the hub loaded it.
func (s *Server) wizardInstance() (name, hubID string) {
	s.cfgMu.Lock()
	name, hubID = s.cfg.Instance, s.cfg.HubID
	s.cfgMu.Unlock()
	if name == "" {
		name = instance.LegacyName(s.st.Dir)
	}
	return name, hubID
}

// suggestedHubName is what the wizard offers as the hub name: the hostname, and the instance
// after it for every instance but the default one.
func suggestedHubName(inst string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "nextboard"
	}
	name := host
	if inst != instance.Default {
		name = host + " · " + inst
	}
	if r := []rune(name); len(r) > 64 {
		name = string(r[:64])
	}
	return name
}

// setupStatusInstance adds what the public setup status says about the instance: its name and
// hubId, never a path (a username in a path is nobody's business before login).
func (s *Server) setupStatusInstance(out map[string]any, needsSetup bool) {
	name, hubID := s.wizardInstance()
	out["instance"] = name
	out["defaultInstance"] = name == instance.Default
	if hubID != "" {
		out["hubId"] = hubID
	}
	if needsSetup {
		out["suggestedHubName"] = suggestedHubName(name)
		// the name New instance… was given (h_instances.go) is what the wizard offers
		if hn := s.hubName(); hn != "" {
			out["suggestedHubName"] = hn
		}
	}
}

// importSource is another instance of this machine that settings can be copied from.
type importSource struct {
	Name    string `json:"name"`
	HubName string `json:"hubName"`
	// SetUp: it has an admin password, which a copy needs.
	SetUp bool   `json:"setUp"`
	Dir   string `json:"-"`
}

// importSources are the other instances of this OS user (instance.List, read only), this hub's
// own folder left out, and only those with a config.json. The web never names a folder; it picks
// one of these by name. Nothing of theirs is written or locked: their hub name and whether they
// have an admin come from reading their config.json and auth.json.
func (s *Server) importSources() []importSource {
	all, err := instance.List()
	if err != nil {
		log.Printf("instance import: listing instances: %v", err)
	}
	self := samePath(s.st.Dir)
	var out []importSource
	for _, in := range all {
		if !in.Configured || samePath(in.DataDir) == self {
			continue
		}
		var cfg struct {
			HubName string `json:"hubName"`
		}
		st := &store.Store{Dir: in.DataDir}
		_, _ = st.Load(config.File, &cfg)
		out = append(out, importSource{Name: in.Name, HubName: cfg.HubName, SetUp: auth.SetUpIn(st), Dir: in.DataDir})
	}
	return out
}

// samePath is p made comparable: absolute, with its symlinks resolved when it exists.
func samePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return filepath.Clean(abs)
}

// handleImportSources: GET /api/instances/import (admin) lists what the copy step can pick from.
func (s *Server) handleImportSources(w http.ResponseWriter, r *http.Request, p *principal) {
	name, _ := s.wizardInstance()
	list := s.importSources()
	if list == nil {
		list = []importSource{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"self": name, "sources": list})
}

// importRequest is the body of POST /api/instances/import. A secret comes only with its own box
// ticked, and only together with what it belongs to.
type importRequest struct {
	From     string `json:"from"`
	Password string `json:"password"`

	Settings bool `json:"settings"`
	// Presets: custom agents (presets.json) and saved layouts.
	Presets            bool `json:"presets"`
	MCP                bool `json:"mcp"`
	MCPSecrets         bool `json:"mcpSecrets"`
	Machines           bool `json:"machines"`
	MachineCredentials bool `json:"machineCredentials"`
	Providers          bool `json:"providers"`
	ProviderKeys       bool `json:"providerKeys"`
	Vault              bool `json:"vault"`
}

func (q *importRequest) normalize() {
	q.MCPSecrets = q.MCPSecrets && q.MCP
	q.MachineCredentials = q.MachineCredentials && q.Machines
	q.ProviderKeys = q.ProviderKeys && q.Providers
}

func (q importRequest) anything() bool {
	return q.Settings || q.Presets || q.MCP || q.Machines || q.Providers || q.Vault
}

func (q importRequest) secrets() bool { return q.MCPSecrets || q.MachineCredentials || q.ProviderKeys }

// importCopied counts what came over.
type importCopied struct {
	Settings     bool `json:"settings"`
	Agents       int  `json:"agents"`
	Layouts      int  `json:"layouts"`
	MCP          int  `json:"mcp"`
	Machines     int  `json:"machines"`
	Providers    int  `json:"providers"`
	VaultItems   int  `json:"vaultItems"`
	Vault        bool `json:"vault"`
	SecretsTaken bool `json:"secretsTaken"`
}

// handleInstanceImport: POST /api/instances/import (admin) copies settings, and the secrets
// ticked, from another instance of this machine into this one. The other instance's admin
// password is required, so a setup link of a new instance does not open its siblings. Its
// auth.json, secret.key, devices, sessions, boards, projects, memory, scrollback, logs and login
// profiles are never copied, and nothing is written in its folder.
func (s *Server) handleInstanceImport(w http.ResponseWriter, r *http.Request, p *principal) {
	// sibling instances and preview ports are same-site: SameSite alone does not stop them
	if !s.checkOrigin(r, p) {
		writeErrCode(w, http.StatusForbidden, "origin", "request from another origin")
		return
	}
	var req importRequest
	if !decode(w, r, &req) {
		return
	}
	req.normalize()
	key := "import:" + clientIP(r)
	if s.limiter.Blocked(key) {
		writeErrCode(w, http.StatusTooManyRequests, "too_many_attempts", "too many wrong passwords, try again in 10 minutes")
		return
	}
	if instance.ValidName(req.From) != nil {
		writeErrCode(w, http.StatusBadRequest, "bad_instance_name", "from: the name of another instance on this machine")
		return
	}
	if !req.anything() {
		writeErrCode(w, http.StatusBadRequest, "nothing_to_copy", "tick at least one thing to copy")
		return
	}
	var src *importSource
	for _, c := range s.importSources() {
		if c.Name != req.From {
			continue
		}
		if src != nil {
			writeErrCode(w, http.StatusConflict, "ambiguous_instance", fmt.Sprintf("two data folders say they are %q; copy with the CLI instead", req.From))
			return
		}
		c := c
		src = &c
	}
	if src == nil {
		writeErrCode(w, http.StatusNotFound, "unknown_instance", fmt.Sprintf("no other instance called %q on this machine", req.From))
		return
	}
	ok, err := auth.CheckPasswordIn(&store.Store{Dir: src.Dir}, req.Password)
	switch {
	case errors.Is(err, auth.ErrNotSetup):
		writeErrCode(w, http.StatusConflict, "not_set_up", fmt.Sprintf("%q has no admin yet; finish its setup first", src.Name))
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	case !ok:
		s.limiter.Fail(key)
		writeErrCode(w, http.StatusForbidden, "bad_password", fmt.Sprintf("wrong admin password of %q", src.Name))
		return
	}
	// the other instance's key is opened only for what is to be sealed again with this one's
	var box *secret.Box
	if req.secrets() {
		if box, err = secret.OpenExisting(filepath.Join(src.Dir, "secret.key")); err != nil {
			writeErrCode(w, http.StatusConflict, "no_master_key", fmt.Sprintf("%q has no master key to open its secrets with", src.Name))
			return
		}
	}
	boxIf := func(on bool) *secret.Box {
		if on {
			return box
		}
		return nil
	}

	var got importCopied
	var notes []string
	fail := func(what string, err error) {
		log.Printf("instance import from %q: %s: %v", src.Name, what, err)
		notes = append(notes, fmt.Sprintf("%s could not be copied: %v", what, err))
	}
	provIDs := map[string]string{}
	if req.Providers {
		ids, n, err := s.providers.CopyFrom(src.Dir, boxIf(req.ProviderKeys))
		notes = append(notes, n...)
		if err != nil {
			fail("API providers", err)
		}
		for from, to := range ids {
			provIDs[from] = to
			s.publishProvider(to)
		}
		got.Providers = len(ids)
	}
	if req.Machines {
		ids, n, err := s.machines.CopyFrom(src.Dir, boxIf(req.MachineCredentials))
		notes = append(notes, n...)
		if err != nil {
			fail("SSH machines", err)
		}
		got.Machines = len(ids)
		if len(ids) > 0 {
			notes = append(notes, "SSH machines are copied disconnected: connect them from Machines")
		}
	}
	if req.MCP {
		ids, n, err := s.mcp.CopyFrom(src.Dir, boxIf(req.MCPSecrets))
		notes = append(notes, n...)
		if err != nil {
			fail("MCP servers", err)
		}
		got.MCP = len(ids)
	}
	if req.Presets {
		ids, n, err := s.agents.CopyFrom(src.Dir)
		notes = append(notes, n...)
		if err != nil {
			fail("custom agents", err)
		}
		if got.Agents = len(ids); got.Agents > 0 {
			s.agentsChanged()
		}
		if s.orch != nil && s.orch.Layouts != nil {
			n, err := s.orch.Layouts.CopyFrom(src.Dir)
			if err != nil {
				fail("layouts", err)
			}
			if got.Layouts = n; n > 0 {
				s.events.Publish(map[string]any{"type": "layouts"})
			}
		}
	}
	if req.Vault {
		n, err := s.credVault.CopyFrom(src.Dir)
		switch {
		case errors.Is(err, vault.ErrExists):
			notes = append(notes, "the vault was not copied: this instance has one already")
		case errors.Is(err, vault.ErrAbsent):
			notes = append(notes, fmt.Sprintf("%q has no vault", src.Name))
		case err != nil:
			fail("the vault", err)
		default:
			got.Vault, got.VaultItems = true, n
			notes = append(notes, "the vault opens with the master password it has there")
		}
	}
	if req.Settings {
		from, err := settings.Open(&store.Store{Dir: src.Dir})
		if err != nil {
			fail("settings", err)
		} else {
			next, n := remapImportedSettings(from.Get(), s.settings.Get(), provIDs, s.machineIDs())
			notes = append(notes, n...)
			if _, err := s.settings.Replace(next); err != nil {
				fail("settings", err)
			} else {
				got.Settings = true
			}
		}
	}
	got.SecretsTaken = box != nil
	log.Printf("instance import: copied from instance %q (settings %v, %d agents, %d layouts, %d MCP servers, %d machines, %d providers, vault %v, secrets %v)",
		src.Name, got.Settings, got.Agents, got.Layouts, got.MCP, got.Machines, got.Providers, got.Vault, got.SecretsTaken)
	if notes == nil {
		notes = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": src.Name, "copied": got, "notes": notes})
}

// machineIDs are the machines this hub has, This PC included.
func (s *Server) machineIDs() map[string]bool {
	out := map[string]bool{machine.LocalID: true}
	for _, m := range s.machines.List() {
		out[m.ID] = true
	}
	for _, m := range s.machines.ListArchived() {
		out[m.ID] = true
	}
	return out
}

// remapImportedSettings is src made to fit this instance (dst is what it has now):
//   - agents.profiles stays this instance's own: a login profile belongs to one instance;
//   - previews.portRange stays this instance's own: each instance has its block of ports;
//   - runner.provider, lead.runner and lead.providers follow the copied providers, or go back to
//     the login when theirs was not copied; nextcode.provider follows them too, or is none;
//   - per-machine choices (agents.defaults, agents.paths) of a machine this instance does not
//     have are dropped.
func remapImportedSettings(src, dst settings.Settings, providers map[string]string, machines map[string]bool) (settings.Settings, []string) {
	next := src
	var notes []string
	next.Agents.Profiles = dst.Agents.Profiles
	next.Previews.PortRange = dst.Previews.PortRange
	mapProv := func(id, what string) string {
		if id == "" {
			return ""
		}
		if to, ok := providers[id]; ok {
			return to
		}
		notes = append(notes, fmt.Sprintf("%s used an API provider that was not copied; it uses the login again", what))
		return ""
	}
	next.Runner.Provider = mapProv(src.Runner.Provider, "background runs (runner.provider)")
	if id := src.NextCode.Provider; id != "" {
		// NextCode's own engine has no login to go back to
		if next.NextCode.Provider = providers[id]; next.NextCode.Provider == "" {
			notes = append(notes, "NextCode's own engine (nextcode.provider) used an API provider that was not copied; pick one in Settings › NextCode")
		}
	}
	if strings.HasPrefix(src.Lead.Runner, "ap_") {
		next.Lead.Runner = mapProv(src.Lead.Runner, "the lead (lead.runner)")
	}
	if len(src.Lead.Providers) > 0 {
		next.Lead.Providers = map[string]string{}
		for ag, id := range src.Lead.Providers {
			if to := mapProv(id, fmt.Sprintf("consoles the lead starts with %s (lead.providers)", ag)); to != "" {
				next.Lead.Providers[ag] = to
			}
		}
	}
	keep := func(m map[string]string) map[string]string {
		out := map[string]string{}
		for id, v := range m {
			if machines[id] {
				out[id] = v
			}
		}
		return out
	}
	next.Agents.Defaults = keep(src.Agents.Defaults)
	next.Agents.Paths = map[string]map[string]string{}
	for id, v := range src.Agents.Paths {
		if machines[id] {
			next.Agents.Paths[id] = v
		}
	}
	return next, notes
}
