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
		return fmt.Errorf("hub belum di-setup; jalankan `nextboard setup-link` dulu")
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
	fmt.Printf("Kode pairing: %s  (berlaku sampai %s, sekali pakai)\n", code, exp.Local().Format(time.Kitchen))
	fmt.Printf("Hub:          %s\n", h.cfg.HubName)
	fmt.Printf("Alamat:       %s\n", strings.Join(urls, ", "))
	fmt.Printf("URI:          %s\n", uri)
	if !hasHTTPS(urls) {
		fmt.Println("\nCatatan: belum ada alamat HTTPS. App Android rilis hanya menerima HTTPS; pasang nginx lalu isi extraUrls di config.json")
		fmt.Println("(atau `nextboard install --public-url https://...`), atau buat kode dari Web UI lewat HTTPS.")
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
