package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coredev-uk/sona-addons/internal/addontest"
)

type request struct {
	Method string
	Path   string
	Auth   string
	Body   entityState
}

// fakeHA is a Home Assistant that records the requests it gets and answers
// 200, as it does when it updates an entity (201 when it creates one).
func fakeHA(t *testing.T, status int) (*httptest.Server, <-chan request) {
	t.Helper()
	got := make(chan request, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		_ = json.NewDecoder(r.Body).Decode(&req.Body)
		got <- req
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func recv(t *testing.T, got <-chan request) request {
	t.Helper()
	select {
	case r := <-got:
		return r
	case <-time.After(addontest.Timeout):
		t.Fatal("Home Assistant received nothing")
		return request{}
	}
}

func start(t *testing.T, secrets map[string]string) *addontest.Sona {
	t.Helper()
	return addontest.Start(t, &addontest.Sona{
		ID: addonID, Grant: []string{"playback.read", "network"}, Secrets: secrets,
	}, func(in io.Reader, out io.Writer) error { return run(in, out, log.New(io.Discard, "", 0), newClient()) })
}

func playing(title string, position float64) map[string]any {
	return map[string]any{
		"running": true, "state": "playing", "position": position, "volume": 80, "shuffle": true, "repeat": "all",
		"private": false, "preview": false, "error": "",
		"track": map[string]any{
			"id": "1441164430", "title": title, "artist": "The Beatles", "album": "Abbey Road", "duration": 259.9,
			"explicit": false, "artwork": "https://is1-ssl.mzstatic.com/image/thumb/Music/600x600bb.jpg",
		},
	}
}

func TestPublishesTheStateAndOffAtShutdown(t *testing.T) {
	srv, got := fakeHA(t, http.StatusCreated)
	s := start(t, map[string]string{"token": "llat"})
	s.WaitSubscribed("settings")
	s.Event("settings", map[string]any{"values": map[string]string{"url": srv.URL + "/", "entity": "media_player.sona_test"}})
	s.Event("status", playing("Come Together", 12.5))

	r := recv(t, got)
	if r.Method != "POST" || r.Path != "/api/states/media_player.sona_test" || r.Auth != "Bearer llat" {
		t.Fatalf("request %+v", r)
	}
	a := r.Body.Attributes
	if r.Body.State != "playing" || a["media_title"] != "Come Together" || a["media_artist"] != "The Beatles" ||
		a["media_album_name"] != "Abbey Road" || a["media_duration"] != 259.0 || a["media_position"] != 12.0 ||
		a["volume_level"] != 0.8 || a["shuffle"] != true || a["repeat"] != "all" || a["supported_features"] != 0.0 ||
		a["entity_picture"] != "https://is1-ssl.mzstatic.com/image/thumb/Music/600x600bb.jpg" {
		t.Fatalf("body %+v", r.Body)
	}
	if _, err := time.Parse(time.RFC3339, a["media_position_updated_at"].(string)); err != nil {
		t.Fatalf("media_position_updated_at: %v", err)
	}

	// Paused is a new state; the same status again is not posted.
	s.Event("status", playing("Come Together", 12.5))
	paused := playing("Come Together", 30)
	paused["state"] = "paused"
	s.Event("status", paused)
	if r := recv(t, got); r.Body.State != "paused" || r.Body.Attributes["media_position"] != 30.0 {
		t.Fatalf("second post %+v", r.Body)
	}

	// Nothing plays: idle, with no track attributes.
	s.Event("status", map[string]any{
		"running": true, "state": "idle", "track": nil, "position": 0, "volume": 80, "shuffle": false,
		"repeat": "off", "private": false, "preview": false, "error": "",
	})
	r = recv(t, got)
	if _, has := r.Body.Attributes["media_title"]; r.Body.State != "idle" || has {
		t.Fatalf("idle post %+v", r.Body)
	}

	s.Shutdown()
	if r := recv(t, got); r.Body.State != "off" {
		t.Fatalf("shutdown post %+v", r.Body)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	if h := s.Hello(); len(h) != 2 || h[0] != "playback.read" || h[1] != "network" {
		t.Fatalf("asked for %v", h)
	}
}

func TestNothingWithoutAddressOrToken(t *testing.T) {
	srv, got := fakeHA(t, http.StatusOK)
	s := start(t, nil) // no token
	s.WaitSubscribed("settings")
	s.Event("status", playing("A", 0))
	s.Event("settings", map[string]any{"values": map[string]string{"url": srv.URL}})
	select {
	case r := <-got:
		t.Fatalf("posted without a token: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
}

// A secret row changing is not an event, so a token saved after the address
// is found by trying again.
func TestRetriesAfterAFailedPost(t *testing.T) {
	old := retryAfter
	retryAfter = 50 * time.Millisecond
	t.Cleanup(func() { retryAfter = old })
	srv, got := fakeHA(t, http.StatusOK)
	s := start(t, nil) // no token yet
	s.WaitSubscribed("settings")
	s.Event("settings", map[string]any{"values": map[string]string{"url": srv.URL}})
	s.Event("status", playing("A", 0))
	time.Sleep(150 * time.Millisecond)
	s.SetSecret("token", "later")
	if r := recv(t, got); r.Auth != "Bearer later" || r.Body.State != "playing" {
		t.Fatalf("after the token was saved: %+v", r)
	}
}

func TestRefusesABadEntityOrAddress(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"entity":    {"url": "http://ha.local:8123", "entity": "light.kitchen"},
		"traversal": {"url": "http://ha.local:8123", "entity": "media_player.x/../../config"},
		"scheme":    {"url": "file:///etc/passwd"},
		"user":      {"url": "http://u:p@ha.local"},
	} {
		if cfg, err := parseConfig(values); err == nil || cfg.base != nil {
			t.Errorf("%s: parseConfig = %+v, %v; want a refusal", name, cfg, err)
		}
	}
	cfg, err := parseConfig(map[string]string{"url": "http://ha.local:8123/"})
	if err != nil || cfg.endpoint() != "http://ha.local:8123/api/states/media_player.sona" {
		t.Fatalf("default entity: %+v %v", cfg, err)
	}
	cfg, err = parseConfig(map[string]string{"url": "https://example.com/ha/"})
	if err != nil || cfg.endpoint() != "https://example.com/ha/api/states/media_player.sona" {
		t.Fatalf("a base with a path: %+v %v", cfg, err)
	}
}

func TestStateMapping(t *testing.T) {
	for in, want := range map[string]string{
		"playing": "playing", "paused": "paused", "loading": "buffering", "idle": "idle", "ended": "idle", "error": "idle",
	} {
		if got := haState(in); got != want {
			t.Errorf("haState(%q) = %q; want %q", in, got, want)
		}
	}
}
