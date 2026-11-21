package server

// NextCode's multi-agent (docs/API-agents.md §30): the "agent" tool of the hub's MCP server, which
// every NextCode console has on every engine (Claude Code, Codex, NextCode's own agent). It hands
// tasks to sub-agents that work side by side, each a conversation of its own on the console's
// account, model, folder and context, and returns their reports. Their chats show in the console's
// under a card per task, and what they ask the owner is asked there. A sub-agent reaches the
// hub's tools with a token of its own, which gives it no sub-agents of its own.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/nextcode"
)

// Limits of the agent tool.
const (
	subAgentTasks    = 8        // tasks in one call
	subAgentParallel = 4        // sub-agents at work at once (each is a CLI)
	subAgentReport   = 24 << 10 // the most of one report the main agent gets
)

var agentToolDef = map[string]any{
	"name": "agent",
	"description": "Hand tasks to sub-agents that work side by side, then get their reports. Use it for work that splits into independent parts: " +
		"research in different places of the code, or changes to separate files or modules. Each sub-agent starts fresh with your tools, " +
		"folder and rules but none of this conversation: give it everything it needs in its prompt (the goal, the files, what to return). " +
		"Several tasks in one call run at the same time; the call returns when all are done, with each one's final report. " +
		"Do not give two sub-agents the same files to change.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type": "array", "minItems": 1, "maxItems": subAgentTasks,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"description": map[string]any{"type": "string", "description": "The task in three to seven words"},
						"prompt":      map[string]any{"type": "string", "description": "The whole task, for an agent that has seen nothing of this conversation"},
					},
					"required": []string{"description", "prompt"},
				},
			},
		},
		"required": []string{"tasks"},
	},
}

// subPreamble goes before a sub-agent's task.
const subPreamble = "You are a sub-agent of NextCode: the main agent handed you the task below and waits for your report. " +
	"Work on it on your own with your tools; the owner may approve or deny what you do. Stay within the task. " +
	"When you are done, answer with a short report of what you found or changed (files, results, anything left open): " +
	"it is all the main agent gets of your work.\n\n"

type subTask struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// subAgentToken is the session a sub-agent's token belongs to (handleRemoteMCP).
func (s *Server) subAgentSession(token string) (string, bool) {
	v, ok := s.subTokens.Load(token)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// runSubAgents carries out one call of the agent tool of console sid: the report of each task,
// and whether the call failed as a whole.
func (s *Server) runSubAgents(ctx context.Context, sid string, args json.RawMessage) (string, bool) {
	var in struct {
		Tasks       []subTask `json:"tasks"`
		Description string    `json:"description"`
		Prompt      string    `json:"prompt"`
	}
	if json.Unmarshal(args, &in) != nil {
		return "The agent tool takes {\"tasks\": [{\"description\", \"prompt\"}, …]}.", true
	}
	if len(in.Tasks) == 0 && strings.TrimSpace(in.Prompt) != "" {
		in.Tasks = []subTask{{Description: in.Description, Prompt: in.Prompt}}
	}
	if len(in.Tasks) == 0 || len(in.Tasks) > subAgentTasks {
		return fmt.Sprintf("Give the agent tool 1 to %d tasks.", subAgentTasks), true
	}
	for i, t := range in.Tasks {
		if strings.TrimSpace(t.Prompt) == "" {
			return fmt.Sprintf("Task %d has no prompt.", i+1), true
		}
		if strings.TrimSpace(t.Description) == "" {
			in.Tasks[i].Description = fmt.Sprintf("Task %d", i+1)
		}
	}
	sess, ok := s.sessions.Get(sid)
	if !ok {
		return "This console is gone.", true
	}
	d := sess.Chat()
	if d == nil {
		return "This console's chat is not open.", true
	}
	// the cards go under the call of the tool, as the main conversation shows it
	key := in.Tasks[0].Prompt
	if len(key) > 60 {
		key = strings.ToValidUTF8(key[:60], "")
	}
	parent := d.RunningTool("agent", key)
	mode := d.Status().PermissionMode

	reports := make([]string, len(in.Tasks))
	failed := make([]bool, len(in.Tasks))
	slots := make(chan struct{}, subAgentParallel)
	var wg sync.WaitGroup
	for i, task := range in.Tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				reports[i], failed[i] = "Stopped before it began.", true
				return
			}
			defer func() { <-slots }()
			reports[i], failed[i] = s.runSubAgent(ctx, sid, sess.SubAgent, d, parent, mode, task)
		}()
	}
	wg.Wait()

	if len(in.Tasks) == 1 {
		return reports[0], failed[0]
	}
	var b strings.Builder
	all := true
	for i, t := range in.Tasks {
		all = all && failed[i]
		state := ""
		if failed[i] {
			state = " (failed)"
		}
		fmt.Fprintf(&b, "## Task %d: %s%s\n%s\n\n", i+1, t.Description, state, reports[i])
	}
	return strings.TrimSpace(b.String()), all
}

