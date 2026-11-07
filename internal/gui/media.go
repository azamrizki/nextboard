package gui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Video and audio of a session (docs/API-gui.md §7). RFB keeps carrying input,
// cursor, clipboard and sharp text at rest, but a scrolling page costs it over
// 100 Mbps. A hardware encoded stream carries big motion in a fraction of that:
// H.264, which every browser decodes well, or AV1, which gives a better picture
// at 40 % less bitrate where the GPU can encode and the viewer can decode it.
//
// Each viewer gets its own encoder, tied to its WebSocket: nothing is left to
// adopt after a hub restart, because the viewer simply connects again.

const (
	maxEncoders = 16 // video and audio together, for maxSessions tiles; this host also runs other services

	videoFPS     = 60
	minVideoMbps = 1.0
	maxVideoMbps = 30.0
	// Bits per pixel set the default rate from the frame area. Measured on
	// scrolling text, AV1 at 0.06 looks better than H.264 at 0.1 (SSIM 0.994
	// against 0.991).
	h264BitsPerPixel = 0.1
	av1BitsPerPixel  = 0.06

	// CodecH264 and CodecAV1 name the video formats.
	CodecH264 = "h264"
	CodecAV1  = "av1"

	// AudioFrameBytes is 20 ms of 48 kHz stereo s16le.
	AudioFrameBytes = 3840
)

// Stream is the output of one running encoder.
type Stream struct {
	Out   io.Reader
	Mbps  float64 // the video rate in use; 0 for audio
	Codec string  // the video format; "" for audio

	cmd     *exec.Cmd
	release func()
	once    sync.Once
}

// Stop ends the encoder.
func (st *Stream) Stop() {
	st.once.Do(func() {
		if st.cmd.Process != nil {
			_ = st.cmd.Process.Kill()
		}
		_ = st.cmd.Wait()
		st.release()
	})
}

// Pictures calls emit for every picture of a video stream.
func (st *Stream) Pictures(emit func(pic []byte, key bool) error) error {
	if st.Codec == CodecAV1 {
		return SplitTemporalUnits(st.Out, emit)
	}
	return SplitAccessUnits(st.Out, emit)
}

// encoderArgs finds out once what the GPU can do: Arc only encodes H.264 in low
// power mode, while other GPUs may not have that mode at all.
func (m *Manager) encoderArgs() ([]string, string) {
	m.probeOnce.Do(func() {
		node := renderNode()
		ffmpeg, ok := lookIn(m.pathEnv(), "ffmpeg")
		if node == "" || !ok {
			return
		}
		works := func(enc ...string) bool {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			args := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-vaapi_device", node,
				"-f", "lavfi", "-i", "color=black:s=320x240:r=30", "-frames:v", "1",
				"-vf", "format=nv12,hwupload"}, enc...)
			args = append(args, "-f", "null", "-")
			cmd := exec.CommandContext(ctx, ffmpeg, args...)
			cmd.Env = m.baseEnv()
			return cmd.Run() == nil
		}
		for _, cand := range [][]string{{"-low_power", "1"}, {}} {
			if works(append([]string{"-c:v", "h264_vaapi"}, cand...)...) {
				m.encArgs, m.encNode = cand, node
				m.encOK = true
				m.encAV1 = works("-c:v", "av1_vaapi")
				return
			}
		}
	})
	return m.encArgs, m.encNode
}

// videoCodecs lists what the encoder can do, best first.
func (m *Manager) videoCodecs() []string {
	m.encoderArgs()
	if m.encAV1 {
		return []string{CodecAV1, CodecH264}
	}
	return []string{CodecH264}
}

func (m *Manager) videoAvailable() bool {
	m.encoderArgs()
	return m.encOK
}

// audioAvailable reports whether sessions can have their own PipeWire sink.
func (m *Manager) audioAvailable() bool {
	for _, b := range []string{"pw-loopback", "pw-record"} {
		if _, ok := lookIn(m.pathEnv(), b); !ok {
			return false
		}
	}
	_, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "pipewire-0"))
	return err == nil
}

func sinkName(id string) string   { return "nextboard_" + id }
func sourceName(id string) string { return sinkName(id) + "_out" }

// startAudio gives the session a sink of its own, which lives exactly as long
// as this process. The priority keeps it from ever becoming the machine's
// default output.
func (m *Manager) startAudio(s *session) {
	if !m.audioAvailable() {
		return
	}
	bin, _ := lookIn(m.pathEnv(), "pw-loopback")
	id := s.pub.ID
	argv := []string{bin, "--channels", "2",
		"--capture-props", fmt.Sprintf("{ media.class=Audio/Sink node.name=%s node.description=%s priority.session=0 node.dont-fallback=true }", sinkName(id), sinkName(id)),
		"--playback-props", fmt.Sprintf("{ media.class=Audio/Source node.name=%s node.description=%s priority.session=0 }", sourceName(id), sourceName(id)),
	}
	if _, err := m.spawn(s, argv, s.dir, m.baseEnv(), "audio.log", nil); err != nil {
		return // the app still runs, silently
	}
	m.mu.Lock()
	s.pub.Audio = true
	m.mu.Unlock()
}

