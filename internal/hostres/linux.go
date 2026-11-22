//go:build linux

package hostres

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// clkTck is USER_HZ, what /proc counts CPU time in: 100 on every Linux the hub runs on.
const clkTck = 100

// sessionEnv is what every process of a console has in its environment (session.startCLI and
// the terminal launch): its children inherit it, so a console's processes are found by it.
var sessionEnv = []byte("NEXTBOARD_SESSION_ID=")

type cpuTimes struct{ total, idle uint64 }

type gpuPrev struct {
	at     time.Time
	energy uint64 // µJ
	idle   uint64 // ms in its idle state
}

type procKey struct {
	pid   int
	start uint64
}

type procPrev struct {
	ticks uint64
	at    time.Time
}

type linuxReader struct {
	proc, sys string // /proc and /sys, or a test's folders
	pciIDs    []string
	dataDir   string
	self      int

	bad     *badFiles
	current atomic.Pointer[string] // the file being read now
	clock   func() time.Time

	prevCPU  []cpuTimes
	gpus     map[string]gpuPrev
	procs    map[procKey]procPrev
	sessions map[procKey]string // whose console a process is ("" none)
	model    string
	names    map[string]string // "8086:56a5" → "Intel Arc A380"
	sensors  map[string]sensorRead
	nvAt     time.Time
	nvLast   []GPU
}

func newReader(dataDir string, bad *badFiles) reader {
	return &linuxReader{proc: "/proc", sys: "/sys", dataDir: dataDir, self: os.Getpid(), bad: bad,
		pciIDs: []string{"/usr/share/hwdata/pci.ids", "/usr/share/misc/pci.ids", "/usr/share/pci.ids"}}
}

func (r *linuxReader) stuckAt() string {
	if p := r.current.Load(); p != nil {
		return *p
	}
	return ""
}

// read is never called twice at once: the sampler waits for one before the next.
func (r *linuxReader) read(withConsoles bool) Snapshot {
	now := time.Now()
	if r.clock != nil {
		now = r.clock()
	}
	s := Snapshot{At: now, Supported: true, OS: "linux", GPUs: []GPU{}, Temps: []Temp{}, Disks: []Disk{}}
	s.Hostname, _ = os.Hostname()
	s.UptimeSec = r.uptime()
	s.CPU = r.cpu()
	s.Memory = r.memory()
	s.Temps = r.temps(now)
	s.GPUs = r.gpusNow(now)
	s.Disks = r.disks()
	s.Hottest = hottestOf(s.Temps)
	if withConsoles {
		s.Consoles = r.consoles(now)
		Heaviest(s.Consoles, len(s.Consoles))
	}
	return s
}

