// Package addon is a small client of Sona's add-on protocol v2
// (https://github.com/coredev-uk/sona-go/blob/main/docs/addons.md).
//
// It uses only the standard library and only the add-on's standard input and
// output, so a program built on it also builds for GOOS=wasip1 GOARCH=wasm.
//
// The shape of a program:
//
//	c := addon.Dial(os.Stdin, os.Stdout, logger)
//	c.Hello(ctx, "my-addon", []string{"playback.read"})
//	c.Subscribe(ctx, "now-playing")
//	for m := range c.Messages() { ... }   // ends when Sona closes the channel
//
// A reader goroutine takes every frame off the channel at once, so a request
// (Hello, Subscribe, Secret, Call) can be made from any goroutine, including
// the one that handles messages. Events and invokes wait in an unbounded
// queue: Sona keeps at most the newest event of each topic and at most four
// invokes in flight, so the queue stays small.
package addon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
)

// Protocol is the protocol version this package speaks.
const Protocol = "2.0.0"

// maxFrame is the longest frame read. Sona's limit is 1 MiB and it reports it
// in the hello answer; the margin is for the newline.
const maxFrame = 1<<20 + 1<<10

// ErrClosed is returned by a request when the channel has ended.
var ErrClosed = errors.New("addon: the channel is closed")

// Error is an error frame. Sona's message for a code is fixed text; switch
// on Code.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return "addon: " + e.Code }

// IsCode reports whether err is an error frame with this code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Message is what Sona sends that is not an answer: an event or an invoke.
type Message struct {
	// Event is the event's name ("now-playing", "settings", "shutdown"…),
	// with its payload in Data. It is empty for an invoke.
	Event string
	Data  json.RawMessage
	// Invoke is set when the user chose one of the add-on's contributions.
	Invoke *Invoke
}

// Target is the item an action was chosen on.
type Target struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL is not sent today; it is read in case a later minor version adds it.
	URL string `json:"url"`
}

// Invoke is Sona's request that the add-on run one of its contributions.
// Answer it exactly once with Reply or Fail, before DeadlineMs passes.
type Invoke struct {
	Action     string
	Target     *Target // nil for a command
	DeadlineMs int

	id int64
	c  *Conn
}

// InvokeResult is the answer to an invoke. Both fields are plain text and
// optional. URL is offered as a button and opens only after a click.
type InvokeResult struct {
	Toast string `json:"toast,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Reply answers the invoke with a result.
func (i *Invoke) Reply(r InvokeResult) error { return i.c.send(response{ID: i.id, Result: r}) }

// Fail answers the invoke with an error. Sona passes only the code on, which
// is lowercase letters and underscores.
func (i *Invoke) Fail(code, message string) error {
	return i.c.send(response{ID: i.id, Error: &Error{Code: code, Message: message}})
}

// HelloResult is Sona's answer to hello.
type HelloResult struct {
	ID          string   `json:"id"`
	Protocol    string   `json:"protocol"`
	Permissions []string `json:"permissions"`
	Limits      struct {
		MaxFrame int `json:"maxFrame"`
	} `json:"limits"`
}

// Granted reports whether the permission is granted in this session.
func (h *HelloResult) Granted(permission string) bool {
	for _, p := range h.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}

type request struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type response struct {
	ID     int64  `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

// frame is any frame Sona sends. Unknown fields are ignored, as the protocol
// requires.
type frame struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// Conn is one session with Sona.
type Conn struct {
	w   io.Writer
	wmu sync.Mutex // one frame at a time
	log *log.Logger

	mu      sync.Mutex
	next    int64
	pending map[int64]chan frame
	closed  bool

	qmu   sync.Mutex
	cond  *sync.Cond
	queue []Message
	done  bool
	out   chan Message
}

// Dial starts reading r (the add-on's standard input) and writes to w (its
// standard output). The log goes to standard error; it never carries a
// secret: do not log one.
func Dial(r io.Reader, w io.Writer, logger *log.Logger) *Conn {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	c := &Conn{w: w, log: logger, pending: map[int64]chan frame{}, out: make(chan Message)}
	c.cond = sync.NewCond(&c.qmu)
	go c.read(r)
	go c.pump()
	return c
}

// Messages delivers events and invokes in order. It is closed when the
// channel ends: Sona closed the add-on's input, which an add-on must treat
// as the end of its run.
func (c *Conn) Messages() <-chan Message { return c.out }

func (c *Conn) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(br)
		if len(bytes.TrimSpace(line)) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			break
		}
	}
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	c.qmu.Lock()
	c.done = true
	c.qmu.Unlock()
	c.cond.Broadcast()
}

