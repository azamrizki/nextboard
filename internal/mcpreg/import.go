package mcpreg

import (
	"encoding/base64"
	"fmt"

	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

// CopyFrom adds the MCP servers of another instance of this machine, whose data folder is dir
// (POST /api/instances/import). With box (that instance's master key) their header and
// environment values come too, opened with its key and sealed again with this hub's. Without it
// they are left out, and a server that had any is copied turned off until they are typed again.
// A name this registry has already is skipped. Nothing in dir is written.
func (s *Store) CopyFrom(dir string, box *secret.Box) ([]string, []string, error) {
	var src []Server
	if _, err := (&store.Store{Dir: dir}).Load(storeName, &src); err != nil {
		return nil, nil, err
	}
	var added, notes []string
	open := func(vars []Var) ([]Var, error) {
		out := make([]Var, 0, len(vars))
		for _, v := range vars {
			raw, err := base64.StdEncoding.DecodeString(v.Value)
			var plain []byte
			if err == nil {
				plain, err = box.Open(raw)
			}
			if err != nil {
				return nil, err
			}
			out = append(out, Var{Name: v.Name, Value: string(plain)})
		}
		return out, nil
	}
	for _, sv := range src {
		in := sv
		in.Args = append([]string{}, sv.Args...)
		// projects are not copied, so their ids mean nothing here; and an empty list would offer
		// a server meant for a few projects to every one of them
		in.Projects = []string{}
		limited := len(sv.Projects) > 0
		if limited {
			in.Enabled = false
		}
		hadValues := len(sv.Headers)+len(sv.Env) > 0
		if box != nil {
			var err error
			if in.Headers, err = open(sv.Headers); err == nil {
				in.Env, err = open(sv.Env)
			}
			if err != nil {
				notes = append(notes, fmt.Sprintf("MCP server %q: its values cannot be opened; not copied", sv.ID))
				continue
			}
		} else {
			in.Headers, in.Env = []Var{}, []Var{}
			if hadValues {
				in.Enabled = false
			}
		}
		if _, err := s.Create(in); err != nil {
			notes = append(notes, fmt.Sprintf("MCP server %q: %v; not copied", sv.ID, err))
			continue
		}
		added = append(added, sv.ID)
		if limited && sv.Enabled {
			notes = append(notes, fmt.Sprintf("MCP server %q: it was offered to some projects of the other instance only; copied turned off", sv.ID))
		}
		if box == nil && hadValues {
			notes = append(notes, fmt.Sprintf("MCP server %q: copied turned off, without its header and environment values; type them in Settings › MCP servers", sv.ID))
		}
	}
	return added, notes, nil
}
