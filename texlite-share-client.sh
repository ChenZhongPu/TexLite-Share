#!/usr/bin/env bash
# Wrapper forwarding to textlite-share-client.sh
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "$DIR/textlite-share-client.sh" ]; then
  exec bash "$DIR/textlite-share-client.sh" "$@"
else
  exec bash <(curl -fsSL https://raw.githubusercontent.com/ChenZhongPu/TexLite-Share/main/textlite-share-client.sh) "$@"
fi
