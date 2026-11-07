package settings

// Phase 7 part A keys (docs/API-studio.md §12): web previews and git.

// Previews configures the preview proxy. PortRange applies after a hub
// restart.
type Previews struct {
	Auto      bool   `json:"auto"`
	PortRange [2]int `json:"portRange"`
}

// Git configures the Git panel.
type Git struct {
	// CommitWithRunner: the commit message button has the CLI read the changes.
	CommitWithRunner bool `json:"commitWithRunner"`
}

func defaultGit() Git { return Git{CommitWithRunner: true} }

func defaultPreviews() Previews { return Previews{Auto: true, PortRange: [2]int{12320, 12339}} }

func validatePreviews(s *Settings) error {
	r := s.Previews.PortRange
	if r[0] < 1024 || r[1] > 65535 || r[1] < r[0] || r[1]-r[0] > 199 {
		return badSetting("previews.portRange must be [start, end] within 1024–65535, at most 200 ports")
	}
	return nil
}
