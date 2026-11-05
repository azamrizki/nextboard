package orch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"nextboard/internal/agentstate"
	"nextboard/internal/auth"
	"nextboard/internal/board"
	"nextboard/internal/delivery"
	"nextboard/internal/jobs"
	"nextboard/internal/session"
)

// execute runs steps in order; a failing step is recorded and later
// independent steps still run (§3.8).
func (s *Service) execute(c *execCtx, steps []Step) {
	for _, st := range steps {
		if err := st.Validate(); err != nil {
			s.say(c, ThreadMessage{Kind: "error", Text: err.Error()})
			continue
		}
		if err := s.runStep(c, st); err != nil {
			s.say(c, ThreadMessage{Kind: "error", Text: st.Op + ": " + err.Error()})
		}
	}
}

func (s *Service) runStep(c *execCtx, st Step) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	switch st.Op {
	case "send":
		mode := st.Mode
		if mode == "" {
			mode = "verbatim"
		}
		if st.Task != nil {
			o := s.sendOrder(c, st.Targets, st.Text, sourceTask, 0)
			s.linkTask(firstNonEmpty(st.WorkspaceID, c.workspaceID), st.Tile, st.Task.Card, o)
			return nil
		}
		s.sendOrder(c, st.Targets, st.Text, "order", 0)
	case "task.add", "task.update", "task.delete":
		return s.applyTaskEdit(c, st)
	case "spawn", "group.add":
		req := SpawnRequest{WorkspaceID: firstNonEmpty(st.WorkspaceID, c.workspaceID), Count: st.Count, Agent: st.Agent, Role: st.Role,
			OrchestratorID: st.OrchestratorID, MachineID: st.MachineID}
		if st.NewGroup != nil {
			req.Group = &SpawnGroup{Name: st.NewGroup.Name, Role: st.NewGroup.Role}
		}
		if st.Op == "group.add" {
			req.Group = &SpawnGroup{ID: st.GroupID}
		}
		res, err := s.Spawn(req)
		if err != nil {
			return err
		}
		var hs []string
		for _, in := range res.Sessions {
			hs = append(hs, "@"+in.Handle)
		}
		s.note(c, "Started "+joinAnd(hs)+".")
	case "group.create":
		wid, err := s.boardOfMembers(st.Members, firstNonEmpty(st.WorkspaceID, c.workspaceID))
		if err != nil {
			return err
		}
		g, err := s.CreateGroup(wid, st.Members, st.Name, st.Role)
		if err != nil {
			return err
		}
		s.note(c, "Created group "+g+".")
	case "group.dissolve":
		wid := firstNonEmpty(st.WorkspaceID, c.workspaceID)
		b, err := s.hub.Board(wid)
		if err != nil {
			return err
		}
		name := ""
		for _, n := range b.Nodes {
			if n.ID == st.GroupID {
				name = GroupOf(n).Name
			}
		}
		if err := s.hub.ApplyOps(wid, []board.Op{{Op: "del", ID: st.GroupID}}); err != nil {
			return err
		}
		s.note(c, "Dissolved group "+name+".")
	case "pause", "resume", "restart", "kill":
		var ok, failed []string
		for _, sid := range st.Targets {
			in, found := s.hub.SessionInfo(sid)
			if !found {
				continue
			}
			var err error
			switch st.Op {
			case "pause":
				if !isAgent(in) {
					continue
				}
				err = s.hub.Pause(ctx, sid)
			case "resume":
				err = s.hub.Resume(ctx, sid)
			case "restart":
				err = s.hub.Restart(sid)
			case "kill":
				err = s.hub.Kill(sid)
			}
			if err != nil {
				failed = append(failed, "@"+in.Handle+" ("+err.Error()+")")
				continue
			}
			ok = append(ok, "@"+in.Handle)
		}
		verb := map[string]string{"pause": "Paused", "resume": "Resumed", "restart": "Restarted", "kill": "Stopped"}[st.Op]
		msg := ""
		if len(ok) > 0 {
			msg = verb + " " + joinAnd(ok) + "."
		}
		if len(failed) > 0 {
			msg = strings.TrimSpace(msg + " Could not " + st.Op + " " + strings.Join(failed, ", ") + ".")
		}
		if msg != "" {
			s.note(c, msg)
		}
	case "handoff":
		h, err := s.StartHandoff(st.Source, HandoffRequest{Agent: st.Agent, MachineID: st.MachineID, ThreadID: c.threadID})
		if err != nil {
			return err
		}
		s.note(c, fmt.Sprintf("Handing off @%s to %s…", h.sourceHandle, st.Agent))
	case "forward":
		f, err := s.CreateForward(c, st.Sources, st.Targets, st.Mode)
		if err != nil {
			return err
		}
		s.note(c, fmt.Sprintf("When %s finish, I'll forward the results to %s.", s.handlesOf(f.Sources), s.handlesOf(f.Targets)))
	case "cancel":
		return s.cancelLast(c, st.OrderID)
	case "ask":
		s.ask(c, st.Query)
	case "ui":
		u := *st.UIAction
		c.ui = append(c.ui, u)
	case "node.add":
		return s.addNode(c, st)
	case "node.update", "node.delete", "connect", "disconnect", "console.rename":
		return s.execBoardStep(c, st)
	case "wire":
		var ops []board.Op
		b, err := s.hub.Board(c.workspaceID)
		if err != nil {
			return err
		}
		for _, t := range st.Targets {
			if hasEdge(b, EdgeWire, st.OrchestratorID, t) {
				continue
			}
			if i := slices.IndexFunc(b.Nodes, func(n *board.Node) bool { return n.ID == t }); i >= 0 {
				if problem := s.noAgentIn(b, b.Nodes[i]); problem != "" {
					s.note(c, "Not connected: "+problem+". Start one there first.")
					continue
				}
			}
			ops = append(ops, board.Op{Op: "putEdge", Edge: &board.Edge{ID: auth.RandomID("e_"), Kind: EdgeWire, Source: st.OrchestratorID, Target: t, Data: json.RawMessage(`{}`)}})
		}
		if len(ops) > 0 {
			if err := s.hub.ApplyOps(c.workspaceID, ops); err != nil {
				return err
			}
		}
		s.note(c, fmt.Sprintf("Connected %d tile(s) to the orchestrator.", len(ops)))
	case "workspace.go":
		c.ui = append(c.ui, UIAction{Action: "goWorkspace", WorkspaceID: st.WorkspaceID})
	case "workspace.create":
		w, err := s.hub.CreateWorkspace(c.projectID, st.Name)
		if err != nil {
			return err
		}
		c.ui = append(c.ui, UIAction{Action: "goWorkspace", WorkspaceID: w.ID})
		s.note(c, "Created workspace "+w.Name+".")
	case "workspace.rename":
		w, err := s.hub.RenameWorkspace(st.WorkspaceID, st.Name)
		if err != nil {
			return err
		}
		s.note(c, "Renamed this workspace to "+w.Name+".")
	case "workspace.close":
		if err := s.hub.CloseWorkspace(st.WorkspaceID); err != nil {
			return err
		}
		s.note(c, "Closed the workspace.")
	case "loop.create":
		l, err := s.Loops.Create(*st.Loop, s.Now(), c.planned)
		if err != nil {
			return err
		}
		s.note(c, fmt.Sprintf("Loop \"%s\" started (every %s).", l.Name, everyText(l.Trigger.EverySec)))
		c.ui = append(c.ui, UIAction{Action: "openPanel", Panel: "loops"})
	case "git":
		return s.gitStep(c, st)
	case "memory.push":
		wid := c.workspaceID
		if c.cardID == "" {
			wid = "" // the main bar reaches every board of the project
		}
		res, err := s.PushMemory(c.projectID, wid, "", st.Targets, []string{"project", "global"})
		if err != nil {
			return err
		}
		s.note(c, fmt.Sprintf("Memory pushed: %d delivered, %d busy, %d skipped, %d not agents.", res.Delivered, res.Busy, res.Skipped, res.NotAgent))
	case "memory.scan":
		j, err := s.ScanMemory(c.projectID, "balanced", "")
		if err != nil {
			return err
		}
		s.note(c, "Scanning the project for memory (job "+j.ID+"). Review the suggestions in the Memory panel.")
		c.ui = append(c.ui, UIAction{Action: "openPanel", Panel: "memory"})
	case "layout.deploy":
		res, err := s.Deploy(c.workspaceID, DeployRequest{LayoutID: st.LayoutID})
		if err != nil {
			return err
		}
		s.note(c, fmt.Sprintf("Deployed the layout: %d console(s).", len(res.Sessions)))
	case "reply":
		s.say(c, ThreadMessage{Kind: "reply", Text: st.Text})
	}
	return nil
}

