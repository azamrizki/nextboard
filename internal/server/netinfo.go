package server

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// localIPs returns non-loopback unicast addresses of up interfaces.
func localIPs() []net.IP {
	var out []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := ifc.Name
		if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "virbr") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLoopback() {
				continue
			}
			out = append(out, ipn.IP)
		}
	}
	return out
}

type tailscaleInfo struct {
	DNSName string
	IPs     []string
}

// detectTailscale asks the local tailscale CLI for this node's MagicDNS
// name (best effort, 2 s timeout).
func detectTailscale() *tailscaleInfo {
	path, err := exec.LookPath("tailscale")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "status", "--json").Output()
	if err != nil {
		return nil
	}
	var st struct {
		Self struct {
			DNSName      string
			TailscaleIPs []string
		}
	}
	if json.Unmarshal(out, &st) != nil || st.Self.DNSName == "" {
		return nil
	}
	return &tailscaleInfo{DNSName: strings.TrimSuffix(st.Self.DNSName, "."), IPs: st.Self.TailscaleIPs}
}

// HubURLs lists every address a client may reach the hub at, best first:
// configured extras, Tailscale name, Tailscale IPs, then LAN IPs.
func HubURLs(listen string, extra []string) []string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = "", "12312"
	}
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, e := range extra {
		add(strings.TrimRight(e, "/"))
	}
	hp := func(h string) string { return "http://" + net.JoinHostPort(h, port) }
	all := host == "" || host == "0.0.0.0" || host == "::"
	if !all {
		add(hp(host))
		return out
	}
	ts := detectTailscale()
	tsIPs := map[string]bool{}
	if ts != nil {
		add(hp(ts.DNSName))
		for _, ip := range ts.IPs {
			if !strings.Contains(ip, ":") {
				add(hp(ip))
			}
			tsIPs[ip] = true
		}
	}
	// IPv4 only: IPv6 addresses are rarely useful for pairing.
	for _, ip := range localIPs() {
		if s := ip.String(); ip.To4() != nil && !tsIPs[s] {
			add(hp(s))
		}
	}
	if len(out) == 0 {
		add("http://127.0.0.1:" + port)
	}
	return out
}

// PrimaryURL is the first advertised URL (for CLI output).
func PrimaryURL(listen string, extra []string) string {
	u := HubURLs(listen, extra)
	if len(u) == 0 {
		_, port, _ := net.SplitHostPort(listen)
		if _, err := strconv.Atoi(port); err != nil {
			port = "12312"
		}
		return "http://127.0.0.1:" + port
	}
	return u[0]
}

// isLoopbackListen reports whether listen only accepts local connections.
func isLoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// AdvertisedURLs is what clients are told to connect to: only the HTTPS
// addresses once there is one, so pairing codes and device tokens cannot
// fall back to cleartext.
func AdvertisedURLs(urls []string) []string {
	var secure []string
	for _, u := range urls {
		if strings.HasPrefix(u, "https://") {
			secure = append(secure, u)
		}
	}
	if len(secure) > 0 {
		return secure
	}
	return urls
}
