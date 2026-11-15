package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// The parts of a project to an agent (ROADMAP #49): its own part, where it works as before, and
// the others, which the hub's file tools reach with a `part` argument. The others on its own machine
// it may change too; those on other machines are read-only unless the project's partAccess is
// "full". A part marked read-only is read-only for everyone.

// blockedAnswer refuses a command blocked in the part the call works in: the hub's list, the
// project's, and that part's own (memguard.go's words).
func (s *Server) blockedAnswer(t remoteTarget, command string) string {
	if s.orch == nil {
		return ""
	}
	part := t.homePartID
	if t.inPartID != "" {
		part = t.inPartID
	}
	return s.orch.BlockedAnswerFor(agent.CommandBlocked(s.blockedIn(t.projectID, part), command))
}

// partTarget is one other part of the console's project.
type partTarget struct {
	id, name, machineID, cwd string
	readOnly                 bool
	reviewKey                string
	// own: a root console changes this part with its own tools, through its link at the root
	// (a part on This PC that is not read only); the hub's write tools refuse it.
	own bool
}

// review is the review the call's changes go to: its part's; "" for a console in no part.
func (t remoteTarget) review() string { return t.reviewKey }

// withParts fills in a console's parts: whether its own part is read-only, and the others. A
// root console (sp.Root) has no own part: every part is another, seen from This PC. A console
// in no part's folder (partId "", not root) has none either, and sees every part from its own
// machine.
func (s *Server) withParts(t *remoteTarget, sp session.Spec) {
	pr, ok := s.projects.Get(sp.ProjectID)
	if !ok {
		return
	}
	home, ok := pr.PartByID(sp.PartID)
	if !sp.Root && !ok && len(pr.Parts) < 2 {
		return // a project of one part: as before parts, nothing more than its own folder
	}
	if sp.Root || !ok {
		home = project.Part{MachineID: sp.MachineID}
		if sp.RemoteMachineID != "" {
			home.MachineID = sp.RemoteMachineID
		}
	}
	t.root = sp.Root
	t.homePart, t.homePartID = home.Name, home.ID
	if home.ID != "" {
		t.reviewKey = reviewKey(pr, home.ID)
	}
	if home.ReadOnly {
		t.readOnly = true
	}
	for _, pt := range pr.Parts {
		if home.ID != "" && pt.ID == home.ID {
			continue
		}
		t.parts = append(t.parts, partTarget{id: pt.ID, name: pt.Name, machineID: pt.MachineID, cwd: pt.Path,
			readOnly: !mayChange(pr, home, pt), reviewKey: reviewKey(pr, pt.ID),
			own: sp.Root && pt.MachineID == machine.LocalID && !pt.ReadOnly})
	}
}

// mayChange reports whether an agent at home in one part may change another: never a read-only
// part; one on the same machine always; one on another machine only with full access.
func mayChange(pr project.Project, home, pt project.Part) bool {
	return !pt.ReadOnly && (pt.MachineID == home.MachineID || pr.PartAccess == project.PartAccessFull)
}

// writable reports whether any write tool can do something for this token.
func (t remoteTarget) writable() bool {
	if t.machineID != "" && !t.readOnly {
		return true
	}
	for _, p := range t.parts {
		if !p.readOnly && !p.own {
			return true
		}
	}
	return false
}

