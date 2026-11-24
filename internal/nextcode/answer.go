package nextcode

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// planKeep is what the agent is told when the owner sends its plan back without a note.
const planKeep = "Keep planning: the owner did not approve this plan yet."

// answerItem is the chat's record of the owner's answer to request r, a question or a plan
// (docs/API-agents.md §30, item kind "answer").
func answerItem(r *Request, a Answer, now time.Time) Item {
	ans := &Answered{Kind: r.Kind, ToolUseID: r.ToolUseID}
	allowed := a.Decision == Allow || a.Decision == AllowAlways
	switch {
	case r.Kind == ReqPlan && allowed:
		ans.Approved, ans.Mode = true, a.Mode
	case r.Kind == ReqPlan:
		if m := strings.TrimSpace(a.Message); m != planKeep {
			ans.Message = m // the owner's note; the words a client sends without one are not
		}
	case !allowed:
		ans.Input = r.Input
		ans.Skipped = true // its message is for the agent ("chose not to answer"), not the owner
	default:
		ans.Input = r.Input
		ans.Answers, ans.Message = a.Answers, a.Message
	}
	return Item{ID: newID("a"), Kind: KindAnswer, At: now, Parent: r.Parent, Text: answerText(ans), Answer: ans}
}

// answerText says an answer in words, a line per question: "Store: Redis"; or the decision on
// a plan: "Approved the plan.", "Keep planning: <note>".
func answerText(a *Answered) string {
	if a.Kind == ReqPlan {
		return planText(a)
	}
	if a.Skipped {
		return "Skipped: the owner answered nothing."
	}
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
		} `json:"questions"`
	}
	_ = json.Unmarshal(a.Input, &in)
	var lines []string
	seen := map[string]bool{}
	for _, q := range in.Questions {
		labels, ok := a.Answers[q.Question]
		if !ok || seen[q.Question] {
			continue
		}
		seen[q.Question] = true
		name := q.Header
		if name == "" {
			name = q.Question
		}
		lines = append(lines, name+": "+strings.Join(labels, ", "))
	}
	// answers to questions the input does not hold (it could not be read)
	var rest []string
	for q := range a.Answers {
		if !seen[q] {
			rest = append(rest, q)
		}
	}
	sort.Strings(rest)
	for _, q := range rest {
		lines = append(lines, q+": "+strings.Join(a.Answers[q], ", "))
	}
	if a.Message != "" {
		lines = append(lines, a.Message)
	}
	return strings.Join(lines, "\n")
}

func planText(a *Answered) string {
	switch {
	case a.Approved && a.Mode == "":
		return "Approved the plan."
	case a.Approved && a.Mode == "acceptEdits":
		return "Approved the plan, with Accept edits: it edits files without asking."
	case a.Approved:
		return "Approved the plan, in permission mode " + a.Mode + "."
	case a.Message == "":
		return "Keep planning (no note)."
	}
	return "Keep planning: " + a.Message
}
