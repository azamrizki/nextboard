//go:build linux

package hostres

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeHost is a /proc and /sys of a PC with an AMD and an Intel card, an NVMe disk, a board
// with WMI sensors, two consoles and a process of no console.
type fakeHost struct {
	t        *testing.T
	root     string
	now      time.Time
	r        *linuxReader
	proc     string
	sys      string
	pciIDs   string
	dataDir  string
	consoles map[int]string
}

func (h *fakeHost) write(rel, body string) {
	h.t.Helper()
	p := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fakeHost) link(rel, target string) {
	h.t.Helper()
	p := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.root, target), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h.root, target), p); err != nil {
		h.t.Fatal(err)
	}
}

// at sets what the counters say: CPU ticks (busy, idle per thread), the Intel card's time in
// RC6 and energy, and each process's ticks.
func (h *fakeHost) at(busy, idle uint64, rc6ms, energyUJ uint64, ticks map[int]uint64) {
	var b strings.Builder
	b.WriteString("cpu  " + u(busy*2) + " 0 0 " + u(idle*2) + " 0 0 0 0 0 0\n")
	for i := range 2 {
		b.WriteString("cpu" + u(uint64(i)) + " " + u(busy) + " 0 0 " + u(idle) + " 0 0 0 0 0 0\n")
	}
	b.WriteString("intr 1 2 3\nctxt 4\n")
	h.write("proc/stat", b.String())
	h.write("sys/class/drm/card1/gt/gt0/rc6_residency_ms", u(rc6ms)+"\n")
	h.write("sys/class/drm/card1/device/hwmon/hwmon9/energy1_input", u(energyUJ)+"\n")
	for pid, tk := range ticks {
		// a command with a space and a parenthesis in it, as /proc may show
		h.write("proc/"+u(uint64(pid))+"/stat", u(uint64(pid))+" (node (worker) x) S 1 1 1 0 -1 4194304 1 0 0 0 "+u(tk)+" 0 0 0 20 0 1 0 5000 1000000 256 18446744073709551615\n")
	}
}

func u(v uint64) string { return strconv.FormatUint(v, 10) }

