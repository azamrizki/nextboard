package orch

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"nextboard/internal/auth"
	"nextboard/internal/board"
	"nextboard/internal/session"
)

// Task boards (docs/API-orchestration.md §3.10): a kanban or checklist tile wired
// from an orchestrator card follows the work of that card's team. An order to one of
// its consoles becomes a task for that console, and the task moves as the order does,
// without a turn or a planner run. With autoRun on, the lead also hands out what waits
// in To do to whichever of its consoles is free.

const (
	sourceTask    = "task"
	taskTickEvery = 5 * time.Second
	maxTaskTitle  = 120
	maxTaskNotes  = 2000
	maxTaskError  = 200
	// plannerTaskCards bounds what the planner reads of the task boards.
	plannerTaskCards = 80
)

// What a task shows of its order.
const (
	taskQueued   = "queued"
	taskWorking  = "working"
	taskNeedsYou = "needsYou"
	taskFinished = "finished"
	taskFailed   = "failed"
)

var taskKinds = map[string]bool{"kanban": true, "checklist": true}

// The columns the engine moves tasks to, found by name in the languages the owner writes.
var (
	todoColumn   = regexp.MustCompile(`(?i)\b(to[ -]?do|next|ready|queued?|antri\w*|rencana)\b`)
	doingColumn  = regexp.MustCompile(`(?i)\b(in[ -]?progress|progress|doing|wip|working|active|ongoing|dikerjakan|proses|berjalan)\b`)
	reviewColumn = regexp.MustCompile(`(?i)\b(review\w*|qa|testing|cek|periksa)\b`)
	doneColumn   = regexp.MustCompile(`(?i)\b(done|complete\w*|finished|shipped|closed|selesai)\b`)
)

func isTaskTile(n *board.Node) bool { return n != nil && taskKinds[n.Kind] }

// TaskEdit is what a planner step changes on a task board.
type TaskEdit struct {
	Card     string `json:"card,omitempty"`
	Title    string `json:"title,omitempty"`
	Notes    string `json:"notes,omitempty"`
	Column   string `json:"column,omitempty"`
	Assignee string `json:"assignee,omitempty"`
	Priority string `json:"priority,omitempty"`
	Due      string `json:"due,omitempty"`
	Done     *bool  `json:"done,omitempty"`
}

// taskTile is a task board and the team behind the cards wired to it.
type taskTile struct {
	node *board.Node
	card string          // the first card wired to it: its thread hears about handed out tasks
	team map[string]bool // sessions those cards direct
	data map[string]any
}

// taskTilesOf lists the task boards of a board that a card is wired to.
func taskTilesOf(b board.Board) []*taskTile {
	byID := map[string]*board.Node{}
	for _, n := range b.Nodes {
		byID[n.ID] = n
	}
	found := map[string]*taskTile{}
	var out []*taskTile
	for _, e := range b.Edges {
		src, dst := byID[e.Source], byID[e.Target]
		if e.Kind != EdgeWire || src == nil || src.Kind != KindOrchestrator || !isTaskTile(dst) {
			continue
		}
		t := found[dst.ID]
		if t == nil {
			t = &taskTile{node: dst, card: src.ID, team: map[string]bool{}, data: map[string]any{}}
			_ = json.Unmarshal(dst.Data, &t.data)
			found[dst.ID] = t
			out = append(out, t)
		}
		for _, w := range b.Edges {
			if w.Kind == EdgeWire && w.Source == src.ID {
				for _, sid := range nodeSessionIDs(b, w.Target) {
					t.team[sid] = true
				}
			}
		}
	}
	return out
}

