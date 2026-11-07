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
	// TypeWebSocket asks for a WebSocket upgrade and wants it granted (101).
	TypeWebSocket = "websocket"
	// TypeDocker asks the machine's docker whether a container runs (and is healthy).
	TypeDocker = "docker"
	// TypeSteam asks a game server for its info (A2S_INFO over UDP), from This PC.
	TypeSteam = "steam"

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
	maxGroupLen   = 40
	maxTags       = 10
	maxTagLen     = 24
	maxJSONPath   = 200
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
	// JSONPath picks a value from a JSON answer ("data.items[0].status"); JSONOp compares
	// it with JSONExpect: == != contains < > <= >=.
	JSONPath   string `json:"jsonPath,omitempty"`
	JSONOp     string `json:"jsonOp,omitempty"`
	JSONExpect string `json:"jsonExpect,omitempty"`

	// docker: a container's name or id
	Container string `json:"container,omitempty"`

	// tcp, ping, dns, steam
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

	// Public puts it on the project's public status page, under PublicName and nothing else
	// of it (public.go).
	Public     bool   `json:"public,omitempty"`
	PublicName string `json:"publicName,omitempty"`

	// Group puts monitors under one heading; Tags filter them. Neither is on the public page.
	Group string   `json:"group,omitempty"`
	Tags  []string `json:"tags,omitempty"`

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

var jsonOps = map[string]bool{"==": true, "!=": true, "contains": true, "<": true, ">": true, "<=": true, ">=": true}

// containerRe is a docker container's name or id, never an option.
var containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

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
			return nil, fmt.Errorf("invalid status code: %q", part)
		}
		out = append(out, [2]int{a, b})
	}
	if len(out) == 0 {
		return nil, errors.New("a status code is required")
	}
	return out, nil
}

