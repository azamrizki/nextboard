package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	fetchTimeout   = 30 * time.Second
	fetchMaxBody   = 5 << 20 // bytes of a body read
	fetchMaxChars  = 100000  // characters of text returned
	fetchRedirects = 5
	fetchUserAgent = "Mozilla/5.0 (compatible; NextCode-WebFetch/1.0)"
)

var errRedirects = fmt.Errorf("more than %d redirects", fetchRedirects)

const webFetchDescription = `Fetches a URL and returns the page as text: HTML becomes plain text, with links as "text (url)"; JSON and other text come as they are.

- url is a full http or https URL. Up to 5 redirects are followed; when the page ends up on another host, the result says so.
- prompt is what you want to learn from the page. No other model reads the page for you: the text comes back to you with the prompt at its top, so read it with that question in mind.
- At most 5 MB is read and 100000 characters returned. Images, PDFs and other binary content are not shown.
- It cannot log in: a page behind a login fails or shows its login page. For a GitHub repository, issue or pull request, gh in Bash usually does better.`

const webFetchSchema = `{
  "type": "object",
  "properties": {
    "url": {"type": "string", "format": "uri", "description": "The URL to fetch"},
    "prompt": {"type": "string", "description": "What you want to learn from the page"}
  },
  "required": ["url", "prompt"],
  "additionalProperties": false
}`

func webFetchTool() Tool {
	return newTool("WebFetch", KindFetch, webFetchDescription, webFetchSchema, runWebFetch)
}

type fetchInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

