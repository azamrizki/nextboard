package providers

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

// CopyFrom adds the providers of another instance of this machine, whose data folder is dir
// (POST /api/instances/import). Each gets a new id here; the result maps the other instance's
// ids to them, so settings that name a provider can follow. With box (that instance's master
// key) the keys come too: opened with its key and sealed again with this hub's. Without it every
// copy has no key and asks for one in Settings. Nothing in dir is written.
func (s *Store) CopyFrom(dir string, box *secret.Box) (map[string]string, []string, error) {
	var src []stored
	if _, err := (&store.Store{Dir: dir}).Load(storeName, &src); err != nil {
		return nil, nil, err
	}
	ids := map[string]string{}
	var notes []string
	s.mu.Lock()
	defer s.mu.Unlock()
	before := slices.Clone(s.list)
	for _, o := range src {
		if slices.ContainsFunc(s.list, func(p stored) bool { return strings.EqualFold(p.Name, o.Name) }) {
			notes = append(notes, fmt.Sprintf("API provider %q: this instance has one with that name; not copied", o.Name))
			continue
		}
		if len(s.list) >= maxProviders {
			notes = append(notes, fmt.Sprintf("API provider %q: at most %d providers; not copied", o.Name, maxProviders))
			continue
		}
		key := ""
		if box != nil && o.Key != "" {
			raw, err := base64.StdEncoding.DecodeString(o.Key)
			var plain []byte
			if err == nil {
				plain, err = box.Open(raw)
			}
			if err != nil {
				notes = append(notes, fmt.Sprintf("API provider %q: its key cannot be opened; copied without it", o.Name))
			} else {
				key = string(plain)
			}
		}
		now := time.Now().UTC()
		p := stored{Provider: Provider{ID: auth.RandomID("ap_"), CreatedAt: now, UpdatedAt: now}}
		models, prices, budget := o.Models, o.Prices, o.Budget
		in := Input{Name: &o.Name, Kind: &o.Kind, BaseURL: &o.BaseURL, API: &o.API, Auth: &o.Auth, Model: &o.Model,
			FastModel: &o.FastModel, Models: &models, Prices: &prices, Budget: &budget}
		if key != "" {
			in.Key = &key
		}
		// not full: a copy may come without its key, which Add would refuse
		if err := s.applyLocked(&p, in, false); err != nil {
			notes = append(notes, fmt.Sprintf("API provider %q: %v; not copied", o.Name, err))
			continue
		}
		if box == nil && o.Key != "" {
			notes = append(notes, fmt.Sprintf("API provider %q: copied without its key; add it in Settings › API providers", o.Name))
		}
		s.list = append(s.list, p)
		ids[o.ID] = p.ID
	}
	if len(ids) == 0 {
		return ids, notes, nil
	}
	if err := s.saveLocked(); err != nil {
		s.list = before
		return nil, notes, err
	}
	return ids, notes, nil
}
