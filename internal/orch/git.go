package orch

// GitActions runs the command bar's git steps on the project's repository
// (docs/API-studio.md §3.4): commitPush, commit, push and pull "press the
// Git panel's buttons". The hub sets Service.Git; nil keeps the phase 6
// reply "Git actions are not available yet".
type GitActions interface {
	// GitStep starts action and returns the immediate note. repo names one
	// of the project's repositories ("" = the only one). Work that continues
	// in a job (push, pull) reports its outcome later through report
	// (failed = error message).
	GitStep(projectID, repo, action, message string, report func(text string, failed bool)) (string, error)
}

// gitStep runs a "git" step. Typed git steps run at once; planner-made
// ones were confirmed as "risky" before reaching here (§3.7).
func (s *Service) gitStep(c *execCtx, st Step) error {
	if s.Git == nil {
		s.say(c, ThreadMessage{Kind: "reply", Text: "Git actions are not available yet."})
		return nil
	}
	threadID := c.threadID
	text, err := s.Git.GitStep(c.projectID, st.Repo, st.Action, st.Message, func(t string, failed bool) {
		kind := "note"
		if failed {
			kind = "error"
		}
		s.Threads.Add(ThreadMessage{ThreadID: threadID, Role: "hub", Kind: kind, Text: t})
	})
	if err != nil {
		return err
	}
	if text != "" {
		s.note(c, text)
	}
	return nil
}
