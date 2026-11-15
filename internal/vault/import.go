package vault

import (
	"fmt"

	"github.com/azamrizki/nextboard/internal/store"
)

// CopyFrom makes this hub's vault a copy of the vault of another instance of this machine, whose
// data folder is dir (POST /api/instances/import). It is copied as it is stored: the key material
// and every item as ciphertext, so the same master password opens it and the hub still reads
// nothing. Items keep their ids, which their encryption is bound to. What belongs to that
// instance alone stays there: the PINs (the other half of each is in a browser of that origin)
// and the agent catalog (it names that instance's projects). It refuses when this hub has a
// vault already. Nothing in dir is written. It returns how many items came.
func (s *Service) CopyFrom(dir string) (int, error) {
	src, err := loadDoc(&store.Store{Dir: dir}, fileName)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	if src == nil || src.Header == nil {
		return 0, ErrAbsent
	}
	vaultID, err := randomID()
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return 0, err
	}
	if s.d.Header != nil {
		return 0, ErrExists
	}
	t := now()
	next := s.d
	next.Rev++
	h := *src.Header
	h.ID, h.KeyRev, h.CreatedAt, h.UpdatedAt = vaultID, 1, t, t
	next.Header = &h
	next.Items = make(map[string]Item, len(src.Items))
	for id, it := range src.Items {
		next.Items[id] = Item{ID: id, Blob: it.Blob, Rev: next.Rev, UpdatedAt: t}
	}
	next.Pins, next.Agents = nil, nil
	if err := next.check(); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := s.save(next); err != nil {
		return 0, err
	}
	s.published()
	return len(next.Items), nil
}
