package gui

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The zero copy display (docs/API-gui.md §9). With Xtigervnc a picture goes from
// the GPU to the CPU and back: the app renders on the GPU, the X server reads it
// out, ffmpeg copies and converts it and uploads it for the encoder. That costs
// over a core for one scrolling tile. With a headless compositor apps hand it GPU
// buffers, and wf-recorder passes those to the encoder without leaving the GPU.
//
// wayvnc keeps the RFB side as it was: input, cursor, clipboard, resize, and the
// sharp picture at rest. It talks to the same viewer through the same bridge.

const (
	// DisplayWayland and DisplayX11 name the kinds of display a session can have.
	DisplayWayland = "wayland"
	DisplayX11     = "x11"

	// swayOutput is the one output of a headless sway.
	swayOutput = "HEADLESS-1"
)

// waylandAvailable reports whether sessions can have the zero copy display.
// Without a working encoder there would be nothing to gain from it.
func (m *Manager) waylandAvailable() bool {
	if m.forceX11 || os.Getenv("NEXTBOARD_GUI_BACKEND") == DisplayX11 {
		return false
	}
	for _, b := range []string{"sway", "wayvnc", "wf-recorder"} {
		if _, ok := lookIn(m.pathEnv(), b); !ok {
			return false
		}
	}
	return renderNode() != "" && m.videoAvailable()
}

func (m *Manager) displayKind() string {
	if m.waylandAvailable() {
		return DisplayWayland
	}
	return DisplayX11
}

// runDir holds the compositor's sockets, private to the session. It is short
// and in tmpfs where possible: socket paths stop at 108 bytes.
func runDirFor(sessionDir, id string) string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "nb-"+id)
	}
	return filepath.Join(sessionDir, "run")
}

func (s *session) runDir() string   { return runDirFor(s.dir, s.pub.ID) }
func (s *session) swaySock() string { return filepath.Join(s.runDir(), "sway.sock") }

// waylandSocket finds the compositor's socket; "" until it exists.
func (s *session) waylandSocket() string {
	m, _ := filepath.Glob(filepath.Join(s.runDir(), "wayland-*"))
	for _, p := range m {
		if !strings.HasSuffix(p, ".lock") {
			return p
		}
	}
	return ""
}

// compositorEnv is what sway, wayvnc and wf-recorder run with.
func (s *session) compositorEnv(base []string) []string {
	wm := filepath.Join(s.dir, "wm")
	return append(base, "SWAYSOCK="+s.swaySock(),
		// Neither may read a config of the desktop's own sway or wayvnc.
		"XDG_CONFIG_HOME="+wm, "XDG_CACHE_HOME="+wm, "XDG_STATE_HOME="+wm)
}

// inRunDir makes a command use the private runtime directory. It is set on the
// command line, as systemd-run needs the real one to find the user's manager,
// and the app needs the real one to find PipeWire. env execs, keeping the pid.
func (s *session) inRunDir(argv []string) []string {
	return append([]string{"/usr/bin/env", "XDG_RUNTIME_DIR=" + s.runDir()}, argv...)
}

// swayConfig: tabs appear only with more than one window, where they are the
// way to switch between them. Dialogs float by themselves. The render deadline
// composes right before the frame goes out, which saves 9 ms of a click's way
// back to the viewer without costing anything measurable.
func swayConfig(w, h int, xwayland string) string {
	return fmt.Sprintf(`xwayland %s
default_border none
default_floating_border normal
workspace_layout tabbed
hide_edge_borders --i3 smart
focus_follows_mouse no
output %s mode %dx%d max_render_time 4
`, xwayland, swayOutput, w, h)
}

// waylandNative reports whether an app is known to run without any X server.
func waylandNative(bin string) bool { return familyOf(bin) != "" }

