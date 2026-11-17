// Package addontest is a fake Sona for tests: it runs an add-on's `run`
// function over in-memory pipes and answers the way Sona does (hello,
// subscribe, secret), so a sample can be tested without Sona and without a
// network. It checks the same rules Sona does on the way: hello first,
// permissions that exist, topics that exist.
package addontest

import (
	"bufio"
	"encoding/json"
	"io"
	"slices"
	"sync"
	"testing"
	"time"
)

// Timeout bounds every wait in a test.
const Timeout = 5 * time.Second

var permissions = []string{
	"playback.read", "playback.control", "queue.read", "lyrics.read", "search",
	"open", "volume", "like", "private.read", "network",
}

var topics = []string{"status", "now-playing", "queue", "settings"}

// Sona is the fake. Set Secrets, Latest and Calls before Start.
type Sona struct {
	t testing.TB
	// ID is the add-on ID Sona started the add-on as; hello must name it.
	ID string
	// Grant is what the user granted; hello's answer is the asked-for
	// permissions that are in it.
	Grant []string
	// Secrets are the add-on's secret rows, by row ID.
	Secrets map[string]string
	// Latest is each topic's latest value, sent when it is subscribed to.
	Latest map[string]any
	// Calls answers `call` requests by command; a command with no entry is
	// refused with unknown_command.
	Calls map[string]func(params json.RawMessage) any

	toAddon   *io.PipeWriter
	fromAddon *io.PipeReader
	wmu       sync.Mutex
	nextID    int64

	mu         sync.Mutex
	subscribed []string
	stopping   bool
	hello      []string
	responses  map[int64]chan map[string]json.RawMessage
	done       chan error
}

// Start runs the add-on's run function against the fake and returns once it
// has started. Close ends the run.
func Start(t testing.TB, s *Sona, run func(in io.Reader, out io.Writer) error) *Sona {
	t.Helper()
	s.t = t
	s.responses = map[int64]chan map[string]json.RawMessage{}
	s.done = make(chan error, 1)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s.toAddon, s.fromAddon = inW, outR
	go func() {
		err := run(inR, outW)
		outW.Close()
		s.done <- err
	}()
	go s.serve()
	t.Cleanup(func() { s.Close() })
	return s
}

// Close closes the add-on's input, as Sona does when it stops an add-on, and
// returns what `run` returned.
func (s *Sona) Close() error {
	s.toAddon.Close()
	select {
	case err := <-s.done:
		s.done <- err
		return err
	case <-time.After(Timeout):
		s.t.Errorf("the add-on did not exit when its input closed")
		return nil
	}
}

// Wait returns what `run` returned, failing the test if it doesn't return.
func (s *Sona) Wait() error { return s.Close() }

// SetSecret sets a secret row while the add-on runs, as the user does in
// Settings. Saving a secret is not an event: the add-on is not told.
func (s *Sona) SetSecret(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Secrets == nil {
		s.Secrets = map[string]string{}
	}
	s.Secrets[key] = value
}

// Hello is the permissions the add-on asked for in its hello.
func (s *Sona) Hello() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.hello)
}

// Subscribed is the topics the add-on is subscribed to.
func (s *Sona) Subscribed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.subscribed)
}

// WaitSubscribed returns once the add-on has subscribed to the topic.
func (s *Sona) WaitSubscribed(topic string) {
	s.t.Helper()
	deadline := time.Now().Add(Timeout)
	for !slices.Contains(s.Subscribed(), topic) {
		if time.Now().After(deadline) {
			s.t.Fatalf("the add-on did not subscribe to %s", topic)
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *Sona) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		s.t.Errorf("addontest: %v", err)
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.toAddon.Write(append(b, '\n')) //nolint:errcheck // the pipe ends with the test
}

// Event sends an event.
func (s *Sona) Event(name string, data any) {
	s.write(map[string]any{"event": name, "data": data})
}

// Shutdown sends the shutdown event. As Sona does when it stops an add-on, it
// answers no request after it: a request made while the add-on shuts down
// gets no answer.
func (s *Sona) Shutdown() {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	s.write(map[string]any{"event": "shutdown"})
}

// Invoke sends an invoke and returns the add-on's answer: its result, or its
// error code.
func (s *Sona) Invoke(action string, target any) (result map[string]any, errCode string) {
	s.t.Helper()
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan map[string]json.RawMessage, 1)
	s.responses[id] = ch
	s.mu.Unlock()
	s.write(map[string]any{"id": id, "method": "invoke", "params": map[string]any{
		"action": action, "target": target, "deadlineMs": 10000,
	}})
	select {
	case f := <-ch:
		if e, ok := f["error"]; ok {
			var ee struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(e, &ee)
			return nil, ee.Code
		}
		_ = json.Unmarshal(f["result"], &result)
		return result, ""
	case <-time.After(Timeout):
		s.t.Fatalf("no answer to the invoke of %s", action)
		return nil, ""
	}
}

