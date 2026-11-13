package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// The parts of a project to an agent (ROADMAP #49): its own part, where it works as before, and
// the others, which the hub's file tools reach with a `part` argument. The others are read-only
// unless the project's partAccess is "full"; a part marked read-only is read-only for everyone.

// partTarget is one other part of the console's project.
type partTarget struct {
	id, name, machineID, cwd string
	readOnly                 bool
}

// withParts fills in a console's parts: whether its own part is read-only, and the others.
func (s *Server) withParts(t *remoteTarget, sp session.Spec) {
	pr, ok := s.projects.Get(sp.ProjectID)
	if !ok {
		return
	}
	home, ok := pr.Part(sp.PartID)
	if !ok {
		home, _ = pr.Part("")
	}
	t.homePart = home.Name
	if home.ReadOnly {
		t.readOnly = true
	}
	for _, pt := range pr.Parts {
		if pt.ID == home.ID {
			continue
		}
		t.parts = append(t.parts, partTarget{id: pt.ID, name: pt.Name, machineID: pt.MachineID, cwd: pt.Path,
			readOnly: pt.ReadOnly || pr.PartAccess != project.PartAccessFull})
	}
}

// writable reports whether any write tool can do something for this token.
func (t remoteTarget) writable() bool {
	if t.machineID != "" && !t.readOnly {
		return true
	}
	for _, p := range t.parts {
		if !p.readOnly {
			return true
		}
	}
	return false
}

// partNames is "Frontend (read only), Gateway", for tool descriptions and answers.
func (t remoteTarget) partNames() string {
	names := make([]string, 0, len(t.parts))
	for _, p := range t.parts {
		n := p.name
		if p.readOnly {
			n += " (read only)"
		}
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

// withPartArg is a file tool with the optional `part` argument.
func (t remoteTarget) withPartArg(tool map[string]any) map[string]any {
	out := maps.Clone(tool)
	schema, _ := tool["inputSchema"].(map[string]any)
	schema = maps.Clone(schema)
	props, _ := schema["properties"].(map[string]any)
	props = maps.Clone(props)
	own := "your own part"
	if t.homePart != "" {
		own = fmt.Sprintf("your own part, %s", t.homePart)
	}
	props["part"] = map[string]any{"type": "string",
		"description": fmt.Sprintf("Another part of the project to work in: %s. Paths are then relative to its folder. Leave it out for %s.", t.partNames(), own)}
	schema["properties"] = props
	out["inputSchema"] = schema
	return out
}

// forPart is the target of one call: the part its `part` argument names, else the console's own.
// The answer is for the agent when the part is not one of the project's.
func (t remoteTarget) forPart(args json.RawMessage) (remoteTarget, string) {
	var a struct {
		Part string `json:"part"`
	}
	_ = json.Unmarshal(args, &a)
	name := strings.TrimSpace(a.Part)
	if name == "" || strings.EqualFold(name, t.homePart) {
		return t, ""
	}
	for _, p := range t.parts {
		if strings.EqualFold(p.name, name) || p.id == name {
			c := t
			c.machineID, c.cwd, c.readOnly = p.machineID, p.cwd, p.readOnly
			c.inPart = p.name
			return c, ""
		}
	}
	if len(t.parts) == 0 {
		return t, "This project has one part: leave part out."
	}
	return t, fmt.Sprintf("The project has no part %q. Its other parts: %s.", name, t.partNames())
}

// partsPrompt tells an agent the parts of its project, "" for a project in one part that is not
// read-only.
func (s *Server) partsPrompt(sp session.Spec) string {
	pr, ok := s.projects.Get(sp.ProjectID)
	if !ok {
		return ""
	}
	home, ok := pr.Part(sp.PartID)
	if !ok {
		home, _ = pr.Part("")
	}
	if len(pr.Parts) < 2 && !home.ReadOnly {
		return ""
	}
	where := func(pt project.Part) string {
		name := pt.MachineID
		if h, ok := s.machines.Host(pt.MachineID); ok {
			name = h.Info().Name
		}
		if pt.MachineID == machine.LocalID {
			name = "This PC"
		}
		return fmt.Sprintf("%s, %s", name, pt.Path)
	}
	var b strings.Builder
	if len(pr.Parts) > 1 {
		b.WriteString("This project spans several parts, each a folder on a machine:\n")
	}
	for _, pt := range pr.Parts {
		if len(pr.Parts) < 2 {
			break
		}
		access := "read only for you"
		switch {
		case pt.ID == home.ID && !pt.ReadOnly:
			access = "yours, where you work"
		case pt.ID == home.ID:
			access = "yours, read only"
		case !pt.ReadOnly && pr.PartAccess == project.PartAccessFull:
			access = "you may change it too"
		}
		fmt.Fprintf(&b, "- %s: %s (%s)\n", pt.Name, where(pt), access)
	}
	if len(pr.Parts) > 1 {
		b.WriteString("Reach another part with the nextboard tools read_file, grep and glob")
		if pr.PartAccess == project.PartAccessFull {
			b.WriteString(" (and write_file, edit_file and bash where you may change it)")
		}
		b.WriteString(", giving part: \"<name>\". Paths there start in that part's folder. Your own part needs no part argument.\n")
	}
	if home.ReadOnly {
		b.WriteString("Your own part is read only: you can read and search it with read_file, grep and glob, but change nothing there and run no commands. Say what should change instead.\n")
	}
	return b.String()
}
