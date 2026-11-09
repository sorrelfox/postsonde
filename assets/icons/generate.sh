#!/bin/sh
# Regenerates every PostSonde icon from the SVG sources in this directory.
#
# Needs: rsvg-convert (brew install librsvg), iconutil (macOS built-in), go.
# Usage: ./generate.sh   (it works from any directory)
set -eu

cd "$(dirname "$0")"

for tool in rsvg-convert iconutil go; do
  command -v "$tool" >/dev/null 2>&1 || { echo "generate.sh: '$tool' not found" >&2; exit 1; }
done

MASTER=postsonde.svg      # used for 48 px and up
SMALL=postsonde-small.svg # heavier strokes, used for 24-32 px
TINY=postsonde-16.svg     # one arc per side, used for 16 px
SIZES="16 24 32 48 64 128 256 512 1024"

src_for() {
  if [ "$1" -le 16 ]; then echo "$TINY"
  elif [ "$1" -le 32 ]; then echo "$SMALL"
  else echo "$MASTER"; fi
}

# render SRC SIZE OUT: full-bleed square PNG.
render() { rsvg-convert -w "$2" -h "$2" -o "$3" "$1"; }

# render_mac SRC SIZE OUT: Apple icon grid. The rounded square takes
# 824/1024 of the canvas, centred, with a transparent margin around it.
render_mac() {
  body=$(( $2 * 824 / 1024 ))
  pad=$(( ($2 - body) / 2 ))
  rsvg-convert -w "$body" -h "$body" --page-width "$2" --page-height "$2" \
    --left "$pad" --top "$pad" -o "$3" "$1"
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# 1. PNG set
rm -rf png && mkdir -p png
for s in $SIZES; do
  render "$(src_for "$s")" "$s" "png/postsonde-$s.png"
done

# 2. Windows .ico (16-256 px)
go run ./tools/mkico/main.go -o postsonde.ico \
  png/postsonde-16.png png/postsonde-24.png png/postsonde-32.png \
  png/postsonde-48.png png/postsonde-64.png png/postsonde-128.png \
  png/postsonde-256.png

# 3. macOS .icns via iconutil
set_dir="$tmp/postsonde.iconset"
mkdir -p "$set_dir"
for base in 16 32 128 256 512; do
  render_mac "$(src_for "$base")" "$base" "$set_dir/icon_${base}x${base}.png"
  dbl=$(( base * 2 ))
  render_mac "$(src_for "$dbl")" "$dbl" "$set_dir/icon_${base}x${base}@2x.png"
done
iconutil -c icns -o postsonde.icns "$set_dir"

# 4. Linux: 512 px icon plus a hicolor theme tree (plain copies, no symlinks)
rm -rf linux && mkdir -p linux/hicolor/scalable/apps
cp png/postsonde-512.png linux/postsonde.png
cp "$MASTER" linux/hicolor/scalable/apps/postsonde.svg
for s in 16 24 32 48 64 128 256 512; do
  mkdir -p "linux/hicolor/${s}x${s}/apps"
  cp "png/postsonde-$s.png" "linux/hicolor/${s}x${s}/apps/postsonde.png"
done

echo "Icons regenerated in $(pwd)"
