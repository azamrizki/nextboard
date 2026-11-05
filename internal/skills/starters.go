package skills

// Starter is a NextBoard starter skill (GET /api/skills/starters); the
// client creates it with POST /api/machines/{id}/skills.
type Starter struct {
	Name         string `json:"name"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	Body         string `json:"body"`
	Content      string `json:"content"` // the SKILL.md as it would be written
}

var starterList = []Starter{
	{Name: "commit-message", Title: "Commit message", ArgumentHint: "[scope]",
		Description: "Write a concise commit message for the staged changes. Use when the user asks for a commit message or to commit.",
		Body: `Look at the staged changes (git diff --staged; if nothing is staged, git diff) and write a commit message:

1. A subject line of at most 72 characters in the imperative mood ("Add …", "Fix …"), with the scope from $ARGUMENTS when given.
2. A blank line, then a short body explaining what changed and why, wrapped at 72 characters.
3. Mention breaking changes explicitly.

Do not commit unless the user asks you to.`},
	{Name: "pr-summary", Title: "Pull request summary", ArgumentHint: "[base branch]",
		Description: "Summarise the current branch as a pull request description. Use when preparing a PR.",
		Body: `Compare the current branch with $ARGUMENTS (default: the repository's main branch) and write a pull request description:

- Summary: what the change does in two or three sentences.
- Changes: a bullet list grouped by area.
- Testing: how it was tested and what a reviewer should try.
- Risks and follow-ups.`},
	{Name: "release-notes", Title: "Release notes", ArgumentHint: "[since tag]",
		Description: "Draft release notes from the commits since a tag. Use before a release.",
		Body:        `Collect the commits since $ARGUMENTS (default: the latest tag) and draft user-facing release notes grouped as New, Improved, Fixed and Breaking. Write for users, not developers; skip internal refactors.`},
	{Name: "write-tests", Title: "Write tests", ArgumentHint: "<file or function>",
		Description: "Write focused tests for a file or function. Use when the user asks for tests.",
		Body: `Write tests for $ARGUMENTS using the project's existing test framework and conventions:

1. Read the code and the existing tests first.
2. Cover the main behaviour, edge cases and error paths.
3. Keep tests independent and deterministic (no real network, clocks or randomness).
4. Run the tests and fix failures in the tests you wrote.`},
	{Name: "find-bugs", Title: "Find bugs", ArgumentHint: "[path]",
		Description: "Review code for likely bugs and explain each finding. Use for a bug hunt or before a release.",
		Body:        `Review $ARGUMENTS (default: the files changed on this branch) for likely bugs: logic errors, unhandled errors, races, resource leaks, off-by-one mistakes and security problems. For each finding give the file and line, why it is a bug, and a suggested fix. Do not change code unless asked.`},
	{Name: "plan-refactor", Title: "Plan a refactor", ArgumentHint: "<area>",
		Description: "Plan a safe, incremental refactor of an area of the code. Use before larger restructuring.",
		Body:        `Study $ARGUMENTS and propose a refactor plan: the current problems, the target design, and a sequence of small steps that each keep the tests green. Note risks and what to test after each step. Do not edit files yet.`},
	{Name: "document-api", Title: "Document an API", ArgumentHint: "<module or endpoint>",
		Description: "Write reference documentation for an API, module or endpoint. Use when docs are missing or stale.",
		Body:        `Document $ARGUMENTS: purpose, parameters, return values or responses, errors, and a short example for each entry point. Match the project's existing documentation style.`},
	{Name: "daily-update", Title: "Daily update",
		Description: "Summarise today's work in this repository for a stand-up. Use at the end of the day.",
		Body:        `Summarise what changed in this repository today (commits, uncommitted changes, open TODOs) as a short stand-up update: Done, In progress, Blocked.`},
	{Name: "explain-code", Title: "Explain code", ArgumentHint: "<file or symbol>",
		Description: "Explain how a piece of code works, step by step. Use when the user asks what code does.",
		Body:        `Explain $ARGUMENTS: what it is for, how it works step by step, how it is used elsewhere in the project, and anything surprising. Keep it concrete and reference file names and lines.`},
	{Name: "speed-check", Title: "Speed check", ArgumentHint: "[path]",
		Description: "Look for performance problems and quick wins. Use when something is slow.",
		Body:        `Look for performance problems in $ARGUMENTS (default: the hot paths of the project): repeated work, N+1 queries, unnecessary allocations, blocking I/O and missing caching. Rank the findings by expected impact and suggest how to measure each one.`},
	{Name: "audit-deps", Title: "Audit dependencies",
		Description: "Review the project's dependencies for outdated, unused or risky packages. Use before upgrades.",
		Body:        `List the project's direct dependencies and flag the ones that are unused, badly outdated, unmaintained or have known security issues. Suggest an upgrade order and what to test. Do not change dependency files unless asked.`},
	{Name: "update-readme", Title: "Update the README",
		Description: "Bring the README in line with the current code. Use after features change.",
		Body:        `Compare the README with the current code (setup steps, commands, configuration, features) and update the parts that are wrong or missing. Keep the existing structure and tone.`},
}

// Starters returns the starter skills with their rendered SKILL.md.
func Starters() []Starter {
	out := make([]Starter, len(starterList))
	for i, s := range starterList {
		s.Content = Render(KindSkill, s.Name, s.Description, s.ArgumentHint, s.Body)
		out[i] = s
	}
	return out
}
