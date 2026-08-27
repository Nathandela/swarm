#!/usr/bin/env python3
"""Reconcile the shipped Kotlin literals against the conversation drawing's copy table.

THE RULE THIS ENFORCES: copy is EXTRACTED from the owner-signed drawing, never retyped.

It exists because retyping is how this tree came to hold FIVE different sentences for the
single fact that a turn had moved on -- two in Composer.kt, one in ErrorRouting.kt, a state
label, and the sheet's own -- while every one of them read plausibly on its own. No reviewer
catches that by reading; a byte comparison catches it every time.

It also exists because the drawing CLAIMED this check before it had one. Section 03 read "The
gate checks copy as recorded text", and no test read the sheet at all; the proof was a sentence
shipping in Composer.kt that appeared on no row, with nothing failing. The honest options were
to retract the claim or to build the check. This is the check.

Originally written by the bridge lane of the chat-surface wave as a throwaway reconciliation
script; promoted here, given a root argument so it can be run against a mutated copy, and
wired to internal/verify/conversation_copy_test.go.

Usage: check-conversation-copy.py <repo-root>
       check-conversation-copy.py --inputs   # every file the check reads, one per line
"""
import html
import os
import re
import sys

DRAWING = os.path.join("docs", "design", "conversation-drawing.html")

# Which files must carry a given copy row, VERBATIM. A row absent from this map is tabled but
# unbound -- legitimate for copy a screen has not been built for yet, which is why an unbound
# row is not an error. A row bound to a file that does not carry it is drift, always.
K = "android/app/src/main/kotlin/dev/swarm/phone/"

# Which files must carry a given copy row, VERBATIM. A row absent from this map is tabled but
# UNBOUND -- legitimate for copy whose screen is not built yet, which is why an unbound row is
# not an error. A row bound to a file that does not carry it is drift, always.
#
# THIS MAP IS THE GATE'S INVENTORY AND ITS HONESTY DEPENDS ON SAYING SO. It covers the rows the
# tree actually ships today; the sheet has more, and the checker prints the ratio on every run
# precisely so a green result is never read as "the whole sheet is checked".
BOUND = {
    # ONE WORD, AND IT IS THE WHOLE OF R6: a bubble is pending until its own echo, and this is
    # what the reader sees while it waits. It went unbound until 2026-08-26 for a reason that
    # was never stated on the run -- seven characters, under the substring floor below -- so the
    # row the pending-bubble ruling turns on was the one row nobody was checking. It is compared
    # as the quoted Kotlin literal now (see `comparison`). If the label ever moves off the
    # composer kit and onto the transcript, this binding moves with it; that is the gate working.
    "bubble.pending":         [K + "ui/kit/Composer.kt"],
    "bubble.refused":         [K + "ui/ErrorRouting.kt", K + "ui/kit/Composer.kt"],
    "bubble.stale":           [K + "ui/ErrorRouting.kt", K + "ui/kit/Composer.kt"],
    "composer.ended":         [K + "ui/kit/Composer.kt"],
    "composer.nochat":        [K + "ui/kit/Composer.kt"],
    "composer.offline":       [K + "ui/kit/Composer.kt"],
    "composer.torn":          [K + "ui/kit/Composer.kt"],
    "decision.pill":          [K + "ui/screens/SessionDetailPanel.kt"],
    "decision.settled.owner": [K + "ui/screens/TranscriptPanel.kt"],
    "decision.unknown":       [K + "ui/screens/TranscriptPanel.kt"],
    "earlier":                [K + "ui/screens/SessionDetailPanel.kt"],
    "empty":                  [K + "ui/screens/TranscriptPanel.kt"],
    "kill.confirm":           [K + "ui/screens/SessionDetailPanel.kt"],
    "menu.a11y":              [K + "ui/screens/SessionDetailPanel.kt"],
    "sync":                   [K + "ui/ConnectionUi.kt"],
    # FOUR ROWS ARE DELIBERATELY UNBOUND, and the reason is a limit of this check rather than a
    # gap in the code: `gap`, `tool.more`, `decision.settled` and `decision.footer` are each
    # COMPOSED at render from fragments -- a verb and a count and a singular/plural noun; a
    # decision token and a place-word; the word "asked" and a clock. The whole sentence is a
    # literal nowhere, so a substring comparison against the source can only ever fail.
    # Binding them would mean a permanent red or a weakened comparison, and both are worse than
    # saying so here.
    #
    # THE FIX IS NOT TO WIDEN THIS SCRIPT, and it was considered. Binding them would mean the
    # sheet tabling the FRAGMENTS -- a "records missing" row and a "repair" row in place of the
    # `gap` row -- which changes what the sheet IS. It records what a reader sees on screen, not
    # how the code assembles it, and a sheet reorganised around string concatenation would be
    # signed by nobody in particular. A checker that understood fragments could cover these;
    # this one does not, and does not pretend to.
    #
    # ONE ROW MOVED THE OTHER WAY. `decision.unknown` could not bind either, for a reason that
    # WAS a gap in the code: its sentence was two literals joined with `+` for line width, so it
    # existed nowhere contiguously. It is one literal now, over this file's usual column limit
    # and with the reason recorded beside it, and it binds.
}

