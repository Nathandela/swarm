package dev.swarm.phone.ui.kit

import android.content.Context
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * origin: .sheet2 .cmd
 * derived: docs/design/substrate-components.md #18 Pairing scaffold
 *
 * A recessed block of monospace text: the pairing command line, and the terminal peek.
 *
 * ONE COMPONENT FOR EVERY MONO BLOCK IN THE APP, which is row 18's own instruction -- "Command
 * line reuses the `.cmd` mono well verbatim ... so every mono block in the app is one component".
 * The pairing scaffold and the terminal peek look identical because they ARE identical: a well
 * fill, a hairline, the card radius and `Mono.Code`. What differs is one ink, below.
 *
 * **THE TERMINAL VARIANT IS THE OLDEST UNKEPT PROMISE IN THIS SLICE.** `internal/design/tokens.json`
 * pins `terminal_peek.fg` to `--p-hero` and `terminal_peek.font` to `--p-mono`, and PB-TOK-3 has
 * enforced that against the JSON since S22 -- against the JSON, which is the whole problem. No
 * Android code ever read it, so the phosphor green the skin is named for has never rendered on a
 * handset. `PhoneSurface`'s peek was a `TextView` with `Typeface.MONOSPACE` and no colour at all.
 * `terminal = true` is where that pin finally reaches a pixel.
 *
 * THE INK IS THE ONLY DIFFERENCE, and it is worth saying what is NOT different. The well fill is
 * `--p-well` in both: the peek is recessed input, not a lit surface, and painting the ground green
 * as well would turn a terminal into a highlight. The font is `--p-mono` in both, which is what
 * `terminal_peek.font` pins and what `Mono.Code` already carries -- so that half of the pin was
 * satisfiable by the type scale and only the foreground needed a component.
 *
 * THE PADDING FOLLOWS THE LEDGER AND NOT ROW 18, and the disagreement is recorded rather than
 * silently resolved. `.sheet2 .cmd` declares `padding: 10px 11px`; PB-DS-1's ledger absorbs 11px
 * into `space_10` (its own recorded rounding, "11 to 10"), so both edges spend `space_10`. Row 18
 * writes "padding `space_10` x `space_12`", which no rounding rule produces from 11. Substrate
 * DREW this component, so the design source plus the ledger are the authority and the row is a
 * transcription of them -- `android/gate/s23_kit_test.go`'s spacing fence says so in as many
 * words: "the ledger is the authority; a component that rounds a design value its own way is
 * where a 2 dp grid stops being one". The row's `space_12` is reported as a documentation defect.
 *
 * @param terminal true for the escape-filtered VT snapshot, which takes `--p-hero`; false for
 *  every other mono block, which takes `--p-ink`.
 * @param lines the GRID's row count, which is the well's floor (agents-tracker-ksvb.3). A well
 *  that only wraps its content is a well that resizes per frame: this one prints
 *  `Snapshot.Text`, which arrives again every time the agent writes a byte, and the number of
 *  lines in it is whatever the daemon rendered at that instant -- so the card grew and shrank
 *  under the reader and the note, the button and the lease sentence below it moved with it. The
 *  machine's terminal has a fixed row count and that is the number that does not move.
 *
 *  A FLOOR AND NOT A HEIGHT, so a snapshot longer than its own grid is still drawn whole rather
 *  than clipped. What is refused is SHRINKING, which is what jumps.
 *
 *  ZERO IS "NOBODY SAID" and leaves the well exactly as it was. The pairing command line is one
 *  line of shell that arrives once and never changes; a floor there would be a height nobody
 *  asked for under a block that cannot move.
 */
fun monoWell(
    context: Context,
    text: CharSequence,
    terminal: Boolean = false,
    lines: Int = 0,
): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Mono_Code)
    setTextColor(
        Kit.colour(
            context,
            if (terminal) R.color.swarm_hero else R.color.swarm_text_primary,
        ),
    )
    this.text = text
    if (lines > 0) minLines = lines
    // A SubstrateSurface rather than a bare GradientDrawable, and not for tidiness: the platform
    // exposes no getter for a shape's stroke or its corner radius, so a well built directly could
    // only ever have its FILL asserted. `SurfaceSpec` is the single input the layers are built
    // from, which is what makes an appearance test able to read what is actually drawn.
    background = wellSurface(context)
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
    )
    // `white-space: pre` -- a terminal grid and a shell command are both column-aligned, and a
    // wrapped line silently misreports what the machine printed.
    setHorizontallyScrolling(true)
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    tag = KitTag.MONO_WELL
}