// boardOfMembers is the one board a new group's consoles are on.
func (s *Service) boardOfMembers(members []string, def string) (string, error) {
	wid := ""
	for _, sid := range members {
		in, ok := s.hub.SessionInfo(sid)
		if !ok || in.WorkspaceID == "" {
			continue
		}
		if wid != "" && wid != in.WorkspaceID {
			return "", ErrMixedBoards
		}
		wid = in.WorkspaceID
	}
	return firstNonEmpty(wid, def), nil
}

func hasEdge(b board.Board, kind, src, dst string) bool {
	for _, e := range b.Edges {
		if e.Kind == kind && e.Source == src && e.Target == dst {
			return true
		}
	}
	return false
}

func (s *Service) handlesOf(sids []string) string {
	var hs []string
	for _, sid := range sids {
		if in, ok := s.hub.SessionInfo(sid); ok {
			hs = append(hs, "@"+in.Handle)
		}
	}
	return joinAnd(hs)
}

// sendOrder creates an order and one delivery per target.
func (s *Service) sendOrder(c *execCtx, targets []string, text, source string, depth int) Order {
	now := s.Now()
	o := Order{ID: auth.RandomID("o_"), ThreadID: c.threadID, ProjectID: c.projectID, CommandID: c.commandID,
		Text: text, Source: source, CreatedAt: now.UTC()}
	var plan []PlanNote
	for _, sid := range uniq(targets) {
		in, _ := s.hub.SessionInfo(sid)
		t := OrderTarget{SessionID: sid, Handle: in.Handle, State: TQueued, Depth: depth}
		d, err := s.hub.Deliver(sid, delivery.Request{Text: text, Source: source, OrderID: o.ID, ForwardDepth: depth})
		if err != nil {
			t.State, t.Error = TFailed, deliveryProblem(err)
			fin := now.UTC()
			t.FinishedAt = &fin
		} else {
			t.DeliveryID = d.ID
			plan = append(plan, PlanNote{Handle: in.Handle, Text: withoutReply(text, c.reply)})
		}
		o.Targets = append(o.Targets, t)
	}
	s.Orders.Put(o)
	for _, t := range o.Targets {
		if d, ok := s.hub.Delivery(t.DeliveryID); ok && t.DeliveryID != "" {
			s.OnDelivery(d)
		}
	}
	o, _ = s.Orders.Get(o.ID)
	s.hub.Publish(map[string]any{"type": "order", "order": o})
	s.syncTasks(o)
	if len(plan) > 0 {
		s.say(c, ThreadMessage{Kind: "plan", Text: "Sent to " + joinAnd(planHandles(plan)) + ".", OrderID: o.ID, Plan: plan})
	}
	for _, t := range o.Targets {
		if t.State == TFailed {
			s.say(c, ThreadMessage{Kind: "error", OrderID: o.ID, SessionID: t.SessionID, Handle: t.Handle, Text: "@" + t.Handle + ": " + t.Error})
		}
	}
	if c.orderID == "" {
		c.orderID = o.ID
	}
	s.attachForwards(o)
	s.Orders.Save(now)
	return o
}

