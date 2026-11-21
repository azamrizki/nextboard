package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testPage = `<!doctype html>
<html><head><title>The  Title</title><style>body { color: red }</style><script>var x = "<b>no</b>";</script></head>
<body>
<nav><a href="/">Home</a> | <a href="#top">Top</a> <a href="javascript:void(0)">JS</a></nav>
<h1>Main   heading</h1>
<p>First &amp; <b>bold</b>
   paragraph with <a href="/docs?a=1&amp;b=2">the docs</a>.</p>
<noscript>Turn on JavaScript</noscript>
<ul><li>one</li><li>two <a href="https://other.example/x">https://other.example/x</a></li></ul>
<pre>  keep   this
    spacing</pre>
<table><tr><th>Name</th><th>Size</th></tr><tr><td>a</td><td>1</td></tr></table>
<p>caf&eacute; &lt;tag&gt; &#8212; end</p>
</body></html>`

func fetchServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, testPage)
	})
	mux.HandleFunc("/data.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"a": [1, 2], "b": "<x>"}`)
	})
	mux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "just   text\n  as is\n") // no content type: sniffed as text/plain
	})
	mux.HandleFunc("/image", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG\r\n\x1a\n0123456789"))
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/page", http.StatusFound)
	})
	mux.HandleFunc("/loop/", func(w http.ResponseWriter, r *http.Request) {
		var n int
		fmt.Sscanf(r.URL.Path, "/loop/%d", &n)
		http.Redirect(w, r, fmt.Sprintf("/loop/%d", n+1), http.StatusFound)
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "<h1>Not here</h1>")
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		chunk := strings.Repeat("z", 1<<20)
		for range 6 {
			fmt.Fprint(w, chunk)
		}
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestWebFetch(t *testing.T) {
	srv := fetchServer(t)
	other := httptest.NewServer(http.RedirectHandler(srv.URL+"/data.json", http.StatusMovedPermanently))
	t.Cleanup(other.Close)

	for _, hc := range []*http.Client{nil, srv.Client()} {
		env := &Env{Cwd: t.TempDir(), HTTP: hc}
		get := func(path string) Result {
			return call(t, "WebFetch", env, map[string]any{"url": path, "prompt": "What is here?"})
		}

		r := get(srv.URL + "/page")
		if r.IsError {
			t.Fatal(r.Output)
		}
		head := "URL: " + srv.URL + "/page\nStatus: 200\nAsked: What is here?\n\n"
		if !strings.HasPrefix(r.Output, head) {
			t.Errorf("header:\n%s", r.Output)
		}
		body := strings.TrimPrefix(r.Output, head)
		for _, want := range []string{
			"The Title\n\n",
			"Home (" + srv.URL + "/) | Top JS",
			"# Main heading\n\n",
			"First & bold paragraph with the docs (" + srv.URL + "/docs?a=1&b=2).",
			"- one\n- two https://other.example/x\n",
			"  keep   this\n    spacing",
			"Name | Size\na | 1",
			"café <tag> — end",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("text lacks %q:\n%s", want, body)
			}
		}
		for _, not := range []string{"color", "var x", "no</b>", "JavaScript", "javascript:", "#top"} {
			if strings.Contains(body, not) {
				t.Errorf("text has %q:\n%s", not, body)
			}
		}

		if r := get(srv.URL + "/data.json"); r.IsError || !strings.HasSuffix(r.Output, "\n\n"+`{"a": [1, 2], "b": "<x>"}`) {
			t.Errorf("json: %+v", r)
		}
		if r := get(srv.URL + "/plain"); r.IsError || !strings.HasSuffix(r.Output, "\n\njust   text\n  as is\n") {
			t.Errorf("plain: %+v", r)
		}
		if r := get(srv.URL + "/image"); r.IsError || !strings.HasSuffix(r.Output, "(content-type image/png, 18 bytes, not shown)") {
			t.Errorf("image: %+v", r)
		}
		r = get(srv.URL + "/redirect")
		if r.IsError || !strings.HasPrefix(r.Output, "URL: "+srv.URL+"/page\nStatus: 200") || strings.Contains(r.Output, "redirected") {
			t.Errorf("redirect: %+v", r.Output[:min(200, len(r.Output))])
		}
		r = get(other.URL + "/")
		if r.IsError || !strings.HasPrefix(r.Output, "URL: "+srv.URL+"/data.json\nStatus: 200\nAsked: What is here?\n\n(redirected from "+other.URL+"/: the page is on another host)\n\n{") {
			t.Errorf("another host: %+v", r)
		}
		if r := get(srv.URL + "/loop/0"); !r.IsError || !strings.Contains(r.Output, "more than 5 redirects") {
			t.Errorf("loop: %+v", r)
		}
		r = get(srv.URL + "/missing")
		if !r.IsError || !strings.Contains(r.Output, "Status: 404") || !strings.HasSuffix(r.Output, "# Not here") {
			t.Errorf("404: %+v", r)
		}
		r = get(srv.URL + "/big")
		if r.IsError || !strings.Contains(r.Output, "(cut at 100000 characters; ") || !strings.HasSuffix(r.Output, "only its first 5 MB was read)") {
			t.Errorf("big: %d bytes, ends %q", len(r.Output), r.Output[max(0, len(r.Output)-120):])
		}
		if n := strings.Count(r.Output, "z"); n != fetchMaxChars {
			t.Errorf("big: %d characters of text", n)
		}
	}
}

func TestWebFetchRedirectsWithinLimit(t *testing.T) {
	// /0 → … → /5 is five redirects: allowed, so the limit is not off by one
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var n int
		fmt.Sscanf(r.URL.Path, "/%d", &n)
		if n == 5 {
			fmt.Fprint(w, "arrived")
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/%d", n+1), http.StatusFound)
	})
	five := httptest.NewServer(mux)
	t.Cleanup(five.Close)
	r := call(t, "WebFetch", &Env{}, map[string]any{"url": five.URL + "/0", "prompt": "p"})
	if r.IsError || !strings.HasSuffix(r.Output, "arrived") {
		t.Errorf("five redirects: %+v", r)
	}
}

func TestWebFetchBadInput(t *testing.T) {
	env := &Env{}
	for _, in := range []map[string]any{
		{"url": "ftp://example.com/x", "prompt": "p"},
		{"url": "file:///etc/passwd", "prompt": "p"},
		{"url": "not a url", "prompt": "p"},
		{"url": "http://", "prompt": "p"},
		{"prompt": "p"},
		{"url": "http://127.0.0.1:1/"},
		{"url": "http://127.0.0.1:1/", "prompt": "p"}, // nothing listens there
	} {
		if r := call(t, "WebFetch", env, in); !r.IsError {
			t.Errorf("%v: %+v", in, r)
		}
	}
}

func TestWebFetchStopped(t *testing.T) {
	srv := fetchServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	r := callCtx(ctx, t, "WebFetch", &Env{}, map[string]any{"url": srv.URL + "/slow", "prompt": "p"})
	if !r.IsError || r.Output != "Stopped" || time.Since(start) > 5*time.Second {
		t.Errorf("got %+v after %v", r, time.Since(start))
	}
}
