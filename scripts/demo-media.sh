#!/usr/bin/env bash
# `make demo`, last step: what web/demo recorded, as the README shows it (docs/media).
# Usage: scripts/demo-media.sh <recorded folder> <docs/media>
set -euo pipefail
src=$1
dst=$2
mkdir -p "$dst"
ff() { ffmpeg -y -loglevel error "$@"; }

cp "$src/demo.mp4" "$src/demo.gif" "$dst/"
for f in board-light board-dark console code layouts vault uptime status-page; do
  cp "$src/shots/$f.png" "$dst/$f.png"
done
# the galaxy is a photo more than a screen: a JPEG is a fifth of the PNG
ff -i "$src/shots/memory.png" -q:v 3 "$dst/memory.jpg"
# the two phone shots side by side, with a clear gap
ff -i "$src/shots/phone-board.png" -i "$src/shots/phone-console.png" \
  -filter_complex "[0]format=rgba,pad=iw+48:ih:0:0:color=0x00000000[a];[a][1]hstack=inputs=2" "$dst/phone.png"
ls -l "$dst"