func (r *linuxReader) file(parts ...string) string {
	p := filepath.Join(parts...)
	if r.bad.has(p) {
		return ""
	}
	r.current.Store(&p)
	b, err := os.ReadFile(p)
	r.current.Store(nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (r *linuxReader) uptime() int64 {
	f := strings.Fields(r.file(r.proc, "uptime"))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return int64(v)
}

// cpu is the load since the last reading, of the whole and of each thread.
func (r *linuxReader) cpu() CPU {
	var c CPU
	var now []cpuTimes
	for line := range strings.Lines(r.file(r.proc, "stat")) {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var t cpuTimes
		// user nice system idle iowait irq softirq steal (guest is in user already)
		for i := 1; i < len(f) && i <= 8; i++ {
			v, _ := strconv.ParseUint(f[i], 10, 64)
			t.total += v
			if i == 4 || i == 5 {
				t.idle += v
			}
		}
		now = append(now, t)
	}
	if len(now) == 0 {
		return c
	}
	c.Threads = len(now) - 1
	busy := func(i int) float64 {
		if i >= len(r.prevCPU) {
			return 0
		}
		dt, di := float64(now[i].total-r.prevCPU[i].total), float64(now[i].idle-r.prevCPU[i].idle)
		if now[i].total < r.prevCPU[i].total || dt <= 0 {
			return 0
		}
		return round1(max(0, min(100, (dt-di)*100/dt)))
	}
	c.Percent = busy(0)
	c.Cores = make([]float64, c.Threads)
	for i := range c.Cores {
		c.Cores[i] = busy(i + 1)
	}
	r.prevCPU = now
	if f := strings.Fields(r.file(r.proc, "loadavg")); len(f) >= 3 {
		for i := range 3 {
			c.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	if r.model == "" {
		r.model = cpuModel(r.file(r.proc, "cpuinfo"))
	}
	c.Model = r.model
	return c
}

func cpuModel(info string) string {
	for _, key := range []string{"model name", "Model", "Hardware", "cpu model"} {
		for line := range strings.Lines(info) {
			k, v, ok := strings.Cut(line, ":")
			if ok && strings.TrimSpace(k) == key {
				return strings.Join(strings.Fields(v), " ")
			}
		}
	}
	return ""
}

func (r *linuxReader) memory() Memory {
	kb := map[string]uint64{}
	for line := range strings.Lines(r.file(r.proc, "meminfo")) {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(f[0], 10, 64)
		kb[k] = n * 1024
	}
	m := Memory{Total: kb["MemTotal"], Available: kb["MemAvailable"], Cached: kb["Cached"] + kb["Buffers"] + kb["SReclaimable"],
		SwapTotal: kb["SwapTotal"]}
	if _, ok := kb["MemAvailable"]; !ok { // kernels before 3.14
		m.Available = kb["MemFree"] + m.Cached
	}
	if m.Total > m.Available {
		m.Used = m.Total - m.Available
	}
	if m.SwapTotal > kb["SwapFree"] {
		m.SwapUsed = m.SwapTotal - kb["SwapFree"]
	}
	return m
}

// sensorKinds says what a hwmon driver measures.
func sensorKind(name string) string {
	switch {
	case name == "k10temp" || name == "coretemp" || name == "zenpower" || name == "cpu_thermal" || strings.HasPrefix(name, "cpu"):
		return "cpu"
	case name == "amdgpu" || name == "i915" || name == "xe" || name == "nouveau" || name == "radeon":
		return "gpu"
	case name == "nvme" || name == "drivetemp":
		return "disk"
	case strings.HasPrefix(name, "iwlwifi") || strings.HasPrefix(name, "mt79") || strings.HasPrefix(name, "ath") || name == "BAT0" || strings.HasPrefix(name, "r8169") || strings.HasPrefix(name, "spd5118"):
		return "other"
	}
	return "board"
}

// the sensor a device is known by, before its others
var mainSensor = map[string][]string{"cpu": {"Tctl", "Tdie", "Package id 0", "CPU"}, "gpu": {"edge", "junction"}, "disk": {"Composite"}}

type reading struct {
	label string
	c     float64
}

// temps is one temperature per device: the sensor it is known by, else its hottest. Boards
// and other chips give their hottest; an ACPI zone only when no board sensor reads.
func (r *linuxReader) temps(now time.Time) []Temp {
	dirs, _ := filepath.Glob(filepath.Join(r.sys, "class", "hwmon", "hwmon*"))
	slices.SortFunc(dirs, func(a, b string) int { return hwmonNum(a) - hwmonNum(b) })
	var out, acpi []Temp
	for _, d := range dirs {
		name := r.file(d, "name")
		if name == "" {
			continue
		}
		kind := sensorKind(name)
		inputs, _ := filepath.Glob(filepath.Join(d, "temp*_input"))
		slices.Sort(inputs)
		// the sensor the device is known by, when it has one, is the only one read: a disk's
		// takes milliseconds (a SMART command)
		if main := r.mainInput(inputs, kind); main != "" {
			inputs = []string{main}
		}
		var rs []reading
		for _, in := range inputs {
			if c, ok := r.sensor(in, now); ok {
				rs = append(rs, reading{label: r.file(strings.TrimSuffix(in, "_input") + "_label"), c: c})
			}
		}
		if len(rs) == 0 {
			continue
		}
		pick := rs[0]
		for _, x := range rs {
			if x.c > pick.c {
				pick = x
			}
		}
		t := Temp{Kind: kind, C: round1(pick.c), Sensor: name}
		switch kind {
		case "cpu":
			t.Label = "CPU"
		case "gpu":
			t.Label = "GPU"
		case "disk":
			t.Label = map[bool]string{true: "NVMe", false: "Disk"}[name == "nvme"]
		case "board":
			t.Label = "Board"
		default:
			t.Label = name
		}
		if name == "acpitz" {
			acpi = append(acpi, t)
			continue
		}
		out = append(out, t)
	}
	if len(acpi) > 0 && !slices.ContainsFunc(out, func(t Temp) bool { return t.Kind == "board" }) {
		out = append(out, acpi[0])
	}
	// two of a kind: numbered, in the order of the system
	n := map[string]int{}
	for _, t := range out {
		n[t.Kind]++
	}
	seen := map[string]int{}
	for i := range out {
		if k := out[i].Kind; k != "other" && n[k] > 1 {
			seen[k]++
			out[i].Label += " " + strconv.Itoa(seen[k])
		}
	}
	order := map[string]int{"cpu": 0, "gpu": 1, "disk": 2, "board": 3, "other": 4}
	slices.SortStableFunc(out, func(a, b Temp) int { return order[a.Kind] - order[b.Kind] })
	return out
}

// mainInput is the input of the sensor a device of kind is known by, "" when it has none.
func (r *linuxReader) mainInput(inputs []string, kind string) string {
	for _, want := range mainSensor[kind] {
		for _, in := range inputs {
			if r.file(strings.TrimSuffix(in, "_input")+"_label") == want {
				return in
			}
		}
	}
	if len(inputs) > 0 && (kind == "disk" || kind == "gpu") {
		return inputs[0] // one sensor stands for the device
	}
	return ""
}

// slowSensor is a sensor whose read takes this long or more (a disk's SMART command, a board's
// WMI call); it is read again only after slowEvery.
const (
	slowSensor = 300 * time.Microsecond
	slowEvery  = 20 * time.Second
)

type sensorRead struct {
	c    float64
	ok   bool
	at   time.Time
	slow bool
}

// sensor reads one temperature in °C, a slow one at most every slowEvery.
func (r *linuxReader) sensor(path string, now time.Time) (float64, bool) {
	if r.sensors == nil {
		r.sensors = map[string]sensorRead{}
	}
	if c, ok := r.sensors[path]; ok && c.slow && now.Sub(c.at) < slowEvery {
		return c.c, c.ok
	}
	t := time.Now()
	v, err := strconv.ParseFloat(r.file(path), 64)
	c := v / 1000
	ok := err == nil && c > 0 && c <= 150
	r.sensors[path] = sensorRead{c: c, ok: ok, at: now, slow: time.Since(t) >= slowSensor}
	return c, ok
}

var trailingNum = regexp.MustCompile(`(\d+)$`)

func hwmonNum(p string) int {
	m := trailingNum.FindString(filepath.Base(p))
	n, _ := strconv.Atoi(m)
	return n
}

var cardRe = regexp.MustCompile(`^card\d+$`)

// gpusNow reads each graphics card's driver: amdgpu's busy share and VRAM, Intel's time out of
// RC6 and its power from the energy counter, and nvidia-smi for NVIDIA.
func (r *linuxReader) gpusNow(now time.Time) []GPU {
	cards, _ := os.ReadDir(filepath.Join(r.sys, "class", "drm"))
	if r.gpus == nil {
		r.gpus = map[string]gpuPrev{}
	}
	out := []GPU{}
	nvidia := false
	for _, e := range cards {
		if !cardRe.MatchString(e.Name()) {
			continue
		}
		card := filepath.Join(r.sys, "class", "drm", e.Name())
		dev := filepath.Join(card, "device")
		vendorID := strings.TrimPrefix(r.file(dev, "vendor"), "0x")
		if vendorID == "" {
			continue // a framebuffer without a device behind it
		}
		driver := ""
		if l, err := filepath.EvalSymlinks(filepath.Join(dev, "driver")); err == nil {
			driver = filepath.Base(l)
		}
		if driver == "nvidia" {
			nvidia = true
			continue
		}
		g := GPU{Vendor: vendorName(vendorID), Driver: driver, Name: r.gpuName(vendorID, strings.TrimPrefix(r.file(dev, "device"), "0x"))}
		prev := r.gpus[e.Name()]
		cur := gpuPrev{at: now}
		if v, ok := r.num(dev, "gpu_busy_percent"); ok { // amdgpu
			g.Busy = ptr(v)
		}
		if v, ok := r.num(dev, "mem_info_vram_used"); ok {
			g.VRAMUsed = ptr(uint64(v))
		}
		if v, ok := r.num(dev, "mem_info_vram_total"); ok {
			g.VRAMTotal = ptr(uint64(v))
		}
		// Intel: the time out of RC6 (i915) or out of idle (xe) since the last reading
		for _, p := range [][]string{{card, "gt", "gt0", "rc6_residency_ms"}, {card, "power", "rc6_residency_ms"}, {dev, "tile0", "gt0", "gtidle", "idle_residency_ms"}} {
			if v, ok := r.num(p...); ok {
				cur.idle = uint64(v)
				if !prev.at.IsZero() && g.Busy == nil && cur.idle >= prev.idle {
					ms := float64(now.Sub(prev.at).Milliseconds())
					if ms > 0 {
						g.Busy = ptr(round1(max(0, min(100, 100-float64(cur.idle-prev.idle)*100/ms))))
					}
				}
				break
			}
		}
		if mons, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*")); len(mons) > 0 {
			h := mons[0]
			if v, ok := r.num(h, "temp1_input"); ok && v > 0 {
				g.TempC = ptr(round1(v / 1000))
			}
			switch {
			case r.has(h, "power1_average"):
				v, _ := r.num(h, "power1_average")
				g.PowerW = ptr(round1(v / 1e6))
			case r.has(h, "power1_input"):
				v, _ := r.num(h, "power1_input")
				g.PowerW = ptr(round1(v / 1e6))
			default:
				if v, ok := r.num(h, "energy1_input"); ok {
					cur.energy = uint64(v)
					if !prev.at.IsZero() && cur.energy >= prev.energy {
						if sec := now.Sub(prev.at).Seconds(); sec > 0 {
							g.PowerW = ptr(round1(float64(cur.energy-prev.energy) / 1e6 / sec))
						}
					}
				}
			}
		}
		r.gpus[e.Name()] = cur
		out = append(out, g)
	}
	if nvidia {
		out = append(out, r.nvidiaSMI(now)...)
	}
	return out
}

// nvidiaSMI asks nvidia-smi, at most every 5 seconds (it takes a while to answer).
func (r *linuxReader) nvidiaSMI(now time.Time) []GPU {
	if now.Sub(r.nvAt) < 5*time.Second {
		return r.nvLast
	}
	r.nvAt = now
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,utilization.gpu,temperature.gpu,power.draw,fan.speed,memory.used,memory.total",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		r.nvLast = nil
		return nil
	}
	r.nvLast = parseNvidiaSMI(out)
	return r.nvLast
}

func parseNvidiaSMI(out []byte) []GPU {
	rows, err := csv.NewReader(bytes.NewReader(out)).ReadAll()
	if err != nil {
		return nil
	}
	var l []GPU
	for _, f := range rows {
		if len(f) < 7 {
			continue
		}
		val := func(i int) (float64, bool) {
			v, err := strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
			return v, err == nil
		}
		g := GPU{Name: strings.TrimSpace(f[0]), Vendor: "nvidia", Driver: "nvidia"}
		if !strings.HasPrefix(g.Name, "NVIDIA") {
			g.Name = "NVIDIA " + g.Name
		}
		if v, ok := val(1); ok {
			g.Busy = ptr(v)
		}
		if v, ok := val(2); ok {
			g.TempC = ptr(v)
		}
		if v, ok := val(3); ok {
			g.PowerW = ptr(round1(v))
		}
		if v, ok := val(5); ok {
			g.VRAMUsed = ptr(uint64(v) << 20)
		}
		if v, ok := val(6); ok {
			g.VRAMTotal = ptr(uint64(v) << 20)
		}
		l = append(l, g)
	}
	return l
}

func (r *linuxReader) has(parts ...string) bool {
	_, err := os.Stat(filepath.Join(parts...))
	return err == nil
}

func (r *linuxReader) num(parts ...string) (float64, bool) {
	s := r.file(parts...)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func ptr[T any](v T) *T { return &v }

func vendorName(id string) string {
	switch strings.ToLower(id) {
	case "8086":
		return "intel"
	case "1002":
		return "amd"
	case "10de":
		return "nvidia"
	}
	return "other"
}

var vendorLabel = map[string]string{"intel": "Intel", "amd": "AMD", "nvidia": "NVIDIA", "other": ""}

// gpuName is the card's name from the PCI ID database: "DG2 [Arc A380]" of vendor 8086 is
// "Intel Arc A380".
func (r *linuxReader) gpuName(vendor, device string) string {
	key := strings.ToLower(vendor + ":" + device)
	if r.names == nil {
		r.names = map[string]string{}
	}
	if n, ok := r.names[key]; ok {
		return n
	}
	short := vendorLabel[vendorName(vendor)]
	name := strings.TrimSpace(short + " GPU")
	for _, p := range r.pciIDs {
		if d := pciDevice(p, vendor, device); d != "" {
			if i, j := strings.Index(d, "["), strings.LastIndex(d, "]"); i >= 0 && j > i {
				d = d[i+1 : j]
			}
			name = d
			if short != "" && !strings.HasPrefix(strings.ToLower(d), strings.ToLower(short)) {
				name = short + " " + d
			}
			break
		}
	}
	r.names[key] = name
	return name
}

// pciDevice finds a device's line in a pci.ids file: a vendor line "8086  Intel Corporation",
// then its devices as "\t56a5  DG2 [Arc A380]".
func pciDevice(file, vendor, device string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	vendor, device = strings.ToLower(vendor), strings.ToLower(device)
	sc := bufio.NewScanner(f)
	in := false
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] != '\t' {
			if in {
				return ""
			}
			in = strings.HasPrefix(strings.ToLower(line), vendor+"  ")
			continue
		}
		if in && len(line) > 1 && line[1] != '\t' && strings.HasPrefix(strings.ToLower(line[1:]), device+"  ") {
			return strings.TrimSpace(line[1+len(device):])
		}
	}
	return ""
}

// disks are the filesystems of the data folder and of /, one each.
func (r *linuxReader) disks() []Disk {
	mounts := r.mountPoints()
	var out []Disk
	seen := map[uint64]bool{}
	for _, p := range []string{r.dataDir, "/"} {
		if p == "" {
			continue
		}
		var st syscall.Stat_t
		var fs syscall.Statfs_t
		if syscall.Stat(p, &st) != nil || syscall.Statfs(p, &fs) != nil || seen[st.Dev] {
			continue
		}
		seen[st.Dev] = true
		total := fs.Blocks * uint64(fs.Bsize)
		used := (fs.Blocks - fs.Bfree) * uint64(fs.Bsize)
		out = append(out, Disk{Path: mountOf(mounts, p), Used: used, Total: total})
	}
	return out
}

func (r *linuxReader) mountPoints() []string {
	var l []string
	for line := range strings.Lines(r.file(r.proc, "self", "mountinfo")) {
		f := strings.Fields(line)
		if len(f) > 4 {
			l = append(l, unescapeMount(f[4]))
		}
	}
	return l
}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

// mountOf is the mount point a path is on: the longest that holds it.
func mountOf(mounts []string, p string) string {
	best := "/"
	for _, m := range mounts {
		if (p == m || strings.HasPrefix(p, strings.TrimSuffix(m, "/")+"/")) && len(m) > len(best) {
			best = m
		}
	}
	return best
}

// consoles adds up the CPU and memory of each console's processes, found by sessionEnv; the
// hub's own process is "hub".
func (r *linuxReader) consoles(now time.Time) []Console {
	ents, err := os.ReadDir(r.proc)
	if err != nil {
		return nil
	}
	if r.procs == nil {
		r.procs, r.sessions = map[procKey]procPrev{}, map[procKey]string{}
	}
	page := uint64(os.Getpagesize())
	threads := max(1, len(r.prevCPU)-1)
	by := map[string]*Console{}
	alive := map[procKey]bool{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		start, ticks, rss, ok := procStat(r.file(r.proc, e.Name(), "stat"))
		if !ok {
			continue
		}
		k := procKey{pid, start}
		alive[k] = true
		id, known := r.sessions[k]
		if !known {
			if pid == r.self {
				id = "hub"
			} else {
				id = envSession(filepath.Join(r.proc, e.Name(), "environ"))
			}
			r.sessions[k] = id
		}
		prev, had := r.procs[k]
		r.procs[k] = procPrev{ticks: ticks, at: now}
		if id == "" {
			continue
		}
		c := by[id]
		if c == nil {
			c = &Console{SessionID: id}
			by[id] = c
		}
		c.Procs++
		c.RSS += rss * page
		if had && ticks >= prev.ticks {
			if sec := now.Sub(prev.at).Seconds(); sec > 0 {
				c.CPU += float64(ticks-prev.ticks) / clkTck / sec * 100 / float64(threads)
			}
		}
	}
	for k := range r.procs {
		if !alive[k] {
			delete(r.procs, k)
			delete(r.sessions, k)
		}
	}
	out := make([]Console, 0, len(by))
	for _, c := range by {
		c.CPU = round1(c.CPU)
		out = append(out, *c)
	}
	return out
}

// procStat reads a /proc/<pid>/stat: its start time, CPU ticks (user and system) and resident
// pages. The command between parentheses may hold spaces and parentheses itself.
func procStat(s string) (start, ticks, rss uint64, ok bool) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 22 {
		return 0, 0, 0, false
	}
	u, _ := strconv.ParseUint(f[11], 10, 64)
	sy, _ := strconv.ParseUint(f[12], 10, 64)
	start, _ = strconv.ParseUint(f[19], 10, 64)
	rss, _ = strconv.ParseUint(f[21], 10, 64)
	return start, u + sy, rss, true
}

// envSession is the console a process belongs to, from its environment ("" none or not
// readable: another user's).
func envSession(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for kv := range bytes.SplitSeq(b, []byte{0}) {
		if v, ok := bytes.CutPrefix(kv, sessionEnv); ok {
			return string(v)
		}
	}
	return ""
}