// deliveryProblem says in the UI's language why a delivery was refused.
func deliveryProblem(err error) string {
	switch {
	case errors.Is(err, delivery.ErrNotAgent):
		return noAgentText
	case errors.Is(err, delivery.ErrTargetBusy):
		return "the agent cannot take text right now (starting, waiting for you, or not in the foreground)."
	case errors.Is(err, delivery.ErrQueueFull):
		return "too many messages are already waiting for this console."
	case errors.Is(err, delivery.ErrTextTooLong):
		return "the text is over 32,000 characters."
	case errors.Is(err, delivery.ErrNotFound):
		return "the console is gone."
	}
	return err.Error()
}

// deliveryFailure is the same for a delivery that failed later, by its code.
func deliveryFailure(d delivery.Delivery) string {
	switch d.Code {
	case "not_agent":
		return noAgentText
	case "agent_exited":
		return "the agent exited before the text was typed."
	case "session_gone":
		return "the console has ended."
	}
	return d.Code + ": " + d.Error
}

// noAgentText is why NextBoard does not type into a console.
const noAgentText = "no agent is running in this console, and NextBoard never types into a plain shell."

func planHandles(p []PlanNote) []string {
	var out []string
	for _, x := range p {
		out = append(out, "@"+x.Handle)
	}
	return out
}