// partNames is "Frontend (read only), Gateway", for tool descriptions and answers. At the root,
// a part changed with the agent's own tools says so.
func (t remoteTarget) partNames() string {
	names := make([]string, 0, len(t.parts))
	for _, p := range t.parts {
		n := p.name
		switch {
		case p.own:
			n += " (on This PC: change it with your own tools)"
		case p.readOnly:
			n += " (read only)"
		}
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

// withPartArg is a file tool with the `part` argument: optional in a part, required at the root.
func (t remoteTarget) withPartArg(tool map[string]any) map[string]any {
	out := maps.Clone(tool)
	schema, _ := tool["inputSchema"].(map[string]any)
	schema = maps.Clone(schema)
	props, _ := schema["properties"].(map[string]any)
	props = maps.Clone(props)
	if t.root {
		props["part"] = map[string]any{"type": "string",
			"description": fmt.Sprintf("The part of the project to work in: %s. Required at the project root. Paths are then relative to its folder.", t.partNames())}
		req, _ := schema["required"].([]string)
		schema["required"] = append(slices.Clone(req), "part")
	} else {
		own := "your own part"
		if t.homePart != "" {
			own = fmt.Sprintf("your own part, %s", t.homePart)
		}
		props["part"] = map[string]any{"type": "string",
			"description": fmt.Sprintf("Another part of the project to work in: %s. Paths are then relative to its folder. Leave it out for %s.", t.partNames(), own)}
	}
	schema["properties"] = props
	out["inputSchema"] = schema
	return out
}

// forPart is the target of one call: the part its `part` argument names, else the console's own.
// The answer is for the agent when the part is not one of the project's, or, at the root, when
// it names none.
func (t remoteTarget) forPart(args json.RawMessage) (remoteTarget, string) {
	var a struct {
		Part string `json:"part"`
	}
	_ = json.Unmarshal(args, &a)
	name := strings.TrimSpace(a.Part)
	if t.root && name == "" {
		return t, fmt.Sprintf("At the project root, name the part: %s.", t.partNames())
	}
	if name == "" || (t.homePart != "" && strings.EqualFold(name, t.homePart)) {
		return t, ""
	}
	for _, p := range t.parts {
		if strings.EqualFold(p.name, name) || p.id == name {
			c := t
			c.machineID, c.cwd, c.readOnly, c.own = p.machineID, p.cwd, p.readOnly, p.own
			c.inPart, c.inPartID, c.reviewKey = p.name, p.id, p.reviewKey
			return c, ""
		}
	}
	if len(t.parts) == 0 {
		return t, "This project has one part: leave part out."
	}
	if t.root {
		return t, fmt.Sprintf("The project has no part %q. Its parts: %s.", name, t.partNames())
	}
	return t, fmt.Sprintf("The project has no part %q. Its other parts: %s.", name, t.partNames())
}

// partsPrompt tells an agent the parts of its project, "" for a project in one part that is not
// read-only. A root console gets a pointer to its root's instruction file instead (rootPrompt). A
// console in no part's folder has no part of its own: every part is another one.
func (s *Server) partsPrompt(sp session.Spec) string {
	pr, ok := s.projects.Get(sp.ProjectID)
	if !ok {
		return ""
	}
	if sp.Root {
		a, ok := s.agents.Get(sp.Agent)
		return s.rootPrompt(pr, ok && a.CapBase() == "claude")
	}
	home, ok := pr.PartByID(sp.PartID)
	if !ok {
		home = project.Part{MachineID: sp.MachineID}
		if sp.RemoteMachineID != "" {
			home.MachineID = sp.RemoteMachineID
		}
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
	changes := false
	for _, pt := range pr.Parts {
		if len(pr.Parts) < 2 {
			break
		}
		access := "read only for you"
		switch {
		case home.ID == "":
			if mayChange(pr, home, pt) {
				access = "you may change it"
				changes = true
			}
		case pt.ID == home.ID && !pt.ReadOnly:
			access = "yours, where you work"
		case pt.ID == home.ID:
			access = "yours, read only"
		case mayChange(pr, home, pt):
			access = "you may change it too"
			changes = true
		}
		fmt.Fprintf(&b, "- %s: %s (%s)\n", pt.Name, where(pt), access)
	}
	if len(pr.Parts) > 1 {
		b.WriteString("Reach another part with the nextboard tools read_file, grep and glob")
		if changes {
			b.WriteString(" (and write_file, edit_file and bash where you may change it)")
		}
		b.WriteString(", giving part: \"<name>\". Paths there start in that part's folder.")
		if home.ID != "" {
			b.WriteString(" Your own part needs no part argument.")
		}
		b.WriteString("\n")
	}
	if home.ReadOnly {
		b.WriteString("Your own part is read only: you can read and search it with read_file, grep and glob, but change nothing there and run no commands. Say what should change instead.\n")
	}
	return b.String()
}
