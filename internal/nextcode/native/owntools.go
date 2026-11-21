package native

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
)

// The agent's own tools: they ask the owner, through the driver, rather than run anything. Their
// inputs are Claude Code's, so the driver shows them as a question and as a plan to approve.
const (
	toolAsk  = "AskUserQuestion"
	toolPlan = "ExitPlanMode"
)

func ownToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{Name: toolAsk, Description: askDescription, Schema: json.RawMessage(askSchema)},
		{Name: toolPlan, Description: planDescription, Schema: json.RawMessage(planSchema)},
	}
}

const askDescription = `Ask the owner one to four questions with a few options each, when you are blocked on a real choice that only they can make (which approach to take, what they meant). The owner picks an option, or several when multiSelect is true, or answers in their own words. Do not use it to ask for leave to do something you can just do, or to present a plan (use ExitPlanMode for that).`

const askSchema = `{
  "type": "object",
  "properties": {
    "questions": {
      "type": "array",
      "description": "The questions, one to four.",
      "minItems": 1,
      "maxItems": 4,
      "items": {
        "type": "object",
        "properties": {
          "question": {"type": "string", "description": "The whole question, clear on its own, ending with a question mark."},
          "header": {"type": "string", "description": "A very short label for the question, at most 12 characters (e.g. \"Database\")."},
          "options": {
            "type": "array",
            "description": "Two to four options to choose from.",
            "minItems": 2,
            "maxItems": 4,
            "items": {
              "type": "object",
              "properties": {
                "label": {"type": "string", "description": "The option, in one to five words."},
                "description": {"type": "string", "description": "What choosing it means, and its trade-offs."}
              },
              "required": ["label", "description"]
            }
          },
          "multiSelect": {"type": "boolean", "description": "Whether the owner may choose more than one option."}
        },
        "required": ["question", "header", "options", "multiSelect"]
      }
    }
  },
  "required": ["questions"]
}`

const planDescription = `In plan mode, present your plan to the owner for approval, once your research is done and the plan is ready. The owner approves it, and you then carry it out, or tells you what to change. Use it only in plan mode, and only for a plan of work to do, not for questions (use AskUserQuestion for those).`

const planSchema = `{
  "type": "object",
  "properties": {
    "plan": {"type": "string", "description": "The plan, in Markdown: concise steps, the files to change, and how the result will be checked."}
  },
  "required": ["plan"]
}`

// questionInput is AskUserQuestion's input, and the owner's answers the driver adds to it.
type questionInput struct {
	Questions []struct {
		Question string `json:"question"`
	} `json:"questions"`
	Answers  map[string]any `json:"answers"`
	Response string         `json:"response"`
}

// validQuestions reports whether an AskUserQuestion input has at least one question.
func validQuestions(input json.RawMessage) bool {
	var in questionInput
	if json.Unmarshal(input, &in) != nil || len(in.Questions) == 0 {
		return false
	}
	for _, q := range in.Questions {
		if strings.TrimSpace(q.Question) == "" {
			return false
		}
	}
	return true
}

// answersText is the result of an answered question: "The owner answered: <question> →
// <answer>; …", and the owner's own words when they gave some. updated is the input the driver
// gave back with the answers; asked the call's own, for the order of the questions.
func answersText(asked, updated json.RawMessage) string {
	var in questionInput
	_ = json.Unmarshal(updated, &in)
	if len(in.Questions) == 0 {
		var orig questionInput
		_ = json.Unmarshal(asked, &orig)
		in.Questions = orig.Questions
	}
	var parts []string
	seen := map[string]bool{}
	for _, q := range in.Questions {
		if v, ok := in.Answers[q.Question]; ok && !seen[q.Question] {
			parts = append(parts, q.Question+" → "+answerString(v))
			seen[q.Question] = true
		}
	}
	var rest []string
	for q := range in.Answers {
		if !seen[q] {
			rest = append(rest, q)
		}
	}
	sort.Strings(rest)
	for _, q := range rest {
		parts = append(parts, q+" → "+answerString(in.Answers[q]))
	}
	var b strings.Builder
	if len(parts) > 0 {
		b.WriteString("The owner answered: " + strings.Join(parts, "; "))
	}
	if r := strings.TrimSpace(in.Response); r != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("The owner's own words: " + r)
	}
	if b.Len() == 0 {
		return "The owner answered without choosing an option."
	}
	return b.String()
}

func answerString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, p := range x {
			parts = append(parts, answerString(p))
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}