func taskList(v any) []map[string]any {
	l, _ := v.([]any)
	out := make([]map[string]any, 0, len(l))
	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func taskStr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func columnsOf(d map[string]any) []map[string]any { return taskList(d["columns"]) }

// taskColumns finds where tasks wait, run and end. Named columns first, then their
// usual places: the first, the one after it, the last.
func taskColumns(cols []map[string]any) (todo, doing, finish, done int) {
	find := func(re *regexp.Regexp) int {
		for i, c := range cols {
			if re.MatchString(taskStr(c, "title")) {
				return i
			}
		}
		return -1
	}
	if todo = find(todoColumn); todo < 0 {
		todo = 0
	}
	if done = find(doneColumn); done < 0 {
		done = len(cols) - 1
	}
	if doing = find(doingColumn); doing < 0 {
		doing = todo
		if todo+1 < done {
			doing = todo + 1
		}
	}
	if finish = find(reviewColumn); finish < 0 {
		finish = done
	}
	return todo, doing, finish, done
}

// moveEntry puts a card at the end of column to, wherever it was.
func moveEntry(cols []map[string]any, card map[string]any, to int) {
	if to < 0 || to >= len(cols) {
		return
	}
	for _, c := range cols {
		kept := make([]any, 0, len(taskList(c["cards"])))
		for _, x := range taskList(c["cards"]) {
			if taskStr(x, "id") != taskStr(card, "id") {
				kept = append(kept, x)
			}
		}
		c["cards"] = kept
	}
	cards, _ := cols[to]["cards"].([]any)
	cols[to]["cards"] = append(cards, card)
}

func containsTask(col, card map[string]any) bool {
	for _, x := range taskList(col["cards"]) {
		if taskStr(x, "id") == taskStr(card, "id") {
			return true
		}
	}
	return false
}

// allTasks is every task of a board, with the column a card is in (-1 for a checklist).
func allTasks(kind string, d map[string]any) (out []struct {
	m   map[string]any
	col int
}) {
	type entry = struct {
		m   map[string]any
		col int
	}
	if kind == "checklist" {
		for _, m := range taskList(d["items"]) {
			out = append(out, entry{m, -1})
		}
		return out
	}
	for i, c := range columnsOf(d) {
		for _, m := range taskList(c["cards"]) {
			out = append(out, entry{m, i})
		}
	}
	return out
}

func taskStateOf(state string) string {
	switch state {
	case TQueued:
		return taskQueued
	case TDelivered, TWorking:
		return taskWorking
	case TNeedsYou:
		return taskNeedsYou
	case TFinished:
		return taskFinished
	}
	return taskFailed
}

var leadingHandles = regexp.MustCompile(`^(?:@\S+[,:]?\s+)+`)

// taskTitle is the first line of an order, without whom it addresses.
func taskTitle(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(leadingHandles.ReplaceAllString(strings.TrimSpace(l), "")); l != "" {
			return clipLen(l, maxTaskTitle)
		}
	}
	return "Task"
}

// followTarget brings the task of one console's order up to date. Only a change of
// state moves it, so a card the user moved by hand stays where they put it.
func followTarget(t *taskTile, o Order, target OrderTarget) bool {
	state := taskStateOf(target.State)
	var card map[string]any
	for _, e := range allTasks(t.node.Kind, t.data) {
		if taskStr(e.m, "orderId") == o.ID && taskStr(e.m, "sessionId") == target.SessionID {
			card = e.m
		}
	}
	if card == nil {
		// what was handed out from the board has its card already
		if o.Source == sourceTask {
			return false
		}
		card = map[string]any{"id": auth.RandomID("i_"), "orderId": o.ID, "sessionId": target.SessionID, "assignee": target.Handle}
		if t.node.Kind == "checklist" {
			card["text"], card["done"] = taskTitle(o.Text), false
			items, _ := t.data["items"].([]any)
			t.data["items"] = append(items, card)
		} else {
			card["title"] = taskTitle(o.Text)
			if strings.TrimSpace(o.Text) != card["title"] {
				card["notes"] = clipLen(strings.TrimSpace(o.Text), maxTaskNotes)
			}
			cols := columnsOf(t.data)
			if len(cols) == 0 {
				cols = []map[string]any{{"id": auth.RandomID("i_"), "title": "To do", "cards": []any{}}}
				t.data["columns"] = []any{cols[0]}
			}
			todo, _, _, _ := taskColumns(cols)
			cards, _ := cols[todo]["cards"].([]any)
			cols[todo]["cards"] = append(cards, card)
		}
	}
	if taskStr(card, "state") == state {
		return false
	}
	card["state"] = state
	delete(card, "error")
	if state == taskFailed {
		card["error"] = clipLen(firstNonEmpty(target.Error, target.State), maxTaskError)
	}
	if t.node.Kind == "checklist" {
		if state == taskFinished {
			card["done"] = true
		}
		return true
	}
	cols := columnsOf(t.data)
	todo, doing, finish, done := taskColumns(cols)
	switch state {
	case taskQueued:
		// waiting in its place, unless it comes back from elsewhere
		if !containsTask(cols[todo], card) {
			moveEntry(cols, card, todo)
		}
	case taskWorking, taskNeedsYou:
		moveEntry(cols, card, doing)
	case taskFinished:
		moveEntry(cols, card, finish)
		if finish == done {
			card["done"] = true
		}
	}
	return true
}

