package agent

import (
	"fmt"

	"github.com/azamrizki/nextboard/internal/store"
)

// CopyFrom adds the custom agents of another instance of this machine, whose data folder is dir
// (POST /api/instances/import). They are plain definitions in presets.json, their env included
// as it is shown in Settings. An id this hub has already (built in, from the catalog or custom)
// is skipped. Nothing in dir is written.
func (s *Store) CopyFrom(dir string) ([]string, []string, error) {
	var src []entry
	if _, err := (&store.Store{Dir: dir}).Load(File, &src); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = false
	s.loadLocked()
	var added, notes []string
	before := append([]entry(nil), s.entries...)
	for _, e := range src {
		if e.ID == "" || e.Command == "" {
			continue
		}
		taken := false
		if _, ok := builtinByID(e.ID); ok {
			taken = true
		}
		for _, x := range s.entries {
			taken = taken || x.ID == e.ID
		}
		if taken {
			notes = append(notes, fmt.Sprintf("custom agent %q: this instance has an agent with that id; not copied", e.ID))
			continue
		}
		s.entries = append(s.entries, e)
		added = append(added, e.ID)
	}
	if len(added) == 0 {
		return added, notes, nil
	}
	if err := s.saveLocked(); err != nil {
		s.entries = before
		return nil, notes, err
	}
	return added, notes, nil
}
