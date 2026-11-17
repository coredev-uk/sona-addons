package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coredev-uk/sona-addons/internal/addontest"
)

// post is what the fake server received.
type post struct {
	Auth   string
	Method string
	Type   string
	Body   Payload
}

func fakeServer(t *testing.T) (*httptest.Server, <-chan post) {
	t.Helper()
	got := make(chan post, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p post
		p.Auth, p.Method, p.Type = r.Header.Get("Authorization"), r.Method, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&p.Body)
		got <- p
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func start(t *testing.T, secrets map[string]string) *addontest.Sona {
	t.Helper()
	return addontest.Start(t, &addontest.Sona{
		ID: addonID, Grant: []string{"playback.read", "network"}, Secrets: secrets,
	}, func(in io.Reader, out io.Writer) error { return run(in, out, log.New(io.Discard, "", 0), newClient()) })
}

func song(id, title string) map[string]any {
	return map[string]any{
		"id": id, "title": title, "artist": "The Beatles", "album": "Abbey Road",
		"duration": 259.0, "explicit": false, "artwork": "",
	}
}

func recv(t *testing.T, got <-chan post) post {
	t.Helper()
	select {
	case p := <-got:
		return p
	case <-time.After(addontest.Timeout):
		t.Fatal("the server received nothing")
		return post{}
	}
}

func none(t *testing.T, got <-chan post) {
	t.Helper()
	select {
	case p := <-got:
		t.Fatalf("the server received %+v; want nothing", p)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestPostsEachSongOnceWithTheToken(t *testing.T) {
	srv, got := fakeServer(t)
	s := start(t, map[string]string{"token": "s3cr3t"})
	s.WaitSubscribed("settings")
	s.Event("settings", map[string]any{"values": map[string]string{"url": srv.URL + "/hook"}})

	s.Event("now-playing", map[string]any{"track": nil, "startedAt": 0})
	none(t, got) // nothing plays: nothing is sent

	np := map[string]any{"track": song("1441164430", "Come Together"), "startedAt": 1767225600000}
	s.Event("now-playing", np)
	p := recv(t, got)
	if p.Method != "POST" || p.Type != "application/json" || p.Auth != "Bearer s3cr3t" {
		t.Fatalf("request %+v", p)
	}
	if p.Body.Event != "now-playing" || p.Body.StartedAt != 1767225600000 || p.Body.Track.Title != "Come Together" || p.Body.Track.Duration != 259 {
		t.Fatalf("body %+v", p.Body)
	}

	s.Event("now-playing", np) // the same event again
	none(t, got)

	np["startedAt"] = 1767225900000 // the same song, played again
	s.Event("now-playing", np)
	if p := recv(t, got); p.Body.StartedAt != 1767225900000 {
		t.Fatalf("the replay was %+v", p.Body)
	}

	if got := s.Hello(); len(got) != 2 || got[0] != "playback.read" || got[1] != "network" {
		t.Fatalf("asked for %v", got)
	}
	s.Shutdown()
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestNoTokenNoHeader(t *testing.T) {
	srv, got := fakeServer(t)
	s := start(t, nil)
	s.WaitSubscribed("settings")
	s.Event("settings", map[string]any{"values": map[string]string{"url": srv.URL}})
	s.Event("now-playing", map[string]any{"track": song("1", "A"), "startedAt": 1})
	if p := recv(t, got); p.Auth != "" {
		t.Fatalf("Authorization = %q; want none", p.Auth)
	}
}

func TestNoAddressSendsNothing(t *testing.T) {
	srv, got := fakeServer(t)
	s := start(t, nil)
	s.WaitSubscribed("settings")
	s.Event("settings", map[string]any{"values": map[string]string{"url": ""}})
	s.Event("now-playing", map[string]any{"track": song("1", "A"), "startedAt": 1})
	none(t, got)
	// Set later: the next song is sent, the one before is not.
	s.Event("settings", map[string]any{"values": map[string]string{"url": srv.URL}})
	s.Event("now-playing", map[string]any{"track": song("2", "B"), "startedAt": 2})
	if p := recv(t, got); p.Body.Track.ID != "2" {
		t.Fatalf("sent song %s", p.Body.Track.ID)
	}
}

func TestDoesNotFollowRedirects(t *testing.T) {
	var mu sync.Mutex
	elsewhere := 0
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		elsewhere++
		mu.Unlock()
	}))
	defer other.Close()
	hits := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
		hits <- struct{}{}
	}))
	defer srv.Close()
	s := start(t, map[string]string{"token": "x"})
	s.WaitSubscribed("settings")
	s.Event("settings", map[string]any{"values": map[string]string{"url": srv.URL}})
	s.Event("now-playing", map[string]any{"track": song("1", "A"), "startedAt": 1})
	<-hits
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if elsewhere != 0 {
		t.Fatal("the redirect was followed")
	}
}

func TestValidTarget(t *testing.T) {
	for s, ok := range map[string]bool{
		"https://example.com/hook":    true,
		"http://192.168.1.5:8123/x":   true,
		"":                            false,
		"ftp://example.com":           false,
		"file:///etc/passwd":          false,
		"https://user:pw@example.com": false,
		"//example.com":               false,
		"example.com/hook":            false,
	} {
		if _, err := validTarget(s); (err == nil) != ok {
			t.Errorf("validTarget(%q) = %v; want ok=%v", s, err, ok)
		}
	}
}
