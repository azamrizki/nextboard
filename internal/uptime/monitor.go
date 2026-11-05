// Package uptime watches services the way Uptime Kuma does (docs/API-uptime.md): the
// hub checks them on a schedule whether or not anyone has a board open, keeps the
// heartbeats, and says so when one goes down or comes back. A board tile only shows it.
package uptime

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	TypeHTTP = "http"
	TypeTCP  = "tcp"
	TypePing = "ping"
	TypeDNS  = "dns"
	TypePush = "push"

	// As in Uptime Kuma: below this a check says little more, and costs the target.
	MinIntervalSec     = 20
	MaxIntervalSec     = 86400
	DefaultIntervalSec = 60
	MaxTimeoutSec      = 60
	MaxRetries         = 10
	// MaxPerProject bounds what one project can make the hub do.
	MaxPerProject = 100

	maxNameLen    = 80
	maxKeywordLen = 200
	maxHeaders    = 20
)

// Header is one extra request header of an HTTP check.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Monitor is what to watch and how often. Which fields count depends on Type.
type Monitor struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	// MachineID is where http, tcp and ping checks run from; "" is This PC. From a server
	// they reach what only that server can see, such as its own loopback.
	MachineID string `json:"machineId,omitempty"`

	// http
	URL           string   `json:"url,omitempty"`
	Method        string   `json:"method,omitempty"`
	Accepted      string   `json:"accepted,omitempty"` // "200-299,301"
	Keyword       string   `json:"keyword,omitempty"`
	KeywordInvert bool     `json:"keywordInvert,omitempty"`
	IgnoreTLS     bool     `json:"ignoreTls,omitempty"`
	Headers       []Header `json:"headers,omitempty"`

	// tcp, ping, dns
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`

	// dns
	DNSType   string `json:"dnsType,omitempty"`
	DNSServer string `json:"dnsServer,omitempty"`
	DNSExpect string `json:"dnsExpect,omitempty"`

	// push: whoever holds the token can report for this monitor, nothing else
	PushToken string `json:"pushToken,omitempty"`

	IntervalSec      int `json:"intervalSec"`
	RetryIntervalSec int `json:"retryIntervalSec"`
	TimeoutSec       int `json:"timeoutSec"`
	// Retries is how many failed checks in a row stay "pending" before it counts as down.
	Retries int  `json:"retries"`
	Active  bool `json:"active"`
	Notify  bool `json:"notify"`

	CreatedAt time.Time `json:"createdAt"`

	// Which warning a certificate already got, so that a restart does not repeat it.
	CertNotAfter time.Time `json:"certNotAfter,omitzero"`
	CertWarned   int       `json:"certWarned,omitempty"`
}

// hostRe keeps a host usable as a program argument: never an option, never a shell word.
var hostRe = regexp.MustCompile(`^[A-Za-z0-9:\[]([A-Za-z0-9._:\-\[\]]*[A-Za-z0-9\]])?$`)

// headerNameRe is the token of RFC 9110.
var headerNameRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

var dnsTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "MX": true, "NS": true, "TXT": true}

// parseAccepted reads "200-299,301" into ranges.
func parseAccepted(s string) ([][2]int, error) {
	var out [][2]int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		from, to, isRange := strings.Cut(part, "-")
		a, errA := strconv.Atoi(strings.TrimSpace(from))
		b := a
		var errB error
		if isRange {
			b, errB = strconv.Atoi(strings.TrimSpace(to))
		}
		if errA != nil || errB != nil || a < 100 || b > 599 || a > b {
			return nil, fmt.Errorf("kode status tidak valid: %q", part)
		}
		out = append(out, [2]int{a, b})
	}
	if len(out) == 0 {
		return nil, errors.New("kode status wajib diisi")
	}
	return out, nil
}

// normalize fills the defaults and refuses what cannot work. The fields of other types
// are cleared, so that changing a monitor's type leaves nothing behind.
func (m *Monitor) normalize() error {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return errors.New("nama wajib diisi")
	}
	if len([]rune(m.Name)) > maxNameLen {
		return fmt.Errorf("nama maksimal %d karakter", maxNameLen)
	}
	if m.MachineID == "local" {
		m.MachineID = ""
	}
	o := *m
	m.URL, m.Method, m.Accepted, m.Keyword, m.KeywordInvert, m.IgnoreTLS, m.Headers = "", "", "", "", false, false, nil
	m.DNSType, m.DNSServer, m.DNSExpect = "", "", ""
	m.Host, m.Port, m.PushToken = "", 0, ""

	switch m.Type {
	case TypeHTTP:
		m.URL = strings.TrimSpace(o.URL)
		u, err := url.Parse(m.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return errors.New("URL harus http:// atau https:// dengan host")
		}
		m.Method = strings.ToUpper(o.Method)
		if m.Method == "" {
			m.Method = "GET"
		}
		if m.Method != "GET" && m.Method != "HEAD" {
			return errors.New("method harus GET atau HEAD")
		}
		m.Accepted = strings.TrimSpace(o.Accepted)
		if m.Accepted == "" {
			m.Accepted = "200-299"
		}
		if _, err := parseAccepted(m.Accepted); err != nil {
			return err
		}
		m.Keyword, m.KeywordInvert, m.IgnoreTLS = o.Keyword, o.KeywordInvert, o.IgnoreTLS
		if len(m.Keyword) > maxKeywordLen {
			return fmt.Errorf("keyword maksimal %d karakter", maxKeywordLen)
		}
		if m.Keyword != "" && m.Method == "HEAD" {
			return errors.New("keyword butuh isi halaman: pakai GET")
		}
		if len(o.Headers) > maxHeaders {
			return fmt.Errorf("header maksimal %d", maxHeaders)
		}
		for _, h := range o.Headers {
			h.Name = strings.TrimSpace(h.Name)
			if h.Name == "" && h.Value == "" {
				continue
			}
			if !headerNameRe.MatchString(h.Name) || strings.ContainsAny(h.Value, "\r\n") {
				return fmt.Errorf("header tidak valid: %q", h.Name)
			}
			m.Headers = append(m.Headers, h)
		}
	case TypeTCP, TypePing, TypeDNS:
		m.Host = strings.TrimSpace(o.Host)
		if !hostRe.MatchString(m.Host) {
			return errors.New("host tidak valid")
		}
		if m.Type == TypeTCP {
			m.Port = o.Port
			if m.Port < 1 || m.Port > 65535 {
				return errors.New("port harus 1 sampai 65535")
			}
		}
		if m.Type == TypeDNS {
			m.MachineID = "" // resolved by the hub: a server has no resolver the hub can ask
			m.DNSType = strings.ToUpper(o.DNSType)
			if m.DNSType == "" {
				m.DNSType = "A"
			}
			if !dnsTypes[m.DNSType] {
				return errors.New("tipe DNS harus A, AAAA, CNAME, MX, NS atau TXT")
			}
			m.DNSServer = strings.TrimSpace(o.DNSServer)
			if m.DNSServer != "" {
				h := m.DNSServer
				if a, _, err := net.SplitHostPort(h); err == nil {
					h = a
				}
				if !hostRe.MatchString(h) {
					return errors.New("server DNS tidak valid")
				}
			}
			m.DNSExpect = strings.TrimSpace(o.DNSExpect)
		}
	case TypePush:
		m.MachineID = "" // the hub only listens
		m.PushToken = o.PushToken
	default:
		return errors.New("tipe harus http, tcp, ping, dns atau push")
	}

	if m.IntervalSec == 0 {
		m.IntervalSec = DefaultIntervalSec
	}
	if m.IntervalSec < MinIntervalSec || m.IntervalSec > MaxIntervalSec {
		return fmt.Errorf("interval harus %d sampai %d detik", MinIntervalSec, MaxIntervalSec)
	}
	if m.RetryIntervalSec == 0 {
		m.RetryIntervalSec = m.IntervalSec
	}
	if m.RetryIntervalSec < MinIntervalSec || m.RetryIntervalSec > MaxIntervalSec {
		return fmt.Errorf("interval ulang harus %d sampai %d detik", MinIntervalSec, MaxIntervalSec)
	}
	if m.TimeoutSec == 0 {
		m.TimeoutSec = min(m.IntervalSec*8/10, 30)
	}
	if m.TimeoutSec < 1 || m.TimeoutSec > MaxTimeoutSec {
		return fmt.Errorf("timeout harus 1 sampai %d detik", MaxTimeoutSec)
	}
	if m.Retries < 0 || m.Retries > MaxRetries {
		return fmt.Errorf("retries harus 0 sampai %d", MaxRetries)
	}
	return nil
}

// Target says what is being watched, for people. Credentials in a URL stay out of it,
// because this text goes to notifications and webhooks.
func (m *Monitor) Target() string {
	switch m.Type {
	case TypeHTTP:
		if u, err := url.Parse(m.URL); err == nil {
			return u.Redacted()
		}
		return ""
	case TypeTCP:
		return net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	case TypePing:
		return m.Host
	case TypeDNS:
		return m.Host + " " + m.DNSType
	}
	return "push"
}
