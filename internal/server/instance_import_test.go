package server

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/mcpreg"
	"github.com/azamrizki/nextboard/internal/providers"
	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/settings"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/vault"
)

// Made-up secrets of the source instance: none may appear in clear anywhere in the target.
const (
	srcPassword   = "kerja-rahasia-01"
	srcProvKey    = "sk-PROVKEY-marker-7781"
	srcSSHPass    = "SSHPASS-marker-5512"
	srcMCPToken   = "Bearer MCPTOKEN-marker-9034"
	srcMachineIP  = "203.0.113.7"
	srcOtherHost  = "198.51.100.23"
	srcVaultBlobN = 48
)

type sourceInstance struct {
	dir       string
	box       *secret.Box
	provID    string
	machineID string
	vaultView vault.View
	hash      string
}

// makeSourceInstance writes a fixture instance "work" into home/.nextboard-work, as a hub of
// it would have left it: an admin, a provider with its key, a server with a password, an MCP
// server with a header value, a custom agent, a layout, a vault, settings that name all of them,
// and the things that must never be copied (devices, sessions).
func makeSourceInstance(t *testing.T, home string) sourceInstance {
	t.Helper()
	dir := filepath.Join(home, ".nextboard-work")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Save("config.json", map[string]any{"instance": "work", "hubId": "wrk7q2m9xa", "hubName": "devbox · work", "listen": "127.0.0.1:12412"}))
	as, err := auth.Open(st)
	must(err)
	code, _, err := as.NewSetupCode()
	must(err)
	_, err = as.CompleteSetup(code, srcPassword)
	must(err)
	box, err := secret.Open(st.Path("secret.key"))
	must(err)

	provs, err := providers.Open(st, box)
	must(err)
	name, kind, base, model, key := "Kerja GLM", "anthropic", "https://api.example.test/anthropic", "glm-4.6", srcProvKey
	pv, err := provs.Add(providers.Input{Name: &name, Kind: &kind, BaseURL: &base, Model: &model, Key: &key})
	must(err)

	reg, err := machine.OpenRegistry(st, box)
	must(err)
	m, err := reg.Add(machine.AddRequest{Name: "staging", Host: srcMachineIP, User: "deploy", AuthMethod: "password", Password: srcSSHPass})
	must(err)
	mid := m.ID()
	reg.Close()
	must(store.WriteFileAtomic(st.Path("known_hosts"), []byte(
		srcMachineIP+" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyOfStagingForTheTestOnly0000000000\n"+
			srcOtherHost+" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyOfAnotherHostNotCopied00000000\n")))

	mcp, err := mcpreg.Open(st, box)
	must(err)
	_, err = mcp.Create(mcpreg.Server{ID: "tracker", Transport: mcpreg.TransportHTTP, URL: "https://mcp.example.test/rpc",
		Headers: []mcpreg.Var{{Name: "Authorization", Value: srcMCPToken}}, Enabled: true})
	must(err)

	must(st.Save("presets.json", []map[string]any{{"id": "kerja-cli", "name": "Kerja CLI", "command": "kerja-cli", "env": []string{"KERJA_MODE=fast"}}}))
	must(st.Save("layouts.json", []map[string]any{{"id": "ly_src0001", "name": "Dua jalur",
		"lanes": []map[string]any{{"key": "a", "x": 0, "y": 0, "w": 600, "h": 400}, {"key": "b", "x": 640, "y": 0, "w": 600, "h": 400}}}}))

	v := vault.New(st)
	salt := make([]byte, 16)
	wrapped := make([]byte, 60)
	blob := make([]byte, srcVaultBlobN)
	for _, b := range [][]byte{salt, wrapped, blob} {
		_, _ = rand.Read(b)
	}
	_, err = v.Init(vault.KeyMaterial{KDF: vault.KDF{Name: "argon2id", M: 64 << 10, T: 3, P: 1},
		Salt: base64.StdEncoding.EncodeToString(salt), WrappedKey: base64.StdEncoding.EncodeToString(wrapped)})
	must(err)
	_, err = v.Batch([]vault.NewItem{{ID: "item-kerja-0001", Blob: base64.StdEncoding.EncodeToString(blob)}})
	must(err)
	view, err := v.Get()
	must(err)

	sets, err := settings.Open(st)
	must(err)
	patch, _ := json.Marshal(map[string]any{
		"runner": map[string]any{"provider": pv.ID},
		"lead":   map[string]any{"name": "Laras", "runner": pv.ID, "providers": map[string]string{"claude": pv.ID}},
		"agents": map[string]any{
			"profiles": map[string]any{"local": map[string]string{"claude": "pf_sourceonly1"}},
			"defaults": map[string]string{"local": "codex", mid: "claude", "m_gone000001": "claude"},
		},
		"previews": map[string]any{"portRange": []int{12420, 12439}},
	})
	_, err = sets.Patch(patch)
	must(err)

	must(st.Save("devices.json", []map[string]any{{"id": "d_kerjaphone", "name": "Kerja phone", "platform": "android", "publicKey": "AAAA"}}))
	must(st.Save("sessions.json", []map[string]any{{"id": "s_kerja00001", "name": "kerja console"}}))

	var af struct {
		PasswordHash string `json:"passwordHash"`
	}
	_, _ = st.Load("auth.json", &af)
	return sourceInstance{dir: dir, box: box, provID: pv.ID, machineID: mid, vaultView: view, hash: af.PasswordHash}
}

