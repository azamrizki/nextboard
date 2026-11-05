package gui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
	"time"
)

func TestSplitAccessUnits(t *testing.T) {
	aud := []byte{0, 0, 0, 1, 9, 0x10}
	sps := []byte{0, 0, 0, 1, 0x67, 0x64, 0, 0x2a}
	idr := []byte{0, 0, 1, 0x65, 0x88, 0x84}
	p := []byte{0, 0, 1, 0x41, 0x9a, 0x01}
	stream := bytes.Join([][]byte{
		{0xff}, // junk before the first delimiter is dropped
		aud, sps, idr,
		aud, p,
		aud, p, p,
		aud, p, // the last frame has no delimiter after it yet
	}, nil)

	for name, r := range map[string]io.Reader{
		"whole":     bytes.NewReader(stream),
		"one byte":  iotest.OneByteReader(bytes.NewReader(stream)),
		"half read": iotest.HalfReader(bytes.NewReader(stream)),
	} {
		var keys []bool
		var sizes []int
		err := SplitAccessUnits(r, func(au []byte, key bool) error {
			keys = append(keys, key)
			sizes = append(sizes, len(au))
			return nil
		})
		if !errors.Is(err, io.EOF) {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(keys) != 3 || !keys[0] || keys[1] || keys[2] {
			t.Errorf("%s: keys = %v", name, keys)
		}
		// A frame starts at the 3 byte start code; the zero before it belongs
		// to the frame before, which decoders ignore as padding.
		if len(sizes) == 3 && (sizes[0] < len(sps)+len(idr) || sizes[2] < 2*len(p)) {
			t.Errorf("%s: sizes = %v", name, sizes)
		}
	}

	stop := errors.New("stop")
	err := SplitAccessUnits(bytes.NewReader(stream), func([]byte, bool) error { return stop })
	if !errors.Is(err, stop) {
		t.Errorf("emit error not returned: %v", err)
	}
}

func TestIsKeyFrame(t *testing.T) {
	cases := map[string]struct {
		au   []byte
		want bool
	}{
		"idr":        {[]byte{0, 0, 1, 9, 0x10, 0, 0, 1, 0x67, 1, 0, 0, 1, 0x65, 2}, true},
		"delta":      {[]byte{0, 0, 1, 9, 0x10, 0, 0, 1, 0x41, 2, 3}, false},
		"delimiter":  {[]byte{0, 0, 1, 9, 0x10}, false},
		"empty":      {nil, false},
		"cut short":  {[]byte{0, 0, 1}, false},
		"sei before": {[]byte{0, 0, 1, 6, 5, 0, 0, 1, 0x65, 9}, true},
	}
	for name, c := range cases {
		if got := isKeyFrame(c.au); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestSplitTemporalUnits(t *testing.T) {
	obu := func(typ byte, payload ...byte) []byte {
		return append([]byte{typ<<3 | obuHasSize, byte(len(payload))}, payload...)
	}
	td := obu(obuTemporalDelimiter)
	seq := obu(obuSequenceHeader, 1, 2, 3)
	// Payload bytes that look exactly like a delimiter must not split anything.
	frame := obu(obuFrame, 0x12, 0x00, 0x12, 0x00)
	padding := obu(15, 9, 9)
	stream := bytes.Join([][]byte{
		td, seq, frame,
		td, frame, padding, // what follows a frame belongs to no picture
		td, frame,
		td, // no frame yet
	}, nil)
	for name, r := range map[string]io.Reader{
		"whole":    bytes.NewReader(stream),
		"one byte": iotest.OneByteReader(bytes.NewReader(stream)),
	} {
		var keys []bool
		var sizes []int
		err := SplitTemporalUnits(r, func(tu []byte, key bool) error {
			keys = append(keys, key)
			sizes = append(sizes, len(tu))
			return nil
		})
		if !errors.Is(err, io.EOF) {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(keys) != 3 || !keys[0] || keys[1] || keys[2] {
			t.Errorf("%s: keys = %v", name, keys)
		}
		want := []int{len(td) + len(seq) + len(frame), len(td) + len(frame), len(td) + len(frame)}
		if len(sizes) == 3 && (sizes[0] != want[0] || sizes[1] != want[1] || sizes[2] != want[2]) {
			t.Errorf("%s: sizes = %v, want %v", name, sizes, want)
		}
	}

	// What Arc writes: a frame header and a tile group instead of a frame OBU.
	hdr := obu(3, 7, 7)
	tile := obu(obuTileGroup, 0x12, 0x00, 5)
	arc := bytes.Join([][]byte{td, seq, hdr, tile, td, hdr, tile}, nil)
	for name, r := range map[string]io.Reader{
		"whole":    bytes.NewReader(arc), // the delimiter ends the first picture
		"one byte": iotest.OneByteReader(bytes.NewReader(arc)),
	} {
		var keys []bool
		var sizes []int
		_ = SplitTemporalUnits(r, func(tu []byte, key bool) error {
			keys = append(keys, key)
			sizes = append(sizes, len(tu))
			return nil
		})
		if len(keys) != 2 || !keys[0] || keys[1] {
			t.Errorf("arc %s: keys = %v", name, keys)
		}
		if len(sizes) == 2 && (sizes[0] != len(td)+len(seq)+len(hdr)+len(tile) || sizes[1] != len(td)+len(hdr)+len(tile)) {
			t.Errorf("arc %s: sizes = %v", name, sizes)
		}
	}

	stop := errors.New("stop")
	if err := SplitTemporalUnits(bytes.NewReader(stream), func([]byte, bool) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("emit error not returned: %v", err)
	}
	if err := SplitTemporalUnits(bytes.NewReader([]byte{obuFrame << 3, 1}), func([]byte, bool) error { return nil }); err == nil || errors.Is(err, io.EOF) {
		t.Errorf("an OBU without a size can't be walked: %v", err)
	}
	long := []byte{obuFrame<<3 | obuHasSize, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 1}
	if err := SplitTemporalUnits(bytes.NewReader(long), func([]byte, bool) error { return nil }); err == nil || errors.Is(err, io.EOF) {
		t.Errorf("an absurd size must fail: %v", err)
	}
}

func TestVideoRate(t *testing.T) {
	if r := videoRate(1280, 800, CodecH264, 0); r < 5 || r > 7 {
		t.Errorf("H.264 default for 1280x800: %v", r)
	}
	if r := videoRate(1280, 800, CodecAV1, 0); r < 3 || r > 4 {
		t.Errorf("AV1 default for 1280x800: %v", r)
	}
	if r := videoRate(3840, 2160, CodecH264, 0); r != maxVideoMbps {
		t.Errorf("4K is capped: %v", r)
	}
	if r := videoRate(64, 64, CodecH264, 0); r != minVideoMbps {
		t.Errorf("tiny has a floor: %v", r)
	}
	if r := videoRate(1280, 800, CodecH264, 3); r != 3 {
		t.Errorf("a slow link's request is honored: %v", r)
	}
}

// Chrome rewrites its environment, so only its place in the process tree shows
// it belongs to the session. It must still be asked to quit, and must not outlive it.
func TestStopReachesChildrenWithoutTag(t *testing.T) {
	m, ev := newTestManager(t, map[string]string{"app": `/bin/sh -c "env -i /bin/sleep 3137 & wait"`})
	s, err := m.Launch(LaunchRequest{AppID: "app"})
	if err != nil {
		t.Fatal(err)
	}
	var child sessionProc
	deadline := time.Now().Add(5 * time.Second)
	for child.pid == 0 && time.Now().Before(deadline) {
		for _, p := range sessionProcs(s.ID) {
			if p.comm == "sleep" {
				child = p
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if child.pid == 0 || child.marked {
		t.Fatalf("child without tag not found: %+v", child)
	}
	if !slices.Contains(quitTargets(s.ID, 0), child.parent) {
		t.Errorf("its parent must be asked to quit: %d", child.parent)
	}
	for _, p := range quitTargets(s.ID, 0) {
		if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", p)); strings.HasPrefix(string(b), "dbus-") {
			t.Errorf("the session bus goes after its app: %d", p)
		}
	}
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitGone(t, m, ev, s.ID)
	if processAlive(child.pid) {
		t.Errorf("child %d outlived the session", child.pid)
	}
}

func TestEncoderLimit(t *testing.T) {
	m := New(Config{Dir: t.TempDir()})
	var releases []func()
	for range maxEncoders {
		r, err := m.takeEncoder()
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	if _, err := m.takeEncoder(); err == nil {
		t.Fatal("the limit must hold")
	}
	releases[0]()
	r, err := m.takeEncoder()
	if err != nil {
		t.Fatalf("a released slot is free again: %v", err)
	}
	r()
}

// A real session on this machine's GPU and PipeWire, when it has them.
func TestVideoAndAudioStreams(t *testing.T) {
	t.Setenv(helperEnv, "Media window")
	m, ev := newTestManager(t, map[string]string{"app": ""})
	s, err := m.Launch(LaunchRequest{AppID: "app", Width: 640, Height: 480})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, m, ev, "first window", func(x Session) bool { return x.State == StateRunning })

	if !m.videoAvailable() {
		t.Log("no hardware encoder here: video not tested")
	} else {
		for _, codec := range m.videoCodecs() {
			st, err := m.VideoStream(s.ID, 641, 481, 0, codec) // odd sizes become even
			if err != nil {
				t.Fatal(err)
			}
			if st.Codec != codec {
				t.Errorf("asked %s, got %s", codec, st.Codec)
			}
			frames, firstKey := 0, false
			done := make(chan error, 1)
			go func() {
				done <- st.Pictures(func(_ []byte, key bool) error {
					if frames == 0 {
						firstKey = key
					}
					frames++
					if frames == 5 {
						return io.EOF
					}
					return nil
				})
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Errorf("%s: no pictures from the encoder", codec)
			}
			st.Stop()
			if frames < 5 || !firstKey {
				t.Errorf("%s: pictures %d, first key %v", codec, frames, firstKey)
			}
		}
		// A format the GPU lacks falls back to the one everything has.
		st, err := m.VideoStream(s.ID, 640, 480, 0, "nonsense")
		if err != nil {
			t.Fatal(err)
		}
		if st.Codec != CodecH264 {
			t.Errorf("unknown format gave %s", st.Codec)
		}
		st.Stop()
	}

	if got := m.List(); len(got) == 1 && got[0].Audio != m.audioAvailable() {
		t.Errorf("session audio %v, machine %v", got[0].Audio, m.audioAvailable())
	}
	if m.audioAvailable() {
		st, err := m.AudioStream(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		// The silent sink still produces samples.
		buf := make([]byte, AudioFrameBytes)
		read := make(chan error, 1)
		go func() {
			_, err := io.ReadFull(st.Out, buf)
			read <- err
		}()
		select {
		case err := <-read:
			if err != nil {
				t.Errorf("audio read: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("no samples from the recorder")
		}
		st.Stop()
	}

	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitGone(t, m, ev, s.ID)
	if left := markedProcs(s.ID, 0); len(left) != 0 {
		t.Errorf("processes left: %v", left)
	}
	m.mu.Lock()
	n := m.encoders
	m.mu.Unlock()
	if n != 0 {
		t.Errorf("encoder slots still taken: %d", n)
	}
}

// The zero copy display, for an app that needs X11: this helper window comes
// through Xwayland, so it also shows that path works with the access list set.
func TestWaylandSession(t *testing.T) {
	t.Setenv(helperEnv, "animate:Wayland window")
	m, ev := newTestManager(t, map[string]string{"app": ""})
	m.forceX11 = false
	if !m.waylandAvailable() {
		t.Skip("sway / wayvnc / wf-recorder belum terpasang, atau tidak ada GPU")
	}
	s, err := m.Launch(LaunchRequest{AppID: "app", Width: 640, Height: 480})
	if err != nil {
		t.Fatal(err)
	}
	if s.Display != DisplayWayland {
		t.Fatalf("display = %q", s.Display)
	}
	waitSession(t, m, ev, "first window", func(x Session) bool { return x.State == StateRunning && strings.Contains(x.Title, "Wayland window") })
	sess := m.sessions[s.ID]

	// RFB answers as it does on X11, on a socket private to the hub's user.
	c, err := m.Dial(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	banner := make([]byte, 12)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(banner); err != nil || !strings.HasPrefix(string(banner), "RFB ") {
		t.Errorf("banner %q, %v", banner, err)
	}
	c.Close()
	if fi, err := os.Stat(sess.runDir()); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("runtime folder: %v %v", fi, err)
	}

	for _, codec := range m.videoCodecs() {
		st, err := m.VideoStream(s.ID, 641, 481, 0, codec) // odd sizes become even
		if err != nil {
			t.Fatal(err)
		}
		if st.Codec != codec {
			t.Errorf("%s stream: %+v", codec, st)
		}
		frames, firstKey := 0, false
		done := make(chan error, 1)
		go func() {
			done <- st.Pictures(func(_ []byte, key bool) error {
				if frames == 0 {
					firstKey = key
				}
				frames++
				if frames == 5 {
					return io.EOF
				}
				return nil
			})
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("%s: no pictures from wf-recorder", codec)
		}
		st.Stop()
		if frames < 5 || !firstKey {
			t.Errorf("%s: pictures %d, first key %v", codec, frames, firstKey)
		}
	}

	// wayvnc crashed under real use: the session must get a new one by itself.
	if err := syscall.Kill(int(sess.rfbPid.Load()), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	var back net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for back == nil && time.Now().Before(deadline) {
		if c, err := m.Dial(s.ID); err == nil {
			banner := make([]byte, 12)
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := c.Read(banner); err == nil && strings.HasPrefix(string(banner), "RFB ") {
				back = c
				break
			}
			c.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	if back == nil {
		t.Fatal("no new RFB server after a crash")
	}
	back.Close()

	// A new hub takes over the running session.
	m.Close()
	ev2 := &evLog{}
	m2 := New(Config{Dir: m.cfg.Dir, Publish: ev2.add})
	m2.pollEvery = 100 * time.Millisecond
	m2.closeGrace = 300 * time.Millisecond
	m2.Start()
	defer m2.Close()
	got := waitSession(t, m2, ev2, "adoption", func(x Session) bool { return x.ID == s.ID && strings.Contains(x.Title, "Wayland window") })
	if got.Display != DisplayWayland || got.State != StateRunning {
		t.Errorf("adopted: %+v", got)
	}
	if err := m2.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitGone(t, m2, ev2, s.ID)
	if left := markedProcs(s.ID, 0); len(left) != 0 {
		t.Errorf("processes left: %v", left)
	}
	if _, err := os.Stat(sess.runDir()); !os.IsNotExist(err) {
		t.Errorf("runtime folder left: %v", err)
	}
	m.mu.Lock()
	delete(m.sessions, s.ID) // already stopped through m2
	m.mu.Unlock()
}

func TestSwayTree(t *testing.T) {
	tree := swayNode{Nodes: []swayNode{{Nodes: []swayNode{
		{Name: "first", Pid: 10},
		{Name: "second", Pid: 11, Focused: true},
		{Name: "a workspace without a pid"},
	}, Floating: []swayNode{{Name: "dialog", Pid: 12}}}}}
	count, focused, newest := walkSway(tree)
	if count != 3 || focused != "second" || newest != "dialog" {
		t.Errorf("count %d, focused %q, newest %q", count, focused, newest)
	}
}

func TestSwayConfig(t *testing.T) {
	c := swayConfig(800, 600, "disable")
	for _, want := range []string{"xwayland disable", "output HEADLESS-1 mode 800x600 max_render_time 4", "hide_edge_borders --i3 smart", "workspace_layout tabbed"} {
		if !strings.Contains(c, want) {
			t.Errorf("config lacks %q:\n%s", want, c)
		}
	}
}
