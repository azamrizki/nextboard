package settings

import (
	"bytes"
	"encoding/json"
)

// Replace swaps the whole document for next, after the same checks as Patch: settings copied from
// another instance of this machine (POST /api/instances/import), which a merge patch could not
// express because it never drops a key the document already has.
func (s *Store) Replace(next Settings) (Settings, error) {
	raw, err := json.Marshal(next)
	if err != nil {
		return Settings{}, err
	}
	doc := Defaults()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Settings{}, badSetting("invalid settings: %v", err)
	}
	if err := validate(&doc); err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.st.Save(File, doc); err != nil {
		return Settings{}, err
	}
	s.cur = doc
	s.view = clone(doc)
	if s.OnChange != nil {
		go s.OnChange(clone(doc))
	}
	return clone(doc), nil
}