func (m *Manager) bootstrapWayland(s *session, e *entry, target string, w, h int) (displayWatch, error) {
	raw, err := parseExec(e.Exec)
	if err != nil {
		return nil, err
	}
	bin, _ := lookIn(m.pathEnv(), raw[binaryIndex(raw)])
	xwayland := "force"
	if waylandNative(bin) {
		xwayland = "disable"
	}
	if err := os.MkdirAll(s.runDir(), 0o700); err != nil {
		return nil, err
	}
	conf := filepath.Join(s.dir, "sway.conf")
	if err := os.WriteFile(conf, []byte(swayConfig(w, h, xwayland)), 0o600); err != nil {
		return nil, err
	}
	sway, _ := lookIn(m.pathEnv(), "sway")
	env := append(s.compositorEnv(m.baseEnv()),
		"WLR_BACKENDS=headless", "WLR_LIBINPUT_NO_DEVICES=1", "WLR_HEADLESS_OUTPUTS=1",
		"WLR_RENDERER=gles2", "WLR_RENDER_DRM_DEVICE="+renderNode())
	cmd, err := m.spawn(s, s.inRunDir([]string{sway, "-c", conf}), s.dir, env, "display.log", nil)
	if err != nil {
		return nil, err
	}
	s.xpid = cmd.Process.Pid
	s.display = -1

	ipc, err := dialSwayReady(s, 8*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sway tidak siap: %s", lastLine(filepath.Join(s.dir, "display.log")))
	}
	ok := false
	defer func() {
		if !ok {
			ipc.close()
		}
	}()
	if err := m.saveState(s); err != nil {
		return nil, err
	}
	if xwayland == "force" {
		if err := lockXwayland(s, ipc); err != nil {
			return nil, fmt.Errorf("xwayland: %w", err)
		}
		if err := m.saveState(s); err != nil {
			return nil, err
		}
	}
	if err := m.startRFB(s); err != nil {
		return nil, err
	}
	if err := m.saveState(s); err != nil {
		return nil, err
	}
	m.startAudio(s)
	if err := m.startApp(s, e, target, ""); err != nil {
		return nil, err
	}
	ok = true
	return ipc, nil
}

