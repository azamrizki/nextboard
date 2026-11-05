package cli

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var previewListen = regexp.MustCompile(`listen PUBLIC_IP:\d+ ssl;`)

func selfSignedCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "board.example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"board.example.com"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// startNginx runs the example from deploy/ as its own unprivileged process on
// high ports, changed only as far as its header tells users to, plus those ports
// and the paths. The machine's own nginx, if any, is not involved.
func startNginx(t *testing.T, nginx, hubListen string) (httpPort, httpsPort int) {
	t.Helper()
	example, err := os.ReadFile("../../deploy/nginx.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	inc, err := filepath.Abs("../../deploy/nextboard-proxy.inc")
	if err != nil {
		t.Fatal(err)
	}
	prefix := t.TempDir()
	cert, key := selfSignedCert(t, prefix)
	httpPort, httpsPort = freePort(t), freePort(t)
	conf := strings.NewReplacer(
		"listen 80;", fmt.Sprintf("listen 127.0.0.1:%d;", httpPort),
		"listen 443 ssl", fmt.Sprintf("listen 127.0.0.1:%d ssl", httpsPort),
		"/etc/letsencrypt/live/board.example.com/fullchain.pem", cert,
		"/etc/letsencrypt/live/board.example.com/privkey.pem", key,
		"proxy_pass http://127.0.0.1:12312", "proxy_pass http://"+hubListen,
		"/etc/nginx/nextboard-proxy.inc", inc,
	).Replace(string(example))
	// The preview ports may be taken on this machine, by a hub that binds all addresses.
	conf = previewListen.ReplaceAllStringFunc(conf, func(string) string {
		return fmt.Sprintf("listen 127.0.0.2:%d ssl;", freePort(t))
	})
	for _, line := range strings.Split(conf, "\n") {
		if l := strings.TrimSpace(line); !strings.HasPrefix(l, "#") && strings.Contains(l, "/etc/") {
			t.Fatalf("a path of the real machine is left in: %s", line)
		}
	}
	main := fmt.Sprintf(`daemon off;
master_process off;
pid %[1]s/nginx.pid;
error_log %[1]s/error.log;
events { worker_connections 64; }
http {
  access_log off;
  client_body_temp_path %[1]s/body;
  proxy_temp_path %[1]s/proxy;
  fastcgi_temp_path %[1]s/fastcgi;
  uwsgi_temp_path %[1]s/uwsgi;
  scgi_temp_path %[1]s/scgi;
%[2]s
}
`, prefix, conf)
	confFile := filepath.Join(prefix, "nginx.conf")
	if err := os.WriteFile(confFile, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-p", prefix, "-e", filepath.Join(prefix, "error.log"), "-c", confFile}
	if out, err := exec.Command(nginx, append(args, "-t")...).CombinedOutput(); err != nil {
		t.Fatalf("deploy/nginx.conf.example does not load: %v\n%s", err, out)
	}
	cmd := exec.Command(nginx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			raw, _ := os.ReadFile(filepath.Join(prefix, "error.log"))
			t.Logf("nginx said: %s\n%s", stderr.String(), raw)
		}
	})
	return httpPort, httpsPort
}

// SPEC F1.2: following the deploy files gives HTTPS with a login that works, a
// Secure cookie and terminals over wss.
func TestBehindTheNginxExample(t *testing.T) {
	nginx, err := exec.LookPath("nginx")
	if err != nil {
		if _, serr := os.Stat("/usr/sbin/nginx"); serr != nil {
			t.Skip("no nginx")
		}
		nginx = "/usr/sbin/nginx"
	}
	b := newBox(t)
	data := t.TempDir()
	startHub(t, b, data)
	httpPort, httpsPort := startNginx(t, nginx, b.listen)

	insecure := &tls.Config{InsecureSkipVerify: true} // the test's own certificate
	jar, _ := cookiejar.New(nil)
	base := fmt.Sprintf("https://127.0.0.1:%d", httpsPort)
	c := &client{t: t, base: base, c: &http.Client{Jar: jar, Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: insecure}}}

	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := c.c.Get(base + "/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer over HTTPS: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Plain HTTP goes to HTTPS.
	noFollow := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(fmt.Sprintf("http://127.0.0.1:%d/board", httpPort))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusMovedPermanently || !strings.HasPrefix(loc, "https://") || !strings.HasSuffix(loc, "/board") {
		t.Errorf("plain HTTP: %d to %q", resp.StatusCode, loc)
	}

	// The wizard's question, and the page around it when this build has one.
	if st, r := c.do("GET", "/api/setup/status", nil); st != 200 || r["needsSetup"] != true {
		t.Fatalf("setup status %d %v", st, r)
	}
	if st, page := c.do("GET", "/setup", nil); !(st == 200 && strings.Contains(fmt.Sprint(page["_raw"]), "<html")) &&
		!(st == 404 && strings.Contains(fmt.Sprint(page["_raw"]), "no web UI")) {
		t.Errorf("the page: %d %v", st, page)
	}

	link := exec.Command(nextboardBinary(t), "setup-link", "--data-dir", data)
	link.Env = b.env()
	out, err := link.CombinedOutput()
	if err != nil {
		t.Fatalf("setup-link: %v %s", err, out)
	}
	m := setupCodeRe.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no link: %s", out)
	}
	if st, r := c.do("POST", "/api/setup", map[string]any{"code": string(m[1]), "password": "rahasia123", "hubName": "devbox"}); st != 200 {
		t.Fatalf("setup over HTTPS %d %v", st, r)
	}
	if st, _ := c.do("GET", "/api/auth/me", nil); st != 200 {
		t.Fatalf("not logged in over HTTPS: %d", st)
	}
	secure, _ := url.Parse(base)
	// The header itself: Go's cookie jar treats a loopback address as secure.
	req, err := http.NewRequest("POST", base+"/api/auth/login", strings.NewReader(`{"password":"rahasia123"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-NextBoard", "1")
	login, err := (&http.Client{Timeout: 20 * time.Second, Transport: c.c.Transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	set := login.Header.Get("Set-Cookie")
	if login.StatusCode != 200 || !strings.Contains(set, "Secure") || !strings.Contains(set, "HttpOnly") || !strings.Contains(set, "SameSite=Strict") {
		t.Errorf("login over HTTPS: %d, cookie %q", login.StatusCode, set)
	}

	// A terminal over wss, through the proxy's upgrade.
	st, sess := c.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": "shell", "cols": 100, "rows": 30})
	if st != 201 {
		t.Fatalf("console %d %v", st, sess)
	}
	wsURL := "wss://127.0.0.1:" + fmt.Sprint(httpsPort) + "/api/sessions/" + sess["id"].(string) + "/ws"
	dialer := websocket.Dialer{TLSClientConfig: insecure, HandshakeTimeout: 10 * time.Second}
	hdr := http.Header{}
	for _, ck := range jar.Cookies(secure) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	hdr.Set("Origin", "https://evil.example")
	if _, resp, err := dialer.Dial(wsURL, hdr); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("another site's page opened a terminal: %v", err)
	}
	hdr.Set("Origin", base)
	ws, _, err := dialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("wss: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(map[string]any{"t": "i", "d": "echo nb-tls-$((40+2))\r"}); err != nil {
		t.Fatal(err)
	}
	var seen bytes.Buffer
	_ = ws.SetReadDeadline(time.Now().Add(20 * time.Second))
	for !strings.Contains(seen.String(), "nb-tls-42") {
		_, frame, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("%v, saw %q", err, seen.String())
		}
		seen.Write(frame)
	}
}
