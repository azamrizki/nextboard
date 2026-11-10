package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/version"
)

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func cmdUpdate(args []string) error {
	fs, _ := newFlags("update")
	repo := fs.String("repo", version.Repo, "GitHub repo owner/name")
	force := fs.Bool("force", false, "reinstall even if the version is the same, or older")
	tag := fs.String("version", "", "install this release, such as a beta (v0.4.0-beta.1), instead of the latest stable one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("GitHub repo not known; use --repo owner/nextboard")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	var rel *ghRelease
	var err error
	if *tag != "" {
		rel, err = releaseByTag(client, *repo, "v"+strings.TrimPrefix(*tag, "v"))
	} else {
		rel, err = latestRelease(client, *repo)
	}
	if err != nil {
		return err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	current := strings.TrimPrefix(version.Version, "v")
	if !*force {
		if latest == current {
			fmt.Println("Already on", version.Version)
			return nil
		}
		// The latest stable release can be older than a beta running here: going back is the
		// user's choice, never what a plain update does.
		if *tag == "" && newerVersion(current, latest) {
			fmt.Printf("This hub runs %s, which is newer than the latest stable release %s.\n", version.Version, rel.TagName)
			fmt.Printf("To go back to it: nextboard update --version %s --force\n", rel.TagName)
			return nil
		}
	}
	want := fmt.Sprintf("nextboard_%s_%s_%s.tar.gz", latest, runtime.GOOS, runtime.GOARCH)
	var archiveURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			archiveURL = a.URL
		case "checksums.txt":
			sumsURL = a.URL
		}
	}
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("asset %s or checksums.txt is missing from release %s", want, rel.TagName)
	}
	sums, err := fetch(client, sumsURL)
	if err != nil {
		return err
	}
	archive, err := fetch(client, archiveURL)
	if err != nil {
		return err
	}
	if err := verifyChecksum(sums, want, archive); err != nil {
		return err
	}
	bin, err := extractBinary(archive)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	tmp := self + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return fmt.Errorf("cannot write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, self); err != nil {
		os.Remove(tmp)
		return err
	}
	fmt.Printf("Updated %s → %s\n", version.Version, rel.TagName)
	if err := restartService(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: service restart failed:", err)
	}
	return nil
}

// latestRelease is the newest stable release: GitHub leaves pre-releases (betas) out of it.
func latestRelease(c *http.Client, repo string) (*ghRelease, error) {
	return release(c, "https://api.github.com/repos/"+repo+"/releases/latest")
}

func releaseByTag(c *http.Client, repo, tag string) (*ghRelease, error) {
	return release(c, "https://api.github.com/repos/"+repo+"/releases/tags/"+url.PathEscape(tag))
}

func release(c *http.Client, u string) (*ghRelease, error) {
	b, err := fetch(c, u)
	if err != nil {
		return nil, err
	}
	var rel ghRelease
	if err := json.Unmarshal(b, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// newerVersion reports whether a is a later version than b, by the rules of semver.org: the
// numbers first, then a pre-release (1.2.0-beta.1) comes before its release (1.2.0). A version
// that is not semver ("dev") is never newer.
func newerVersion(a, b string) bool {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := range 3 {
		if va.core[i] != vb.core[i] {
			return va.core[i] > vb.core[i]
		}
	}
	switch {
	case len(va.pre) == 0 || len(vb.pre) == 0:
		return len(va.pre) == 0 && len(vb.pre) > 0
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		x, y := va.pre[i], vb.pre[i]
		if x == y {
			continue
		}
		nx, errX := strconv.Atoi(x)
		ny, errY := strconv.Atoi(y)
		switch {
		case errX == nil && errY == nil:
			return nx > ny
		case errX == nil:
			return false // numbers sort before words
		case errY == nil:
			return true
		default:
			return x > y
		}
	}
	return len(va.pre) > len(vb.pre)
}

type semver struct {
	core [3]int
	pre  []string
}

func parseVersion(v string) (semver, bool) {
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	v, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var out semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		out.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return semver{}, false
		}
		out.pre = strings.Split(pre, ".")
	}
	return out, true
}

func fetch(c *http.Client, url string) ([]byte, error) {
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func verifyChecksum(sums []byte, name string, data []byte) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == name {
			if f[0] != got {
				return fmt.Errorf("checksum %s does not match", name)
			}
			return nil
		}
	}
	return fmt.Errorf("checksum for %s not found", name)
}

func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("nextboard binary not found in the archive")
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == "nextboard" && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}