func (s *Sona) serve() {
	sc := bufio.NewReaderSize(s.fromAddon, 1<<20)
	first := true
	for {
		line, err := sc.ReadBytes('\n')
		if len(line) > 0 {
			s.handle(line, first)
			first = false
		}
		if err != nil {
			return
		}
	}
}

func (s *Sona) fail(id *json.RawMessage, code string) {
	s.write(map[string]any{"id": id, "error": map[string]string{"code": code, "message": "refused"}})
}

func (s *Sona) handle(line []byte, first bool) {
	var f struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
		Params json.RawMessage  `json:"params"`
		Result json.RawMessage  `json:"result"`
	}
	if err := json.Unmarshal(line, &f); err != nil {
		s.t.Errorf("a frame that is not JSON: %q", line)
		return
	}
	if first && f.Method != "hello" {
		s.t.Errorf("the first frame must be hello, got %q", line)
		return
	}
	if f.Method == "" {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(line, &raw)
		var id int64
		if f.ID != nil {
			_ = json.Unmarshal(*f.ID, &id)
		}
		s.mu.Lock()
		ch := s.responses[id]
		delete(s.responses, id)
		s.mu.Unlock()
		if ch != nil {
			ch <- raw
		}
		return
	}
	s.mu.Lock()
	stopping := s.stopping
	s.mu.Unlock()
	if stopping {
		return
	}
	switch f.Method {
	case "hello":
		var p struct {
			ID          string   `json:"id"`
			Permissions []string `json:"permissions"`
			Protocol    struct{ Min, Max string }
		}
		if err := json.Unmarshal(f.Params, &p); err != nil || p.ID != s.ID || p.Permissions == nil ||
			p.Protocol.Min != "2.0.0" || p.Protocol.Max != "2.0.0" {
			s.t.Errorf("a bad hello: %s", line)
			s.fail(f.ID, "bad_hello")
			return
		}
		granted := []string{}
		for _, perm := range p.Permissions {
			if !slices.Contains(permissions, perm) {
				s.t.Errorf("hello asks for a permission that does not exist: %q", perm)
				s.fail(f.ID, "bad_hello")
				return
			}
			if slices.Contains(s.Grant, perm) {
				granted = append(granted, perm)
			}
		}
		s.mu.Lock()
		s.hello = p.Permissions
		s.mu.Unlock()
		s.write(map[string]any{"id": f.ID, "result": map[string]any{
			"id": p.ID, "protocol": "2.0.0", "permissions": granted,
			"limits": map[string]int{"maxFrame": 1 << 20, "calls": 8, "invokes": 4, "subscriptions": 8, "args": 32, "argBytes": 4096, "queue": 256},
		}})
	case "subscribe", "unsubscribe":
		var p struct {
			Topics []string `json:"topics"`
		}
		_ = json.Unmarshal(f.Params, &p)
		for _, topic := range p.Topics {
			if !slices.Contains(topics, topic) {
				s.fail(f.ID, "unknown_topic")
				return
			}
		}
		s.mu.Lock()
		if f.Method == "subscribe" {
			for _, topic := range p.Topics {
				if !slices.Contains(s.subscribed, topic) {
					s.subscribed = append(s.subscribed, topic)
				}
			}
		} else {
			s.subscribed = slices.DeleteFunc(s.subscribed, func(topic string) bool { return slices.Contains(p.Topics, topic) })
		}
		now := slices.Clone(s.subscribed)
		s.mu.Unlock()
		s.write(map[string]any{"id": f.ID, "result": map[string][]string{"topics": now}})
		if f.Method == "subscribe" {
			for _, topic := range p.Topics {
				if v, ok := s.Latest[topic]; ok {
					s.Event(topic, v)
				}
			}
		}
	case "secret":
		var p struct {
			Key string `json:"key"`
		}
		_ = json.Unmarshal(f.Params, &p)
		s.mu.Lock()
		v, ok := s.Secrets[p.Key]
		s.mu.Unlock()
		if !ok || v == "" {
			s.fail(f.ID, "not_found")
			return
		}
		s.write(map[string]any{"id": f.ID, "result": map[string]string{"value": v}})
	case "call":
		var p struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(f.Params, &p)
		fn := s.Calls[p.Command]
		if fn == nil {
			s.fail(f.ID, "unknown_command")
			return
		}
		s.write(map[string]any{"id": f.ID, "result": fn(f.Params)})
	default:
		s.fail(f.ID, "unknown_method")
	}
}
