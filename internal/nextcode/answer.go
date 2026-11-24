package nextcode

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// answerItem is the chat's record of the owner's answer to request r, a question
// (docs/API-agents.md §30, item kind "answer").
func answerItem(r *Request, a Answer, now time.Time) Item {
	ans := &Answered{ToolUseID: r.ToolUseID, Input: r.Input}
	if a.Decision == Deny || a.Decision == DenyStop {
		ans.Skipped = true // its message is for the agent ("chose not to answer"), not the owner
	} else {
		ans.Answers, ans.Message = a.Answers, a.Message
	}
	return Item{ID: newID("a"), Kind: KindAnswer, At: now, Parent: r.Parent, Text: answerText(ans), Answer: ans}
}

// answerText says an answer in words, a line per question: "Store: Redis".
func answerText(a *Answered) string {
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
