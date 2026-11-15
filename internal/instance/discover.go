package instance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/azamrizki/nextboard/internal/config"
)

// Service is the unit (Linux) or LaunchAgent (macOS) that runs an instance.
type Service struct {
	Kind     string `json:"kind"` // "systemd" or "launchd"
	Name     string `json:"name"` // nextboard-work.service, id.nextboard.work
	UnitPath string `json:"unitPath"`
	// Bin is the program the unit starts (its ExecStart, or ProgramArguments[0]).
	Bin string `json:"bin"`
}

// Instance is one NextBoard instance found on this machine.
type Instance struct {
	Name  string `json:"name"`
	HubID string `json:"hubId,omitempty"`
	// DataDir is absolute; ConfigFile is its config.json.
	DataDir    string `json:"dataDir"`
	ConfigFile string `json:"configFile"`
	// Configured: the data folder holds a config.json (a unit can name a folder that does not).
	Configured   bool   `json:"configured"`
	Listen       string `json:"listen"`
	StatusListen string `json:"statusListen,omitempty"`
	// PreviewRange is settings.json previews.portRange, or the hub's default.
	PreviewRange [2]int   `json:"previewRange"`
	Service      *Service `json:"service,omitempty"`
	// Running: a hub holds the folder's hub.lock; PID is the one it wrote there.
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
	// Problems: duplicate names, ports another instance also uses, a unit and a config that
	// disagree, a config that does not read.
	Problems []string `json:"problems,omitempty"`
}

// Ports are the ports the instance claims: its hub, its status listener, its previews.
func (i Instance) Ports() []int {
	var out []int
	for _, l := range []string{i.Listen, i.StatusListen} {
		if p := portOf(l); p > 0 && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if r := i.PreviewRange; r[0] > 0 && r[1] >= r[0] && r[1]-r[0] < 1000 {
		for p := r[0]; p <= r[1]; p++ {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

func portOf(listen string) int {
	if listen == "" {
		return 0
	}
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return 0
	}
	return n
}

// List finds the instances on this machine, without writing anything: the services
// `nextboard install` set up (~/.config/systemd/user/nextboard[-<name>].service, or
// ~/Library/LaunchAgents/id.nextboard[.<name>].plist) and the folders ~/.nextboard and
// ~/.nextboard-* that hold a config.json, merged by data folder. The default instance comes
// first, then by name.
func List() ([]Instance, error) {
	h, err := home()
	if err != nil {
		return nil, err
	}
	return list(h, runtime.GOOS)
}

// unit is a service found on disk, with the data folder it serves.
type unit struct {
	name    string // instance name from the unit's file name
	svc     Service
	dataDir string
}

func list(home, goos string) ([]Instance, error) {
	byDir := map[string]*Instance{}
	units := map[string][]unit{}
	var order []string
	add := func(dir string) *Instance {
		key := canonical(dir)
		if in, ok := byDir[key]; ok {
			return in
		}
		in := &Instance{DataDir: key}
		byDir[key] = in
		order = append(order, key)
		return in
	}
	for _, u := range listUnits(home, goos) {
		add(u.dataDir)
		key := canonical(u.dataDir)
		units[key] = append(units[key], u)
	}
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		n := e.Name()
		if n != ".nextboard" && !strings.HasPrefix(n, ".nextboard-") {
			continue
		}
		dir := filepath.Join(home, n)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, config.File)); err != nil || fi.IsDir() {
			continue
		}
		add(dir)
	}

	out := make([]Instance, 0, len(order))
	for _, key := range order {
		in := byDir[key]
		fill(in, units[key])
		out = append(out, *in)
	}
	markProblems(out)
	sort.SliceStable(out, func(a, b int) bool {
		da, db := out[a].Name == Default, out[b].Name == Default
		if da != db {
			return da
		}
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		return out[a].DataDir < out[b].DataDir
	})
	return out, nil
}