// startRFB starts wayvnc and waits for its socket.
func (m *Manager) startRFB(s *session) error {
	wayvnc, ok := lookIn(m.pathEnv(), "wayvnc")
	if !ok {
		return errors.New("wayvnc tidak ditemukan")
	}
	// A socket left by a crash would keep the new one from listening.
	_ = os.Remove(s.sock())
	// A frame every 60th of a second is what typing feels like over RFB.
	env := append(s.compositorEnv(m.baseEnv()), "WAYLAND_DISPLAY="+filepath.Base(s.waylandSocket()))
	cmd, err := m.spawn(s, s.inRunDir([]string{wayvnc, "-f", strconv.Itoa(videoFPS), "-u", s.sock()}), s.dir, env, "rfb.log", nil)
	if err != nil {
		return err
	}
	s.rfbPid.Store(int64(cmd.Process.Pid))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(s.sock()); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("wayvnc tidak siap: " + lastLine(filepath.Join(s.dir, "rfb.log")))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// keepRFB restarts wayvnc when it is gone. It has been seen to crash while the
// display changes size, and without it a tile can't take input anymore. The
// viewer connects again by itself, and the app never notices.
func (m *Manager) keepRFB(s *session) {
	if s.pub.Display != DisplayWayland || processAlive(int(s.rfbPid.Load())) {
		return
	}
	if m.startRFB(s) == nil {
		_ = m.saveState(s)
	}
}

// lockXwayland closes what wlroots leaves open: its Xwayland takes no cookie, so
// any local user could connect to the X11 apps on this display. Starting it right
// away and setting its access list leaves no moment without the lock, which is
// why the config says "force": a lazily started one would lose the list at every
// restart.
func lockXwayland(s *session, ipc *swayIPC) error {
	file := filepath.Join(s.runDir(), "xdisplay")
	if err := ipc.command(`exec printf %s "$DISPLAY" > '` + file + `'`); err != nil {
		return err
	}
	var value string
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(file); err == nil && len(b) > 0 {
			value = string(b)
			break
		}
		if time.Now().After(deadline) {
			return errors.New("nomor display tidak diketahui")
		}
		time.Sleep(20 * time.Millisecond)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(value), ":"))
	if err != nil {
		return fmt.Errorf("nomor display tidak valid: %q", value)
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	var x *xconn
	for {
		if x, err = dialX(n, nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer x.close()
	if err := x.restrictToUser(u.Username); err != nil {
		return err
	}
	s.display = n
	return nil
}

// swayIPC is the part of sway's i3-compatible protocol the hub needs.
type swayIPC struct {
	c net.Conn
}

const (
	swayMagic      = "i3-ipc"
	swayRunCommand = 0
	swayGetOutputs = 3
	swayGetTree    = 4
)

func dialSway(path string) (*swayIPC, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &swayIPC{c: c}, nil
}

// dialSwayReady waits for a compositor that has its output and its socket.
func dialSwayReady(s *session, wait time.Duration) (*swayIPC, error) {
	deadline := time.Now().Add(wait)
	for {
		ipc, err := dialSway(s.swaySock())
		if err == nil {
			var outs []json.RawMessage
			b, err := ipc.request(swayGetOutputs, "")
			if err == nil && json.Unmarshal(b, &outs) == nil && len(outs) > 0 && s.waylandSocket() != "" {
				return ipc, nil
			}
			ipc.close()
		}
		if time.Now().After(deadline) || !processAlive(s.xpid) {
			return nil, errors.New("sway tidak menjawab")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (i *swayIPC) close() { i.c.Close() }

func (i *swayIPC) request(typ uint32, payload string) ([]byte, error) {
	_ = i.c.SetDeadline(time.Now().Add(5 * time.Second))
	head := make([]byte, len(swayMagic)+8)
	copy(head, swayMagic)
	binary.LittleEndian.PutUint32(head[len(swayMagic):], uint32(len(payload)))
	binary.LittleEndian.PutUint32(head[len(swayMagic)+4:], typ)
	if _, err := i.c.Write(append(head, payload...)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(i.c, head); err != nil {
		return nil, err
	}
	if string(head[:len(swayMagic)]) != swayMagic {
		return nil, errors.New("sway: balasan tidak dikenal")
	}
	n := binary.LittleEndian.Uint32(head[len(swayMagic):])
	if n > 64<<20 {
		return nil, errors.New("sway: balasan terlalu besar")
	}
	body := make([]byte, n)
	_, err := io.ReadFull(i.c, body)
	return body, err
}

func (i *swayIPC) command(cmd string) error {
	b, err := i.request(swayRunCommand, cmd)
	if err != nil {
		return err
	}
	var res []struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return err
	}
	for _, r := range res {
		if !r.Success {
			return fmt.Errorf("sway: %s", r.Error)
		}
	}
	return nil
}

type swayNode struct {
	Name     string     `json:"name"`
	Pid      int        `json:"pid"`
	Focused  bool       `json:"focused"`
	Nodes    []swayNode `json:"nodes"`
	Floating []swayNode `json:"floating_nodes"`
}

// windows counts the app windows and names the focused one, or the newest
// without focus. Only app windows have a pid in sway's tree.
func (i *swayIPC) windows() (int, string, error) {
	b, err := i.request(swayGetTree, "")
	if err != nil {
		return 0, "", err
	}
	var root swayNode
	if err := json.Unmarshal(b, &root); err != nil {
		return 0, "", err
	}
	count, focused, newest := walkSway(root)
	if focused == "" {
		focused = newest
	}
	return count, focused, nil
}

func walkSway(n swayNode) (count int, focused, newest string) {
	if n.Pid > 0 {
		count++
		newest = n.Name
		if n.Focused {
			focused = n.Name
		}
	}
	for _, list := range [][]swayNode{n.Nodes, n.Floating} {
		for _, c := range list {
			cn, cf, cl := walkSway(c)
			count += cn
			if cf != "" {
				focused = cf
			}
			if cl != "" {
				newest = cl
			}
		}
	}
	return count, focused, newest
}

// waylandVideoStream starts wf-recorder on the compositor's output. The frames
// stay GPU buffers on their way to the encoder.
func (m *Manager) waylandVideoStream(s *session, w, h int, rate float64, codec string, cand []string, node string) (*Stream, error) {
	bin, ok := lookIn(m.pathEnv(), "wf-recorder")
	if !ok {
		return nil, newErr(503, "video_unavailable", "wf-recorder tidak ditemukan")
	}
	enc, mux := "h264_vaapi", "h264"
	if codec == CodecAV1 {
		enc, mux = "av1_vaapi", "obu"
	}
	bits := func(f float64) string { return strconv.Itoa(int(f * 1e6)) }
	argv := []string{bin, "-y", "-o", swayOutput, "-c", enc, "-d", node, "-b", "0",
		// A pipe gets its data in blocks of 32 KB, which at this bitrate means
		// several frames of delay, unless the tee muxer flushes every packet.
		"-m", "tee", "-f", "[f=" + mux + ":flush_packets=1]pipe:1",
		// Rate control takes its frame rate from here. Without it, it assumes the
		// microsecond time base and starves every frame, down to blocky text.
		"-r", strconv.Itoa(videoFPS),
		"-g", fmt.Sprintf("0,0 %dx%d", w, h),
		"-F", "scale_vaapi=format=nv12:out_range=tv",
	}
	param := func(k, v string) { argv = append(argv, "-p", k+"="+v) }
	if codec == CodecH264 {
		for i := 0; i+1 < len(cand); i += 2 {
			param(strings.TrimPrefix(cand[i], "-"), cand[i+1])
		}
		param("profile", "high")
		// A delimiter on every frame, which is how SplitAccessUnits finds them.
		param("aud", "1")
	}
	param("async_depth", "1")
	param("g", "600")
	param("rc_mode", "VBR")
	param("b", bits(rate))
	param("maxrate", bits(rate*1.5))
	param("bufsize", bits(rate/2))
	// The GPU's conversion is BT.601 whatever it is asked for. Tagged as such the
	// colors arrive exact; tagged BT.709 red and green are visibly off.
	param("color_range", "tv")
	param("colorspace", "smpte170m")
	param("color_primaries", "smpte170m")
	param("color_trc", "smpte170m")
	env := append(s.compositorEnv(m.baseEnv()), "WAYLAND_DISPLAY="+filepath.Base(s.waylandSocket()))
	return m.startStream(s, s.inRunDir(argv), env, rate, codec)
}
