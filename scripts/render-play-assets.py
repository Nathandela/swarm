#!/usr/bin/env python3
"""Render the Play Console upload assets from the launcher icon's own geometry.

    python3 scripts/render-play-assets.py

WHY THIS EXISTS. The Play Console's 512x512 store icon and 1024x500 feature graphic are
SEPARATE uploads and are NOT taken from the APK. A shipped launcher icon makes them look
handled; the gap only surfaces when the Console refuses the submission. So they are generated
here rather than drawn once by hand somewhere nobody can find again.

WHAT THEY ARE NOT. These are upload artifacts, not app resources. They are written to
docs/ops/play-assets/ and must never land under android/app/src/main/res -- a PNG there would
be a second, rasterised copy of the icon competing with the vector the launcher actually uses,
and it would have to answer to the token gate.

THE GEOMETRY IS COPIED, AND THAT IS THE ONE WEAKNESS. The coordinates below are the same ones
in android/app/src/main/res/drawable/ic_launcher_foreground.xml, restated because nothing here
can parse an Android VectorDrawable. If that file's mark changes, this must be re-run and these
numbers updated; the assets do not follow it automatically. Keeping them in one script at least
makes "re-render the store assets" a single command rather than an act of recollection.

DEPENDENCY, STATED PLAINLY: Pillow, which is NOT part of this repository's toolchain and not
present on a stock macOS python3.

    python3 -m pip install pillow

THE WORDMARK IS SET IN --p-font, NOT --p-mono, and the first attempt got that wrong. Menlo is
monospace, so a narrow letter sits in a wide cell: measured, `r` carries a 21-unit left bearing
inside its 72-unit advance while `a` ends 10 short of its own, putting 31 units of white
between them against 7 between `w` and `a`. The advances are uniform -- it is not a fallback or
a kerning fault -- but the INK is not, and the eye reads the result as a spacing defect between
"swa" and "rm".

--p-font ("-apple-system, BlinkMacSystemFont, \"SF Pro Text\", sans-serif") is the app's primary
type and the right token for brand text; --p-mono is for terminal content, which a wordmark is
not. On macOS those names resolve to SFNS.ttf. Set SWARM_ASSET_FONT to override.
"""
import os
import sys

try:
    from PIL import Image, ImageDraw, ImageFont
except ImportError:  # pragma: no cover - the message IS the handling
    sys.exit("this script needs Pillow: python3 -m pip install pillow")

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "docs", "ops", "play-assets")

# The three token colours, from android/app/src/main/res/values/colors.xml. They are the same
# origin the app draws with (PB-TOK-1); an asset in a colour the product does not use would be
# a brand that exists only in the store listing.
BACKGROUND = (0x08, 0x09, 0x0A)
INK = (0xF7, 0xF8, 0xF8)

# The mark, in the adaptive icon's 108x108 space. See ic_launcher_foreground.xml.
CHEVRON = [(38.0, 42.0), (52.0, 54.0), (38.0, 66.0)]
CURSOR = [(59.0, 66.0), (72.0, 66.0)]
STROKE = 7.0

# The mark's DRAWN extent in 108-space, stroke included. It is not the canvas: the glyph
# occupies 41x31 of the 108x108 box, so sizing a lockup by the canvas renders a mark a third
# the height you asked for -- which is exactly how the first feature graphic came out, with the
# prompt reading as an afterthought beside the wordmark.
MARK_BOX = (34.5, 38.5, 75.5, 69.5)

# Supersampling factor. The mark is pure geometry, so rendering large and downsampling with a
# good filter is all the antialiasing it needs and avoids a drawing dependency.
SS = 4


