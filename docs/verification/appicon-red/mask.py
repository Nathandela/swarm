#!/usr/bin/env python3
"""Apply the launcher masks to the rendered icon, measure what reaches where, and lay it out.

An adaptive icon that compiles is not an adaptive icon that looks right. This takes render.py's
output, masks it the way a launcher does -- the mask covers the central 72dp of the 108dp canvas,
and the outer 18dp on each side is reserve the user never sees -- and reports how many mark pixels
each mask shape removes.

    python3 render.py /tmp/appicon && python3 mask.py /tmp/appicon

Writes launcher_<mask>_<size>.png, mono_<mask>_<size>.png and contact_sheet.png. LOOK AT THEM.
"""
import math
import subprocess
import sys
from pathlib import Path

from PIL import Image, ImageDraw

SS = 12           # px per dp in the working render
CANVAS = 108 * SS
INSET = 18 * SS   # the reserve on each side; the mask lives inside what is left


def render(out, name, px):
    png = out / ("%s_%d.png" % (Path(name).stem, px))
    subprocess.run(["rsvg-convert", "-w", str(px), "-h", str(px), str(out / name), "-o", str(png)],
                   check=True)
    return Image.open(png).convert("RGBA")


def mask_image(kind):
    m = Image.new("L", (CANVAS, CANVAS), 0)
    d = ImageDraw.Draw(m)
    box = [INSET, INSET, CANVAS - INSET, CANVAS - INSET]
    if kind == "circle":
        d.ellipse(box, fill=255)
    elif kind == "squircle":
        d.rounded_rectangle(box, radius=20 * SS, fill=255)
    elif kind == "rounded":
        d.rounded_rectangle(box, radius=12 * SS, fill=255)
    else:
        d.rectangle(box, fill=255)
    return m


def masked(base, kind, size):
    img = base.copy()
    img.putalpha(mask_image(kind))
    return img.crop((INSET, INSET, CANVAS - INSET, CANVAS - INSET)).resize((size, size), Image.LANCZOS)


def is_mark(rgb):
    """The mark is the only strongly green thing on the canvas."""
    return rgb[1] - rgb[2] > 40


def extent(img, label):
    """Max radius of mark pixels from the canvas centre, in dp. MEASURED, not derived."""
    px = img.convert("RGB").load()
    w, h = img.size
    s = w / 108.0
    best, at, xs, ys = 0.0, None, [], []
    for y in range(h):
        for x in range(w):
            if is_mark(px[x, y]):
                bx, by = (x + 0.5) / s, (y + 0.5) / s
                xs.append(bx)
                ys.append(by)
                d = math.hypot(bx - 54, by - 54)
                if d > best:
                    best, at = d, (round(bx, 2), round(by, 2))
    print("%s: bbox x %.2f..%.2f y %.2f..%.2f  max r %.2f at %s  [Android guarantees 33]"
          % (label, min(xs), max(xs), min(ys), max(ys), best, at))
    return best


def clipped(base, kind):
    px, mp = base.convert("RGB").load(), mask_image(kind).load()
    total = cut = 0
    for y in range(CANVAS):
        for x in range(CANVAS):
            if is_mark(px[x, y]):
                total += 1
                if mp[x, y] < 128:
                    cut += 1
    return cut, total


def main():
    out = Path(sys.argv[1])
    base = render(out, "shipped_full.svg", CANVAS)
    mono = render(out, "shipped_mono.svg", CANVAS)

    extent(base, "shipped drawable, unmasked")
    for kind in ("circle", "squircle", "rounded", "square"):
        cut, total = clipped(base, kind)
        print("mask %-9s: clips %d of %d mark pixels (%.3f%%)" % (kind, cut, total, 100.0 * cut / total))

    for size in (48, 96, 512):
        for kind in ("circle", "squircle"):
            masked(base, kind, size).save(out / ("launcher_%s_%d.png" % (kind, size)))
            masked(mono, kind, size).save(out / ("mono_%s_%d.png" % (kind, size)))

    # A contact sheet on mid grey, so both the near-black ground and the flat monochrome are
    # visible against it. 48dp is shown at 8x nearest-neighbour: the question at 48dp is whether
    # the three legs keep a channel between them, and that is a question about actual pixels.
    # Row 1: the icon. Row 2: the same artwork as a themed-icon silhouette. Left half is 48dp at
    # 8x nearest-neighbour, right half is 192dp, both masks in each.
    cell, zoom, pad = 48 * 8, 192, 20
    sheet = Image.new("RGB", (pad + 2 * (cell + pad) + 2 * (zoom + pad),
                              pad + 2 * (cell + pad)), (110, 112, 116))
    for row, src in enumerate((base, mono)):
        y = pad + row * (cell + pad)
        x = pad
        for kind in ("circle", "squircle"):
            z = masked(src, kind, 48).resize((cell, cell), Image.NEAREST)
            sheet.paste(z, (x, y), z)
            x += cell + pad
        for kind in ("circle", "squircle"):
            img = masked(src, kind, zoom)
            sheet.paste(img, (x, y + (cell - zoom) // 2), img)
            x += zoom + pad
    sheet.save(out / "contact_sheet.png")
    print("wrote %s" % (out / "contact_sheet.png"))


if __name__ == "__main__":
    main()
