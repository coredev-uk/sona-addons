# home-assistant-bridge

Shows what Sona plays in [Home Assistant](https://www.home-assistant.io), as the entity `media_player.sona` (its title, artist, album, artwork, position, volume, shuffle and repeat). **Read-only:** Home Assistant cannot control Sona through it.

- **Permissions:** `playback.read` (the `status` topic) and `network`.
- **Settings:** *Home Assistant address* (`http://homeassistant.local:8123`), *Long-lived access token* (a secret row: create one in your Home Assistant profile, under Security) and *Entity* (`media_player.sona` by default; `media_player.*` or `sensor.*`).
- It publishes the newest state at most once a second, again every five minutes, and `off` when Sona stops it. A private listen or a preview shows as `idle`.
- It writes no files and does not follow redirects. Its log holds the host and Home Assistant's status, never the token.

## Why the REST API, not MQTT discovery

The bridge `POST`s to `/api/states/<entity_id>`. MQTT discovery would give a registered entity (a unique ID, an availability topic, a device card), but it needs a broker, a second secret and an MQTT client in the add-on, and many Home Assistant installs have no broker. The REST API needs only the address and a token that every install has, and the entity is enough for dashboards, automations (`state: playing`) and the media control card.

What it costs you:

- The entity is not in Home Assistant's entity registry: you cannot rename it or give it an area in the UI, and Home Assistant forgets it when it restarts until the next post (the bridge re-posts every five minutes for this).
- Without a broker there is no "unavailable" state: when Sona stops, the bridge says `off`; if Sona is killed, the last state stays.

## Control is not included

A bridge that lets Home Assistant play and pause Sona would need Home Assistant to reach the add-on (a listening port, or polling) and the `playback.control` permission, which also lets the add-on skip tracks for as long as it runs. This sample keeps to `playback.read`. If you add control, ask for `playback.control` in the manifest and the add-on's approval panel will say so.