// runSubAgent runs one task to its end: its report, and whether it failed.
func (s *Server) runSubAgent(ctx context.Context, sid string, open func(token, mode string) (*nextcode.Driver, error), d *nextcode.Driver, parent, mode string, task subTask) (string, bool) {
	token := auth.RandomID("sa_")
	s.subTokens.Store(token, sid)
	defer s.subTokens.Delete(token)

	input, _ := json.Marshal(task)
	card := nextcode.Item{ID: auth.RandomID("agent_"), Kind: nextcode.KindTool, Parent: parent,
		Tool: &nextcode.Tool{Name: "Agent", Input: input, Status: nextcode.ToolRunning}}
	d.PutItem(card)
	finish := func(report string, status string) (string, bool) {
		out := report
		if len(out) > subAgentReport {
			out = clipReport(out, subAgentReport) + "\n[… cut]"
		}
		c := card
		c.Tool = &nextcode.Tool{Name: "Agent", Input: input, Output: out, Status: status}
		d.PutItem(c)
		return out, status != nextcode.ToolDone
	}

	child, err := open(token, mode)
	if err != nil {
		return finish("The sub-agent could not start: "+err.Error(), nextcode.ToolError)
	}
	defer child.Close()
	stop := d.Attach(child, card.ID, card.ID+"/")
	defer stop()

	_, sub := child.Subscribe()
	defer child.Unsubscribe(sub)
	if err := child.Send(subPreamble + "Task: " + task.Description + "\n\n" + task.Prompt); err != nil {
		return finish("The sub-agent could not start: "+err.Error(), nextcode.ToolError)
	}
	if err := waitTurn(ctx, sub); err != nil {
		_ = child.Interrupt()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return finish("Stopped.", nextcode.ToolStopped)
		}
		return finish("The sub-agent ended: "+err.Error(), nextcode.ToolError)
	}
	if report := strings.TrimSpace(child.Final()); report != "" {
		return finish(report, nextcode.ToolDone)
	}
	if msg := child.LastError(); msg != "" {
		return finish("The sub-agent failed: "+msg, nextcode.ToolError)
	}
	return finish("The sub-agent gave no report.", nextcode.ToolError)
}

// waitTurn waits for the end of the turn the sub-agent's first message started.
func waitTurn(ctx context.Context, sub *nextcode.Sub) error {
	started := false
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		case f, ok := <-sub.C:
			if !ok {
				return errors.New("its chat closed")
			}
			if f.T != nextcode.FrameStatus || f.Status == nil {
				continue
			}
			if f.Status.Working {
				started = true
				continue
			}
			if started {
				return nil
			}
			if f.Status.Error != "" {
				return errors.New(f.Status.Error)
			}
		}
	}
}

// clipReport keeps a report within n bytes on a whole character.
func clipReport(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