// normalize fills the defaults and refuses what cannot work. The fields of other types
// are cleared, so that changing a monitor's type leaves nothing behind.
func (m *Monitor) normalize() error {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return errors.New("a name is required")
	}
	if len([]rune(m.Name)) > maxNameLen {
		return fmt.Errorf("the name is at most %d characters", maxNameLen)
	}
	if m.MachineID == "local" {
		m.MachineID = ""
	}
	if err := m.normalizeLabels(); err != nil {
		return err
	}
	o := *m
	m.URL, m.Method, m.Accepted, m.Keyword, m.KeywordInvert, m.IgnoreTLS, m.Headers = "", "", "", "", false, false, nil
	m.JSONPath, m.JSONOp, m.JSONExpect, m.Container = "", "", "", ""
	m.DNSType, m.DNSServer, m.DNSExpect = "", "", ""
	m.Host, m.Port, m.PushToken = "", 0, ""

	switch m.Type {
	case TypeWebSocket:
		m.URL = strings.TrimSpace(o.URL)
		u, err := url.Parse(m.URL)
		if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() == "" {
			return errors.New("URL must be ws:// or wss:// with a host")
		}
		m.IgnoreTLS = o.IgnoreTLS
		if err := m.takeHeaders(o.Headers); err != nil {
			return err
		}
	case TypeDocker:
		m.Container = strings.TrimSpace(o.Container)
		if !containerRe.MatchString(m.Container) {
			return errors.New("container must be a name or id")
		}
	case TypeSteam:
		m.MachineID = "" // UDP: the hub asks, a server's SSH carries no datagrams
		m.Host = strings.TrimSpace(o.Host)
		if !hostRe.MatchString(m.Host) {
			return errors.New("invalid host")
		}
		m.Port = o.Port
		if m.Port == 0 {
			m.Port = 27015
		}
		if m.Port < 1 || m.Port > 65535 {
			return errors.New("the port must be 1 to 65535")
		}
	case TypeHTTP:
		m.URL = strings.TrimSpace(o.URL)
		u, err := url.Parse(m.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return errors.New("URL must be http:// or https:// with a host")
		}
		m.Method = strings.ToUpper(o.Method)
		if m.Method == "" {
			m.Method = "GET"
		}
		if m.Method != "GET" && m.Method != "HEAD" {
			return errors.New("method must be GET or HEAD")
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
			return fmt.Errorf("keyword is at most %d characters", maxKeywordLen)
		}
		if m.Keyword != "" && m.Method == "HEAD" {
			return errors.New("a keyword needs the body: use GET")
		}
		if err := m.takeHeaders(o.Headers); err != nil {
			return err
		}
		m.JSONPath = strings.TrimSpace(o.JSONPath)
		if m.JSONPath != "" {
			if len(m.JSONPath) > maxJSONPath {
				return fmt.Errorf("JSON query is at most %d characters", maxJSONPath)
			}
			if _, err := parseJSONPath(m.JSONPath); err != nil {
				return err
			}
			if m.Method == "HEAD" {
				return errors.New("a JSON query needs the body: use GET")
			}
			m.JSONOp, m.JSONExpect = o.JSONOp, o.JSONExpect
			if m.JSONOp == "" {
				m.JSONOp = "=="
			}
			if !jsonOps[m.JSONOp] {
				return errors.New("JSON condition must be ==, !=, contains, <, >, <= or >=")
			}
			if len(m.JSONExpect) > maxKeywordLen {
				return fmt.Errorf("expected value is at most %d characters", maxKeywordLen)
			}
		}
	case TypeTCP, TypePing, TypeDNS:
		m.Host = strings.TrimSpace(o.Host)
		if !hostRe.MatchString(m.Host) {
			return errors.New("invalid host")
		}
		if m.Type == TypeTCP {
			m.Port = o.Port
			if m.Port < 1 || m.Port > 65535 {
				return errors.New("the port must be 1 to 65535")
			}
		}
		if m.Type == TypeDNS {
			m.MachineID = "" // resolved by the hub: a server has no resolver the hub can ask
			m.DNSType = strings.ToUpper(o.DNSType)
			if m.DNSType == "" {
				m.DNSType = "A"
			}
			if !dnsTypes[m.DNSType] {
				return errors.New("DNS type must be A, AAAA, CNAME, MX, NS or TXT")
			}
			m.DNSServer = strings.TrimSpace(o.DNSServer)
			if m.DNSServer != "" {
				h := m.DNSServer
				if a, _, err := net.SplitHostPort(h); err == nil {
					h = a
				}
				if !hostRe.MatchString(h) {
					return errors.New("invalid DNS server")
				}
			}
			m.DNSExpect = strings.TrimSpace(o.DNSExpect)
		}
	case TypePush:
		m.MachineID = "" // the hub only listens
		m.PushToken = o.PushToken
	default:
		return errors.New("type must be http, tcp, ping, dns, push, websocket, docker or steam")
	}

	if m.IntervalSec == 0 {
		m.IntervalSec = DefaultIntervalSec
	}
	if m.IntervalSec < MinIntervalSec || m.IntervalSec > MaxIntervalSec {
		return fmt.Errorf("the interval must be %d to %d seconds", MinIntervalSec, MaxIntervalSec)
	}
	if m.RetryIntervalSec == 0 {
		m.RetryIntervalSec = m.IntervalSec
	}
	if m.RetryIntervalSec < MinIntervalSec || m.RetryIntervalSec > MaxIntervalSec {
		return fmt.Errorf("the retry interval must be %d to %d seconds", MinIntervalSec, MaxIntervalSec)
	}
	if m.TimeoutSec == 0 {
		m.TimeoutSec = min(m.IntervalSec*8/10, 30)
	}
	if m.TimeoutSec < 1 || m.TimeoutSec > MaxTimeoutSec {
		return fmt.Errorf("the timeout must be 1 to %d seconds", MaxTimeoutSec)
	}
	if m.Retries < 0 || m.Retries > MaxRetries {
		return fmt.Errorf("retries must be 0 to %d", MaxRetries)
	}
	return nil
}

func (m *Monitor) takeHeaders(in []Header) error {
	if len(in) > maxHeaders {
		return fmt.Errorf("at most %d headers", maxHeaders)
	}
	for _, h := range in {
		h.Name = strings.TrimSpace(h.Name)
		if h.Name == "" && h.Value == "" {
			continue
		}
		if !headerNameRe.MatchString(h.Name) || strings.ContainsAny(h.Value, "\r\n") {
			return fmt.Errorf("invalid header: %q", h.Name)
		}
		m.Headers = append(m.Headers, h)
	}
	return nil
}

// normalizeLabels tidies the group and the tags: trimmed, tags once each.
func (m *Monitor) normalizeLabels() error {
	m.Group = strings.TrimSpace(m.Group)
	if len([]rune(m.Group)) > maxGroupLen {
		return fmt.Errorf("group is at most %d characters", maxGroupLen)
	}
	seen := map[string]bool{}
	var tags []string
	for _, t := range m.Tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if len([]rune(t)) > maxTagLen || strings.ContainsAny(t, ",\r\n") {
			return fmt.Errorf("a tag is at most %d characters, without commas", maxTagLen)
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	if len(tags) > maxTags {
		return fmt.Errorf("at most %d tags", maxTags)
	}
	m.Tags = tags
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
	case TypeWebSocket:
		if u, err := url.Parse(m.URL); err == nil {
			return u.Redacted()
		}
		return ""
	case TypeDocker:
		return "container " + m.Container
	case TypeSteam:
		return net.JoinHostPort(m.Host, strconv.Itoa(m.Port)) + " (Steam)"
	}
	return "push"
}
