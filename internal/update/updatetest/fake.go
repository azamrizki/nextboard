// Package updatetest is a fake GitHub for tests of the update: releases of one repository, their
// archives and checksums.txt, on an httptest server. Nothing reaches github.com.
package updatetest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/azamrizki/nextboard/internal/update"
)

// Fake answers the GitHub API's release calls and the downloads of one repository.
type Fake struct {
	*httptest.Server
	Repo string

	mu       sync.Mutex
	releases []update.Release
	files    map[string][]byte // download path → body
	requests []string
}

// New starts a fake GitHub for repo; it stops with the test.
func New(t *testing.T, repo string) *Fake {
	t.Helper()
	f := &Fake{Repo: repo, files: map[string][]byte{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// Client is an update client pointed at the fake.
func (f *Fake) Client() *update.Client {
	return &update.Client{HTTP: f.Server.Client(), Repo: f.Repo, API: f.URL, Downloads: f.URL}
}

// Publish adds a release whose archive for this OS and architecture holds exe as nextboard.
func (f *Fake) Publish(t *testing.T, tag string, prerelease bool, exe []byte) {
	t.Helper()
	name := update.AssetName(tag)
	archive := Archive(t, exe)
	sum := sha256.Sum256(archive)
	base := "/" + f.Repo + "/releases/download/" + tag + "/"
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[base+name] = archive
	f.files[base+"checksums.txt"] = []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	f.releases = append(f.releases, update.Release{
		TagName: tag, Prerelease: prerelease, HTMLURL: f.URL + "/" + f.Repo + "/releases/tag/" + tag,
		Assets: []update.Asset{
			{Name: name, URL: f.URL + base + name, Size: int64(len(archive))},
			{Name: "checksums.txt", URL: f.URL + base + "checksums.txt"},
		},
	})
}

// Draft adds a draft release, which no client may pick.
func (f *Fake) Draft(tag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, update.Release{TagName: tag, Draft: true})
}

// Tamper replaces a release's archive after its checksum was written.
func (f *Fake) Tamper(tag string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files["/"+f.Repo+"/releases/download/"+tag+"/"+update.AssetName(tag)] = body
}

// MoveAsset points a release's archive at another URL.
func (f *Fake) MoveAsset(tag, url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.releases {
		if f.releases[i].TagName == tag {
			f.releases[i].Assets[0].URL = url
		}
	}
}

// Requests is every path asked so far.
func (f *Fake) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.RequestURI())
	api := "/repos/" + f.Repo + "/releases"
	switch p := r.URL.Path; {
	case p == api:
		// newest first, as GitHub lists them
		out := make([]update.Release, 0, len(f.releases))
		for i := len(f.releases) - 1; i >= 0; i-- {
			out = append(out, f.releases[i])
		}
		_ = json.NewEncoder(w).Encode(out)
	case p == api+"/latest":
		for i := len(f.releases) - 1; i >= 0; i-- {
			if rel := f.releases[i]; !rel.Draft && !rel.Prerelease {
				_ = json.NewEncoder(w).Encode(rel)
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(p, api+"/tags/"):
		tag := strings.TrimPrefix(p, api+"/tags/")
		for _, rel := range f.releases {
			if rel.TagName == tag && !rel.Draft {
				_ = json.NewEncoder(w).Encode(rel)
				return
			}
		}
		http.NotFound(w, r)
	default:
		body, ok := f.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	}
}

// Archive is a release archive holding exe as nextboard, beside a README as the real ones have.
func Archive(t *testing.T, exe []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
		mode int64
	}{{"README.md", []byte("# NextBoard\n"), 0o644}, {"nextboard", exe, 0o755}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
