#!/usr/bin/env bash
# Regenerate the admin UI icons from the source artwork.
#
# The source is a 1024x652 illustration; the icons are a 1:1 crop of the cat's
# head. Everything is derived from SOURCE by this script, so tweaking the crop
# is a one-line change followed by a rerun — do not hand-edit the output files.
#
# Requires ImageMagick 7 (`magick`) and cwebp.
#
# Usage: tools/make-icons.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SOURCE="$ROOT/web/assets-source/logo-source.jpg"
OUT="$ROOT/web/public"

# The square to cut out of the source, and where the art actually sits:
#   CROP  — WxH+X+Y, must be square
#   SIZE  — master render size (512 is plenty; every icon is a downscale of it)
CROP="560x560+440+70"
SIZE=512

command -v magick >/dev/null || { echo "ImageMagick 7 (magick) not found" >&2; exit 1; }
[ -f "$SOURCE" ] || { echo "missing source artwork: $SOURCE" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> Cropping ${CROP} from the source"
magick "$SOURCE" -crop "$CROP" +repage -strip -resize "${SIZE}x${SIZE}" "$TMP/master.png"

# Palette reduction costs nothing visually on flat illustration and roughly
# halves the PNGs, which matters because these are embedded in the binary.
raster() { # raster <size> <out.png>
  magick "$TMP/master.png" -strip -resize "${1}x${1}" \
    -colors 256 -dither FloydSteinberg -define png:compression-level=9 "$2"
}

echo "==> favicon.ico (16/32/48)"
raster 16 "$TMP/16.png"
raster 32 "$TMP/32.png"
raster 48 "$TMP/48.png"
magick "$TMP/16.png" "$TMP/32.png" "$TMP/48.png" -strip "$OUT/favicon.ico"
cp "$TMP/32.png" "$OUT/favicon-32.png"

echo "==> apple-touch-icon.png (180, opaque — iOS does not honour alpha)"
magick "$TMP/master.png" -strip -resize 180x180 -background '#ffffff' -alpha remove -alpha off \
  -colors 256 -dither FloydSteinberg -define png:compression-level=9 "$OUT/apple-touch-icon.png"

echo "==> logo.webp (128, rounded — used in the app bar and on the login card)"
# Rounded corners are baked in so the <img> needs no CSS mask; radius is ~22%.
magick "$TMP/master.png" -strip -resize 128x128 \
  \( +clone -alpha extract \
     -draw 'fill black polygon 0,0 0,28 28,0 fill white circle 28,28 28,0' \
     \( +clone -flip \) -compose Multiply -composite \
     \( +clone -flop \) -compose Multiply -composite \) \
  -alpha off -compose CopyOpacity -composite "$TMP/logo-128.png"
cwebp -quiet -q 88 -alpha_q 100 "$TMP/logo-128.png" -o "$OUT/logo.webp"

echo "==> Done. Rebuild the UI so the assets land in web/dist:"
ls -la "$OUT" | tail -n +2 | awk '{printf "    %-24s %8.1f KB\n", $9, $5/1024}'
echo "    cd web && npm run build && cd .. && go build -o nicedroid ./cmd/server"
