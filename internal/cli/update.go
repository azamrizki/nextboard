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
	"os"
	"path/filepath"
	"runtime"
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
	repo := fs.String("repo", version.Repo, "repo GitHub owner/nama")
	force := fs.Bool("force", false, "pasang ulang walau versinya sama")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("repo GitHub belum diketahui; pakai --repo owner/nextboard")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	rel, err := latestRelease(client, *repo)
	if err != nil {
		return err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if !*force && latest == strings.TrimPrefix(version.Version, "v") {
		fmt.Println("Sudah versi terbaru:", version.Version)
		return nil
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
		return fmt.Errorf("aset %s atau checksums.txt tidak ada di rilis %s", want, rel.TagName)
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
		return fmt.Errorf("tidak bisa menulis %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, self); err != nil {
		os.Remove(tmp)
		return err
	}
	fmt.Printf("Diperbarui %s → %s\n", version.Version, rel.TagName)
	if err := restartService(); err != nil {
		fmt.Fprintln(os.Stderr, "peringatan: restart service gagal:", err)
	}
	return nil
}

func latestRelease(c *http.Client, repo string) (*ghRelease, error) {
	b, err := fetch(c, "https://api.github.com/repos/"+repo+"/releases/latest")
	if err != nil {
		return nil, err
	}
	var rel ghRelease
	if err := json.Unmarshal(b, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
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
				return fmt.Errorf("checksum %s tidak cocok", name)
			}
			return nil
		}
	}
	return fmt.Errorf("checksum untuk %s tidak ditemukan", name)
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
			return nil, errors.New("binary nextboard tidak ada di arsip")
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == "nextboard" && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}