// CancelOrder is POST /api/orders/{id}/cancel: deliveries not typed yet
// are cancelled; text already pasted stays in the agent's input.
func (s *Service) CancelOrder(id string) (Order, error) {
	o, ok := s.Orders.Get(id)
	if !ok {
		return Order{}, ErrOrderNotFound
	}
	for _, t := range o.Targets {
		if t.DeliveryID != "" && t.State == TQueued {
			if d, err := s.hub.CancelDelivery(t.DeliveryID); err == nil {
				s.OnDelivery(d)
			}
		}
	}
	o, _ = s.Orders.Get(id)
	s.hub.Publish(map[string]any{"type": "order", "order": o})
	return o, nil
}

func (s *Service) cancelLast(c *execCtx, id string) error {
	if id == "" {
		for _, o := range s.Orders.List(func(o Order) bool { return o.ThreadID == c.threadID && o.Active() }) {
			id = o.ID
			break
		}
	}
	if id == "" {
		s.say(c, ThreadMessage{Kind: "reply", Text: "There is nothing to cancel."})
		return nil
	}
	o, err := s.CancelOrder(id)
	if err != nil {
		return err
	}
	n := 0
	for _, t := range o.Targets {
		if t.State == TCancelled {
			n++
		}
	}
	if n == 0 {
		s.note(c, "Too late: the order was already typed into the console(s).")
	} else {
		s.note(c, fmt.Sprintf("Cancelled \"%s\" for %d console(s).", clipText(o.Text, 60), n))
	}
	return nil
}

