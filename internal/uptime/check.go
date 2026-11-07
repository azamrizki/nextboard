package uptime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/version"
)

const (
	maxRedirects = 10
	maxBody      = 1 << 20 // what a keyword is looked for in
	maxMsg       = 300
)

// Result is the outcome of one check.
type Result struct {
	OK  bool
	Ms  int // -1 when no time was measured
	Msg string
	// Cert is the certificate of an HTTPS target, also when it failed the check.
	Cert *Cert
	// Waiting: the machine the check runs from is out of reach, which says nothing
	// about the target. It does not count as a heartbeat.
	Waiting bool
}

// Cert is what a check saw of a certificate.
type Cert struct {
	Subject  string    `json:"subject"`
	Issuer   string    `json:"issuer"`
	NotAfter time.Time `json:"notAfter"`
}

func failed(msg string) Result { return Result{Ms: -1, Msg: clip(msg)} }

func clip(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxMsg {
		return string(r[:maxMsg]) + "…"
	}
	return s
}

// errText is an error without the URL that net/http wraps around it: a URL can hold
// credentials, and this text goes to notifications.
func errText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Timeout"
	}
	return err.Error()
}

// check runs one check of m from h (nil for a DNS check, which the hub does itself).
func check(ctx context.Context, m Monitor, h machine.Host) Result {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.TimeoutSec)*time.Second)
	defer cancel()
	var r Result
	switch m.Type {
	case TypeHTTP:
		r = checkHTTP(ctx, m, h)
	case TypeTCP:
		r = checkTCP(ctx, m, h)
	case TypePing:
		r = checkPing(ctx, m, h)
	case TypeDNS:
		r = checkDNS(ctx, m)
	case TypeWebSocket:
		r = checkWebSocket(ctx, m, h)
	case TypeDocker:
		r = checkDocker(ctx, m, h)
	case TypeSteam:
		r = checkSteam(ctx, m)
	default:
		return failed("Tipe tidak dikenal")
	}
	if !r.OK && h != nil && !h.Connected() {
		return Result{Ms: -1, Waiting: true, Msg: "Waiting for " + h.Info().Name}
	}
	return r
}

func checkHTTP(ctx context.Context, m Monitor, h machine.Host) Result {
	accepted, err := parseAccepted(m.Accepted)
	if err != nil {
		return failed(err.Error())
	}
	// A new connection every time: a kept one would not notice a server gone away, and
	// the time would not include connecting. No proxy from the hub's environment either,
	// since the question is whether this target answers.
	tr := &http.Transport{
		DialContext:       func(ctx context.Context, _, addr string) (net.Conn, error) { return machine.Dial(ctx, h, addr) },
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: m.IgnoreTLS}, //nolint:gosec // the owner's choice per monitor
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{
		Transport: tr,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("lebih dari %d redirect", maxRedirects)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, m.Method, m.URL, nil)
	if err != nil {
		return failed(errText(err))
	}
	req.Header.Set("User-Agent", "NextBoard-Uptime/"+version.Version)
	req.Header.Set("Accept", "*/*")
	for _, hd := range m.Headers {
		req.Header.Set(hd.Name, hd.Value)
	}
	start := time.Now()
	resp, err := cl.Do(req)
	if err != nil {
		return failed(errText(err))
	}
	defer resp.Body.Close()
	ms := int(time.Since(start).Milliseconds())

	r := Result{Ms: ms, Msg: resp.Status}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		c := resp.TLS.PeerCertificates[0]
		r.Cert = &Cert{Subject: c.Subject.CommonName, Issuer: c.Issuer.CommonName, NotAfter: c.NotAfter}
		if r.Cert.Issuer == "" && len(c.Issuer.Organization) > 0 {
			r.Cert.Issuer = c.Issuer.Organization[0]
		}
	}
	ok := false
	for _, a := range accepted {
		ok = ok || (resp.StatusCode >= a[0] && resp.StatusCode <= a[1])
	}
	if !ok {
		return r
	}
	if m.Keyword == "" && m.JSONPath == "" {
		r.OK = true
		return r
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		r.Msg = errText(err)
		return r
	}
	r.Ms = int(time.Since(start).Milliseconds())
	if m.JSONPath != "" {
		ok, msg := jsonQuery(body, m.JSONPath, m.JSONOp, m.JSONExpect)
		r.Msg = clip(resp.Status + ", " + msg)
		if !ok || m.Keyword == "" {
			r.OK = ok
			return r
		}
	}
	found := strings.Contains(string(body), m.Keyword)
	switch {
	case found && !m.KeywordInvert:
		r.OK = true
	case !found && m.KeywordInvert:
		r.OK = true
	case found:
		r.Msg = clip(fmt.Sprintf("%s, %q found", resp.Status, m.Keyword))
	default:
		r.Msg = clip(fmt.Sprintf("%s, %q not found", resp.Status, m.Keyword))
	}
	return r
}