func (s *Service) saveTasks(wid string, t *taskTile) {
	set := map[string]any{"items": t.data["items"]}
	if t.node.Kind == "kanban" {
		set = map[string]any{"columns": t.data["columns"]}
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return
	}
	_ = s.hub.ApplyOps(wid, []board.Op{{Op: "patch", ID: t.node.ID, Set: map[string]json.RawMessage{"data": raw}}})
}

// syncTasks follows an order on the task boards of the teams it went to. Forwards and
// other traffic between agents are no work the owner handed out.
func (s *Service) syncTasks(o Order) {
	if o.Source != "order" && o.Source != sourceTask {
		return
	}
	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	byBoard := map[string][]OrderTarget{}
	for _, t := range o.Targets {
		if in, ok := s.hub.SessionInfo(t.SessionID); ok && in.WorkspaceID != "" {
			byBoard[in.WorkspaceID] = append(byBoard[in.WorkspaceID], t)
		}
	}
	for wid, targets := range byBoard {
		b, err := s.hub.Board(wid)
		if err != nil {
			continue
		}
		for _, t := range taskTilesOf(b) {
			changed := false
			for _, target := range targets {
				if t.team[target.SessionID] && followTarget(t, o, target) {
					changed = true
				}
			}
			if changed {
				s.saveTasks(wid, t)
			}
		}
	}
}

// taskWaiting says whether a task can be handed out: it never was, or it came back to
// To do by hand after it ended.
func taskWaiting(e map[string]any, col, todo int) bool {
	if e["done"] == true || (col >= 0 && col != todo) {
		return false
	}
	switch taskStr(e, "state") {
	case "":
		return true
	case taskFinished, taskFailed:
		return col >= 0
	}
	return false
}

// tasksTick hands out waiting tasks of the boards the owner asked to run by themselves.
func (s *Service) tasksTick(now time.Time) {
	if now.Sub(s.tasksLast) < taskTickEvery {
		return
	}
	s.tasksLast = now
	seen := map[string]bool{}
	for _, in := range s.hub.Sessions() {
		if in.WorkspaceID == "" || seen[in.WorkspaceID] {
			continue
		}
		seen[in.WorkspaceID] = true
		b, err := s.hub.Board(in.WorkspaceID)
		if err != nil {
			continue
		}
		for _, t := range taskTilesOf(b) {
			if t.data["autoRun"] == true {
				s.runTasks(in.WorkspaceID, t)
			}
		}
	}
}

func (s *Service) runTasks(wid string, t *taskTile) {
	var free []session.Info
	for sid := range t.team {
		if in, ok := s.hub.SessionInfo(sid); ok && s.free(in) {
			free = append(free, in)
		}
	}
	if len(free) == 0 {
		return
	}
	_, proj, ok := s.hub.Workspace(wid)
	if !ok {
		return
	}
	todo := 0
	if t.node.Kind == "kanban" {
		todo, _, _, _ = taskColumns(columnsOf(t.data))
	}
	c := &execCtx{threadID: t.card, projectID: proj.ID, workspaceID: wid, cardID: t.card}
	for _, e := range allTasks(t.node.Kind, t.data) {
		if len(free) == 0 {
			return
		}
		if !taskWaiting(e.m, e.col, todo) {
			continue
		}
		pick := -1
		want := strings.TrimPrefix(taskStr(e.m, "assignee"), "@")
		for i, in := range free {
			if want == "" || strings.EqualFold(in.Handle, want) {
				pick = i
				break
			}
		}
		if pick < 0 {
			continue
		}
		in := free[pick]
		free = append(free[:pick], free[pick+1:]...)
		title := firstNonEmpty(taskStr(e.m, "title"), taskStr(e.m, "text"))
		text := title
		if notes := taskStr(e.m, "notes"); notes != "" && notes != title {
			text += "\n\n" + notes
		}
		o := s.sendOrder(c, []string{in.ID}, text, sourceTask, 0)
		s.linkTask(wid, t.node.ID, taskStr(e.m, "id"), o)
	}
}

