package main

import (
	"io"
	"log"
	"strings"
	"testing"

	"github.com/coredev-uk/sona-addons/internal/addon"
	"github.com/coredev-uk/sona-addons/internal/addontest"
)

func TestLink(t *testing.T) {
	for _, tc := range []struct {
		name    string
		target  addon.Target
		country string
		want    string
	}{
		{"song", addon.Target{Kind: "song", ID: "1441164430"}, "", "https://song.link/i/1441164430"},
		{"album", addon.Target{Kind: "album", ID: "1441164426"}, "", "https://album.link/i/1441164426"},
		{"storefront row", addon.Target{Kind: "song", ID: "1441164430"}, "gb", "https://song.link/gb/i/1441164430"},
		{"bad storefront row", addon.Target{Kind: "song", ID: "1441164430"}, "g/../b", "https://song.link/i/1441164430"},
		{"storefront of the URL wins", addon.Target{Kind: "album", ID: "1441164426", URL: "https://music.apple.com/de/album/abbey-road/1441164426"}, "gb", "https://album.link/de/i/1441164426"},
		{"another host's URL is ignored", addon.Target{Kind: "song", ID: "1", URL: "https://evil.example/de/x"}, "", "https://song.link/i/1"},
		{"library song", addon.Target{Kind: "song", ID: "i.AbC123"}, "", ""},
		{"empty id", addon.Target{Kind: "song"}, "", ""},
		{"artist", addon.Target{Kind: "artist", ID: "136975"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Link(tc.target, tc.country)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("Link = %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
}

func TestToastStaysInSonasLimits(t *testing.T) {
	res := toast(addon.Target{Kind: "song", ID: "1", Name: strings.Repeat("é", 5000)}, "")
	if len(res.Toast) > 500 || len(res.URL) > 2048 {
		t.Fatalf("toast %d bytes, url %d bytes", len(res.Toast), len(res.URL))
	}
}

func TestSession(t *testing.T) {
	s := addontest.Start(t, &addontest.Sona{ID: addonID},
		func(in io.Reader, out io.Writer) error { return run(in, out, log.New(io.Discard, "", 0)) })
	s.WaitSubscribed("settings")
	s.Event("settings", map[string]any{"values": map[string]string{"storefront": "GB"}})

	res, code := s.Invoke(action, map[string]string{"kind": "song", "id": "1441164430", "name": "Come Together"})
	if code != "" || res["url"] != "https://song.link/gb/i/1441164430" || !strings.Contains(res["toast"].(string), "Come Together") {
		t.Fatalf("invoke answered %v, %q", res, code)
	}
	if got := s.Hello(); len(got) != 0 {
		t.Fatalf("asked for permissions %v; it needs none", got)
	}

	res, _ = s.Invoke(action, map[string]string{"kind": "song", "id": "i.AbC", "name": "Mine"})
	if res["url"] != nil || !strings.Contains(res["toast"].(string), "not in the Apple Music catalogue") {
		t.Fatalf("a library song answered %v", res)
	}
	if _, code := s.Invoke("addon.copy-songlink.other", map[string]string{"kind": "song", "id": "1"}); code != "failed" {
		t.Fatalf("an unknown action answered %q", code)
	}

	s.Shutdown()
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestEndsWhenInputEnds(t *testing.T) {
	s := addontest.Start(t, &addontest.Sona{ID: addonID},
		func(in io.Reader, out io.Writer) error { return run(in, out, log.New(io.Discard, "", 0)) })
	s.WaitSubscribed("settings")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