// RFBCounter returns the running count of bytes the session's displays sent
// over RFB. It rises when the screen moves, also while the encoder sleeps.
func (m *Manager) RFBCounter(id string) (*atomic.Int64, error) {
	s, err := m.liveSession(id)
	if err != nil {
		return nil, err
	}
	return &s.rfbBytes, nil
}

// liveSession returns a session that can still be connected to.
func (m *Manager) liveSession(id string) (*session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.stopping {
		return nil, notFound()
	}
	return s, nil
}

// takeEncoder claims a slot; the returned func gives it back.
func (m *Manager) takeEncoder() (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.encoders >= maxEncoders {
		return nil, newErr(http.StatusConflict, "too_many_encoders", "at most %d video and audio streams at once", maxEncoders)
	}
	m.encoders++
	return func() {
		m.mu.Lock()
		m.encoders--
		m.mu.Unlock()
	}, nil
}

func (m *Manager) startStream(s *session, argv, env []string, mbps float64, codec string) (*Stream, error) {
	release, err := m.takeEncoder()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = s.dir
	// Tagged like the rest of the session, so its teardown ends it too.
	cmd.Env = append(env, markerEnv+"="+s.pub.ID)
	out, err := cmd.StdoutPipe()
	if err != nil {
		release()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		release()
		return nil, err
	}
	return &Stream{Out: out, Mbps: mbps, Codec: codec, cmd: cmd, release: release}, nil
}

// videoRate picks the bitrate: the viewer's request, or one from the frame area
// and format, both within what a remote link and the encoder can take.
func videoRate(w, h int, codec string, requested float64) float64 {
	if requested <= 0 {
		bpp := h264BitsPerPixel
		if codec == CodecAV1 {
			bpp = av1BitsPerPixel
		}
		requested = float64(w*h) * videoFPS * bpp / 1e6
	}
	return max(minVideoMbps, min(maxVideoMbps, requested))
}

// VideoStream starts a hardware encoder on the session's display, in the wanted
// format if the GPU has it, else H.264; Stream.Codec says which. The frame is
// rounded down to even numbers, as 4:2:0 video requires.
func (m *Manager) VideoStream(id string, w, h int, mbps float64, codec string) (*Stream, error) {
	s, err := m.liveSession(id)
	if err != nil {
		return nil, err
	}
	cand, node := m.encoderArgs()
	if !m.encOK {
		return nil, newErr(http.StatusServiceUnavailable, "video_unavailable", "hardware video encoder not available")
	}
	if codec != CodecAV1 || !m.encAV1 {
		codec = CodecH264
	}
	w, h = clamp(w, 64, 3840, defaultW)&^1, clamp(h, 64, 2160, defaultH)&^1
	rate := videoRate(w, h, codec, mbps)
	if s.pub.Display == DisplayWayland {
		return m.waylandVideoStream(s, w, h, rate, codec, cand, node)
	}
	bits := func(f float64) string { return strconv.Itoa(int(f * 1e6)) }
	ffmpeg, _ := lookIn(m.pathEnv(), "ffmpeg")
	argv := []string{ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-vaapi_device", node,
		"-f", "x11grab", "-framerate", strconv.Itoa(videoFPS), "-video_size", fmt.Sprintf("%dx%d", w, h),
		// The viewer's RFB connection draws the cursor, without any delay.
		"-draw_mouse", "0", "-i", fmt.Sprintf(":%d", s.display),
		// Color conversion stays on the CPU: on Arc, uploading RGB and converting
		// with scale_vaapi shears the picture at widths like 992 and shifts colors.
		// The conversion is tagged, so the browser reads the colors right.
		"-vf", "scale=out_color_matrix=bt709:out_range=tv,format=nv12,hwupload"}
	if codec == CodecAV1 {
		argv = append(argv, "-c:v", "av1_vaapi")
	} else {
		argv = append(argv, "-c:v", "h264_vaapi")
		argv = append(argv, cand...)
		// A delimiter on every frame, which is how SplitAccessUnits finds them.
		argv = append(argv, "-profile:v", "high", "-aud", "1")
	}
	argv = append(argv,
		// No frames waiting inside the encoder: every one of them is delay.
		"-async_depth", "1",
		"-bf", "0", "-g", "600",
		"-rc_mode", "VBR", "-b:v", bits(rate), "-maxrate", bits(rate*1.5), "-bufsize", bits(rate/2),
		"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv",
		"-fflags", "+nobuffer", "-flags", "low_delay", "-flush_packets", "1")
	if codec == CodecAV1 {
		argv = append(argv, "-f", "obu", "pipe:1")
	} else {
		argv = append(argv, "-f", "h264", "pipe:1")
	}
	env := append(m.baseEnv(), fmt.Sprintf("DISPLAY=:%d", s.display), "XAUTHORITY="+filepath.Join(s.dir, "xauth"))
	return m.startStream(s, argv, env, rate, codec)
}

