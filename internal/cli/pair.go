package cli

import (
	"fmt"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/server"
)

func cmdPair(args []string) error {
	fs, dir := newFlags("pair")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := loadHub(*dir)
	if err != nil {
		return err
	}
	as, err := auth.Open(h.st)
	if err != nil {
		return err
	}
	if as.NeedsSetup() {
		return fmt.Errorf("hub not set up yet; run `nextboard setup-link` first")
	}
	code, exp, err := as.NewPairCode()
	if err != nil {
		return err
	}
	urls := server.AdvertisedURLs(server.HubURLs(h.cfg.Listen, h.cfg.ExtraURLs))
	uri := server.PairURI(h.cfg.HubName, urls, code)
	q, err := qr.Encode(uri, qr.M)
	if err != nil {
		return err
	}
	fmt.Println(renderQR(q))
	fmt.Printf("Pairing code: %s  (valid until %s, single use)\n", code, exp.Local().Format(time.Kitchen))
	fmt.Printf("Hub:          %s\n", h.cfg.HubName)
	fmt.Printf("Address:      %s\n", strings.Join(urls, ", "))
	fmt.Printf("URI:          %s\n", uri)
	if !hasHTTPS(urls) {
		fmt.Println("\nNote: no HTTPS address yet. The release Android app accepts only HTTPS; set up nginx, then fill in extraUrls in config.json")
		fmt.Println("(or `nextboard install --public-url https://...`), or create the code from the Web UI over HTTPS.")
	}
	return nil
}

// renderQR draws the code with half-block characters, two modules per line,
// using explicit ANSI colors so it scans on dark and light terminals.
func renderQR(c *qr.Code) string {
	const quiet = 2
	n := c.Size
	black := func(x, y int) bool {
		if x < 0 || y < 0 || x >= n || y >= n {
			return false
		}
		return c.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		for x := -quiet; x < n+quiet; x++ {
			fg, bg := "97", "107" // white
			if black(x, y) {
				fg = "30"
			}
			if black(x, y+1) {
				bg = "40"
			}
			fmt.Fprintf(&b, "\x1b[%s;%sm▀", fg, bg)
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}

func hasHTTPS(urls []string) bool {
	for _, u := range urls {
		if strings.HasPrefix(u, "https://") {
			return true
		}
	}
	return false
}
