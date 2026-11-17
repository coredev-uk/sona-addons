#!/usr/bin/env python3
"""A reference client of Sona's add-on protocol v2, in plain Python 3.

What it shows, in about a hundred lines:

* the channel: one JSON object per line on standard input and output;
  the log goes to standard error (Sona keeps it in the add-on's log);
* `hello` first, then `subscribe`;
* every event it receives is logged: `status`, `now-playing`, `settings`;
* `call`: the palette command "Log Sona's status" makes the add-on ask Sona
  for `status` and answer the invoke with a toast;
* `shutdown`, and exiting when standard input ends.

Unknown fields are ignored, as the protocol requires. Read the protocol in
https://github.com/coredev-uk/sona-go/blob/main/docs/addons.md
"""
import collections
import json
import sys

ADDON_ID = "reference-client"
PERMISSIONS = ["playback.read"]

_next_id = 0
# Frames that arrive while a request waits for its answer are kept here and
# handled afterwards, in order.
_backlog = collections.deque()


def log(message):
    print(message, file=sys.stderr, flush=True)


def send(frame):
    sys.stdout.write(json.dumps(frame, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def read_frame():
    """The next frame, or None when Sona closed our input."""
    while True:
        line = sys.stdin.readline()
        if not line:
            return None
        try:
            return json.loads(line)
        except ValueError:
            log("ignored a line that is not JSON")


def next_frame():
    return _backlog.popleft() if _backlog else read_frame()


def request(method, params):
    """Send a request and wait for its answer. Returns (result, error code)."""
    global _next_id
    _next_id += 1
    request_id = _next_id
    send({"id": request_id, "method": method, "params": params})
    while True:
        frame = read_frame()
        if frame is None:
            sys.exit(0)  # the channel ended: so do we
        if frame.get("id") == request_id and "method" not in frame:
            if "error" in frame:
                return None, frame["error"].get("code", "failed")
            return frame.get("result"), None
        _backlog.append(frame)  # an event or an invoke: later


def handle_invoke(frame):
    """Sona asks us to run one of our contributions; answer with a toast."""
    params = frame.get("params", {})
    action = params.get("action")
    log("invoke %s (deadline %s ms)" % (action, params.get("deadlineMs")))
    if action != "addon.%s.status" % ADDON_ID:
        send({"id": frame["id"], "error": {"code": "failed", "message": "unknown action"}})
        return
    result, code = request("call", {"command": "status"})
    if code:
        log("call status refused: %s" % code)
        send({"id": frame["id"], "result": {"toast": "Sona refused: %s" % code}})
        return
    track = result.get("track")
    log("call status -> %s" % json.dumps(result, separators=(",", ":")))
    toast = "Sona is %s" % result.get("state")
    if track:
        toast += ": %s, %s" % (track.get("title"), track.get("artist"))
    send({"id": frame["id"], "result": {"toast": toast[:400]}})


def main():
    result, code = request("hello", {
        "id": ADDON_ID,
        "protocol": {"min": "2.0.0", "max": "2.0.0"},
        "permissions": PERMISSIONS,
    })
    if code:
        sys.exit("hello refused: %s" % code)
    log("hello answered: %s" % json.dumps(result, separators=(",", ":")))

    # "settings" needs no permission. Subscribing delivers each topic's latest
    # value at once, then every change.
    _, code = request("subscribe", {"topics": ["status", "now-playing", "settings"]})
    if code:
        sys.exit("subscribe refused: %s" % code)

    while True:
        frame = next_frame()
        if frame is None:
            log("input closed: exiting")
            return
        event = frame.get("event")
        if event == "shutdown":
            log("shutdown: exiting")
            return
        if event:
            log("event %s %s" % (event, json.dumps(frame.get("data"), separators=(",", ":"))))
        elif frame.get("method") == "invoke":
            handle_invoke(frame)
        elif "method" in frame:
            send({"id": frame.get("id"), "error": {"code": "unknown_method", "message": "unknown method"}})


if __name__ == "__main__":
    main()