// linkTask makes a card the task of an order that was just sent. The board is read
// again: the order took a moment, and the user may have changed it.
func (s *Service) linkTask(wid, tile, ref string, o Order) {
	if len(o.Targets) == 0 {
		return
	}
	s.tasksMu.Lock()
	b, err := s.hub.Board(wid)
	if err != nil {
		s.tasksMu.Unlock()
		return
	}
	for _, n := range b.Nodes {
		if n.ID != tile || !isTaskTile(n) {
			continue
		}
		t := &taskTile{node: n, data: map[string]any{}}
		_ = json.Unmarshal(n.Data, &t.data)
		if card := findTask(n.Kind, t.data, ref); card != nil {
			card["orderId"], card["sessionId"], card["assignee"] = o.ID, o.Targets[0].SessionID, o.Targets[0].Handle
			delete(card, "state")
			delete(card, "done")
			if n.Kind == "checklist" {
				card["done"] = false
			}
			s.saveTasks(wid, t)
		}
	}
	s.tasksMu.Unlock()
	if x, ok := s.Orders.Get(o.ID); ok {
		s.syncTasks(x)
	}
}

// findTask finds a card by id, or by its title.
func findTask(kind string, d map[string]any, ref string) map[string]any {
	ref = strings.TrimSpace(ref)
	all := allTasks(kind, d)
	for _, e := range all {
		if taskStr(e.m, "id") == ref {
			return e.m
		}
	}
	for _, e := range all {
		if strings.EqualFold(firstNonEmpty(taskStr(e.m, "title"), taskStr(e.m, "text")), ref) {
			return e.m
		}
	}
	return nil
}

func columnNamed(cols []map[string]any, ref string) int {
	for i, c := range cols {
		if taskStr(c, "id") == ref || strings.EqualFold(taskStr(c, "title"), strings.TrimSpace(ref)) {
			return i
		}
	}
	return -1
}

// applyTaskEdit carries out a planner's task.add, task.update or task.delete.
func (s *Service) applyTaskEdit(c *execCtx, st Step) error {
	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	wid := firstNonEmpty(st.WorkspaceID, c.workspaceID)
	b, err := s.hub.Board(wid)
	if err != nil {
		return err
	}
	var n *board.Node
	for _, x := range b.Nodes {
		if x.ID == st.Tile && isTaskTile(x) {
			n = x
		}
	}
	if n == nil || st.Task == nil {
		s.note(c, "That task board is gone.")
		return nil
	}
	t := &taskTile{node: n, data: map[string]any{}}
	_ = json.Unmarshal(n.Data, &t.data)
	cols := columnsOf(t.data)
	edit := st.Task
	var card map[string]any
	if st.Op == "task.add" {
		card = map[string]any{"id": auth.RandomID("i_")}
		if n.Kind == "checklist" {
			card["text"], card["done"] = edit.Title, false
			items, _ := t.data["items"].([]any)
			t.data["items"] = append(items, card)
		} else {
			if len(cols) == 0 {
				cols = []map[string]any{{"id": auth.RandomID("i_"), "title": "To do", "cards": []any{}}}
				t.data["columns"] = []any{cols[0]}
			}
			card["title"] = edit.Title
			to, _, _, _ := taskColumns(cols)
			if i := columnNamed(cols, edit.Column); edit.Column != "" && i >= 0 {
				to = i
			}
			cards, _ := cols[to]["cards"].([]any)
			cols[to]["cards"] = append(cards, card)
		}
	} else if card = findTask(n.Kind, t.data, edit.Card); card == nil {
		s.note(c, "No task named “"+edit.Card+"” on "+TitleOf(n)+".")
		return nil
	}
	if st.Op == "task.delete" {
		id := taskStr(card, "id")
		drop := func(list []map[string]any) []any {
			kept := make([]any, 0, len(list))
			for _, x := range list {
				if taskStr(x, "id") != id {
					kept = append(kept, x)
				}
			}
			return kept
		}
		if n.Kind == "checklist" {
			t.data["items"] = drop(taskList(t.data["items"]))
		} else {
			for _, col := range cols {
				col["cards"] = drop(taskList(col["cards"]))
			}
		}
		s.saveTasks(wid, t)
		return nil
	}
	if edit.Title != "" {
		if n.Kind == "checklist" {
			card["text"] = edit.Title
		} else {
			card["title"] = edit.Title
		}
	}
	set := func(k, v string) {
		if v != "" {
			card[k] = v
		}
	}
	if n.Kind == "kanban" {
		set("notes", clipLen(edit.Notes, maxTaskNotes))
		if edit.Priority == "low" || edit.Priority == "med" || edit.Priority == "high" {
			card["priority"] = edit.Priority
		}
		set("due", edit.Due)
		if i := columnNamed(cols, edit.Column); edit.Column != "" && i >= 0 && st.Op != "task.add" {
			moveEntry(cols, card, i)
		}
	}
	set("assignee", strings.TrimPrefix(edit.Assignee, "@"))
	if edit.Done != nil {
		card["done"] = *edit.Done
	}
	s.saveTasks(wid, t)
	return nil
}

