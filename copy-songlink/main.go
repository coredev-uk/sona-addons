// copy-songlink is a Sona add-on that offers "Get song.link" on songs and
// albums. It builds the song.link address from the item's Apple Music ID, so
// it needs no permission and no network, and uses only its standard input and
// output: the same source builds natively and for GOOS=wasip1 GOARCH=wasm.
//
// Sona's protocol has no clipboard. The answer to the invoke is a toast that
// shows the address and offers it as its button; the address opens in the
// browser only when the user clicks it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/coredev-uk/sona-addons/internal/addon"
)

const (
	addonID = "copy-songlink"
	action  = "addon." + addonID + ".get"
	// maxName keeps the toast far under Sona's 500 bytes: a name is cut to
	// this many runes.
	maxName = 80
)

var (
	catalogueID = regexp.MustCompile(`^[0-9]{1,20}$`)
	storefront  = regexp.MustCompile(`^[a-z]{2}$`)
)

func main() {
	logger := log.New(os.Stderr, addonID+": ", 0)
	if err := run(os.Stdin, os.Stdout, logger); err != nil {
		logger.Print(err)
		os.Exit(1)
	}
}

// run speaks the protocol until Sona stops the add-on or closes its input.
func run(in io.Reader, out io.Writer, logger *log.Logger) error {
	ctx := context.Background()
	c := addon.Dial(in, out, logger)
	// No permissions: the address is built from the item's ID alone.
	if _, err := c.Hello(ctx, addonID, nil); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	// The "settings" topic needs no permission: it is the add-on's own rows.
	if err := c.Subscribe(ctx, "settings"); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	logger.Print("ready")

	country := ""
	for {
		m, ok := c.Next()
		if !ok {
			break
		}
		switch {
		case m.Event == "shutdown":
			return nil
		case m.Event == "settings":
			var s struct {
				Values map[string]string `json:"values"`
			}
			if json.Unmarshal(m.Data, &s) == nil {
				country = strings.ToLower(strings.TrimSpace(s.Values["storefront"]))
				if !storefront.MatchString(country) {
					country = ""
				}
			}
		case m.Invoke != nil:
			answer(m.Invoke, country, logger)
		}
	}
	return nil
}

func answer(inv *addon.Invoke, country string, logger *log.Logger) {
	if inv.Action != action || inv.Target == nil {
		logger.Printf("refused %q", inv.Action)
		_ = inv.Fail("failed", "unknown action")
		return
	}
	res := toast(*inv.Target, country)
	logger.Printf("answered %s %s: %q", inv.Target.Kind, inv.Target.ID, res.Toast)
	_ = inv.Reply(res)
}

// toast is the answer for the item the action was chosen on.
func toast(t addon.Target, country string) addon.InvokeResult {
	name := cut(t.Name, maxName)
	link, ok := Link(t, country)
	if !ok {
		return addon.InvokeResult{Toast: fmt.Sprintf("“%s” has no song.link: it is not in the Apple Music catalogue.", name)}
	}
	return addon.InvokeResult{Toast: fmt.Sprintf("“%s” on song.link: %s", name, link), URL: link}
}

// Link is the address of the item at song.link (songs) or album.link
// (albums), which are the same service. The storefront comes from the
// target's Apple Music URL when it has one, else from country. ok is false
// for an item with no catalogue ID (a song only in the library has a library
// ID) and for a kind song.link does not cover.
func Link(t addon.Target, country string) (link string, ok bool) {
	var host string
	switch t.Kind {
	case "song":
		host = "song.link"
	case "album":
		host = "album.link"
	default:
		return "", false
	}
	if !catalogueID.MatchString(t.ID) {
		return "", false
	}
	if sf := storefrontOf(t.URL); sf != "" {
		country = sf
	}
	if !storefront.MatchString(country) {
		country = ""
	}
	if country != "" {
		country += "/"
	}
	// "/i/<id>" is song.link's form for an Apple Music (iTunes) ID.
	return "https://" + host + "/" + country + "i/" + t.ID, true
}

// storefrontOf is the storefront of a music.apple.com URL
// (https://music.apple.com/gb/album/...), or "".
func storefrontOf(u string) string {
	rest, ok := strings.CutPrefix(u, "https://music.apple.com/")
	if !ok {
		return ""
	}
	sf, _, _ := strings.Cut(rest, "/")
	if !storefront.MatchString(sf) {
		return ""
	}
	return sf
}

// cut shortens s to n runes, never in the middle of one.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