// ask answers status questions from the hub's own state (§4).
func (s *Service) ask(c *execCtx, q string) {
	sc, err := s.newScope(c.workspaceID, c.cardID)
	if err != nil {
		return
	}
	agents := sc.agentsInScope()
	switch q {
	case "help":
		s.say(c, ThreadMessage{Kind: "reply", Text: "Try: \"@Arkan run the tests\", \"spawn 3 codex reviewers\", \"group Arkan and Bintang\", \"pause everyone\", " +
			"\"when Arkan finishes, pass it to Bintang\", \"hand off to codex\", \"every 15 minutes run the tests\", \"push memory\", \"deploy Trio\", \"status\"."})
	case "status", "needsYou":
		counts := map[string]int{}
		var needs []string
		for _, in := range agents {
			st := in.AgentState
			if in.Paused {
				st = "paused"
			}
			counts[st]++
			if in.AgentState == agentstate.NeedsYou {
				who := "@" + in.Handle
				if in.Activity != "" {
					who += " (" + in.Activity + ")"
				}
				needs = append(needs, who)
			}
		}
		if q == "needsYou" {
			if len(needs) == 0 {
				s.say(c, ThreadMessage{Kind: "reply", Text: "Nobody needs you right now."})
			} else {
				s.say(c, ThreadMessage{Kind: "reply", Text: "Waiting for you: " + strings.Join(needs, ", ") + "."})
			}
			return
		}
		var parts []string
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
		}
		txt := fmt.Sprintf("%d agent(s): %s.", len(agents), strings.Join(parts, ", "))
		if len(agents) == 0 {
			txt = "No agents here yet. Say \"spawn 2 agents\"."
		}
		if len(needs) > 0 {
			txt += " Needs you: " + strings.Join(needs, ", ") + "."
		}
		txt += sc.perBoard(agents)
		s.say(c, ThreadMessage{Kind: "reply", Text: txt})
	case "finished", "summary":
		orders := s.Orders.List(func(o Order) bool { return o.ThreadID == c.threadID })
		if len(orders) == 0 {
			s.say(c, ThreadMessage{Kind: "reply", Text: "No orders in this thread yet."})
			return
		}
		o := orders[0]
		var lines []string
		for _, t := range o.Targets {
			line := "@" + t.Handle + ": " + t.State
			if t.State == TFinished {
				if r, ok := s.hub.Reply(t.SessionID); ok {
					line += " — " + clipText(r.Text, 160)
				}
			}
			if t.Error != "" {
				line += " (" + t.Error + ")"
			}
			lines = append(lines, line)
		}
		head := "Last order \"" + clipText(o.Text, 80) + "\":\n"
		if q == "summary" {
			if ok, _ := s.PlannerAvailable(""); ok {
				s.summarise(c, head+strings.Join(lines, "\n"))
				return
			}
		}
		s.say(c, ThreadMessage{Kind: "reply", Text: head + strings.Join(lines, "\n")})
	}
}

// perBoard is " Main: 2, Workspace 2: 1." when the agents span several boards.
func (sc *scope) perBoard(agents []session.Info) string {
	counts := map[string]int{}
	for _, in := range agents {
		counts[in.WorkspaceID]++
	}
	if len(counts) < 2 {
		return ""
	}
	var parts []string
	for _, w := range sc.proj.Workspaces {
		if n := counts[w.ID]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", w.Name, n))
		}
	}
	return " " + strings.Join(parts, ", ") + "."
}

// summarise asks the runner for a short summary (≤ 30 s); the raw
// excerpts are the fallback.
func (s *Service) summarise(c *execCtx, raw string) {
	tid := c.threadID
	_, err := s.hub.SubmitRunner(jobs.RunnerRequest{Purpose: "thread-summary", ProjectID: c.projectID, Agent: "claude",
		Command: s.claudeCommand(), Prompt: "Summarise these agent reports for the user in at most 5 short sentences:\n\n" + raw,
		OnFinish: func(j jobs.Job) {
			text := raw
			if j.State == jobs.StateDone {
				var r struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(j.Result, &r) == nil && strings.TrimSpace(r.Text) != "" {
					text = r.Text
				}
			}
			s.Threads.Add(ThreadMessage{ThreadID: tid, Role: "hub", Kind: "reply", Text: text})
		}})
	if err != nil {
		s.say(c, ThreadMessage{Kind: "reply", Text: raw})
		return
	}
	s.note(c, "Summarising…")
}

// claudeCommand is the path override of claude on This PC, if any.
func (s *Service) claudeCommand() string {
	return s.hub.Settings().Agents.Paths["local"]["claude"]
}