// canonical is the absolute folder with links resolved, when it exists.
func canonical(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// fill reads what the folder's config.json and settings.json say, and its lock.
func fill(in *Instance, us []unit) {
	in.ConfigFile = filepath.Join(in.DataDir, config.File)
	in.Listen = config.DefaultListen
	m, exists, err := readConfig(in.DataDir)
	in.Configured = exists
	var id Identity
	if err == nil {
		id, err = identityOf(in.DataDir, m, exists)
	}
	if err != nil {
		in.Problems = append(in.Problems, err.Error())
		id = Identity{Name: LegacyName(in.DataDir)}
	}
	in.HubID = id.HubID
	if l, _ := stringField(m, "listen", in.ConfigFile); l != "" {
		in.Listen = l
	}
	in.StatusListen, _ = stringField(m, "statusListen", in.ConfigFile)
	in.PreviewRange = previewRange(in.DataDir)

	in.Name = id.Name
	if len(us) > 0 {
		// the unit whose name the config records, else the first one
		pick := us[0]
		for _, u := range us {
			if id.Recorded && u.name == id.Name {
				pick = u
				break
			}
		}
		svc := pick.svc
		in.Service = &svc
		if !id.Recorded {
			in.Name = pick.name
		} else if pick.name != id.Name {
			in.Problems = append(in.Problems, fmt.Sprintf("the service %s runs this folder, but its config.json says instance %s", svc.Name, id.Name))
		}
		if len(us) > 1 {
			names := make([]string, len(us))
			for i, u := range us {
				names[i] = u.svc.Name
			}
			in.Problems = append(in.Problems, "several services run this folder: "+strings.Join(names, ", "))
		}
	}
	in.Running, in.PID = Running(in.DataDir)
}

// DefaultPreviewRange is the hub's previews.portRange when settings.json names none.
var DefaultPreviewRange = [2]int{defaultPreviewStart, defaultPreviewStart + PreviewPorts - 1}

func previewRange(dataDir string) [2]int {
	raw, err := os.ReadFile(filepath.Join(dataDir, "settings.json"))
	if err != nil {
		return DefaultPreviewRange
	}
	var s struct {
		Previews struct {
			PortRange [2]int `json:"portRange"`
		} `json:"previews"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return DefaultPreviewRange
	}
	r := s.Previews.PortRange
	if r[0] < 1024 || r[1] > 65535 || r[1] < r[0] {
		return DefaultPreviewRange
	}
	return r
}

func markProblems(ins []Instance) {
	for a := range ins {
		for b := range ins {
			if a == b {
				continue
			}
			if ins[a].Name == ins[b].Name {
				ins[a].Problems = append(ins[a].Problems, fmt.Sprintf("another instance is also named %s: %s", ins[b].Name, ins[b].DataDir))
			}
			if shared := sharedPorts(ins[a].Ports(), ins[b].Ports()); len(shared) > 0 {
				ins[a].Problems = append(ins[a].Problems, fmt.Sprintf("instance %s (%s) also uses %s", ins[b].Name, ins[b].DataDir, portList(shared)))
			}
		}
	}
}

func sharedPorts(a, b []int) []int {
	var out []int
	for _, p := range a {
		if slices.Contains(b, p) {
			out = append(out, p)
		}
	}
	return out
}

func portList(ps []int) string {
	s := make([]string, 0, 4)
	for i, p := range ps {
		if i == 3 {
			s = append(s, fmt.Sprintf("and %d more", len(ps)-3))
			break
		}
		s = append(s, strconv.Itoa(p))
	}
	word := "port "
	if len(ps) > 1 {
		word = "ports "
	}
	return word + strings.Join(s, ", ")
}

// listUnits reads the services of this OS's service manager.
func listUnits(home, goos string) []unit {
	var dir, prefix, suffix, def string
	switch goos {
	case "linux":
		dir, def, prefix, suffix = filepath.Join(home, ".config", "systemd", "user"), "nextboard.service", "nextboard-", ".service"
	case "darwin":
		dir, def, prefix, suffix = filepath.Join(home, "Library", "LaunchAgents"), "id.nextboard.plist", "id.nextboard.", ".plist"
	default:
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []unit
	for _, e := range entries {
		fn := e.Name()
		name := ""
		if fn == def {
			name = Default
		} else if x, ok := strings.CutPrefix(fn, prefix); ok {
			if x, ok = strings.CutSuffix(x, suffix); ok && ValidName(x) == nil && x != Default {
				name = x
			}
		}
		if name == "" {
			continue
		}
		path := filepath.Join(dir, fn)
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var args []string
		var env map[string]string
		svc := Service{UnitPath: path}
		if goos == "darwin" {
			args, env = parsePlist(raw)
			svc.Kind, svc.Name = "launchd", LaunchLabel(name)
		} else {
			args, env = parseUnit(raw)
			svc.Kind, svc.Name = "systemd", ServiceName(name)+".service"
		}
		dataDir, ok := servedDir(home, args, env)
		if !ok {
			continue // not `nextboard serve`
		}
		svc.Bin = args[0]
		out = append(out, unit{name: name, svc: svc, dataDir: dataDir})
	}
	return out
}

// servedDir is the data folder `<bin> serve …` uses with the unit's arguments and environment,
// by Resolve's rules (except that --instance does not look at other units).
func servedDir(home string, args []string, env map[string]string) (string, bool) {
	if len(args) < 2 || args[1] != "serve" {
		return "", false
	}
	if h := env["HOME"]; h != "" {
		home = h
	}
	flags := map[string]string{}
	for i := 2; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			continue
		}
		k := strings.TrimLeft(a, "-")
		if k, v, ok := strings.Cut(k, "="); ok {
			flags[k] = v
			continue
		}
		if (k == "data-dir" || k == "instance") && i+1 < len(args) {
			flags[k] = args[i+1]
			i++
		}
	}
	abs := func(d string) string {
		if !filepath.IsAbs(d) {
			d = filepath.Join(home, d)
		}
		return filepath.Clean(d)
	}
	switch {
	case flags["data-dir"] != "":
		return abs(flags["data-dir"]), true
	case ValidName(flags["instance"]) == nil:
		return dirIn(home, flags["instance"]), true
	case env["NEXTBOARD_HOME"] != "":
		return abs(env["NEXTBOARD_HOME"]), true
	case ValidName(env["NEXTBOARD_INSTANCE"]) == nil:
		return dirIn(home, env["NEXTBOARD_INSTANCE"]), true
	}
	return dirIn(home, Default), true
}

// parseUnit reads a systemd unit's ExecStart (the last one) and Environment lines.
func parseUnit(raw []byte) ([]string, map[string]string) {
	var args []string
	env := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "ExecStart="); ok {
			if f := splitWords(rest); len(f) > 0 {
				f[0] = strings.TrimLeft(f[0], "@-:+!")
				args = f
			}
		} else if rest, ok := strings.CutPrefix(line, "Environment="); ok {
			for _, kv := range splitWords(rest) {
				if k, v, ok := strings.Cut(kv, "="); ok {
					env[k] = v
				}
			}
		}
	}
	return args, env
}

// splitWords splits a unit file value into words, honouring "double" and 'single' quotes.
func splitWords(s string) []string {
	var out []string
	var cur strings.Builder
	in, quote := false, rune(0)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// parsePlist reads a LaunchAgent's ProgramArguments and EnvironmentVariables.
func parsePlist(raw []byte) ([]string, map[string]string) {
	d := xml.NewDecoder(bytes.NewReader(raw))
	var top map[string]any
	for top == nil {
		tok, err := d.Token()
		if err != nil {
			return nil, nil
		}
		if st, ok := tok.(xml.StartElement); ok && st.Name.Local == "dict" {
			v, err := plistValue(d, st, 0)
			if err != nil {
				return nil, nil
			}
			top, _ = v.(map[string]any)
			if top == nil {
				return nil, nil
			}
		}
	}
	var args []string
	if list, ok := top["ProgramArguments"].([]any); ok {
		for _, a := range list {
			s, _ := a.(string)
			args = append(args, s)
		}
	}
	env := map[string]string{}
	if m, ok := top["EnvironmentVariables"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				env[k] = s
			}
		}
	}
	return args, env
}

func plistValue(d *xml.Decoder, start xml.StartElement, depth int) (any, error) {
	if depth > 16 {
		return nil, fmt.Errorf("plist nested too deep")
	}
	switch start.Name.Local {
	case "dict", "array":
		m, list, key := map[string]any{}, []any{}, ""
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if start.Name.Local == "dict" && t.Name.Local == "key" {
					if err := d.DecodeElement(&key, &t); err != nil {
						return nil, err
					}
					continue
				}
				v, err := plistValue(d, t, depth+1)
				if err != nil {
					return nil, err
				}
				if start.Name.Local == "dict" {
					m[key] = v
				} else {
					list = append(list, v)
				}
			case xml.EndElement:
				if start.Name.Local == "dict" {
					return m, nil
				}
				return list, nil
			}
		}
	case "string", "integer", "real", "date", "data":
		var s string
		err := d.DecodeElement(&s, &start)
		return s, err
	case "true", "false":
		return start.Name.Local == "true", d.Skip()
	}
	return nil, d.Skip()
}
