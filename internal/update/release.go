// Package update finds a newer NextBoard release on GitHub and puts it in place of the running
// binary: what `nextboard update` does in a terminal and the hub does from its web UI
// (docs/API.md "Hub update"). The archive comes only from the repository's own releases, must
// match the release's checksums.txt, and nothing in it runs before the binary is swapped; the old
// binary stays next to the new one as <binary>.old.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/version"
)

// TestAPIEnv points the client at a fake GitHub, for tests only: the API and every download then
// come from that base URL instead of api.github.com and github.com.
const TestAPIEnv = "NEXTBOARD_TEST_RELEASE_API"

// Client reads one repository's GitHub releases.
type Client struct {
	HTTP *http.Client
	Repo string // owner/name
	// API is the GitHub API's base URL; Downloads is the base every asset must come from,
	// followed by /<Repo>/releases/download/.
	API, Downloads string
}

// NewClient is a client for repo on GitHub, or on the fake one TestAPIEnv names.
func NewClient(repo string, timeout time.Duration) *Client {
	c := &Client{HTTP: &http.Client{Timeout: timeout}, Repo: repo, API: "https://api.github.com", Downloads: "https://github.com"}
	if base := strings.TrimRight(os.Getenv(TestAPIEnv), "/"); base != "" {
		c.API, c.Downloads = base, base
	}
	return c
}

// Release is the part of a GitHub release this package reads.
type Release struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset is one file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Latest is the newest stable release: GitHub leaves pre-releases (betas) out of it.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	var rel Release
	if err := c.getJSON(ctx, "/repos/"+c.Repo+"/releases/latest", &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// ByTag is the release of one tag, such as v0.9.0-beta.2.
func (c *Client) ByTag(ctx context.Context, tag string) (*Release, error) {
	var rel Release
	if err := c.getJSON(ctx, "/repos/"+c.Repo+"/releases/tags/"+url.PathEscape(tag), &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// Newest is the newest release a build of this channel follows: the latest stable one for a
// stable build; for a beta the newest of all, beta or stable (a beta of 0.9.0 is followed by
// 0.9.0-beta.3 or by 0.9.0 itself). A dev build follows none.
func (c *Client) Newest(ctx context.Context, channel string) (*Release, error) {
	switch channel {
	case version.ChannelStable:
		return c.Latest(ctx)
	case version.ChannelBeta:
	default:
		return nil, errors.New("a dev build follows no release")
	}
	var list []Release
	if err := c.getJSON(ctx, "/repos/"+c.Repo+"/releases?per_page=30", &list); err != nil {
		return nil, err
	}
	var best *Release
	for i := range list {
		r := &list[i]
		if r.Draft || version.Channel(r.TagName) == version.ChannelDev {
			continue
		}
		if best == nil || version.Newer(r.TagName, best.TagName) {
			best = r
		}
	}
	if best == nil {
		return nil, errors.New("the repository has no releases")
	}
	return best, nil
}

// Check is what a hub knows about a newer release.
type Check struct {
	// Status: "current" (nothing newer), "available", "dev" (a dev build follows no release, so
	// nothing was asked), "unknown" (GitHub could not be asked: offline, rate limited).
	Status    string    `json:"status"`
	Current   string    `json:"current"`
	Channel   string    `json:"channel"`
	Latest    string    `json:"latest,omitempty"`  // the newest release's tag
	URL       string    `json:"url,omitempty"`     // its page
	Command   string    `json:"command,omitempty"` // how to install it in a terminal
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// Check status values.
const (
	StatusCurrent   = "current"
	StatusAvailable = "available"
	StatusDev       = "dev"
	StatusUnknown   = "unknown"
)

// CheckFor compares the newest release of current's channel with current. A dev build asks
// nothing.
func (c *Client) CheckFor(ctx context.Context, current string) (Check, *Release) {
	ch := Check{Current: current, Channel: version.Channel(current), CheckedAt: time.Now().UTC()}
	if ch.Channel == version.ChannelDev {
		ch.Status = StatusDev
		return ch, nil
	}
	rel, err := c.Newest(ctx, ch.Channel)
	if err != nil {
		ch.Status, ch.Error = StatusUnknown, err.Error()
		return ch, nil
	}
	ch.Latest, ch.URL = rel.TagName, rel.HTMLURL
	if !version.Newer(rel.TagName, current) {
		ch.Status = StatusCurrent
		return ch, rel
	}
	ch.Status = StatusAvailable
	ch.Command = "nextboard update"
	if rel.Prerelease || version.Channel(rel.TagName) == version.ChannelBeta {
		// a plain update installs only the latest stable release
		ch.Command = "nextboard update --version " + rel.TagName
	}
	return ch, rel
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	b, err := c.fetch(ctx, c.API+path, 4<<20, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("GitHub answered something unexpected: %w", err)
	}
	return nil
}

// fetch GETs u, at most limit bytes; onRead (may be nil) hears how much came of how much.
func (c *Client) fetch(ctx context.Context, u string, limit int64, onRead func(got, total int64)) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NextBoard/"+version.Version)
	if strings.HasPrefix(u, c.API) {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("GET %s: %d bytes is more than the %d expected", u, resp.ContentLength, limit)
	}
	var r io.Reader = io.LimitReader(resp.Body, limit+1)
	if onRead != nil {
		r = &counting{r: r, total: resp.ContentLength, on: onRead}
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: more than the %d bytes expected", u, limit)
	}
	return b, nil
}

type counting struct {
	r     io.Reader
	got   int64
	total int64
	on    func(got, total int64)
}

func (c *counting) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.got += int64(n)
	c.on(c.got, c.total)
	return n, err
}