func checkTCP(ctx context.Context, m Monitor, h machine.Host) Result {
	addr := net.JoinHostPort(strings.Trim(m.Host, "[]"), strconv.Itoa(m.Port))
	start := time.Now()
	conn, err := machine.Dial(ctx, h, addr)
	if err != nil {
		return failed(errText(err))
	}
	conn.Close()
	return Result{OK: true, Ms: int(time.Since(start).Milliseconds()), Msg: "Port open"}
}

var pingTime = regexp.MustCompile(`time[=<]\s*([0-9.]+)\s*ms`)

// checkPing uses the machine's ping, as Uptime Kuma does: raw ICMP needs rights the hub
// does not have. The wait is the check's timeout, because -W means different things on
// different systems.
func checkPing(ctx context.Context, m Monitor, h machine.Host) Result {
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Argv: []string{"ping", "-c", "1", m.Host}, MaxOutput: 16 << 10})
	if err != nil {
		return failed(errText(err))
	}
	if res.ExitCode != 0 {
		if ctx.Err() != nil || res.ExitCode == -1 {
			return failed("Timeout")
		}
		text := strings.TrimSpace(string(res.Stderr))
		if text == "" {
			text = "Host unreachable"
		}
		return failed(lastLine(text))
	}
	r := Result{OK: true, Ms: -1, Msg: "Reachable"}
	if t := pingTime.FindSubmatch(res.Stdout); t != nil {
		if v, err := strconv.ParseFloat(string(t[1]), 64); err == nil {
			r.Ms = int(v + 0.5)
		}
	}
	return r
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func checkDNS(ctx context.Context, m Monitor) Result {
	res := net.DefaultResolver
	if m.DNSServer != "" {
		server := m.DNSServer
		if _, _, err := net.SplitHostPort(server); err != nil {
			server = net.JoinHostPort(strings.Trim(server, "[]"), "53")
		}
		res = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server)
		}}
	}
	start := time.Now()
	answers, err := lookup(ctx, res, m.DNSType, m.Host)
	ms := int(time.Since(start).Milliseconds())
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) {
			switch {
			case de.IsNotFound:
				return failed("No such record")
			case de.IsTimeout:
				return failed("Timeout")
			}
		}
		return failed(errText(err))
	}
	if len(answers) == 0 {
		return failed("No such record")
	}
	msg := clip(strings.Join(answers, ", "))
	if m.DNSExpect != "" {
		for _, a := range answers {
			if strings.Contains(a, m.DNSExpect) {
				return Result{OK: true, Ms: ms, Msg: msg}
			}
		}
		return Result{Ms: ms, Msg: clip(fmt.Sprintf("%q not in %s", m.DNSExpect, msg))}
	}
	return Result{OK: true, Ms: ms, Msg: msg}
}

func lookup(ctx context.Context, res *net.Resolver, typ, host string) ([]string, error) {
	switch typ {
	case "A", "AAAA":
		family := "ip4"
		if typ == "AAAA" {
			family = "ip6"
		}
		ips, err := res.LookupIP(ctx, family, host)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(ips))
		for i, ip := range ips {
			out[i] = ip.String()
		}
		return out, nil
	case "CNAME":
		c, err := res.LookupCNAME(ctx, host)
		if err != nil {
			return nil, err
		}
		return []string{c}, nil
	case "MX":
		list, err := res.LookupMX(ctx, host)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(list))
		for i, mx := range list {
			out[i] = fmt.Sprintf("%d %s", mx.Pref, mx.Host)
		}
		return out, nil
	case "NS":
		list, err := res.LookupNS(ctx, host)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(list))
		for i, ns := range list {
			out[i] = ns.Host
		}
		return out, nil
	case "TXT":
		return res.LookupTXT(ctx, host)
	}
	return nil, errors.New("tipe DNS tidak dikenal")
}