// snapshot is every file of a folder with its content, to show nothing was written there.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		out[strings.TrimPrefix(p, dir)] = string(b)
		return nil
	})
	return out
}

// grepDir reports the files under dir that hold needle.
func grepDir(dir string, needle []byte) []string {
	var hits []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, needle) {
			hits = append(hits, p)
		}
		return nil
	})
	return hits
}

// importHub is a set-up target hub with a fixture sibling "work" in a scratch HOME.
func importHub(t *testing.T) (*testHub, sourceInstance) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := makeSourceInstance(t, home)
	h := newTestHub(t)
	h.setup()
	return h, src
}

func TestSetupStatusSaysInstanceNotPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newTestHub(t)
	st, body := h.do("GET", "/api/setup/status", nil, nil)
	if st != 200 || body["needsSetup"] != true {
		t.Fatalf("status %d %v", st, body)
	}
	if body["instance"] != "default" || body["defaultInstance"] != true {
		t.Fatalf("instance fields: %v", body)
	}
	if s, _ := body["suggestedHubName"].(string); s == "" || strings.Contains(s, "·") {
		t.Fatalf("a default instance's suggested name is the hostname alone: %v", body)
	}
	raw, _ := json.Marshal(body)
	for _, p := range []string{h.srv.st.Dir, os.Getenv("HOME")} {
		if strings.Contains(string(raw), p) {
			t.Fatalf("setup status leaks a path %q: %s", p, raw)
		}
	}
	h.setup()
	if _, body := h.do("GET", "/api/setup/status", nil, nil); body["suggestedHubName"] != nil || body["instance"] != "default" {
		t.Fatalf("after setup: %v", body)
	}
}

func TestSetupStatusSuggestsNameOfNamedInstance(t *testing.T) {
	if got := suggestedHubName("work"); !strings.HasSuffix(got, " · work") {
		t.Fatalf("suggested %q", got)
	}
	if got := suggestedHubName("default"); strings.Contains(got, "·") {
		t.Fatalf("suggested %q", got)
	}
}