// tasksForPlanner is what the planner reads of the task boards: each card with what
// it needs to name it, also those no lead is wired to yet.
func tasksForPlanner(b board.Board, name string) []map[string]any {
	linked := map[string]bool{}
	for _, t := range taskTilesOf(b) {
		linked[t.node.ID] = true
	}
	out := []map[string]any{}
	left := plannerTaskCards
	row := func(m map[string]any) map[string]any {
		r := map[string]any{"id": taskStr(m, "id"), "title": firstNonEmpty(taskStr(m, "title"), taskStr(m, "text"))}
		for _, k := range []string{"assignee", "state", "priority", "due"} {
			if v := taskStr(m, k); v != "" {
				r[k] = v
			}
		}
		if m["done"] == true {
			r["done"] = true
		}
		return r
	}
	for _, n := range b.Nodes {
		if !isTaskTile(n) {
			continue
		}
		d := map[string]any{}
		_ = json.Unmarshal(n.Data, &d)
		tile := map[string]any{"tile": n.ID, "kind": n.Kind, "title": taskStr(d, "title"), "board": name, "linked": linked[n.ID], "autoRun": d["autoRun"] == true}
		if n.Kind == "checklist" {
			items := []map[string]any{}
			for _, m := range taskList(d["items"]) {
				if left > 0 {
					items, left = append(items, row(m)), left-1
				}
			}
			tile["items"] = items
		} else {
			cols := []map[string]any{}
			for _, c := range columnsOf(d) {
				cards := []map[string]any{}
				for _, m := range taskList(c["cards"]) {
					if left > 0 {
						cards, left = append(cards, row(m)), left-1
					}
				}
				cols = append(cols, map[string]any{"title": taskStr(c, "title"), "cards": cards})
			}
			tile["columns"] = cols
		}
		out = append(out, tile)
	}
	return out
}

// translateTaskStep resolves a planner's task step to the tile it means.
func (s *Service) translateTaskStep(sc *scope, ps plannerStep, st Step) (Step, string) {
	n, wid, problem := sc.findTile(ps.Tile)
	if problem != "" {
		return st, problem
	}
	if !isTaskTile(n) {
		return st, "Tasks live on a kanban or checklist tile; step dropped."
	}
	st.Tile, st.WorkspaceID = n.ID, wid
	st.Task = &TaskEdit{Card: ps.Card, Title: firstNonEmpty(ps.Title, ps.Name), Notes: ps.Text, Column: ps.Column,
		Assignee: ps.Assignee, Priority: ps.Priority, Due: ps.Due, Done: ps.Done}
	if st.Op == "task.add" && st.Task.Title == "" {
		return st, "A new task needs a title; step dropped."
	}
	if st.Op != "task.add" && st.Task.Card == "" {
		return st, "Planner task step named no card; step dropped."
	}
	return st, ""
}