func newFakeHost(t *testing.T) *fakeHost {
	h := &fakeHost{t: t, root: t.TempDir(), now: time.Unix(1_800_000_000, 0)}
	h.proc, h.sys = filepath.Join(h.root, "proc"), filepath.Join(h.root, "sys")
	h.dataDir = filepath.Join(h.root, "data")
	if err := os.MkdirAll(h.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h.write("proc/uptime", "1223924.51 14000000.00\n")
	h.write("proc/loadavg", "0.97 1.36 1.46 2/3134 4091952\n")
	h.write("proc/cpuinfo", "processor\t: 0\nvendor_id\t: AuthenticAMD\nmodel name\t: AMD Ryzen 5 3600XT 6-Core Processor\n\nprocessor\t: 1\n")
	h.write("proc/meminfo", "MemTotal:       16000000 kB\nMemFree:         4000000 kB\nMemAvailable:    9000000 kB\nBuffers:          100000 kB\nCached:          5000000 kB\nSReclaimable:     400000 kB\nSwapTotal:      20000000 kB\nSwapFree:       14000000 kB\n")
	h.write("proc/self/mountinfo", "22 1 259:2 / / rw,relatime - ext4 /dev/nvme0n1p2 rw\n90 22 0:45 / "+h.dataDir+" rw - tmpfs tmpfs rw\n")

	// sensors: k10temp (Tctl is the CPU's), nvme (Composite), a board of three unlabeled
	// sensors, an ACPI zone (left out: the board has its own), wifi
	h.write("sys/class/hwmon/hwmon0/name", "acpitz\n")
	h.write("sys/class/hwmon/hwmon0/temp1_input", "16800\n")
	h.write("sys/class/hwmon/hwmon1/name", "nvme\n")
	h.write("sys/class/hwmon/hwmon1/temp1_input", "48850\n")
	h.write("sys/class/hwmon/hwmon1/temp1_label", "Composite\n")
	h.write("sys/class/hwmon/hwmon1/temp2_input", "51850\n")
	h.write("sys/class/hwmon/hwmon1/temp2_label", "Sensor 1\n")
	h.write("sys/class/hwmon/hwmon2/name", "k10temp\n")
	h.write("sys/class/hwmon/hwmon2/temp1_input", "62125\n")
	h.write("sys/class/hwmon/hwmon2/temp1_label", "Tctl\n")
	h.write("sys/class/hwmon/hwmon2/temp3_input", "66000\n")
	h.write("sys/class/hwmon/hwmon2/temp3_label", "Tccd1\n")
	h.write("sys/class/hwmon/hwmon3/name", "gigabyte_wmi\n")
	h.write("sys/class/hwmon/hwmon3/temp1_input", "47000\n")
	h.write("sys/class/hwmon/hwmon3/temp2_input", "52000\n")
	h.write("sys/class/hwmon/hwmon3/temp3_input", "-1000\n")
	h.write("sys/class/hwmon/hwmon4/name", "iwlwifi_1\n")
	h.write("sys/class/hwmon/hwmon4/temp1_input", "41000\n")

	// cards: an AMD (its own busy share, VRAM, power) and an Intel Arc (RC6, energy)
	h.write("sys/class/drm/card0/device/vendor", "0x1002\n")
	h.write("sys/class/drm/card0/device/device", "0x73bf\n")
	h.link("sys/class/drm/card0/device/driver", "sys/bus/pci/drivers/amdgpu")
	h.write("sys/class/drm/card0/device/gpu_busy_percent", "37\n")
	h.write("sys/class/drm/card0/device/mem_info_vram_used", "2147483648\n")
	h.write("sys/class/drm/card0/device/mem_info_vram_total", "17163091968\n")
	h.write("sys/class/drm/card0/device/hwmon/hwmon7/temp1_input", "61000\n")
	h.write("sys/class/drm/card0/device/hwmon/hwmon7/power1_average", "123000000\n")
	h.write("sys/class/drm/card1/device/vendor", "0x8086\n")
	h.write("sys/class/drm/card1/device/device", "0x56a5\n")
	h.link("sys/class/drm/card1/device/driver", "sys/bus/pci/drivers/i915")
	h.write("sys/class/drm/card1/device/hwmon/hwmon9/temp1_input", "55000\n")
	h.write("sys/class/drm/card1-DP-1/status", "connected\n") // a connector, no card
	h.write("sys/class/drm/card2/status", "")                 // a framebuffer without a device

	h.pciIDs = filepath.Join(h.root, "pci.ids")
	h.write("pci.ids", "# comment\n1002  Advanced Micro Devices, Inc. [AMD/ATI]\n\t73bf  Navi 21 [Radeon RX 6800/6800 XT / 6900 XT]\n\t\t1002 0e3a  Radeon RX 6900 XT\n8086  Intel Corporation\n\t56a0  DG2 [Arc A770]\n\t56a5  DG2 [Arc A380]\n")

	h.consoles = map[int]string{100: "s_web", 101: "s_web", 102: "s_api"}
	for pid, id := range h.consoles {
		h.write("proc/"+u(uint64(pid))+"/environ", "HOME=/home/alice\x00NEXTBOARD_SESSION_ID="+id+"\x00TERM=xterm\x00")
	}
	h.write("proc/200/environ", "HOME=/home/alice\x00")

	h.r = &linuxReader{proc: h.proc, sys: h.sys, pciIDs: []string{filepath.Join(h.root, "none.ids"), h.pciIDs}, dataDir: h.dataDir, self: 200,
		bad: &badFiles{}, clock: func() time.Time { return h.now }}
	return h
}

func TestLinuxReader(t *testing.T) {
	h := newFakeHost(t)
	h.at(1000, 1000, 50_000, 5_000_000_000, map[int]uint64{100: 100, 101: 50, 102: 10, 200: 5})
	first := h.r.read(true)
	if !first.Supported || first.CPU.Percent != 0 || first.CPU.Threads != 2 || first.CPU.Model != "AMD Ryzen 5 3600XT 6-Core Processor" {
		t.Fatalf("first reading %+v", first.CPU)
	}
	// 2 s later: per thread 300 busy and 100 idle ticks, the card out of RC6 half the time and
	// at 18 W, s_web's processes 80 ticks busy of 2 threads × 2 s
	h.now = h.now.Add(2 * time.Second)
	h.at(1300, 1100, 51_000, 5_036_000_000, map[int]uint64{100: 160, 101: 70, 102: 10, 200: 9})
	s := h.r.read(true)

	if s.CPU.Percent != 75 || len(s.CPU.Cores) != 2 || s.CPU.Cores[1] != 75 || s.CPU.Load != [3]float64{0.97, 1.36, 1.46} {
		t.Fatalf("cpu %+v", s.CPU)
	}
	if s.UptimeSec != 1223924 {
		t.Fatalf("uptime %d", s.UptimeSec)
	}
	m := s.Memory
	if m.Total != 16_000_000*1024 || m.Used != 7_000_000*1024 || m.Cached != 5_500_000*1024 || m.SwapUsed != 6_000_000*1024 || int(m.Percent()) != 43 {
		t.Fatalf("memory %+v", m)
	}

	var temps []string
	for _, x := range s.Temps {
		temps = append(temps, x.Kind+":"+x.Label+"="+ftoa(x.C))
	}
	if got := strings.Join(temps, " "); got != "cpu:CPU=62.1 disk:NVMe=48.9 board:Board=52 other:iwlwifi_1=41" {
		t.Fatalf("temps %s", got)
	}
	if s.Hottest == nil || s.Hottest.Label != "CPU" || s.Hottest.C != 62.1 {
		t.Fatalf("hottest %+v", s.Hottest)
	}

	if len(s.GPUs) != 2 {
		t.Fatalf("gpus %+v", s.GPUs)
	}
	amd, intel := s.GPUs[0], s.GPUs[1]
	if amd.Name != "AMD Radeon RX 6800/6800 XT / 6900 XT" || amd.Vendor != "amd" || amd.Driver != "amdgpu" || *amd.Busy != 37 || *amd.TempC != 61 ||
		*amd.PowerW != 123 || *amd.VRAMUsed != 2<<30 || *amd.VRAMTotal != 17163091968 {
		t.Fatalf("amd %+v", amd)
	}
	if intel.Name != "Intel Arc A380" || intel.Driver != "i915" || *intel.Busy != 50 || *intel.TempC != 55 || *intel.PowerW != 18 || intel.VRAMTotal != nil {
		t.Fatalf("intel %+v busy %v power %v", intel, deref(intel.Busy), deref(intel.PowerW))
	}

	if len(s.Disks) != 2 || s.Disks[0].Path != h.dataDir || s.Disks[1].Path != "/" || s.Disks[0].Total == 0 {
		t.Fatalf("disks %+v", s.Disks)
	}

	// s_web: (60+20) ticks / 100 per s / 2 s / 2 threads = 20%; the hub's own process is "hub"
	by := map[string]Console{}
	for _, c := range s.Consoles {
		by[c.SessionID] = c
	}
	if c := by["s_web"]; c.CPU != 20 || c.Procs != 2 || c.RSS != 2*256*uint64(os.Getpagesize()) {
		t.Fatalf("s_web %+v", c)
	}
	if c := by["hub"]; c.CPU != 1 || c.Procs != 1 {
		t.Fatalf("hub %+v", c)
	}
	if s.Consoles[0].SessionID != "s_web" || len(s.Consoles) != 3 {
		t.Fatalf("heaviest first %+v", s.Consoles)
	}

	// a process gone is forgotten; a reading without consoles names none
	if err := os.RemoveAll(filepath.Join(h.proc, "101")); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(2 * time.Second)
	if s := h.r.read(false); s.Consoles != nil {
		t.Fatalf("consoles without asking %+v", s.Consoles)
	}
	s = h.r.read(true)
	if by := consoleOf(s, "s_web"); by.Procs != 1 {
		t.Fatalf("after one ended %+v", by)
	}
}

func consoleOf(s Snapshot, id string) Console {
	for _, c := range s.Consoles {
		if c.SessionID == id {
			return c
		}
	}
	return Console{}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// A sensor whose read is slow is read again only every slowEvery; one stuck for good is
// skipped by the next reader.
func TestSlowAndStuckFiles(t *testing.T) {
	h := newFakeHost(t)
	h.at(1, 1, 1, 1, nil)
	in := filepath.Join(h.sys, "class", "hwmon", "hwmon1", "temp1_input")
	h.r.sensors = map[string]sensorRead{in: {c: 40, ok: true, at: h.now, slow: true}}
	if c, _ := h.r.sensor(in, h.now.Add(slowEvery/2)); c != 40 {
		t.Fatalf("slow sensor read again at once: %v", c)
	}
	if c, _ := h.r.sensor(in, h.now.Add(slowEvery)); c != 48.85 {
		t.Fatalf("slow sensor not read after %s: %v", slowEvery, c)
	}
	h.r.bad.add(in)
	if c, ok := h.r.sensor(in, h.now.Add(2*slowEvery)); ok {
		t.Fatalf("a stuck file read: %v", c)
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	l := parseNvidiaSMI([]byte("NVIDIA GeForce RTX 3070, 12, 54, 38.21, 30, 1024, 8192\nTesla T4, [N/A], 40, 27.5, [N/A], 0, 15360\n"))
	if len(l) != 2 || l[0].Name != "NVIDIA GeForce RTX 3070" || *l[0].Busy != 12 || *l[0].TempC != 54 || *l[0].PowerW != 38.2 || *l[0].VRAMUsed != 1<<30 {
		t.Fatalf("first %+v", l)
	}
	if l[1].Name != "NVIDIA Tesla T4" || l[1].Busy != nil || *l[1].VRAMTotal != 15360<<20 {
		t.Fatalf("second %+v", l[1])
	}
}

func TestProcStat(t *testing.T) {
	start, ticks, rss, ok := procStat("4242 (my (odd) prog) S 1 1 1 0 -1 0 0 0 0 0 30 12 0 0 20 0 1 0 777 1000 99 0")
	if !ok || start != 777 || ticks != 42 || rss != 99 {
		t.Fatalf("%d %d %d %v", start, ticks, rss, ok)
	}
	if _, _, _, ok := procStat("garbage"); ok {
		t.Fatal("garbage parsed")
	}
}
