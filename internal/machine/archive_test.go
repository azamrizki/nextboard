package machine

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestArchiveRestoreRemove: an archived server is out of the list and never dials; restored,
// it is back where it was. Deleting one for good forgets the host keys the hub accepted for
// its address, unless another machine uses that address; ~/.ssh is not touched.
func TestArchiveRestoreRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.ssh/config of the machine running the test
	r := newTestRegistry(t)
	add := func(host string, port int) string {
		t.Helper()
		m, err := r.Add(AddRequest{Name: host, Host: host, Port: port, User: "alice", AuthMethod: "agent"})
		if err != nil {
			t.Fatal(err)
		}
		return m.ID()
	}
	a, b, c := add("dev-a.invalid", 2201), add("dev-b.invalid", 2202), add("dev-b.invalid", 2202)

	if _, err := r.Archive(LocalID); !errors.Is(err, ErrLocal) {
		t.Fatalf("archive This PC: %v", err)
	}
	in, err := r.Archive(a)
	if err != nil || in.ArchivedAt == nil || in.Status == StatusConnected {
		t.Fatalf("archive %v %+v", err, in)
	}
	ids := func(l []Info) string {
		var out []string
		for _, in := range l {
			out = append(out, in.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(r.List()); got != LocalID+","+b+","+c {
		t.Fatalf("active %s", got)
	}
	if got := ids(r.ListArchived()); got != a || !r.IsArchived(a) || r.IsArchived(b) {
		t.Fatalf("archived %s", got)
	}
	m, _ := r.SSH(a)
	if err := m.Connect(t.Context()); !errors.Is(err, ErrArchived) {
		t.Fatalf("connect archived: %v", err)
	}
	r.Close()
	again, err := OpenRegistry(r.st, r.box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	if !again.IsArchived(a) || ids(again.List()) != LocalID+","+b+","+c {
		t.Fatal("archived lost on reopen")
	}
	r = again
	in, err = r.Restore(a)
	if err != nil || in.ArchivedAt != nil {
		t.Fatalf("restore %v %+v", err, in)
	}
	if ids(r.List()) != LocalID+","+a+","+b+","+c {
		t.Fatalf("restored out of its place: %s", ids(r.List()))
	}
	if _, err := r.Restore(a); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("restore active: %v", err)
	}
	if _, err := r.Archive(a); err != nil { // it would keep dialling a made-up host
		t.Fatal(err)
	}

	kh := r.st.Path("known_hosts")
	lines := []string{
		"[dev-a.invalid]:2201 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA1",
		"[dev-b.invalid]:2202 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB2",
		"[dev-a.invalid]:2201,[dev-z.invalid]:22 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC3",
		"[dev-z.invalid]:22 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID4",
	}
	if err := os.WriteFile(kh, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	read := func() string { b, _ := os.ReadFile(kh); return string(b) }
	if err := r.Remove(b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(), "dev-b.invalid") {
		t.Fatal("a host key another machine uses went")
	}
	if err := r.Remove(a); err != nil {
		t.Fatal(err)
	}
	want := lines[1] + "\n[dev-z.invalid]:22 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC3\n" + lines[3] + "\n"
	if got := read(); got != want {
		t.Fatalf("known_hosts after a:\n%s\nwant:\n%s", got, want)
	}
	if err := r.Remove(c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), "dev-b.invalid") {
		t.Fatal("the last machine of an address went without its host key")
	}
}