// AudioStream starts recording the session's sink as raw 48 kHz stereo s16le.
func (m *Manager) AudioStream(id string) (*Stream, error) {
	s, err := m.liveSession(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	has := s.pub.Audio
	m.mu.Unlock()
	if !has {
		return nil, newErr(http.StatusServiceUnavailable, "audio_unavailable", "this session has no audio")
	}
	bin, _ := lookIn(m.pathEnv(), "pw-record")
	argv := []string{bin, "--target", sourceName(id), "--raw", "--format", "s16", "--rate", "48000", "--channels", "2", "--latency", "20ms", "-"}
	return m.startStream(s, argv, m.baseEnv(), 0, "")
}

// AV1 OBU types that matter here (AV1 specification §5.3.2).
const (
	obuSequenceHeader         = 1
	obuTemporalDelimiter      = 2
	obuTileGroup              = 4
	obuFrame                  = 6
	obuMaxSizeBytes           = 8
	obuHasExtension      byte = 1 << 2
	obuHasSize           byte = 1 << 1
)

// SplitTemporalUnits reads a low overhead AV1 stream and calls emit once per
// picture. Unlike H.264 these streams can't be searched for a marker, as any
// byte value can appear in a payload, so it walks the OBU sizes.
//
// To save a frame of latency a picture doesn't wait for the next delimiter when
// it is clearly complete: after a frame OBU, or after a tile group when nothing
// more of the encoder's packet has arrived. If more does follow, the delimiter
// still ends the picture.
func SplitTemporalUnits(r io.Reader, emit func(tu []byte, key bool) error) error {
	br := bufio.NewReaderSize(r, 256<<10)
	var tu []byte
	key, pending := false, false
	flush := func() error {
		err := emit(append([]byte(nil), tu...), key)
		tu, key, pending = tu[:0], false, false
		return err
	}
	for {
		h, err := br.ReadByte()
		if err != nil {
			return err
		}
		if h&obuHasSize == 0 {
			return errors.New("av1: OBU without a size")
		}
		typ := h >> 3 & 0xf
		head := []byte{h}
		if h&obuHasExtension != 0 {
			e, err := br.ReadByte()
			if err != nil {
				return err
			}
			head = append(head, e)
		}
		size := 0
		for i := 0; ; i++ {
			b, err := br.ReadByte()
			if err != nil {
				return err
			}
			if i == obuMaxSizeBytes {
				return errors.New("av1: invalid OBU size")
			}
			head = append(head, b)
			size |= int(b&0x7f) << (7 * i)
			if b&0x80 == 0 {
				break
			}
		}
		if size < 0 || size > 64<<20 {
			return errors.New("av1: invalid OBU size")
		}
		if typ == obuTemporalDelimiter {
			if pending {
				if err := flush(); err != nil {
					return err
				}
			}
			// A new picture starts. What came after the last one belongs to none.
			tu, key = tu[:0], false
		}
		start := len(tu)
		tu = append(tu, head...)
		tu = append(tu, make([]byte, size)...)
		if _, err := io.ReadFull(br, tu[start+len(head):]); err != nil {
			return err
		}
		if typ == obuSequenceHeader {
			key = true
		}
		if typ == obuFrame || typ == obuTileGroup {
			pending = true
		}
		if typ == obuFrame || (typ == obuTileGroup && br.Buffered() == 0) {
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

var audStart = []byte{0, 0, 1, 9}

// SplitAccessUnits reads an Annex B stream and calls emit once per frame, with
// whether it can start decoding. A frame ends where the next delimiter starts,
// so one frame of latency is the price of finding its end.
func SplitAccessUnits(r io.Reader, emit func(au []byte, key bool) error) error {
	buf := make([]byte, 0, 1<<20)
	chunk := make([]byte, 128<<10)
	for {
		n, err := r.Read(chunk)
		buf = append(buf, chunk[:n]...)
		for {
			first := bytes.Index(buf, audStart)
			if first < 0 {
				// Keep what may be the beginning of a start code.
				if len(buf) > 3 {
					buf = append(buf[:0], buf[len(buf)-3:]...)
				}
				break
			}
			next := bytes.Index(buf[first+len(audStart):], audStart)
			if next < 0 {
				buf = append(buf[:0], buf[first:]...)
				break
			}
			end := first + len(audStart) + next
			au := append([]byte(nil), buf[first:end]...)
			if e := emit(au, isKeyFrame(au)); e != nil {
				return e
			}
			buf = append(buf[:0], buf[end:]...)
		}
		if err != nil {
			return err
		}
	}
}

// isKeyFrame looks at the first picture of a frame: an IDR slice is type 5.
func isKeyFrame(au []byte) bool {
	start := []byte{0, 0, 1}
	for off := 0; off+3 < len(au); {
		i := bytes.Index(au[off:], start)
		if i < 0 || off+i+3 >= len(au) {
			return false
		}
		off += i + 3
		switch au[off] & 0x1f {
		case 5:
			return true
		case 1:
			return false
		}
	}
	return false
}
