# now-playing-webhook

Posts each song that starts to a web address of yours.

- **Permissions:** `playback.read` (the `now-playing` topic) and `network` (to reach your server).
- **Settings:** *Web address* (an `http` or `https` address; empty sends nothing) and *Access token* (a secret row, optional). A token is sent as `Authorization: Bearer <token>`.
- It skips a repeat of the same event, sends nothing while nothing plays, and never hears a private listen or a preview.
- It does not follow redirects, so a token only reaches the address you typed.
- It writes no files. Its log (Settings → Add-ons → Show log) holds the song's ID, the server's host and its status, never the title, the path or the token.
- A song that starts before the address is set is not sent; the next one is.

Each song is one `POST` with `Content-Type: application/json`:

```json
{
  "event": "now-playing",
  "startedAt": 1767225600000,
  "track": {
    "id": "1441164430", "title": "Come Together", "artist": "The Beatles", "album": "Abbey Road",
    "duration": 259, "explicit": false,
    "artwork": "https://is1-ssl.mzstatic.com/image/thumb/Music/600x600bb.jpg"
  }
}
```

`startedAt` is the start time in milliseconds since the epoch. Any `2xx` answer is fine; the answer is not read.

The tests run the whole session against a fake server on the loopback address (`main_test.go`); try it by hand the same way, against a listener of your own.
