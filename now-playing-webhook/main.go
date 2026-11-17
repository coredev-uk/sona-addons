// now-playing-webhook is a Sona add-on that posts each song that starts to a
// web address of yours, as JSON.
//
// It subscribes to "now-playing", and sends one POST per song that starts: a
// repeat of the same event is skipped, and nothing is sent while nothing
// plays. The address is a settings row; the optional token is a secret row,
// read with the protocol's "secret" method for each post and sent as
// `Authorization: Bearer`. The add-on keeps no state on disk.
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coredev-uk/sona-addons/internal/addon"
)

const (
	addonID = "now-playing-webhook"
	// postTimeout bounds one post; a slow server only delays the next song.
	postTimeout = 10 * time.Second
	// maxQueue is how many songs wait while the server is slow: when it is
	// full the oldest is dropped.
	maxQueue = 16
)

func main() {
	logger := log.New(os.Stderr, addonID+": ", 0)
	if err := run(os.Stdin, os.Stdout, logger, newClient()); err != nil {
		logger.Print(err)
		os.Exit(1)
	}
}

// newClient never follows a redirect: the address is the user's, and a
// redirect would send the song, and the token, somewhere they didn't choose.
func newClient() *http.Client {
	return &http.Client{
		Timeout:       postTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Track is the song, as Sona's events carry it.
type Track struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Album    string  `json:"album"`
	Duration float64 `json:"duration"`
	Explicit bool    `json:"explicit"`
	Artwork  string  `json:"artwork"`
}

// Payload is the body of each post.
type Payload struct {
	Event     string `json:"event"`
	StartedAt int64  `json:"startedAt"`
	Track     Track  `json:"track"`
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
	// The user may grant less than the add-on asks. Without playback.read
	// there is nothing to hear; without network the post would only fail.
	for _, p := range []string{"playback.read", "network"} {
		if !hello.Granted(p) {
			return fmt.Errorf("the permission %s is not granted", p)
		}
	}
	if err := c.Subscribe(ctx, "now-playing", "settings"); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	w := &worker{c: c, client: client, logger: logger, wake: make(chan struct{}, 1)}
	go w.run(ctx)
	logger.Print("ready")

	last := ""
	for m := range c.Messages() {
		switch m.Event {
		case "shutdown":
			return nil
		case "settings":
			var s struct {
				Values map[string]string `json:"values"`
			}
			if json.Unmarshal(m.Data, &s) == nil {
				w.setTarget(strings.TrimSpace(s.Values["url"]))
			}
		case "now-playing":
			var np struct {
				Track     *Track `json:"track"`
				StartedAt int64  `json:"startedAt"`
			}
			if json.Unmarshal(m.Data, &np) != nil || np.Track == nil {
				continue // nothing plays: send nothing
			}
			// The same song at the same start is the same event again
			// (a second subscribe, a replay by Sona): not a new play.
			key := np.Track.ID + "@" + strconv.FormatInt(np.StartedAt, 10)
			if key == last {
				continue
			}
			last = key
			w.enqueue(Payload{Event: "now-playing", StartedAt: np.StartedAt, Track: *np.Track})
		}
	}
	return nil
}

// worker posts in order on its own goroutine, so a slow server never keeps
// the add-on from reading Sona (and from answering its shutdown).
type worker struct {
	c      *addon.Conn
	client *http.Client
	logger *log.Logger
	wake   chan struct{}

	mu     sync.Mutex
	target string
	queue  []Payload
}

func (w *worker) setTarget(u string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if u != w.target {
		w.target = u
		if u == "" {
			w.logger.Print("no web address set: nothing is sent")
		}
	}
}

func (w *worker) enqueue(p Payload) {
	w.mu.Lock()
	if w.target == "" {
		w.mu.Unlock()
		return
	}
	if len(w.queue) >= maxQueue {
		w.queue = w.queue[1:]
		w.logger.Print("the server is slow: dropped the oldest song")
	}
	w.queue = append(w.queue, p)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *worker) next() (Payload, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) == 0 {
		return Payload{}, "", false
	}
	p := w.queue[0]
	w.queue = w.queue[1:]
	return p, w.target, true
}

func (w *worker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		}
		for {
			p, target, ok := w.next()
			if !ok {
				break
			}
			w.post(ctx, target, p)
		}
	}
}

// post sends one song. It logs the song's ID and the server's host, never
// the title, the address's path or the token.
func (w *worker) post(ctx context.Context, target string, p Payload) {
	u, err := validTarget(target)
	if err != nil {
		w.logger.Printf("not sent: %v", err)
		return
	}
	body, err := json.Marshal(p)
	if err != nil {
		w.logger.Printf("not sent: %v", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		w.logger.Print("not sent: the web address is not usable")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", addonID+"/1")
	// Read when needed: a secret row changing is not an event.
	switch token, err := w.c.Secret(ctx, "token"); {
	case err == nil:
		req.Header.Set("Authorization", "Bearer "+token)
	case addon.IsCode(err, "not_found"):
	default:
		w.logger.Printf("not sent: reading the token: %v", err)
		return
	}
	resp, err := w.client.Do(req)
	if err != nil {
		// err holds the address; say only what failed.
		w.logger.Printf("song %s: not sent to %s: the request failed", p.Track.ID, u.Host)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	w.logger.Printf("song %s: %s answered %d", p.Track.ID, u.Host, resp.StatusCode)
}

// validTarget accepts an http or https address with a host and no user name
// or password (a token is a secret row, not part of the address).
func validTarget(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("the web address is not an http or https address")
	}
	return u, nil
}
