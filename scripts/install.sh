#!/bin/sh
# Installs a built add-on folder into Sona's add-ons folder:
#
#   scripts/install.sh dist/native/copy-songlink
#
# Sona refuses a folder that another user, or your group, can write to, so
# this copies with a fixed mode rather than your umask. SONA_ADDONS_DIR
# overrides the destination (default: $XDG_CONFIG_HOME/sona/addons, which is
# ~/.config/sona/addons on Linux).
set -eu

[ "$#" -eq 1 ] && [ -f "$1/addon.json" ] || {
	echo "usage: $0 <built add-on folder, with an addon.json>" >&2
	exit 2
}

src=${1%/}
id=$(basename "$src")
dest=${SONA_ADDONS_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/sona/addons}

# Sona keeps the add-ons folder to yourself (mode 0700): an add-on is code
# that Sona runs.
mkdir -p "$dest"
chmod 700 "$dest"

# A fresh copy: a file the new version no longer has must not stay behind.
rm -rf "${dest:?}/$id"
cp -R "$src" "$dest/$id"
chmod -R go-w "$dest/$id"
echo "installed $id in $dest"
echo "turn it on in Sona: Settings > Add-ons"