func runWebFetch(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in fetchInput
	if err := decode("WebFetch", input, &in); err != nil {
		return fail("%v", err)
	}
	if strings.TrimSpace(in.URL) == "" {
		return fail("WebFetch needs url: the page to fetch")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return fail("WebFetch needs prompt: what you want to learn from the page")
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fail("WebFetch takes a full http or https URL, not %q", in.URL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fail("WebFetch cannot fetch %s: %v", u, err)
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,application/json;q=0.9,*/*;q=0.5")
	resp, err := fetchClient(env.HTTP).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return stopped()
		}
		if errors.Is(err, errRedirects) {
			return fail("Could not fetch %s: %v", u, errRedirects)
		}
		return fail("Could not fetch %s: %v", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBody+1))
	if ctx.Err() != nil {
		return stopped()
	}
	if err != nil {
		return fail("Could not read %s: %v", u, err)
	}
	big := len(body) > fetchMaxBody
	if big {
		body = body[:fetchMaxBody]
	}
	final := resp.Request.URL
	text := pageText(resp.Header.Get("Content-Type"), body, final)
	text, cut := cutText(text, fetchMaxChars)

	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\nStatus: %d\nAsked: %s\n\n", final, resp.StatusCode, strings.TrimSpace(in.Prompt))
	if !strings.EqualFold(final.Host, u.Host) {
		fmt.Fprintf(&b, "(redirected from %s: the page is on another host)\n\n", u)
	}
	if strings.TrimSpace(text) == "" {
		text = "(the page has no text)"
	}
	b.WriteString(text)
	if cut > 0 {
		fmt.Fprintf(&b, "\n\n(cut at %d characters; %d more not shown)", fetchMaxChars, cut)
	}
	if big {
		b.WriteString("\n\n(the body is over 5 MB: only its first 5 MB was read)")
	}
	return Result{Output: b.String(), IsError: resp.StatusCode >= 400}
}

// fetchClient is the agent's HTTP client (or one with a 30 s timeout), following at most
// fetchRedirects redirects.
func fetchClient(hc *http.Client) *http.Client {
	c := &http.Client{Timeout: fetchTimeout}
	if hc != nil {
		cp := *hc
		c = &cp
	}
	c.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
		if len(via) > fetchRedirects {
			return errRedirects
		}
		return nil
	}
	return c
}

// pageText is a body as text for the model: HTML turned into text, any other text as it is,
// and only a note for the rest.
func pageText(contentType string, body []byte, base *url.URL) string {
	mt, _, _ := mime.ParseMediaType(contentType)
	if mt == "" {
		mt, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	switch {
	case mt == "text/html" || mt == "application/xhtml+xml":
		return htmlText(body, base)
	case textual(mt):
		return strings.ToValidUTF8(string(body), "�")
	}
	return fmt.Sprintf("(content-type %s, %d bytes, not shown)", mt, len(body))
}

func textual(mt string) bool {
	switch {
	case strings.HasPrefix(mt, "text/"), strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"):
		return true
	}
	switch mt {
	case "application/json", "application/xml", "application/javascript", "application/x-javascript",
		"application/ecmascript", "application/yaml", "application/x-yaml", "application/toml",
		"application/x-ndjson", "application/x-sh", "application/graphql", "application/sql":
		return true
	}
	return false
}

// htmlText turns an HTML page into readable text: script, style and the like dropped, blocks
// on lines of their own, links as "text (url)", whitespace collapsed (but in <pre>).
func htmlText(body []byte, base *url.URL) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return strings.ToValidUTF8(string(body), "�")
	}
	w := &textWriter{base: base}
	w.node(doc)
	return strings.TrimSpace(strings.ToValidUTF8(w.b.String(), "�"))
}

// textWriter writes the text of HTML nodes, owing newlines and spaces until the next word so
// that runs of whitespace and empty blocks collapse.
type textWriter struct {
	b     strings.Builder
	base  *url.URL
	nl    int  // newlines owed before the next word
	space bool // a space owed before the next word
	pre   int  // within <pre>
	col   int  // cells written in the current table row
}

// full is true once enough text is written; the rest of the page is skipped.
func (w *textWriter) full() bool { return w.b.Len() > fetchMaxChars*4 }

func (w *textWriter) word(s string) {
	if w.b.Len() > 0 {
		switch {
		case w.nl > 0:
			w.b.WriteString(strings.Repeat("\n", min(w.nl, 2)))
		case w.space:
			w.b.WriteByte(' ')
		}
	}
	w.nl, w.space = 0, false
	w.b.WriteString(s)
}

func (w *textWriter) block(n int) { w.nl = max(w.nl, n) }

func (w *textWriter) text(s string) {
	if w.pre > 0 {
		w.word("")
		w.b.WriteString(s)
		return
	}
	if s != "" && strings.TrimLeft(s, " \t\r\n\f ") != s {
		w.space = true
	}
	for i, f := range strings.Fields(s) {
		if i > 0 {
			w.space = true
		}
		w.word(f)
	}
	if s != "" && strings.TrimRight(s, " \t\r\n\f ") != s {
		w.space = true
	}
}

func (w *textWriter) children(n *html.Node) {
	for c := n.FirstChild; c != nil && !w.full(); c = c.NextSibling {
		w.node(c)
	}
}

func (w *textWriter) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.text(n.Data)
		return
	case html.DocumentNode:
		w.children(n)
		return
	case html.ElementNode:
	default:
		return
	}
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript, atom.Template, atom.Svg, atom.Iframe, atom.Object, atom.Canvas:
		return
	case atom.Head: // only its title
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.DataAtom == atom.Title {
				w.children(c)
				w.block(2)
			}
		}
		return
	case atom.Br:
		w.block(1)
		return
	case atom.Hr:
		w.block(2)
		return
	case atom.A:
		w.link(n)
		return
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		w.block(2)
		w.word(strings.Repeat("#", int(n.Data[1]-'0')))
		w.space = true
		w.children(n)
		w.block(2)
		return
	case atom.Li:
		w.block(1)
		w.word("-")
		w.space = true
		w.children(n)
		w.block(1)
		return
	case atom.Pre:
		w.block(2)
		w.pre++
		w.children(n)
		w.pre--
		w.block(2)
		return
	case atom.Tr:
		w.block(1)
		w.col = 0
		w.children(n)
		w.block(1)
		return
	case atom.Td, atom.Th:
		if w.col > 0 {
			w.space = true
			w.word("|")
			w.space = true
		}
		w.col++
		w.children(n)
		return
	case atom.P, atom.Blockquote, atom.Table, atom.Ul, atom.Ol, atom.Dl, atom.Figure, atom.Form, atom.Fieldset, atom.Details:
		w.block(2)
		w.children(n)
		w.block(2)
		return
	case atom.Div, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Nav, atom.Main, atom.Aside,
		atom.Dt, atom.Dd, atom.Figcaption, atom.Summary, atom.Address, atom.Caption, atom.Thead, atom.Tbody, atom.Tfoot,
		atom.Body, atom.Html, atom.Center, atom.Option, atom.Legend:
		w.block(1)
		w.children(n)
		w.block(1)
		return
	}
	w.children(n)
}

// link writes a link as "text (url)", or its url alone when it has no text of its own.
func (w *textWriter) link(n *html.Node) {
	start := w.b.Len()
	w.children(n)
	href := w.href(n)
	if href == "" {
		return
	}
	shown := strings.TrimSpace(w.b.String()[start:])
	switch {
	case shown == "":
		w.space = true
		w.word(href)
	case shown != href:
		w.space = true
		w.word("(" + href + ")")
	}
}

// href is a link's target, absolute; "" for none, a fragment on the same page, or a script.
func (w *textWriter) href(n *html.Node) string {
	var h string
	for _, a := range n.Attr {
		if a.Key == "href" {
			h = strings.TrimSpace(a.Val)
		}
	}
	if h == "" || strings.HasPrefix(h, "#") || strings.HasPrefix(strings.ToLower(h), "javascript:") {
		return ""
	}
	if w.base != nil {
		if u, err := w.base.Parse(h); err == nil {
			return u.String()
		}
	}
	return h
}
