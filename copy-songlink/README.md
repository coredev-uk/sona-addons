# copy-songlink

Adds **Get song.link** to the menu of a song or an album. It answers with a toast that shows the [song.link](https://song.link) address and offers it as a button; the address opens only when you click the button (Sona's protocol has no clipboard, and opens links only on a click).

- **Permissions:** none. It builds the address from the item's Apple Music ID (`https://song.link/i/<id>`, `https://album.link/i/<id>` for an album), so it calls nobody.
- **Settings:** *Country*, two letters (`gb`, `us`…), added to the address as `https://song.link/gb/i/<id>`. Empty lets song.link choose. A music.apple.com address in the target, if a later Sona sends one, wins.
- A song that is only in your library has no catalogue ID and so no song.link: the toast says so.
- **Only standard input and output.** The same source builds natively and for `GOOS=wasip1 GOARCH=wasm`:

```sh
make native   # dist/native/copy-songlink   (manifest: addon.json)
make wasm     # dist/wasm/copy-songlink     (manifest: addon.wasm.json, "runtime": "wasm")
```

The WebAssembly build uses `addon.wasm.json`, which is the native manifest with `"runtime": "wasm"` and the `.wasm` file as the command. It needs no permission, as a WebAssembly add-on has no network.
