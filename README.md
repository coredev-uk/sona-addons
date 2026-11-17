# Sona add-ons: samples

Sample add-ons for [Sona](https://github.com/coredev-uk/sona-go), a desktop Apple Music client. Each folder is a complete add-on you can run, read and copy.

**Protocol:** these samples speak **Sona's add-on protocol v2** (`2.0.0`). The protocol, the manifest and the permission model are in Sona's [`docs/addons.md`](https://github.com/coredev-uk/sona-go/blob/main/docs/addons.md); read it first if you will write your own.

| Sample | What it does | Permissions | Written in |
| --- | --- | --- | --- |
| [`now-playing-webhook`](now-playing-webhook) | Posts each song that starts to a web address of yours, as JSON. | `playback.read`, `network` | Go |
| [`home-assistant-bridge`](home-assistant-bridge) | Shows what Sona plays in Home Assistant, as a `media_player` entity. Read-only. | `playback.read`, `network` | Go |
| [`copy-songlink`](copy-songlink) | "Get song.link" on songs and albums. Also builds for WebAssembly. | none | Go (standard input and output only) |
| [`reference-client`](reference-client) | A readable client: logs every event it receives and shows `call`. | `playback.read` | Python 3 |

Each asks only for the permissions it needs, keeps any file it writes under the folder in `SONA_ADDON_DATA`, and logs to standard error.

## What an add-on is

A folder with an `addon.json` and a program. Sona starts the program, talks to it as one JSON object per line over its standard input and output, and stops it when you turn it off or quit.

- **Permissions.** The manifest lists what the add-on asks for (`playback.read`, `network`…). Turning the add-on on shows them, and nothing runs until you approve. Revoke one and the add-on's next call is refused.
- **The sandbox.** An add-on runs as you, inside a sandbox: it can read its own folder and the libraries a program needs, write only its private data folder (`SONA_ADDON_DATA`), and reach the internet only with the `network` permission. The details, and what each platform enforces, are in [`docs/addons.md`](https://github.com/coredev-uk/sona-go/blob/main/docs/addons.md) and Sona's `docs/security-review.md`.
- **Privacy.** An add-on never hears a private listen or a 30-second preview: for those, Sona says nothing is playing.
- Only run an add-on whose code you trust, as you would any program.

## Build

You need Go 1.25 or later. Python needs nothing built.

```sh
make            # builds every sample into dist/
make test       # vet, tests (-race), and checks every built addon.json
```

`dist/native/<id>/` holds one folder per sample, in the form Sona installs. `dist/wasm/copy-songlink/` is the WebAssembly build of the same source.

## Install a sample

Copy its folder into Sona's add-ons folder (`~/.config/sona/addons` on Linux; create it with mode 0700):

```sh
make install ID=copy-songlink      # builds, then copies with the modes Sona requires
# or by hand:
install -d -m 0700 ~/.config/sona/addons
cp -R dist/native/copy-songlink ~/.config/sona/addons/
chmod -R go-w ~/.config/sona/addons/copy-songlink
```

Sona refuses a folder that your group or others can write to (`unsafe_owner`), which is why the copy fixes the modes. Then open **Settings → Add-ons**, press **Look again**, and turn the add-on on. Python's `reference-client` needs no build: copy `reference-client/` itself.

Sample settings (the webhook's address, Home Assistant's token) are rows in the same page.

## Run one without installing it

```sh
sona addons dev dist/native/copy-songlink
```

Sona must be running. It asks you to confirm in its window, then prints the add-on's log until you press Ctrl+C. Nothing is installed, no grant is saved, and the add-on's settings rows are not shown (rows hold their defaults): so the webhook and Home Assistant samples are easiest to try against a fake server, as their tests do. Linux only for now.

## Develop

`internal/addon` is a small client of the protocol in plain Go (a reader goroutine, requests, events, invokes), and `internal/addontest` is a fake Sona, so a sample's tests run its whole session in memory. The Go samples use both; copy them if you write an add-on in Go.

```sh
go test -race ./...
go run ./tools/checkmanifest dist/native/* dist/wasm/*
```

`tools/checkmanifest` applies Sona's manifest rules to a built folder. The rules are a copy of Sona's (its package is internal), so `sona addons dev` stays the authority.

## License

[MIT](LICENSE).
