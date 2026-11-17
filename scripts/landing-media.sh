#!/usr/bin/env bash
# Fills landing/media/ with the images the landing page shows: one file per slot and theme, named
# <slot>-light.png and <slot>-dark.png (the page's <picture> elements read exactly these names).
#
#   scripts/landing-media.sh          copy, crop and scale every slot from the sources below
#
# THE SLOTS. Swapping an image is a change to this table (or just replacing the file in landing/media/); the HTML
# does not change. Each row: slot, light source, dark source, width, height (the frame the page expects; a source
# is scaled to that width and cropped from the top to that height). A dark source of - means one picture for both themes (<slot>.png). Sources are paths in this repository.
# When docs/media/ has the README's stable file names, point the rows at those.
#
# Needs python3 with Pillow (best), or macOS `sips`. Nothing else, and no network. LANDING_MEDIA_PYTHON=/path/to/python
# names a Python that has Pillow when the default one has not.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
out=landing/media
mkdir -p "$out"

SLOTS='
hero       docs/media/write-light.png         docs/media/write-dark.png         1440 900
shelf      docs/media/library-light.png       docs/media/library-dark.png       1440 900
alts       docs/media/alternatives-light.png  docs/media/alternatives-dark.png  1440 900
outline    docs/media/outline-light.png       docs/media/outline-dark.png       1440 900
compendium docs/media/compendium-light.png    docs/media/compendium-dark.png    1440 900
studio     docs/media/studio-light.png        docs/media/studio-dark.png        1440 900
compile    docs/media/compile-light.png       docs/media/compile-dark.png       1440 900
phone      docs/media/phone-write-light.png   docs/media/phone-write-dark.png   600 1298
demo-poster docs/media/demo-poster.png        -                                 1280 720
'

# Pillow (python3 -m pip install pillow) scales, crops and reduces each image to a 256-colour PNG, which is a third
# of the size of the full-colour file for a screenshot of flat fills and text; without it, `sips` scales and crops
# only and the files are larger.
PY=""
if python3 -c 'import PIL' >/dev/null 2>&1; then PY=python3
elif [ -n "${LANDING_MEDIA_PYTHON:-}" ]; then PY=$LANDING_MEDIA_PYTHON; fi

make_one() { # make_one src dst width height
  local src=$1 dst=$2 w=$3 h=$4
  [ -f "$src" ] || { echo "landing-media: $src is missing" >&2; exit 1; }
  if [ -n "$PY" ]; then
    "$PY" -I - "$src" "$dst" "$w" "$h" <<'PYEOF'
import sys
from PIL import Image
src, dst, w, h = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
im = Image.open(src).convert("RGB")
im = im.resize((w, round(im.height * w / im.width)), Image.LANCZOS).crop((0, 0, w, h))
im = im.quantize(256, method=Image.MEDIANCUT, dither=Image.NONE)
im.save(dst, "PNG", optimize=True)
PYEOF
  elif command -v sips >/dev/null 2>&1; then
    sips --resampleWidth "$w" "$src" --out "$dst" >/dev/null
    sips --cropToHeightWidth "$h" "$w" --cropOffset 0 0 "$dst" >/dev/null
  else
    echo "landing-media: needs python3 with Pillow, or sips" >&2; exit 1
  fi
}

printf '%s\n' "$SLOTS" | while read -r slot light dark w h; do
  [ -n "$slot" ] || continue
  if [ "$dark" = "-" ]; then   # one picture for both themes
    make_one "$light" "$out/$slot.png" "$w" "$h"
    echo "landing-media: $slot  $(wc -c <"$out/$slot.png" | tr -d ' ') bytes  ${w}x${h}"
    continue
  fi
  make_one "$light" "$out/$slot-light.png" "$w" "$h"
  make_one "$dark" "$out/$slot-dark.png" "$w" "$h"
  echo "landing-media: $slot  $(wc -c <"$out/$slot-light.png" | tr -d ' ') + $(wc -c <"$out/$slot-dark.png" | tr -d ' ') bytes  ${w}x${h}"
done

# THE VIDEO is not stored a second time. docs/media/demo.mp4 and demo.webm are copied into the Pages artifact by
# .github/workflows/pages.yml at deploy time (scripts/publish/pages.yml). For a local preview this copies them next to
# the pictures; landing/.gitignore keeps those two copies out of git.
for v in demo.mp4 demo.webm; do
  if [ -f "docs/media/$v" ]; then cp "docs/media/$v" "$out/$v"; else echo "landing-media: docs/media/$v is missing (the page's video will not play locally)" >&2; fi
done
