package preview

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Listening is a TCP port something listens on (docs/API-studio.md §7.13).
type Listening struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
	// Process is empty when the system does not say, as for another user's programs.
	Process string `json:"process"`
	PID     int    `json:"pid,omitempty"`
}

// ListeningScript lists listening ports: ss on Linux, lsof elsewhere.
const ListeningScript = `if command -v ss >/dev/null 2>&1; then ss -ltnpH 2>/dev/null; elif command -v lsof >/dev/null 2>&1; then lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null; fi; echo ` + psMark + `; ps -eo pid=,args= 2>/dev/null`

const psMark = "@@PS@@"

// The name ss gives is the thread's, "MainThread" for a Node program, so the command line says it
// better: its program and, for an interpreter, what that runs.
var interpreters = map[string]bool{"node": true, "python": true, "python3": true, "ruby": true, "php": true, "deno": true, "bun": true, "perl": true}

func programName(args string) string {
	f := strings.Fields(args)
	if len(f) == 0 {
		return ""
	}
	base := strings.TrimPrefix(path.Base(f[0]), "-")
	if interpreters[base] {
		for _, a := range f[1:] {
			if !strings.HasPrefix(a, "-") {
				return base + " " + path.Base(a)
			}
		}
	}
	return base
}

var ssUser = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)

func splitHostPort(s string) (string, int, bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	return strings.Trim(s[:i], "[]"), port, true
}

// ParseListening reads what ListeningScript prints. One port shows once, by its owner if known.
func ParseListening(out string) []Listening {
	out, procs, _ := strings.Cut(out, psMark)
	names := map[int]string{}
	for _, line := range strings.Split(procs, "\n") {
		pid, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(pid); err == nil {
			names[n] = programName(args)
		}
	}
	byPort := map[int]Listening{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		var l Listening
		switch {
		case len(f) >= 4 && f[0] == "LISTEN": // ss
			host, port, ok := splitHostPort(f[3])
			if !ok {
				continue
			}
			l = Listening{Port: port, Address: host}
			if m := ssUser.FindStringSubmatch(line); m != nil {
				l.Process = m[1]
				l.PID, _ = strconv.Atoi(m[2])
			}
		case len(f) >= 9 && f[len(f)-1] == "(LISTEN)": // lsof
			host, port, ok := splitHostPort(f[len(f)-2])
			if !ok {
				continue
			}
			l = Listening{Port: port, Address: host, Process: f[0]}
			l.PID, _ = strconv.Atoi(f[1])
		default:
			continue
		}
		if old, seen := byPort[l.Port]; seen && (old.Process != "" || l.Process == "") {
			continue
		}
		byPort[l.Port] = l
	}
	items := make([]Listening, 0, len(byPort))
	for _, l := range byPort {
		if n := names[l.PID]; n != "" {
			l.Process = n
		}
		items = append(items, l)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Port < items[j].Port })
	return items
}