def inputs():
    """Every file this check reads, the drawing first.

    THE MUTATION HARNESS COPIES EXACTLY THIS LIST, and derives it from here rather than
    repeating it. It repeated it once: internal/verify/conversation_copy_test.go named four
    files while BOUND named five, so every mutation run died on the two it could not open --
    which made each negative control pass on a rejection its mutation had not caused, and one
    of them would have passed against an unmutated tree (agents-tracker-3jop). A second copy
    of this list is a second thing to keep in step, and it did not stay in step.
    """
    out = [DRAWING]
    for files in BOUND.values():
        for rel in files:
            if rel not in out:
                out.append(rel)
    return out


# WHAT THIS CHECK DOES NOT DO, stated here because the drawing's own claim depends on it.
#
# IT CHECKS ONE DIRECTION. It asks "does the tabled string appear in the file that must carry
# it". It does NOT ask "is every user-visible string in these files on the sheet" -- and that
# second direction is the one that catches a fifth wording being ADDED tomorrow. It would have
# passed all four of this tree's stale-turn wordings happily, because one of them was correct.
# The second direction is a real piece of work, not a widening of this one: it wants
# s24_screens_test.go's approach, sweeping string literals out of the kit and screen files,
# subtracting what a row claims, and failing on the remainder behind an allowlist for the
# strings that are not copy at all -- tags, keys, test ids. Tracked, not pretended.

def code_only(src):
    """Kotlin with its comments removed.

    A GATE THAT MATCHES A COMMENT IS VACUOUS, and this file is full of long KDoc that quotes
    the very sentences being checked -- the argument for a piece of copy sits directly above
    the copy. Without this, a sentence deleted from the code but still quoted in the comment
    that explains it would pass, which is the precise shape of a check that cannot fail.
    `android/gate/s24_screens_test.go` strips comments for the same reason on the same files.

    Deliberately simple: block comments out, then any line whose first non-space characters
    are `//` or `*`. It can only ever remove MORE than a perfect parser would, never less, so
    it cannot manufacture a pass.
    """
    src = re.sub(r"/\*.*?\*/", "", src, flags=re.S)
    return "\n".join(
        ln for ln in src.split("\n")
        if not ln.lstrip().startswith("//") and not ln.lstrip().startswith("*")
    )


SUBSTRING_FLOOR = 12

# A bare substring search for a SHORT word proves nothing: `sending` matches inside `resending`
# and inside any identifier that happens to contain it, so a pass would say only that the
# letters occur somewhere. That is the real reason for a length floor, and it is a reason to
# compare short cells DIFFERENTLY rather than to drop them.


def comparison(cell):
    """How one tabled cell is compared against the source: (needle, how, why-not).

    TWO CELLS ARE NOT COMPARED, FOR TWO DIFFERENT REASONS, and reporting either under the
    other's name is a lie the run tells about its own coverage. Until 2026-08-26 every skip
    printed "template only", which is true of `<change> - <path> - +N -M` and false of
    `sending`: the second is a literal, it has bytes, and they are the bytes on screen. The
    row that ruling belongs to went unchecked while the run described it as a limit of the
    check (agents-tracker-3jop).

    A TEMPLATE cannot be compared at all -- the whole sentence is assembled at render and
    exists nowhere as a contiguous string. A SHORT LITERAL can, as the quoted Kotlin literal
    the source must write, which is exact where a bare substring would be incidental.
    """
    if not cell:
        return None, "", ("an empty cell, which every file on earth carries: comparing it "
                          "would print OK and mean nothing")
    if "<" in cell or ">" in cell:
        return None, "", ("a <placeholder> template: the sentence is composed at render and "
                          "exists nowhere as contiguous bytes")
    if len(cell) >= SUBSTRING_FLOOR:
        return cell, "", ""
    if '"' in cell or "\\" in cell or "$" in cell:
        return None, "", ("a short literal carrying a quote, a backslash or a Kotlin template "
                          "mark, which this checker cannot requote for an exact comparison")
    return '"%s"' % cell, ("as the quoted Kotlin literal, being under the %d-character floor "
                           "where a bare substring would match inside an identifier"
                           % SUBSTRING_FLOOR), ""


