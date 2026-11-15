// Package art makes a project's cover and icon with an image tool on This PC
// (docs/API-board.md §1, "Create with AI"). The only tool now is Codex's built-in image
// generation, run headless. What goes to the tool is a short brief of the project from its
// own files; what comes back is hostile input: it is never executed, only a file it names
// inside the tool's own image folder is read, and that file must be a real PNG or JPEG.
package art

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"

	"golang.org/x/image/draw"
)

// Kinds of image.
const (
	KindCover = "cover"
	KindIcon  = "icon"
)

// Limits of what is made and kept.
const (
	CoverMaxWidth = 1600
	IconSize      = 256
	// MaxImageBytes bounds the file a tool produced; maxPixels its decoded size.
	MaxImageBytes = 25 << 20
	maxSide       = 8192
)

// Capability is GET /api/art/capability.
type Capability struct {
	Available bool   `json:"available"`
	Tool      string `json:"tool"`
	Reason    string `json:"reason"`
}

// Tool makes one image from a prompt. Tools are tried in order; only Codex exists now.
type Tool interface {
	Name() string
	// Check says whether the tool can make images now, and why not.
	Check(ctx context.Context) Capability
	// Generate makes one image and returns the bytes of the file it produced.
	Generate(ctx context.Context, kind, prompt string, progress func(phase, text string)) ([]byte, error)
}

// Pick returns the first tool that can make images, or the reason of the first one.
func Pick(ctx context.Context, tools []Tool) (Tool, Capability) {
	var first Capability
	for i, t := range tools {
		c := t.Check(ctx)
		if c.Available {
			return t, c
		}
		if i == 0 {
			first = c
		}
	}
	if len(tools) == 0 {
		return nil, Capability{Reason: "no image tool is known"}
	}
	return nil, first
}

// Error is a failure with a stable code, for the job's code.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func fail(code, format string, a ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Code is err's code ("failed" when it has none).
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "failed"
}

// Image is what is stored: encoded, at its final size.
type Image struct {
	Data   []byte
	Type   string // image/png | image/jpeg
	Width  int
	Height int
}

// Process checks that raw is a PNG or JPEG of sane size and makes the stored form: a cover at
// most CoverMaxWidth wide (JPEG), an icon IconSize square from its centre (PNG).
func Process(kind string, raw []byte) (Image, error) {
	if len(raw) == 0 || len(raw) > MaxImageBytes {
		return Image{}, fail("bad_image", "the image file is empty or too large")
	}
	switch http.DetectContentType(raw) {
	case "image/png", "image/jpeg":
	default:
		return Image{}, fail("bad_image", "the produced file is not a PNG or JPEG image")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxSide || cfg.Height > maxSide {
		return Image{}, fail("bad_image", "the image cannot be read or is too large")
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Image{}, fail("bad_image", "the image cannot be read: %v", err)
	}
	var out bytes.Buffer
	switch kind {
	case KindIcon:
		b := src.Bounds()
		side := min(b.Dx(), b.Dy())
		x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
		crop := image.Rect(x0, y0, x0+side, y0+side)
		dst := image.NewNRGBA(image.Rect(0, 0, IconSize, IconSize))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
		if err := png.Encode(&out, dst); err != nil {
			return Image{}, err
		}
		return Image{Data: out.Bytes(), Type: "image/png", Width: IconSize, Height: IconSize}, nil
	case KindCover:
		b := src.Bounds()
		w, h := b.Dx(), b.Dy()
		var img image.Image = src
		if w > CoverMaxWidth {
			h = max(1, h*CoverMaxWidth/w)
			w = CoverMaxWidth
			dst := image.NewRGBA(image.Rect(0, 0, w, h))
			draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
			img = dst
		}
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 86}); err != nil {
			return Image{}, err
		}
		return Image{Data: out.Bytes(), Type: "image/jpeg", Width: w, Height: h}, nil
	}
	return Image{}, fail("bad_kind", "unknown image kind %q", kind)
}

// Make is the whole job: the brief of the project, the prompt, one image from the tool, and
// its stored form.
func Make(ctx context.Context, t Tool, kind string, src Source, progress func(phase, text string)) (Image, error) {
	if kind != KindCover && kind != KindIcon {
		return Image{}, fail("bad_kind", "unknown image kind %q", kind)
	}
	if progress == nil {
		progress = func(string, string) {}
	}
	progress("reading", "Reading the project…")
	brief := Brief(ctx, src)
	progress("generating", "Creating the image…")
	raw, err := t.Generate(ctx, kind, Prompt(kind, brief), progress)
	if err != nil {
		return Image{}, err
	}
	progress("saving", "Saving…")
	return Process(kind, raw)
}
