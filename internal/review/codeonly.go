package review

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Review is for code (docs/API-studio.md §7.10). Images, audio, video, fonts, archives, office
// documents and other binary files take no part: a diff of them says nothing, git keeps their
// history, and they were most of what a copy of a project weighed. A file with none of these
// extensions that holds a NUL byte in its first 8 KB is binary as well.

// assetExts are the extensions of files that are no code, lower case and without the dot.
var assetExts = func() map[string]bool {
	m := map[string]bool{}
	for _, group := range []string{
		// images and design files (svg is text, and stays)
		"png jpg jpeg jpe jfif gif webp bmp ico icns cur tif tiff psd psb heic heif avif jxl raw cr2 cr3 nef arw dng xcf ai eps sketch fig xd kra",
		// audio and video (not ts: that is TypeScript)
		"mp3 wav ogg oga opus flac aac m4a wma aif aiff mid midi amr mp4 m4v mov avi mkv webm wmv flv mpg mpeg 3gp ogv",
		// fonts
		"woff woff2 ttf otf eot ttc",
		// archives and packages
		"zip gz tgz bz2 xz zst 7z rar tar lz lzma lz4 cab jar war ear apk aab ipa dmg iso deb rpm msi",
		// programs and their parts
		"exe dll so dylib a o lib pdb class pyc pyo wasm node bin",
		// office documents
		"pdf doc docx xls xlsx ppt pptx odt ods odp epub",
		// data and models
		"sqlite sqlite3 db mdb parquet avro npy npz pkl pickle h5 hdf5 onnx pt pth ckpt safetensors tflite",
		// 3D
		"glb fbx blend stl usdz",
	} {
		for _, e := range strings.Fields(group) {
			m[e] = true
		}
	}
	return m
}()

// isAsset says the path names no code, by its extension.
func isAsset(rel string) bool {
	ext := path.Ext(rel)
	if len(ext) < 2 {
		return false
	}
	return assetExts[strings.ToLower(ext[1:])]
}

// assetExcludes are the exclude lines for the shadow repository: git status never lists such a
// file, so a folder of images costs no snapshot anything. Git matches case by case on Linux.
var assetExcludes = func() []string {
	out := make([]string, 0, len(assetExts))
	for e := range assetExts {
		var b strings.Builder
		b.WriteString("*.")
		for _, r := range e {
			if r >= 'a' && r <= 'z' {
				b.WriteString("[" + string(r) + string(r-'a'+'A') + "]")
			} else {
				b.WriteRune(r)
			}
		}
		out = append(out, b.String())
	}
	return out
}()

// binaryFile says the file at abs holds a NUL byte in its first 8 KB.
func binaryFile(abs string) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [8 << 10]byte
	n, _ := io.ReadFull(f, b[:])
	return looksBinary(b[:n])
}

// codeOnlyMark is a file in a shadow that says its history holds no assets any more.
const codeOnlyMark = "nb-code-only"

// codeOnlyLimit bounds the one rewrite of a shadow's history.
const codeOnlyLimit = 10 * time.Minute

// codeOnlyDone: the shadow was made for code, or has been cleaned. One not made yet will be.
func (sh shadow) codeOnlyDone() bool {
	if _, err := os.Stat(filepath.Join(sh.gitDir, codeOnlyMark)); err == nil {
		return true
	}
	return !sh.exists()
}

func (sh shadow) exists() bool {
	_, err := os.Stat(filepath.Join(sh.gitDir, "HEAD"))
	return err == nil
}

func (sh shadow) markCodeOnly() {
	if f, err := os.Create(filepath.Join(sh.gitDir, codeOnlyMark)); err == nil {
		f.Close()
	}
}

// codeOnly takes the assets out of a shadow made before review was for code: out of its index,
// out of every snapshot in its history, and then off the disk. The snapshots keep their code,
// their times and who they name; only their commit ids change. False: try again later.
func (sh shadow) codeOnly(ctx context.Context, now time.Time) bool {
	if !sh.exists() {
		sh.markCodeOnly()
		return true
	}
	ls, err := sh.git(ctx, nil, "ls-files", "-z")
	if err != nil {
		return false
	}
	var drop bytes.Buffer
	for _, rel := range strings.Split(string(ls), "\x00") {
		if rel != "" && isAsset(rel) {
			drop.WriteString(rel)
			drop.WriteByte(0)
		}
	}
	if drop.Len() > 0 {
		if _, err := sh.git(ctx, drop.Bytes(), "rm", "--cached", "-f", "-q", "--ignore-unmatch", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return false
		}
	}
	rewrote := false
	if _, err := sh.git(ctx, nil, "rev-parse", "-q", "--verify", historyRef); err == nil {
		exp, err := sh.git(ctx, nil, "fast-export", "--no-data", "--use-done-feature", "--signed-tags=strip", historyRef)
		if err != nil {
			return false
		}
		kept, dropped, err := withoutAssets(exp)
		if err != nil {
			return false
		}
		if dropped {
			if _, err := sh.git(ctx, kept, "fast-import", "--quiet", "--force", "--done"); err != nil {
				return false
			}
			rewrote = true
		}
	}
	if rewrote || drop.Len() > 0 {
		// the old snapshots are unreachable now: the cut a prune made names one of them
		_ = os.Remove(filepath.Join(sh.gitDir, "shallow"))
		_, _ = sh.git(ctx, nil, "reflog", "expire", "--expire=now", "--all")
		if _, err := sh.git(ctx, nil, "-c", "pack.threads=1", "-c", "pack.windowMemory=64m", "gc", "--quiet", "--prune=now"); err != nil {
			return false
		}
		if f, err := os.Create(filepath.Join(sh.gitDir, prunedMark)); err == nil {
			f.Close()
		}
		_ = os.Chtimes(filepath.Join(sh.gitDir, prunedMark), now, now)
	}
	sh.markCodeOnly()
	return true
}

// withoutAssets drops the lines of a fast-export stream that add or delete an asset. A data
// block is copied by its length, so a message is never read as a command.
func withoutAssets(stream []byte) ([]byte, bool, error) {
	r := bufio.NewReader(bytes.NewReader(stream))
	var out bytes.Buffer
	out.Grow(len(stream))
	dropped := false
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 && errors.Is(err, io.EOF) {
			return out.Bytes(), dropped, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		s := string(bytes.TrimSuffix(line, []byte("\n")))
		if n, ok := strings.CutPrefix(s, "data "); ok {
			size, perr := strconv.Atoi(n)
			if perr != nil || size < 0 {
				return nil, false, errors.New("fast-export: a data block without a length")
			}
			out.Write(line)
			if _, err := io.CopyN(&out, r, int64(size)); err != nil {
				return nil, false, err
			}
			continue
		}
		if p, ok := exportedPath(s); ok && isAsset(p) {
			dropped = true
			continue
		}
		out.Write(line)
	}
}

// exportedPath is the path of an "M <mode> <blob> <path>" or "D <path>" line.
func exportedPath(s string) (string, bool) {
	var p string
	switch {
	case strings.HasPrefix(s, "M "):
		f := strings.SplitN(s, " ", 4)
		if len(f) < 4 {
			return "", false
		}
		p = f[3]
	case strings.HasPrefix(s, "D "):
		p = s[2:]
	default:
		return "", false
	}
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			p = u
		}
	}
	return p, true
}
