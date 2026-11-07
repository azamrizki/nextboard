package machine

import (
	"os"
	"strconv"
	"strings"
)

// procTreeMax bounds the processes ReadBytes adds up: a CLI with its MCP
// servers and tools is a few dozen at most.
const procTreeMax = 64

// ReadBytes is how many bytes the process pid and the processes below it
// have read so far (rchar of /proc/<pid>/io: files, pipes and sockets alike).
// It is for This PC, where agents run; false where /proc has no answer, as
// on macOS or once the process is gone.
func ReadBytes(pid int) (uint64, bool) {
	if pid <= 0 {
		return 0, false
	}
	var total uint64
	seen := 0
	ok := false
	queue := []int{pid}
	for len(queue) > 0 && seen < procTreeMax {
		p := queue[0]
		queue = queue[1:]
		seen++
		if n, got := rchar(p); got {
			total += n
			ok = true
		} else if p == pid {
			return 0, false
		}
		queue = append(queue, children(p)...)
	}
	return total, ok
}

func rchar(pid int) (uint64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, found := strings.CutPrefix(line, "rchar: "); found {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// children lists the children of every thread of pid.
func children(pid int) []int {
	dir := "/proc/" + strconv.Itoa(pid) + "/task"
	tasks, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []int
	for _, t := range tasks {
		b, err := os.ReadFile(dir + "/" + t.Name() + "/children")
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}