// addNode places a tile (near the card in card scope, else right of the
// board's content) and wires it.
func (s *Service) addNode(c *execCtx, st Step) error {
	wid := firstNonEmpty(st.WorkspaceID, c.workspaceID)
	b, err := s.hub.Board(wid)
	if err != nil {
		return err
	}
	w, h := 280.0, 200.0
	data := map[string]any{}
	for k, v := range st.Data {
		data[k] = v
	}
	switch st.Kind {
	case "checklist":
		w, h = 320, 360
		data["title"], data["items"] = "Checklist", []any{}
	case "kanban":
		w, h = 900, 420
		for k, v := range kanbanData("Board", "Backlog", "To do", "In progress", "Done") {
			data[k] = v
		}
	case "calendar":
		w, h = 640, 520
		data["title"], data["month"], data["events"] = "Calendar", s.Now().Format("2006-01"), []any{}
	case "uptime":
		w, h = 1000, 640
		data["title"] = "Uptime"
	case "orchestrator":
		w, h = 420, 520
		n := 0
		for _, x := range b.Nodes {
			if x.Kind == KindOrchestrator {
				n++
			}
		}
		title := "Orchestrator"
		if n > 0 {
			title = fmt.Sprintf("Orchestrator %d", n+1)
		}
		data["title"] = title
	case "web":
		w, h = 1024, 700
		s.webTileData(c, data)
	}
	// the one editor card of a board is brought into view, not doubled
	if st.Kind == KindEditor {
		for _, n := range b.Nodes {
			if n.Kind == KindEditor {
				c.ui = append(c.ui, UIAction{Action: "focus", Target: n.ID})
				return nil
			}
		}
	}
	if dw, dh := addNodeDefaults(st, s.machineOf(c.projectID), data); dw > 0 {
		w, h = dw, dh
	}
	// what the plan says wins over each kind's defaults above
	for k, v := range st.Data {
		data[k] = v
	}
	x, y := rightOf(b.Nodes)
	if c.cardID != "" {
		for _, n := range b.Nodes {
			if n.ID == c.cardID {
				x, y = n.X+n.W+40, n.Y
			}
		}
	}
	if g := st.Geom; g != nil {
		for _, f := range []struct {
			dst *float64
			v   *float64
		}{{&x, g.X}, {&y, g.Y}, {&w, g.W}, {&h, g.H}} {
			if f.v != nil {
				*f.dst = *f.v
			}
		}
	}
	raw, _ := json.Marshal(data)
	id := auth.RandomID("n_")
	ops := []board.Op{{Op: "put", Node: &board.Node{ID: id, Kind: st.Kind, X: x, Y: y, W: w, H: h, Data: raw}}}
	for _, t := range st.WireTo {
		ops = append(ops, board.Op{Op: "putEdge", Edge: &board.Edge{ID: auth.RandomID("e_"), Kind: EdgeWire, Source: id, Target: t, Data: json.RawMessage(`{}`)}})
	}
	if err := s.hub.ApplyOps(wid, ops); err != nil {
		return err
	}
	c.ui = append(c.ui, UIAction{Action: "focus", Target: id})
	s.note(c, "Added a "+st.Kind+".")
	return nil
}

// rightOf is the auto position right of every node.
func rightOf(nodes []*board.Node) (float64, float64) {
	if len(nodes) == 0 {
		return 0, 0
	}
	maxX, minY := -1e9, 1e9
	for _, n := range nodes {
		maxX, minY = max(maxX, n.X+n.W), min(minY, n.Y)
	}
	return min(maxX+80, board.MaxCoord-2000), max(minY, -board.MaxCoord)
}

var _ = session.StatusRunning

// machineOf is the machine of a project ("" when it is gone).
func (s *Service) machineOf(projectID string) string {
	p, ok := s.hub.Project(projectID)
	if !ok {
		return ""
	}
	return p.MachineID
}
