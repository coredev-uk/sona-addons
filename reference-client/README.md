# reference-client

A client of protocol v2 in one readable Python file, with no dependencies.

- **Permissions:** `playback.read`.
- Says `hello`, subscribes to `status`, `now-playing` and `settings`, and **logs every event** it receives to standard error (Settings → Add-ons → Show log, or the terminal under `sona addons dev`).
- Adds the palette command **Log Sona's status**. It shows `call`: it asks Sona for `status` (allowed by `playback.read`), logs the result and answers the invoke with a toast.
- Handles `shutdown` and exits when its input ends.

```sh
sona addons dev reference-client
```

A frame that arrives while the client waits for an answer is kept and handled afterwards, so a single thread is enough. The programs in the other folders show the same session in Go.
