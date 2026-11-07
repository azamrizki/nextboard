package preview

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PortProbe is one entry of GET /api/machines/{id}/ports.
type PortProbe struct {
	Port int  `json:"port"`
	Open bool `json:"open"`
	HTTP bool `json:"http"`
}

// MaxProbePorts bounds one probe request.
const MaxProbePorts = 20

// HTTPProbeTimeout bounds the GET / check of an open port.
var HTTPProbeTimeout = 1500 * time.Millisecond

// ParsePorts reads "3000,3001,5173" (≤ 20 distinct ports 1–65535).
func ParsePorts(s string) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > 65535 {
			return nil, newErr(http.StatusBadRequest, "bad_port", "invalid port: %q", f)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, newErr(http.StatusBadRequest, "bad_port", "no ports to probe")
	}
	if len(out) > MaxProbePorts {
		return nil, newErr(http.StatusBadRequest, "too_many", "at most %d ports", MaxProbePorts)
	}
	return out, nil
}

// ProbePorts checks loopback ports of a machine in parallel: a TCP
// connect within ProbeTimeout, then an HTTP answer to GET / within
// HTTPProbeTimeout.
func ProbePorts(ctx context.Context, dial func(ctx context.Context, port int) (net.Conn, error), ports []int) []PortProbe {
	out := make([]PortProbe, len(ports))
	var wg sync.WaitGroup
	for i, port := range ports {
		out[i].Port = port
		wg.Add(1)
		go func(i, port int) {
			defer wg.Done()
			dctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
			c, err := dial(dctx, port)
			cancel()
			if err != nil {
				return
			}
			defer c.Close()
			out[i].Open = true
			_ = c.SetDeadline(time.Now().Add(HTTPProbeTimeout))
			stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Now()) })
			defer stop()
			req := "GET / HTTP/1.1\r\nHost: localhost:" + strconv.Itoa(port) + "\r\nUser-Agent: NextBoard-probe\r\nAccept: */*\r\nConnection: close\r\n\r\n"
			if _, err := c.Write([]byte(req)); err != nil {
				return
			}
			line, err := bufio.NewReader(c).ReadString('\n')
			if err == nil || len(line) >= 8 {
				out[i].HTTP = strings.HasPrefix(line, "HTTP/1.") || strings.HasPrefix(line, "HTTP/2")
			}
		}(i, port)
	}
	wg.Wait()
	sort.SliceStable(out, func(i, k int) bool { return out[i].Port < out[k].Port })
	return out
}
