#!/usr/bin/env python3
"""Restate the SHIPPED launcher icon as SVG, so it can be rasterised and looked at.

Reads res/mipmap-anydpi-v26/ic_launcher.xml, follows its layers into
res/drawable/ic_launcher_foreground.xml, resolves every @color/ through res/values/colors.xml,
and writes SVG that is a mechanical restatement of what those files say. NO COORDINATE IS TYPED
HERE, which is the point: what gets inspected is the drawable rather than the design source it
was transcribed from.

    python3 render.py [outdir]     # default outdir: a temporary directory, path printed

Needs rsvg-convert (brew install librsvg) and Pillow. Companion: mask.py.
"""
import re
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
RES = REPO / "android/app/src/main/res"
A = "{http://schemas.android.com/apk/res/android}"


def colours():
    root = ET.parse(RES / "values/colors.xml").getroot()
    return {c.get("name"): c.text.strip() for c in root.findall("color")}


def css(ref, table):
    """An Android colour reference or literal -> (css hex, opacity)."""
    if ref.startswith("@color/"):
        ref = table[ref[len("@color/"):]]
    ref = ref.lstrip("#")
    if len(ref) == 8:
        return "#" + ref[2:], int(ref[:2], 16) / 255.0
    return "#" + ref, 1.0


def foreground(table):
    """The foreground drawable's paths, restated as SVG elements."""
    vec = ET.parse(RES / "drawable/ic_launcher_foreground.xml").getroot()
    body = []
    for p in vec.findall("path"):
        attrs = ['d="%s"' % p.get(A + "pathData")]
        fill, stroke = p.get(A + "fillColor"), p.get(A + "strokeColor")
        if fill:
            c, o = css(fill, table)
            attrs += ['fill="%s"' % c, 'fill-opacity="%s"' % o]
        else:
            attrs.append('fill="none"')
        if stroke:
            c, o = css(stroke, table)
            attrs += ['stroke="%s"' % c, 'stroke-opacity="%s"' % o,
                      'stroke-width="%s"' % p.get(A + "strokeWidth"),
                      'stroke-linecap="%s"' % p.get(A + "strokeLineCap", "butt"),
                      'stroke-linejoin="%s"' % p.get(A + "strokeLineJoin", "miter"),
                      'stroke-miterlimit="%s"' % p.get(A + "strokeMiterLimit", "4")]
        body.append("  <path " + " ".join(attrs) + "/>")
    return vec.get(A + "viewportWidth"), vec.get(A + "viewportHeight"), "\n".join(body)


def background(table):
    """The adaptive icon's background layer, and a check that monochrome reuses the foreground."""
    ai = ET.parse(RES / "mipmap-anydpi-v26/ic_launcher.xml").getroot()
    fg = ai.find("foreground").get(A + "drawable")
    mono = ai.find("monochrome").get(A + "drawable")
    if fg != mono:
        raise SystemExit("the monochrome layer is %s, not the foreground %s" % (mono, fg))
    return css(ai.find("background").get(A + "drawable"), table)[0]


def write(path, vw, vh, bg, body):
    path.write_text(
        '<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s">\n'
        % (vw, vh, vw, vh)
        + ('  <rect width="%s" height="%s" fill="%s"/>\n' % (vw, vh, bg) if bg else "")
        + body + "\n</svg>\n")


def main():
    out = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(tempfile.mkdtemp(prefix="appicon-"))
    out.mkdir(parents=True, exist_ok=True)
    table = colours()
    vw, vh, body = foreground(table)
    bg = background(table)

    write(out / "shipped_full.svg", vw, vh, bg, body)
    # Monochrome: Android 13 tints the SAME foreground flat. This is the worst case a themed
    # icon puts it through, one colour on one colour, with no hue left to separate the shapes.
    write(out / "shipped_mono.svg", vw, vh, "#1C1C1C",
          re.sub(r'(fill|stroke)="#[0-9A-Fa-f]{6}"', lambda m: '%s="#DADCE0"' % m.group(1), body))
    subprocess.run(["rsvg-convert", "-w", "1080", "-h", "1080",
                    str(out / "shipped_full.svg"), "-o", str(out / "shipped_1080.png")], check=True)
    print("foreground viewport %sx%s, background layer %s" % (vw, vh, bg))
    print("wrote %s" % out)
    return out


if __name__ == "__main__":
    main()
