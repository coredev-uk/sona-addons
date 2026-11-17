// home-assistant-bridge is a Sona add-on that shows what Sona plays in Home
// Assistant, as a `media_player` entity set through Home Assistant's REST API
// (POST /api/states/<entity_id>). It is read-only: Home Assistant can not
// control Sona through it.
//
// It subscribes to "status" and publishes the newest state, at most once a
// second, and again every few minutes (Home Assistant forgets an entity set
// through the REST API when it restarts). When Sona stops it, it publishes
// "off". The address is a settings row, the long-lived token a secret row.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/coredev-uk/sona-addons/internal/addon"
)

const (
	addonID       = "home-assistant-bridge"
	defaultEntity = "media_player.sona"

	postTimeout     = 10 * time.Second
	shutdownTimeout = 700 * time.Millisecond // Sona waits a second before it ends the add-on
	minInterval     = time.Second            // between two posts: a seek bar dragged is many events
	refreshEvery    = 5 * time.Minute
)

// retryAfter is the wait before trying again after a post that failed.
var retryAfter = 30 * time.Second

var entityExpr = regexp.MustCompile(`^(media_player|sensor)\.[a-z0-9_]{1,60}$`)

func main() {
	logger := log.New(os.Stderr, addonID+": ", 0)
	if err := run(os.Stdin, os.Stdout, logger, newClient()); err != nil {
		logger.Print(err)
		os.Exit(1)
	}
}

// newClient never follows a redirect: the token must reach only the address
// the user typed.
func newClient() *http.Client {
	return &http.Client{
		Timeout:       postTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type track struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Album    string  `json:"album"`
	Duration float64 `json:"duration"`
	Artwork  string  `json:"artwork"`
}

type status struct {
	State    string  `json:"state"`
	Track    *track  `json:"track"`
	Position float64 `json:"position"`
	Volume   int     `json:"volume"`
	Shuffle  bool    `json:"shuffle"`
	Repeat   string  `json:"repeat"`
}

// entityState is the body Home Assistant takes.
type entityState struct {
	State      string         `json:"state"`
	Attributes map[string]any `json:"attributes"`
}

// haState maps Sona's state to a media_player state. Sona's "idle", "ended"
// and "error" are all "nothing is playing".
func haState(s string) string {
	switch s {
	case "playing":
		return "playing"
	case "paused":
		return "paused"
	case "loading":
		return "buffering"
	}
	return "idle"
}

// build is the entity for a status. now stamps the position, so Home
// Assistant's card can run its clock on from it.
func build(s status, now time.Time) entityState {
	attrs := map[string]any{
		"friendly_name":      "Sona",
		"icon":               "mdi:music",
		"media_content_type": "music",
		"volume_level":       float64(s.Volume) / 100,
		"shuffle":            s.Shuffle,
		"repeat":             s.Repeat,
		"supported_features": 0, // read-only: Home Assistant offers no controls
	}
	if t := s.Track; t != nil {
		attrs["media_title"] = t.Title
		attrs["media_artist"] = t.Artist
		attrs["media_album_name"] = t.Album
		attrs["media_duration"] = int(t.Duration)
		attrs["media_position"] = int(s.Position)
		attrs["media_position_updated_at"] = now.UTC().Format(time.RFC3339)
		if t.Artwork != "" {
			attrs["entity_picture"] = t.Artwork
		}
	}
	return entityState{State: haState(s.State), Attributes: attrs}
}

// config is what the settings rows say.
type config struct {
	base   *url.URL // Home Assistant's address, nil when unset or not usable
	entity string
}

func parseConfig(values map[string]string) (config, error) {
	var cfg config
	cfg.entity = strings.TrimSpace(values["entity"])
	if cfg.entity == "" {
		cfg.entity = defaultEntity
	}
	raw := strings.TrimSpace(values["url"])
	if raw == "" {
		return cfg, nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil:
		return cfg, fmt.Errorf("the Home Assistant address is not an http or https address")
	case !entityExpr.MatchString(cfg.entity):
		return cfg, fmt.Errorf("the entity must look like media_player.sona")
	}
	u.RawQuery, u.Fragment = "", ""
	cfg.base = u
	return cfg, nil
}

// endpoint is the REST address of the entity.
func (c config) endpoint() string {
	return strings.TrimRight(c.base.String(), "/") + "/api/states/" + c.entity
}

type publisher struct {
	c      *addon.Conn
	client *http.Client
	logger *log.Logger

	cfg    config
	lastOK string // the body last accepted for cfg, to skip an unchanged one
	// token is the last token read. At shutdown Sona no longer answers
	// requests, so "off" is posted with it instead of reading the secret again.
	token string
}

// run speaks the protocol until Sona stops the add-on or closes its input.
func run(in io.Reader, out io.Writer, logger *log.Logger, client *http.Client) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := addon.Dial(in, out, logger)
	hello, err := c.Hello(ctx, addonID, []string{"playback.read", "network"})
	if err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	for _, p := range []string{"playback.read", "network"} {
		if !hello.Granted(p) {
			return fmt.Errorf("the permission %s is not granted", p)
		}
	}
	if err := c.Subscribe(ctx, "status", "settings"); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	p := &publisher{c: c, client: client, logger: logger}
	// The event loop only keeps the newest status and settings; the worker
	// posts, so a slow Home Assistant never keeps the add-on from reading Sona.
	updates := make(chan update, 1)
	stop, finished := make(chan struct{}), make(chan struct{})
	go func() {
		p.work(ctx, updates, stop)
		close(finished)
	}()
	logger.Print("ready")

	// However the loop ends (Sona's shutdown event, or its input closing),
	// the worker says "off" before the add-on exits.
	defer func() {
		close(stop)
		<-finished
	}()
	for m := range c.Messages() {
		switch m.Event {
		case "shutdown":
			return nil
		case "settings":
			var s struct {
				Values map[string]string `json:"values"`
			}
			if json.Unmarshal(m.Data, &s) == nil {
				offer(updates, update{settings: s.Values})
			}
		case "status":
			var s status
			if json.Unmarshal(m.Data, &s) == nil {
				offer(updates, update{status: &s})
			}
		}
	}
	return nil
}

