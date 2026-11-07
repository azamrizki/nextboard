package settings

// Usage configures the usage pill and the reset chime
// (docs/API-studio.md §10, §12). Voice settings are per client
// (localStorage, §11) and not stored by the hub.
type Usage struct {
	// Background allows automatic checks; off by default because each
	// Claude check adds 2 lines to ~/.claude/history.jsonl and each Codex
	// check makes Codex write a [tui] block into ~/.codex/config.toml.
	Background bool `json:"background"`
	// Agents switches checks per CLI (any trigger); a missing key means
	// enabled. Codex is off by default for the config write above.
	Agents map[string]bool `json:"agents"`
	Chime  Chime           `json:"chime"`
}

// Chime is settings.usage.chime (F3).
type Chime struct {
	Enabled bool   `json:"enabled"`
	Play    string `json:"play"`  // after100 | always
	Sound   string `json:"sound"` // bell | chime | pop | soft
}

func defaultUsage() Usage {
	return Usage{Background: false, Agents: map[string]bool{"codex": false}, Chime: Chime{Enabled: true, Play: "after100", Sound: "bell"}}
}

func validateUsage(s *Settings) error {
	if s.Usage.Agents == nil {
		s.Usage.Agents = map[string]bool{}
	}
	for k := range s.Usage.Agents {
		if k == "" || len(k) > 64 {
			return badSetting("usage.agents: invalid agent id")
		}
	}
	switch s.Usage.Chime.Play {
	case "after100", "always":
	default:
		return badSetting(`usage.chime.play must be "after100" or "always"`)
	}
	switch s.Usage.Chime.Sound {
	case "bell", "chime", "pop", "soft":
	default:
		return badSetting(`usage.chime.sound must be "bell", "chime", "pop" or "soft"`)
	}
	return nil
}