def draw_mark(draw, cx, cy, scale):
    """Stroke the mark centred on (cx, cy), where scale maps one 108-space unit to pixels.

    Round caps and joins are drawn explicitly: PIL's line joint handling does not cap ends, and
    a butt-capped chevron reads as a broken glyph at small sizes.
    """
    def place(pt):
        return (cx + (pt[0] - 54.0) * scale, cy + (pt[1] - 54.0) * scale)

    width = max(1, int(round(STROKE * scale)))
    radius = width / 2.0
    for path in (CHEVRON, CURSOR):
        points = [place(p) for p in path]
        draw.line(points, fill=INK, width=width, joint="curve")
        for x, y in points:
            draw.ellipse((x - radius, y - radius, x + radius, y + radius), fill=INK)


def store_icon(path):
    """512x512, RGB (no alpha -- Play rejects an alpha channel on this field).

    THE MARK IS SIZED AS THE LAUNCHER SHOWS IT, not as the 108dp canvas holds it. A launcher
    masks the adaptive canvas down to its inner 72dp, so mapping 72dp (not 108) onto 512px puts
    the mark at the optical size a phone's home screen renders. Compositing the full 108 canvas
    instead would shrink it by a third and it would read as undersized beside every other icon
    in the store list.
    """
    size = 512
    img = Image.new("RGB", (size * SS, size * SS), BACKGROUND)
    draw_mark(ImageDraw.Draw(img), size * SS / 2, size * SS / 2, (size / 72.0) * SS)
    img.resize((size, size), Image.LANCZOS).save(path, "PNG", optimize=True)


def feature_graphic(path):
    """1024x500, RGB.

    A BANNER, NOT A LARGE ICON. The mark and the wordmark are one horizontal lockup, centred
    and well clear of the edges: the Console crops this asset differently across its surfaces,
    so anything near a border is liable to be cut.

    THE TWO ELEMENTS ARE SIZED AGAINST EACH OTHER, not against the canvas. The mark is scaled
    so its drawn height matches the wordmark's, which is what makes it read as a lockup rather
    than as a small glyph parked next to some text.
    """
    w, h = 1024, 500
    img = Image.new("RGB", (w * SS, h * SS), BACKGROUND)
    draw = ImageDraw.Draw(img)

    wordmark = "swarm"
    font_path = os.environ.get("SWARM_ASSET_FONT", "/System/Library/Fonts/SFNS.ttf")
    if not os.path.exists(font_path):
        sys.exit("no wordmark font at %s; set SWARM_ASSET_FONT" % font_path)
    font = ImageFont.truetype(font_path, int(120 * SS))

    left, top, right, bottom = draw.textbbox((0, 0), wordmark, font=font)
    text_w, text_h = right - left, bottom - top

    # The mark is sized ABOVE the wordmark's height, not equal to it. "swarm" is all lowercase,
    # so its bounding box is roughly the x-height; a symbol matched to that reads as smaller
    # than the letters beside it, because the eye compares the symbol to the whole word. 1.35x
    # is where the two carry the same weight.
    x0, y0, x1, y1 = MARK_BOX
    mark_h = 1.35 * text_h
    scale = mark_h / (y1 - y0)
    mark_w = (x1 - x0) * scale
    gap = 0.5 * text_h

    total = mark_w + gap + text_w
    x = (w * SS - total) / 2.0
    mid = h * SS / 2.0

    draw_mark(draw, x + mark_w / 2.0 + ((x0 + x1) / 2.0 - 54.0) * -scale, mid, scale)
    draw.text((x + mark_w + gap - left, mid - text_h / 2.0 - top), wordmark, font=font, fill=INK)

    img.resize((w, h), Image.LANCZOS).save(path, "PNG", optimize=True)


def main():
    os.makedirs(OUT, exist_ok=True)
    icon = os.path.join(OUT, "play-store-icon-512.png")
    feature = os.path.join(OUT, "play-feature-graphic-1024x500.png")
    store_icon(icon)
    feature_graphic(feature)
    for p in (icon, feature):
        with Image.open(p) as im:
            print("%s  %dx%d  %s  %.0f KB"
                  % (os.path.relpath(p, ROOT), im.width, im.height, im.mode,
                     os.path.getsize(p) / 1024.0))


if __name__ == "__main__":
    main()