// update is a change for the worker: new settings, or a new status.
type update struct {
	settings map[string]string
	status   *status
}

// offer hands an update to the worker. The channel holds one: when it is
// full, the waiting update is merged with the new one, and the newest of
// each kind wins.
func offer(ch chan update, u update) {
	for {
		select {
		case ch <- u:
			return
		default:
		}
		select {
		case old := <-ch:
			if u.settings == nil {
				u.settings = old.settings
			}
			if u.status == nil {
				u.status = old.status
			}
		default:
		}
	}
}

// work publishes until stop is closed; then it says "off".
func (p *publisher) work(ctx context.Context, updates <-chan update, stop <-chan struct{}) {
	var last *status
	refresh := time.NewTicker(refreshEvery)
	defer refresh.Stop()
	publishNow := false
	var retry <-chan time.Time // set while the last post failed
	for {
		select {
		case <-retry:
			retry = nil
			publishNow = true
		case <-stop:
			p.off()
			return
		case u := <-updates:
			if u.settings != nil {
				cfg, err := parseConfig(u.settings)
				if err != nil {
					p.logger.Printf("not publishing: %v", err)
					cfg = config{}
				}
				if (cfg.base == nil) != (p.cfg.base == nil) || cfg.entity != p.cfg.entity ||
					(cfg.base != nil && cfg.base.String() != p.cfg.base.String()) {
					p.cfg, p.lastOK = cfg, ""
					publishNow = true
				}
			}
			if u.status != nil {
				last = u.status
				publishNow = true
			}
		case <-refresh.C:
			p.lastOK = ""
			publishNow = true
		}
		if !publishNow || last == nil || p.cfg.base == nil {
			continue
		}
		publishNow = false
		if !p.publish(ctx, *last) {
			// A secret row changing is not an event: a token saved after the
			// address is found by trying again.
			retry = time.After(retryAfter)
		}
		// Pace the posts; stopping cuts the wait short.
		select {
		case <-stop:
			p.off()
			return
		case <-time.After(minInterval):
		}
	}
}

// publish posts the status unless it is what Home Assistant already has. It
// reports false when a post failed.
func (p *publisher) publish(ctx context.Context, s status) bool {
	st := build(s, time.Now())
	// The position's stamp changes every time: leave it out of the comparison.
	cmp := st
	cmp.Attributes = map[string]any{}
	for k, v := range st.Attributes {
		if k != "media_position_updated_at" {
			cmp.Attributes[k] = v
		}
	}
	key, _ := json.Marshal(cmp)
	if string(key) == p.lastOK {
		return true
	}
	if !p.post(ctx, st) {
		return false
	}
	p.lastOK = string(key)
	return true
}

// off publishes "off" when Sona stops the add-on, within Sona's grace.
func (p *publisher) off() {
	if p.cfg.base == nil || p.lastOK == "" || p.token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	p.send(ctx, entityState{State: "off", Attributes: map[string]any{"friendly_name": "Sona", "icon": "mdi:music"}})
}

// post reads the token (a secret row changing is not an event) and sends the
// state.
func (p *publisher) post(ctx context.Context, st entityState) bool {
	token, err := p.c.Secret(ctx, "token")
	if err != nil {
		p.token = ""
		if addon.IsCode(err, "not_found") {
			p.logger.Print("not publishing: no access token is set")
		} else {
			p.logger.Printf("not publishing: reading the token: %v", err)
		}
		return false
	}
	p.token = token
	return p.send(ctx, st)
}

// send posts the state. It logs the host and Home Assistant's status, never
// the token or the address's path.
func (p *publisher) send(ctx context.Context, st entityState) bool {
	body, err := json.Marshal(st)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.endpoint(), bytes.NewReader(body))
	if err != nil {
		p.logger.Print("not publishing: the address is not usable")
		return false
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", addonID+"/1")
	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Printf("%s: the request failed", p.cfg.base.Host)
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	ok := resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated
	switch {
	case ok:
		p.logger.Printf("%s: %s is now %s", p.cfg.base.Host, p.cfg.entity, st.State)
	case resp.StatusCode == http.StatusUnauthorized:
		p.logger.Printf("%s refused the access token (401)", p.cfg.base.Host)
	default:
		p.logger.Printf("%s answered %d", p.cfg.base.Host, resp.StatusCode)
	}
	return ok
}
