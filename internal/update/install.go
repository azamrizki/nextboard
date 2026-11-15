package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// maxArchive bounds a download and the binary in it.
const maxArchive = 200 << 20

// Phases an Install reports.
const (
	PhaseDownload = "download"
	PhaseVerify   = "verify"
	PhaseSwap     = "swap"
)

// Progress hears an Install's phase; pct is 0–100 while downloading, -1 otherwise.
type Progress func(phase, text string, pct float64)

// AssetName is the archive of a release for this OS and architecture.
func AssetName(tag string) string {
	return fmt.Sprintf("nextboard_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
}

// OldPath is where Install keeps the binary it replaced, for a manual rollback.
func OldPath(bin string) string { return bin + ".old" }

// Install downloads rel's archive for this machine, checks it against the release's
// checksums.txt and puts the nextboard binary in it in place of bin: written next to bin, the
// binary it replaces kept as bin.old, then renamed over bin in one step. Nothing from the
// download runs. Assets must come from c.Repo's own releases.
func (c *Client) Install(ctx context.Context, rel *Release, bin string, progress Progress) error {
	if progress == nil {
		progress = func(string, string, float64) {}
	}
	want := AssetName(rel.TagName)
	var archive, sums *Asset
	for i := range rel.Assets {
		switch rel.Assets[i].Name {
		case want:
			archive = &rel.Assets[i]
		case "checksums.txt":
			sums = &rel.Assets[i]
		}
	}
	if archive == nil || sums == nil {
		return fmt.Errorf("asset %s or checksums.txt is missing from release %s", want, rel.TagName)
	}
	for _, a := range []*Asset{archive, sums} {
		if err := c.ownAsset(a.URL); err != nil {
			return err
		}
	}
	progress(PhaseDownload, "Downloading "+want+"…", 0)
	sumsBody, err := c.fetch(ctx, sums.URL, 1<<20, nil)
	if err != nil {
		return err
	}
	last := -1
	data, err := c.fetch(ctx, archive.URL, maxArchive, func(got, total int64) {
		if total <= 0 {
			return
		}
		if pct := int(got * 100 / total); pct != last {
			last = pct
			progress(PhaseDownload, "Downloading "+want+"…", float64(pct))
		}
	})
	if err != nil {
		return err
	}
	progress(PhaseVerify, "Checking the SHA-256 against checksums.txt…", -1)
	if err := VerifyChecksum(sumsBody, want, data); err != nil {
		return err
	}
	exe, err := ExtractBinary(data)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	progress(PhaseSwap, "Replacing the binary (the old one stays as "+filepath.Base(OldPath(bin))+")…", -1)
	return Replace(bin, exe)
}

// ownAsset refuses a download from anywhere but the repository's releases.
func (c *Client) ownAsset(u string) error {
	prefix := strings.ToLower(c.Downloads + "/" + c.Repo + "/releases/download/")
	if !strings.HasPrefix(strings.ToLower(u), prefix) {
		return fmt.Errorf("refusing %s: not a download of %s's releases", u, c.Repo)
	}
	return nil
}

// Writable says whether a binary in dir can be replaced: the new one is written next to it.
func Writable(dir string) error {
	f, err := os.CreateTemp(dir, ".nextboard-update-check-*")
	if err != nil {
		return fmt.Errorf("the folder %s is not writable", dir)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// Replace puts exe in place of bin: written to bin.new, bin kept as bin.old (a hard link, or a
// copy where links do not work), then bin.new renamed over bin, so bin is never half written.
func Replace(bin string, exe []byte) error {
	tmp := bin + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", tmp, err)
	}
	_, err = f.Write(exe)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cannot write %s: %w", tmp, err)
	}
	old := OldPath(bin)
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Remove(tmp)
		return fmt.Errorf("cannot replace %s: %w", old, err)
	}
	if err := os.Link(bin, old); err != nil {
		if err := copyFile(bin, old); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("cannot keep the old binary as %s: %w", old, err)
		}
	}
	if err := os.Rename(tmp, bin); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(to)
		return err
	}
	return out.Close()
}

// VerifyChecksum checks data against name's line in a checksums.txt.
func VerifyChecksum(sums []byte, name string, data []byte) error {
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

// ExtractBinary is the nextboard file of a release archive.
func ExtractBinary(archive []byte) ([]byte, error) {
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
			return io.ReadAll(io.LimitReader(tr, maxArchive))
		}
	}
}
