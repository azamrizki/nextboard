package project

import (
	"errors"
	"testing"
	"time"
)

func TestArchiveRestore(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	a := addProject(t, s, t.TempDir(), "shop-api")
	b := addProject(t, s, t.TempDir(), "shop-web")

	got, err := s.Archive(a.ID)
	if err != nil || got.ArchivedAt == nil {
		t.Fatalf("archive: %v %+v", err, got)
	}
	first := *got.ArchivedAt
	// archiving again changes nothing
	if again, err := s.Archive(a.ID); err != nil || !again.ArchivedAt.Equal(first) {
		t.Fatalf("archive again: %v %+v", err, again)
	}
	if !s.IsArchived(a.ID) || s.IsArchived(b.ID) || s.IsArchived("p_missing000") {
		t.Fatal("IsArchived")
	}
	if l := s.List(); len(l) != 1 || l[0].ID != b.ID {
		t.Fatalf("active list %+v", l)
	}
	if l := s.Archived(); len(l) != 1 || l[0].ID != a.ID {
		t.Fatalf("archived list %+v", l)
	}
	if len(s.ListAll()) != 2 {
		t.Fatal("ListAll")
	}
	// the most recently opened active project is the default, never an archived one
	if p, _, ok := s.Default(); !ok || p.ID != b.ID {
		t.Fatalf("default %v %v", p.ID, ok)
	}
	// it stays archived on disk
	s2 := openStore(t, dir)
	if !s2.IsArchived(a.ID) {
		t.Fatal("archived lost on reopen")
	}

	time.Sleep(5 * time.Millisecond)
	if _, err := s.Archive(b.ID); err != nil {
		t.Fatal(err)
	}
	if l := s.Archived(); len(l) != 2 || l[0].ID != b.ID {
		t.Fatalf("archived newest first %+v", l)
	}
	if _, _, ok := s.Default(); ok {
		t.Fatal("default with every project archived")
	}

	r, err := s.Restore(a.ID)
	if err != nil || r.ArchivedAt != nil {
		t.Fatalf("restore %v %+v", err, r)
	}
	if _, err := s.Restore(a.ID); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("restore active: %v", err)
	}
	if _, err := s.Restore("p_missing000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore missing: %v", err)
	}
	if _, err := s.Archive("p_missing000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archive missing: %v", err)
	}
}

func TestProjectImages(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := addProject(t, s, t.TempDir(), "shop-api")
	now := time.Now().UTC()
	got, err := s.Patch(p.ID, Patch{Cover: &ImageChange{Image: &Image{AssetID: "a_cover1234", Source: ImageUpload, At: now}}, Name: strPtr("shop")})
	if err != nil || got.Cover == nil || got.Cover.AssetID != "a_cover1234" || got.Name != "shop" || got.Icon != nil {
		t.Fatalf("cover %v %+v", err, got)
	}
	// one save, refused as a whole
	if _, err := s.Patch(p.ID, Patch{Icon: &ImageChange{Image: &Image{AssetID: "a_icon12345", Source: ImageUpload, At: now}}, Name: strPtr("")}); !errors.Is(err, ErrBadName) {
		t.Fatalf("bad name with icon: %v", err)
	}
	if cur, _ := s.Get(p.ID); cur.Icon != nil {
		t.Fatal("icon saved with a refused patch")
	}
	got, err = s.SetImage(p.ID, ImageIcon, &Image{AssetID: "a_icon12345", Source: ImageAI, At: now})
	if err != nil || got.Icon == nil || got.Icon.Source != ImageAI {
		t.Fatalf("set icon %v %+v", err, got)
	}
	// a copy: changing what came back changes nothing in the store
	got.Icon.AssetID = "a_changed00"
	if cur, _ := s.Get(p.ID); cur.Icon.AssetID != "a_icon12345" {
		t.Fatal("Get shares the image")
	}
	got, err = s.Patch(p.ID, Patch{Cover: &ImageChange{}})
	if err != nil || got.Cover != nil || got.Icon == nil {
		t.Fatalf("remove cover %v %+v", err, got)
	}
	if _, err := s.SetImage(p.ID, "poster", nil); !errors.Is(err, ErrBadImageKind) {
		t.Fatalf("bad kind: %v", err)
	}
}

func strPtr(s string) *string { return &s }
