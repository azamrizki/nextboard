package uptime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6455 names it for Sec-WebSocket-Accept
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/version"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// checkWebSocket asks for an upgrade and wants it granted: 101 with the accept key of
// RFC 6455. The connection is closed right after; no message is sent.
func checkWebSocket(ctx context.Context, m Monitor, h machine.Host) Result {
	u, err := url.Parse(m.URL)
	if err != nil {
		return failed(errText(err))
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	key := base64.StdEncoding.EncodeToString(raw)
	tr := &http.Transport{
		DialContext:       func(ctx context.Context, _, addr string) (net.Conn, error) { return machine.Dial(ctx, h, addr) },
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: m.IgnoreTLS}, //nolint:gosec // the owner's choice per monitor
		DisableKeepAlives: true,
		// an upgrade is HTTP/1.1 only
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return failed(errText(err))
	}
	req.Header.Set("User-Agent", "NextBoard-Uptime/"+version.Version)
	for _, hd := range m.Headers {
		req.Header.Set(hd.Name, hd.Value)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	start := time.Now()
	resp, err := cl.Do(req)
	if err != nil {
		return failed(errText(err))
	}
	defer resp.Body.Close()
	r := Result{Ms: int(time.Since(start).Milliseconds()), Msg: resp.Status}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		c := resp.TLS.PeerCertificates[0]
		r.Cert = &Cert{Subject: c.Subject.CommonName, Issuer: c.Issuer.CommonName, NotAfter: c.NotAfter}
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		r.Msg = clip(resp.Status + ", no upgrade")
		return r
	}
	sum := sha1.Sum([]byte(key + wsGUID)) //nolint:gosec // see the import
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		r.Msg = "101 with a wrong Sec-WebSocket-Accept"
		return r
	}
	r.OK, r.Msg = true, "Upgraded"
	return r
}

// checkDocker asks the machine's docker about a container: up while it runs, and while
// it is healthy if it has a health check. The name goes as an argument, never through a
// shell.
func checkDocker(ctx context.Context, m Monitor, h machine.Host) Result {
	start := time.Now()
	res, err := machine.Exec(ctx, h, machine.ExecSpec{
		Argv:      []string{"docker", "inspect", "--type", "container", "--format", "{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}", "--", m.Container},
		MaxOutput: 16 << 10,
	})
	if err != nil {
		return failed(errText(err))
	}
	if res.ExitCode != 0 && (ctx.Err() != nil || res.ExitCode == -1) {
		return failed("Timeout")
	}
	return dockerResult(res.ExitCode, string(res.Stdout), string(res.Stderr), int(time.Since(start).Milliseconds()))
}

// dockerResult reads what docker inspect said.
func dockerResult(exit int, stdout, stderr string, ms int) Result {
	if exit != 0 {
		text := strings.TrimSpace(stderr)
		switch {
		case exit == 127 || strings.Contains(text, "not found") && strings.Contains(text, "docker"):
			return failed("docker is not installed here")
		case strings.Contains(strings.ToLower(text), "no such"):
			return failed("No such container")
		case strings.Contains(text, "permission denied"):
			return failed("No access to docker (is the user in the docker group?)")
		}
		if text == "" {
			text = "docker inspect failed"
		}
		return failed(lastLine(text))
	}
	state, health, _ := strings.Cut(strings.TrimSpace(stdout), "|")
	msg := state
	if health != "" {
		msg += " (" + health + ")"
	}
	ok := state == "running" && (health == "" || health == "healthy")
	return Result{OK: ok, Ms: ms, Msg: clip(msg)}
}

// a2sInfo is the A2S_INFO request of the Source engine query protocol.
var a2sInfo = append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'T'}, append([]byte("Source Engine Query"), 0)...)

// checkSteam asks a game server for its info, answering a challenge if it sends one.
func checkSteam(ctx context.Context, m Monitor) Result {
	addr := net.JoinHostPort(strings.Trim(m.Host, "[]"), strconv.Itoa(m.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return failed(errText(err))
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	start := time.Now()
	req := a2sInfo
	buf := make([]byte, 1400)
	for try := 0; try < 3; try++ {
		if _, err := conn.Write(req); err != nil {
			return failed(errText(err))
		}
		n, err := conn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return failed("No answer")
			}
			return failed(errText(err))
		}
		p := buf[:n]
		if len(p) < 5 || !bytes.Equal(p[:4], []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
			return failed("Not a Steam game server answer")
		}
		switch p[4] {
		case 'A': // S2C_CHALLENGE: ask again with it
			if len(p) < 9 {
				return failed("Not a Steam game server answer")
			}
			req = append(append([]byte{}, a2sInfo...), p[5:9]...)
			continue
		case 'I':
			info, err := parseA2SInfo(p[5:])
			if err != nil {
				return failed(err.Error())
			}
			return Result{OK: true, Ms: int(time.Since(start).Milliseconds()), Msg: clip(info)}
		}
		return failed("Not a Steam game server answer")
	}
	return failed("The server kept sending challenges")
}

// parseA2SInfo reads name, map and players of an A2S_INFO answer (after its header).
func parseA2SInfo(p []byte) (string, error) {
	bad := errors.New("Not a Steam game server answer")
	if len(p) < 1 {
		return "", bad
	}
	p = p[1:] // protocol
	var fields [4]string
	for i := range fields {
		end := bytes.IndexByte(p, 0)
		if end < 0 {
			return "", bad
		}
		fields[i] = string(p[:end])
		p = p[end+1:]
	}
	if len(p) < 4 {
		return "", bad
	}
	_ = binary.LittleEndian.Uint16(p[:2]) // app id
	players, max := p[2], p[3]
	return fmt.Sprintf("%s · %s · %d/%d players", fields[0], fields[1], players, max), nil
}