func TestInstanceImportSourcesAndRefusals(t *testing.T) {
	h, src := importHub(t)
	before := snapshot(t, src.dir)

	st, body := h.do("GET", "/api/instances/import", nil, nil)
	list, _ := body["sources"].([]any)
	if st != 200 || len(list) != 1 {
		t.Fatalf("sources %d %v", st, body)
	}
	one := list[0].(map[string]any)
	if one["name"] != "work" || one["setUp"] != true || one["hubName"] != "devbox · work" {
		t.Fatalf("source %v", one)
	}
	if raw, _ := json.Marshal(body); strings.Contains(string(raw), src.dir) {
		t.Fatalf("the source list names a folder: %s", raw)
	}

	all := map[string]any{"from": "work", "password": srcPassword, "settings": true, "presets": true, "mcp": true, "mcpSecrets": true,
		"machines": true, "machineCredentials": true, "providers": true, "providerKeys": true, "vault": true}
	with := func(k string, v any) map[string]any {
		out := map[string]any{}
		for a, b := range all {
			out[a] = b
		}
		out[k] = v
		return out
	}
	if st, body := h.do("POST", "/api/instances/import", all, map[string]string{"Origin": "http://127.0.0.1:1"}); st != 403 || body["code"] != "origin" {
		t.Fatalf("foreign origin: %d %v", st, body)
	}
	if st, _ := h.do("POST", "/api/instances/import", all, map[string]string{"X-NextBoard": ""}); st != 403 {
		t.Fatalf("no csrf header: %d", st)
	}
	if st, body := h.do("POST", "/api/instances/import", with("from", "../etc"), nil); st != 400 || body["code"] != "bad_instance_name" {
		t.Fatalf("path as a name: %d %v", st, body)
	}
	if st, body := h.do("POST", "/api/instances/import", with("from", "personal"), nil); st != 404 || body["code"] != "unknown_instance" {
		t.Fatalf("unknown: %d %v", st, body)
	}
	if st, body := h.do("POST", "/api/instances/import", map[string]any{"from": "work", "password": srcPassword}, nil); st != 400 || body["code"] != "nothing_to_copy" {
		t.Fatalf("nothing ticked: %d %v", st, body)
	}
	// this instance's own password does not open the sibling
	if st, body := h.do("POST", "/api/instances/import", with("password", "rahasia123"), nil); st != 403 || body["code"] != "bad_password" {
		t.Fatalf("own password: %d %v", st, body)
	}
	if len(h.srv.providers.List()) != 0 || h.srv.settings.Get().Lead.Name == "Laras" {
		t.Fatal("a refused import copied something")
	}
	for i := 0; i < 4; i++ {
		if st, _ := h.do("POST", "/api/instances/import", with("password", "salah-sekali"), nil); st != 403 {
			t.Fatalf("wrong password #%d: %d", i, st)
		}
	}
	if st, _ := h.do("POST", "/api/instances/import", all, nil); st != 429 {
		t.Fatalf("after 5 wrong passwords even the right one waits: %d", st)
	}
	if after := snapshot(t, src.dir); len(after) != len(before) {
		t.Fatalf("the source folder changed: %d files before, %d after", len(before), len(after))
	}
}

