package dev.swarm.phone.ui.kit

import android.content.Context
import android.text.TextUtils
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #31 File change row
 *
 * One file the agent changed: what it did, to what, and by how much. The diff opens elsewhere.
 *
 * **OWNER RULING R9, AND THE THING IT MUST NOT BECOME IS WHAT SHIPS TODAY.**
 * `TranscriptPanel.kt:465` draws a unified diff inline and unconditionally, so a rename plus a
 * twelve-file refactor costs a screen per file on the one surface whose entire purpose is
 * continuous reading. This costs one line per file. The diff is not deleted -- it is moved to a
 * screen that can give it room to scroll sideways, which is what a diff needs and what a message
 * stream cannot give it.
 *
 * **IT TAKES [activityRow]'s CARD, PADDING AND GAP VERBATIM**, and that is the derivation rather
 * than economy. This row sits AMONG tool rows in the same stream; a tighter box or a different
 * fill would make it read as a different KIND of object, when what it actually is is the same kind
 * of object carrying different cells. Row 14's `--p-card`, `--p-hair`, `--p-card-r` and key light,
 * at `space_10` x `space_12` with a `space_10` gap.
 *
 * **THE PATH ELLIPSIZES AT THE MIDDLE, AND IT IS THE ONLY IDENTITY IN THIS KIT THAT DOES.**
 * [Kit.identityCell]'s rule is that a name is distinguished by its FRONT, which is true of a
 * project, a machine and a session -- and false of a path, whose distinguishing half is its last
 * segment. `ui/kit/Composer.kt` clipped at the end is `ui/kit/Compo...`, which has thrown away the
 * only part a reader was looking for; clipped in the middle it is `ui/...Composer.kt`, which has
 * thrown away the part they can infer. So this cell sets the mark itself rather than calling the
 * shared treatment, and the divergence is stated here and in the row rather than left to look like
 * an oversight.
 *
 * **THE COUNTS ARE ONE STRING IN ONE INK.** The drawing tints `+N` `--p-ok` and `-M` `--p-err`,
 * and doing that would mean splitting the machine's own words to decide which half is which --
 * the parse IS-TOOL-1 refuses one hop earlier, applied to a field this side did not author. The
 * sign is what carries the direction, and it is the wire's own sign. A caller with a machine that
 * journals the two numbers separately can revisit this; a caller inventing the split from a string
 * cannot.
 *
 * **NEVER GROUPED**, which is the drawing's own sentence and the plan's: a count of files is not a
 * record of what changed, and the moment N files become "N files changed" the reader has lost the
 * only thing this row was for.
 *
 * IT DOES NOT HANDLE ITS OWN TAP. What a row OPENS is the screen's, exactly as everywhere else in
 * this kit -- the exception is [conversationMenu], and it is an exception because its rows are
 * built from data rather than composed.
 */
fun fileChangeRow(
    context: Context,
    verb: CharSequence,
    path: CharSequence,
    counts: CharSequence,
): LinearLayout = KitStack(
    context,
    LinearLayout.HORIZONTAL,
    Kit.dimenPx(context, R.dimen.swarm_space_10),
).apply {
    // Row 14's arrangement: the three cells share a baseline so the sans verb and the mono path
    // sit on one line rather than on two boxes that happen to overlap. CENTER_VERTICAL is what
    // places that shared line inside the 48 dp the floor below asks for -- the two are not
    // alternatives; the gravity positions the group and the baseline aligns inside it.
    isBaselineAligned = true
    gravity = Gravity.CENTER_VERTICAL
    background = cardSurface(context, attention = false)
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_12),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_12),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
    )
    // The row opens a diff, so it is a control and not a record -- which is the one thing that
    // separates it from the activity row it is otherwise drawn as. Row 14 states no floor because
    // nothing there is tappable.
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    Kit.focusable(this, componentRadiusPx = Kit.dimen(context, R.dimen.swarm_radius_card))
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)

    addView(
        Kit.textView(context).apply {
            Kit.appearance(this, R.style.TextAppearance_Swarm_Body_Message)
            // `--p-ink`, row 14's body ink: the verb is the sentence of this row, not a
            // qualifier on one. It is also the WIRE's own word -- `modify`, `rename` -- and this
            // side never writes a summary of what a change did.
            setTextColor(Kit.colour(context, R.color.swarm_text_primary))
            text = verb
            // One line: the wire's change verbs are single words, and a row whose first cell
            // wrapped would push the path and the counts out of line with the rows around it.
            Kit.identityCell(this)
            layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
        },
    )
    addView(
        Kit.textView(context).apply {
            // MONO, because a path is a machine fact and this skin puts machine facts in the
            // machine's face -- row 27's subtitle makes the same call about a machine name.
            Kit.appearance(this, R.style.TextAppearance_Swarm_Mono_Meta)
            setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
            text = path
            isSingleLine = true
            // MIDDLE, and the KDoc above is the whole argument: a path's last segment is what
            // distinguishes two of them, so this is the one cell in the kit where clipping the
            // front is the honest truncation.
            ellipsize = TextUtils.TruncateAt.MIDDLE
            // The path takes the slack, so the verb and the counts keep their own widths and the
            // long cell is the one that gives.
            layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
        },
    )
    addView(
        Kit.textView(context).apply {
            Kit.appearance(this, R.style.TextAppearance_Swarm_Mono_Meta)
            setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
            text = counts
            Kit.identityCell(this)
            // HUGGING at the trailing edge. `+12 -24` is short and fixed-width in this face
            // (`tnum` is on the mono roles), so it never needs to give ground to the path.
            layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
        },
    )
}
