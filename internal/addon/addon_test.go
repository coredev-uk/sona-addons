package addon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// pair connects a Conn to a scripted Sona.
func pair(t *testing.T) (*Conn, *bufio.Reader, io.WriteCloser) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := Dial(inR, outW, nil)
	t.Cleanup(func() { inW.Close(); outR.Close() })
	return c, bufio.NewReader(outR), inW
}

func line(t *testing.T, r *bufio.Reader) map[string]any {
	t.Helper()
	b, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%q: %v", b, err)
	}
	return m
}

func TestHelloAndErrors(t *testing.T) {
	c, out, in := pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		f := line(t, out)
		if f["method"] != "hello" || f["id"] != 1.0 {
			t.Errorf("hello frame %v", f)
		}
		// Unknown fields are ignored.
		io.WriteString(in, `{"id":1,"result":{"id":"x","protocol":"2.0.0","permissions":["network"],"limits":{"maxFrame":1,"new":true},"extra":1}}`+"\n")
		line(t, out)
		io.WriteString(in, `{"id":2,"error":{"code":"unknown_topic","message":"no"}}`+"\n")
	}()
	h, err := c.Hello(ctx, "x", nil)
	if err != nil || !h.Granted("network") || h.Granted("search") {
		t.Fatalf("hello = %+v, %v", h, err)
	}
	if err := c.Subscribe(ctx, "nope"); !IsCode(err, "unknown_topic") {
		t.Fatalf("subscribe = %v", err)
	}
}

func TestNextInOrderThenClosed(t *testing.T) {
	c, out, in := pair(t)
	answers := make(chan map[string]any, 2)
	go func() {
		for range 2 {
			answers <- line(t, out)
		}
	}()
	go func() {
		io.WriteString(in, "not json\n\n")
		io.WriteString(in, `{"event":"settings","data":{"values":{}}}`+"\n")
		io.WriteString(in, `{"id":7,"method":"invoke","params":{"action":"addon.x.go","target":{"kind":"song","id":"1","name":"N","more":1},"deadlineMs":5}}`+"\n")
		io.WriteString(in, `{"id":8,"method":"mystery"}`+"\n")
		in.Close()
	}()
	var names []string
	for {
		m, ok := c.Next()
		if !ok {
			break
		}
		if m.Invoke != nil {
			names = append(names, m.Invoke.Action+" "+m.Invoke.Target.ID)
			if err := m.Invoke.Reply(InvokeResult{Toast: "hi"}); err != nil {
				t.Error(err)
			}
		} else {
			names = append(names, m.Event)
		}
	}
	if strings.Join(names, ",") != "settings,addon.x.go 1" {
		t.Fatalf("messages %v", names)
	}
	byID := map[float64]map[string]any{}
	for range 2 {
		a := <-answers
		byID[a["id"].(float64)] = a
	}
	if got := byID[7]; got["result"].(map[string]any)["toast"] != "hi" {
		t.Fatalf("invoke answer %v", got)
	}
	if got := byID[8]; got["error"].(map[string]any)["code"] != "unknown_method" {
		t.Fatalf("unknown method answer %v", got)
	}
}

func TestRequestFailsWhenTheChannelEnds(t *testing.T) {
	c, out, in := pair(t)
	go func() { line(t, out); in.Close() }()
	if _, err := c.Hello(context.Background(), "x", nil); err != ErrClosed {
		t.Fatalf("err = %v; want ErrClosed", err)
	}
	if err := c.Subscribe(context.Background(), "status"); err != ErrClosed {
		t.Fatalf("after the end, err = %v; want ErrClosed", err)
	}
}

func TestLongFrame(t *testing.T) {
	c, _, in := pair(t)
	go func() {
		io.WriteString(in, `{"event":"queue","data":"`+strings.Repeat("a", 900<<10)+`"}`+"\n")
		in.Close()
	}()
	m, ok := c.Next()
	if !ok || m.Event != "queue" || len(m.Data) < 900<<10 {
		t.Fatalf("long frame: %v %d", ok, len(m.Data))
	}
}