func TestInstanceImportWithoutSecrets(t *testing.T) {
	h, src := importHub(t)
	before := snapshot(t, src.dir)
	ownHash := func() string {
		var af struct {
			PasswordHash string `json:"passwordHash"`
		}
		_, _ = h.srv.st.Load("auth.json", &af)
		return af.PasswordHash
	}()
	ownKey, _ := os.ReadFile(h.srv.st.Path("secret.key"))
	ownRange := h.srv.settings.Get().Previews.PortRange
	ownProfiles := h.srv.settings.Get().Agents.Profiles

	st, body := h.do("POST", "/api/instances/import", map[string]any{"from": "work", "password": srcPassword,
		"settings": true, "presets": true, "mcp": true, "machines": true, "providers": true,
		// a secret without what it belongs to is not taken
		"vault": false, "providerKeys": false, "machineCredentials": false, "mcpSecrets": false}, nil)
	if st != 200 {
		t.Fatalf("import %d %v", st, body)
	}
	copied := body["copied"].(map[string]any)
	for k, want := range map[string]any{"settings": true, "agents": 1.0, "layouts": 1.0, "mcp": 1.0, "machines": 1.0, "providers": 1.0, "vault": false, "secretsTaken": false} {
		if copied[k] != want {
			t.Fatalf("copied.%s = %v, want %v (%v)", k, copied[k], want, body)
		}
	}

	// providers: copied under a new id, without the key
	ps := h.srv.providers.List()
	if len(ps) != 1 || ps[0].Name != "Kerja GLM" || ps[0].HasKey || ps[0].ID == src.provID {
		t.Fatalf("providers %+v", ps)
	}
	newProv := ps[0].ID
	// machines: same id, no credentials
	cfg, ok := h.srv.machines.SSH(src.machineID)
	if !ok || cfg.Config().CredRef != "" || cfg.Config().Host != srcMachineIP {
		t.Fatalf("machine %v %+v", ok, cfg)
	}
	if _, err := os.Stat(h.srv.st.Path("creds", src.machineID+".enc")); err == nil {
		t.Fatal("the server's credentials were copied without their box")
	}
	known, _ := os.ReadFile(h.srv.st.Path("known_hosts"))
	if !strings.Contains(string(known), srcMachineIP) || strings.Contains(string(known), srcOtherHost) {
		t.Fatalf("known_hosts: %q", known)
	}
	// MCP: definition only, off, no values
	ms := h.srv.mcp.List()
	if len(ms) != 1 || ms[0].ID != "tracker" || ms[0].Enabled || len(ms[0].Headers) != 0 {
		t.Fatalf("mcp %+v", ms)
	}
	if _, ok := h.srv.agents.Get("kerja-cli"); !ok {
		t.Fatal("custom agent not copied")
	}
	if l := h.srv.orch.Layouts.All(); l[len(l)-1].Name != "Dua jalur" {
		t.Fatalf("layout not copied: %+v", l[len(l)-1])
	}
	if v, _ := h.srv.credVault.Get(); v.Exists {
		t.Fatal("the vault came without its box")
	}

	// settings: copied and fitted to this instance
	got := h.srv.settings.Get()
	if got.Lead.Name != "Laras" {
		t.Fatalf("lead name %q", got.Lead.Name)
	}
	if got.Previews.PortRange != ownRange || got.Previews.PortRange == [2]int{12420, 12439} {
		t.Fatalf("portRange %v, want this instance's %v", got.Previews.PortRange, ownRange)
	}
	if pf := got.Agents.Profiles["local"]["claude"]; pf == "pf_sourceonly1" || pf != ownProfiles["local"]["claude"] {
		t.Fatalf("profiles %v, want this instance's %v", got.Agents.Profiles, ownProfiles)
	}
	if got.Runner.Provider != newProv || got.Lead.Runner != newProv || got.Lead.Providers["claude"] != newProv {
		t.Fatalf("provider references not remapped to %s: runner %q lead %q %v", newProv, got.Runner.Provider, got.Lead.Runner, got.Lead.Providers)
	}
	if got.Agents.Defaults["local"] != "codex" || got.Agents.Defaults[src.machineID] != "claude" || got.Agents.Defaults["m_gone000001"] != "" {
		t.Fatalf("per-machine defaults %v", got.Agents.Defaults)
	}

	// never copied
	if ownHash == "" || ownHash == src.hash || func() string {
		var af struct {
			PasswordHash string `json:"passwordHash"`
		}
		_, _ = h.srv.st.Load("auth.json", &af)
		return af.PasswordHash
	}() != ownHash {
		t.Fatal("auth.json changed or came from the source")
	}
	if k, _ := os.ReadFile(h.srv.st.Path("secret.key")); !bytes.Equal(k, ownKey) {
		t.Fatal("secret.key changed")
	}
	srcKey, _ := os.ReadFile(filepath.Join(src.dir, "secret.key"))
	for _, needle := range [][]byte{srcKey, []byte("d_kerjaphone"), []byte("s_kerja00001"), []byte(src.hash),
		[]byte(srcProvKey), []byte(srcSSHPass), []byte("MCPTOKEN-marker")} {
		if hits := grepDir(h.srv.st.Dir, needle); len(hits) > 0 {
			t.Fatalf("%q found in the target: %v", needle, hits)
		}
	}
	if after := snapshot(t, src.dir); len(after) != len(before) {
		t.Fatalf("the source folder changed: %d files before, %d after", len(before), len(after))
	} else {
		for k, v := range before {
			if after[k] != v {
				t.Fatalf("source file %s was written", k)
			}
		}
	}
}

