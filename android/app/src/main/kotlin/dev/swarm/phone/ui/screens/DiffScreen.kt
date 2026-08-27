package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import dev.swarm.phone.ui.kit.monoWell

/**
 * OWNER RULING R9's SECOND HALF -- one file's unified diff, on a screen with room to read it.
 *
 * **THE DIFF IS MOVED AND NOT DELETED, AND THIS SCREEN IS THE WHOLE OF THAT CLAIM.** What shipped
 * drew `diff_excerpt` into the reading column unconditionally on every changed file, so a refactor
 * touching nine files cost nine screens of the conversation and the owner's screenshots needed two
 * frames to capture one session. The counter-argument that kept it there -- "a diff is the only
 * rendering of what actually changed on disk" -- is answered rather than overruled: what changed is
 * WHERE it is drawn. Without this screen, R9 is a deletion wearing a routing decision.
 *
 * **SIDEWAYS IS THE POINT.** A unified diff is column-aligned text whose leading `+`/`-` column
 * carries the meaning, and a reading column at body width either wraps it -- misreporting what the
 * producer normalized -- or clips it with nothing below wide enough to reach the rest. This is the
 * width a diff was always going to need and the one thing a message stream cannot give it.
 *
 * **IT NEITHER RE-RENDERS NOR RE-WRAPS.** §3.4's `diff_excerpt` is the producer's own normalized
 * unified diff -- "producers normalize ... consumers never see the raw pair" -- so what this side
 * owes it is a surface wide enough to read it on, and nothing else. No syntax colouring, no
 * per-line tinting: the drawing's own row for the chip already refused to split the machine's
 * `+N -M` into two inks, and splitting a diff body would be the same parse at a hundred times the
 * scale.
 *
 * **SAME WELL, SAME RULES** -- the drawing's own words, and [literalScreen] is where they are
 * spent, so the two screens cannot drift apart on the edit that matters.
 */
object DiffTag {
    /** The drill header: where this came from, and which file it is about. */
    const val NAV = "diff.nav"

    /** The producer's own unified diff, in the app's one mono block. */
    const val WELL = "diff.well"
}

/**
 * @param path §3.4's `path` -- or, on a rename, both paths in the direction the file moved. It IS
 *  the title: the verb and the counts are already on the chip the reader tapped, and repeating them
 *  here would be the same fact in two voices.
 * @param diff [TranscriptRoute.Diff]'s `text`, drawn byte for byte.
 * @param onBack where the chevron goes. Null draws no control at all.
 */
fun diffScreen(
    context: Context,
    path: CharSequence,
    diff: CharSequence,
    onBack: (() -> Unit)? = null,
): View = literalScreen(
    context = context,
    title = path,
    // ITS OWN WELL, NAMED HERE. See [outputScreen]: the arrangement is shared because the drawing
    // says "same well, same rules", and the PARTS are each screen's own so that neither can be
    // fenced by a claim about a factory the other calls.
    well = monoWell(context, diff).apply { tag = DiffTag.WELL },
    onBack = onBack,
    navTag = DiffTag.NAV,
)
