package orch

import (
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/store"
)

// CopyFrom adds the saved layouts of another instance of this machine, whose data folder is dir
// (POST /api/instances/import), each under a new id. Nothing in dir is written.
func (l *Layouts) CopyFrom(dir string) (int, error) {
	var src []Layout
	if _, err := (&store.Store{Dir: dir}).Load(layoutsFile, &src); err != nil {
		return 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	t := time.Now().UTC()
	for _, x := range src {
		if x.Builtin || validateLayout(&x) != nil {
			continue
		}
		x.ID, x.CreatedAt = auth.RandomID("ly_"), &t
		l.list = append(l.list, x)
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, l.st.Save(layoutsFile, l.list)
}