ROW = re.compile(r'<tr><td class="k">([a-z0-9.]+)</td><td>(.*?)</td><td>(.*?)</td></tr>', re.S)


def tabled_copy(root):
    """Every row of section 03, as key -> list of the bold cells it tables."""
    path = os.path.join(root, DRAWING)
    try:
        src = open(path, encoding="utf-8").read()
    except OSError as exc:
        print("CANNOT READ THE DRAWING at %s: %s" % (path, exc))
        print("The sheet is the source of truth for copy; without it this check is vacuous.")
        return None
    out = {}
    for key, says, _when in ROW.findall(src):
        bolds = re.findall(r"<b>(.*?)</b>", says, re.S)
        out[key] = [html.unescape(re.sub(r"<[^>]+>", "", b)).strip() for b in bolds]
    return out


def main(argv):
    if len(argv) != 2:
        print("\n".join(__doc__.strip().splitlines()[-2:]))
        return 2
    root = argv[1]
    if root == "--inputs":
        print("\n".join(inputs()))
        return 0
    tabled = tabled_copy(root)
    if tabled is None:
        return 1
    if not tabled:
        # A parse that matched nothing would report "no violations" perfectly. It is a
        # failure, not a pass: the same defect class PB-DOC-7 was written against.
        print("PARSED ZERO ROWS from the copy table. A check that matched nothing cannot fail,")
        print("and an unfailable check is indistinguishable from a passing one.")
        return 1

    faults = 0
    checked = 0
    for key, files in sorted(BOUND.items()):
        if key not in tabled or not tabled[key]:
            print("MISSING ROW  %s: bound to %d file(s) and on no row of the sheet."
                  % (key, len(files)))
            print("             Either the row was renamed, or a screen ships copy nobody signed.")
            faults += 1
            continue
        # EVERY bold cell, not just the first. Several rows table a placeholder AND the
        # sentence under it; checking one of two silently halves the coverage while looking
        # complete. Each cell reports how it was compared, or why it could not be.
        cells = [(c,) + comparison(c) for c in tabled[key]]
        if not any(needle for _c, needle, _how, _why in cells):
            # A BINDING THAT COMPARES NOTHING IS NOT A NOTE. Naming a file in BOUND asserts
            # that the file carries this copy; if no cell can be compared, the assertion is
            # never made, and the run reads exactly like one where it held.
            print("\nNOTHING TO COMPARE  %s: bound to %d file(s), and no cell of its row can"
                  % (key, len(files)))
            print("                    be compared, so the binding asserts nothing.")
            for cell, _needle, _how, why in cells:
                print("                    %r: %s" % (cell, why))
            print("                    Either the row stopped tabling a literal, or the")
            print("                    binding belongs on a row that still does.")
            faults += 1
            continue
        print("\n%s" % key)
        for sentence, needle, how, why in cells:
            if not needle:
                print("  not compared: %r -- %s" % (sentence, why))
                continue
            exotic = [hex(ord(c)) for c in sentence if ord(c) > 127]
            print("  extracted : %r\n  codepoints: %s"
                  % (sentence, exotic or "all ASCII"))
            if how:
                print("  compared  : %r %s" % (needle, how))
            for rel in files:
                path = os.path.join(root, rel)
                try:
                    carried = needle in code_only(open(path, encoding="utf-8").read())
                except OSError as exc:
                    print("  UNREADABLE %s (%s)" % (rel, exc))
                    faults += 1
                    continue
                checked += 1
                print("  %-5s %s" % ("OK" if carried else "DRIFT", rel))
                if not carried:
                    print("        the sheet says the words above and this file does not carry")
                    print("        them byte for byte. A near-miss is the failure mode: an en")
                    print("        dash for an em dash, or a curled apostrophe, reads")
                    print("        identically and is not the same string.")
                    faults += 1

    print("\n%d binding(s) checked across %d of %d tabled row(s), ONE DIRECTION."
          % (checked, len(BOUND), len(tabled)))
    print("Unbound rows are tabled copy whose screen is not built yet, and are NOT checked.")
    print("The reverse direction -- every shipped string being on the sheet -- is NOT checked.")
    if not checked and not faults:
        print("NOTHING WAS ACTUALLY COMPARED. Treating as a failure for the reason above.")
        return 1
    return 1 if faults else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