// readLine reads one line of at most maxFrame bytes. A longer line is an
// error: Sona never sends one.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := br.ReadSlice('\n')
		line = append(line, part...)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(line) > maxFrame {
				return nil, fmt.Errorf("addon: a frame is longer than %d bytes", maxFrame)
			}
		default:
			return line, err
		}
	}
}

func (c *Conn) dispatch(line []byte) {
	var f frame
	if err := json.Unmarshal(line, &f); err != nil {
		c.log.Printf("ignored a frame that is not JSON: %v", err)
		return
	}
	switch {
	case f.ID != nil && f.Method == "invoke":
		var p struct {
			Action     string  `json:"action"`
			Target     *Target `json:"target"`
			DeadlineMs int     `json:"deadlineMs"`
		}
		if err := json.Unmarshal(f.Params, &p); err != nil {
			_ = c.send(response{ID: *f.ID, Error: &Error{Code: "bad_arguments", Message: "bad invoke"}})
			return
		}
		c.push(Message{Invoke: &Invoke{Action: p.Action, Target: p.Target, DeadlineMs: p.DeadlineMs, id: *f.ID, c: c}})
	case f.ID != nil && f.Method != "":
		_ = c.send(response{ID: *f.ID, Error: &Error{Code: "unknown_method", Message: "unknown method"}})
	case f.ID != nil:
		c.mu.Lock()
		ch := c.pending[*f.ID]
		delete(c.pending, *f.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- f // buffered: the waiter may have given up
		}
	case f.Event != "":
		c.push(Message{Event: f.Event, Data: f.Data})
	}
}

func (c *Conn) push(m Message) {
	c.qmu.Lock()
	c.queue = append(c.queue, m)
	c.qmu.Unlock()
	c.cond.Signal()
}

func (c *Conn) pump() {
	defer close(c.out)
	for {
		c.qmu.Lock()
		for len(c.queue) == 0 && !c.done {
			c.cond.Wait()
		}
		if len(c.queue) == 0 {
			c.qmu.Unlock()
			return
		}
		m := c.queue[0]
		c.queue[0] = Message{}
		c.queue = c.queue[1:]
		c.qmu.Unlock()
		c.out <- m
	}
}

func (c *Conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(b)
	return err
}

// Request sends a request and waits for its answer, decoding the result into
// result (which may be nil). An error frame comes back as *Error.
func (c *Conn) Request(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.next++
	id := c.next
	ch := make(chan frame, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.send(request{ID: id, Method: method, Params: params}); err != nil {
		c.forget(id)
		return err
	}
	select {
	case f, ok := <-ch:
		switch {
		case !ok:
			return ErrClosed
		case f.Error != nil:
			return f.Error
		case result != nil:
			return json.Unmarshal(f.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *Conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Hello must be the first request. permissions are what the add-on asks for
// (the same as its manifest's); the answer says which are granted now.
func (c *Conn) Hello(ctx context.Context, id string, permissions []string) (*HelloResult, error) {
	if permissions == nil {
		permissions = []string{}
	}
	type version struct {
		Min string `json:"min"`
		Max string `json:"max"`
	}
	var res HelloResult
	err := c.Request(ctx, "hello", struct {
		ID          string   `json:"id"`
		Protocol    version  `json:"protocol"`
		Permissions []string `json:"permissions"`
	}{id, version{Protocol, Protocol}, permissions}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Subscribe subscribes to topics ("status", "now-playing", "queue",
// "settings"). Sona then sends each topic's latest value at once.
func (c *Conn) Subscribe(ctx context.Context, topics ...string) error {
	return c.Request(ctx, "subscribe", map[string][]string{"topics": topics}, nil)
}

// Secret reads one of the add-on's own secret rows. It fails with code
// "not_found" while the row is empty.
func (c *Conn) Secret(ctx context.Context, key string) (string, error) {
	var res struct {
		Value string `json:"value"`
	}
	if err := c.Request(ctx, "secret", map[string]string{"key": key}, &res); err != nil {
		return "", err
	}
	return res.Value, nil
}

// Call runs one of the sona CLI's commands (docs/addons.md, "call"); params
// is {"command": …, "args": […]}. The result is the command's JSON.
func (c *Conn) Call(ctx context.Context, params, result any) error {
	return c.Request(ctx, "call", params, result)
}