func TestInstanceImportResealsTickedSecrets(t *testing.T) {
	h, src := importHub(t)
	st, body := h.do("POST", "/api/instances/import", map[string]any{"from": "work", "password": srcPassword,
		"providers": true, "providerKeys": true, "machines": true, "machineCredentials": true, "mcp": true, "mcpSecrets": true, "vault": true}, nil)
	if st != 200 {
		t.Fatalf("import %d %v", st, body)
	}
	if c := body["copied"].(map[string]any); c["secretsTaken"] != true || c["vault"] != true || c["vaultItems"] != 1.0 || c["settings"] != false {
		t.Fatalf("copied %v", c)
	}
	ownBox, err := secret.Open(h.srv.st.Path("secret.key"))
	if err != nil {
		t.Fatal(err)
	}

	// the provider key opens with this hub's key, and not with the source's
	ps := h.srv.providers.List()
	if len(ps) != 1 || !ps[0].HasKey {
		t.Fatalf("providers %+v", ps)
	}
	if k, err := h.srv.providers.Key(ps[0].ID); err != nil || k != srcProvKey {
		t.Fatalf("key %q %v", k, err)
	}
	var stored []struct {
		Key string `json:"key"`
	}
	_, _ = h.srv.st.Load("providers.json", &stored)
	raw, _ := base64.StdEncoding.DecodeString(stored[0].Key)
	if _, err := src.box.Open(raw); err == nil {
		t.Fatal("the provider key is still sealed with the source's master key")
	}
	if plain, err := ownBox.Open(raw); err != nil || string(plain) != srcProvKey {
		t.Fatalf("not sealed with this hub's key: %v", err)
	}

	// the server's password, sealed again
	blob, err := os.ReadFile(h.srv.st.Path("creds", src.machineID+".enc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.box.Open(blob); err == nil {
		t.Fatal("the credentials are still sealed with the source's master key")
	}
	plain, err := ownBox.Open(blob)
	if err != nil || !strings.Contains(string(plain), srcSSHPass) {
		t.Fatalf("credentials %v", err)
	}
	if m, _ := h.srv.machines.SSH(src.machineID); m.Config().CredRef != src.machineID {
		t.Fatalf("credRef %q", m.Config().CredRef)
	}

	// the MCP header value, sealed again, and the server stays on
	ms := h.srv.mcp.List()
	if len(ms) != 1 || !ms[0].Enabled || len(ms[0].Headers) != 1 || !ms[0].Headers[0].Set {
		t.Fatalf("mcp %+v", ms)
	}
	var svs []mcpreg.Server
	_, _ = h.srv.st.Load("mcp-servers.json", &svs)
	hv, _ := base64.StdEncoding.DecodeString(svs[0].Headers[0].Value)
	if v, err := ownBox.Open(hv); err != nil || string(v) != srcMCPToken {
		t.Fatalf("mcp value %v", err)
	}

	// the vault: the same ciphertext and key material, no PINs or agent catalog
	v, err := h.srv.credVault.Get()
	if err != nil || !v.Exists {
		t.Fatalf("vault %v %+v", err, v)
	}
	if v.Header.KeyMaterial != src.vaultView.Header.KeyMaterial || len(v.Items) != 1 || v.Items[0].Blob != src.vaultView.Items[0].Blob || v.Items[0].ID != src.vaultView.Items[0].ID {
		t.Fatalf("vault copy differs: %+v", v)
	}
	// a second copy does not replace the vault this instance has now
	st, body = h.do("POST", "/api/instances/import", map[string]any{"from": "work", "password": srcPassword, "vault": true}, nil)
	if st != 200 || body["copied"].(map[string]any)["vault"] != false || !strings.Contains(strings.Join(anyStrings(body["notes"]), "\n"), "has one already") {
		t.Fatalf("second vault copy: %d %v", st, body)
	}
	for _, needle := range [][]byte{[]byte(srcProvKey), []byte(srcSSHPass), []byte("MCPTOKEN-marker")} {
		if hits := grepDir(h.srv.st.Dir, needle); len(hits) > 0 {
			t.Fatalf("%q in clear in the target: %v", needle, hits)
		}
	}
}

func anyStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestRemapImportedSettings(t *testing.T) {
	src := settings.Defaults()
	src.Runner.Provider = "ap_gone1"
	src.Lead.Runner = "codex"
	src.Lead.Providers = map[string]string{"claude": "ap_kept1", "codex": "ap_gone1"}
	src.Agents.Paths = map[string]map[string]string{"local": {"claude": "/opt/claude"}, "m_other00001": {"codex": "/usr/bin/codex"}}
	dst := settings.Defaults()
	dst.Previews.PortRange = [2]int{12520, 12539}
	next, notes := remapImportedSettings(src, dst, map[string]string{"ap_kept1": "ap_new01"}, map[string]bool{"local": true})
	if next.Runner.Provider != "" || next.Lead.Runner != "codex" || next.Lead.Providers["claude"] != "ap_new01" || next.Lead.Providers["codex"] != "" {
		t.Fatalf("remap %+v %+v", next.Runner, next.Lead)
	}
	if next.Previews.PortRange != dst.Previews.PortRange || next.Agents.Paths["local"]["claude"] != "/opt/claude" || next.Agents.Paths["m_other00001"] != nil {
		t.Fatalf("%v %v", next.Previews.PortRange, next.Agents.Paths)
	}
	if len(notes) != 2 {
		t.Fatalf("notes %v", notes)
	}
}
