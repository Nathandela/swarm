package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #26 Message bubble
 *
 * WHAT THE READER SAID, drawn as the one thing on this screen that is theirs.
 *
 * THE AGENT HAS NO BUBBLE, and that asymmetry is the component's whole reason to exist. Two
 * bubbles is a chat between two strangers; here one party is the person holding the phone and
 * the other is a machine reporting on their own work, so the agent's prose is written on the
 * ground and the reader's words are raised off it. The screen that composes them supplies the
 * alignment -- `Gravity.END` is layout, which is all `android/gate/s24_screens_test.go` leaves a
 * screen -- and this supplies the surface.
 *
 * IT IS NOT `activityRow` WITH A DIFFERENT FILL. The transcript drew every sender in the same
 * bordered card, which is what made the owner's screenshot read as a log rather than a
 * conversation: a row says "here is a record", and a bubble says "somebody said this". They are
 * different claims and the reuse rule (§2) does not reach across them -- the same rule that made
 * `monoWell` one component for every mono block is what makes this a second one.
 *
 * NO TAIL. Row 26 argues it: this skin's radius ladder is 16 / 20 / 12 / 10 (ADR-020; Obsidian's
 * was 14 / 18 / 10 / 8) and has nothing small
 * enough to read as a tail, so a tail would either look slightly wrong at `--p-chip-r` or invent
 * a step. Alignment and fill already say who spoke.
 */
enum class BubbleState {
    /**
     * Sent, and the agent's own transcript has echoed it back (owner ruling R6). The fill alone,
     * because settling IS the acknowledgement and a tick would be a second copy of it.
     */
    SETTLED,

    /**
     * Signed and away, and not yet echoed.
     *
     * IT IS NOT A SPINNER. A send is acknowledged when the daemon wrote bytes into a PTY, not
     * when the agent accepted them -- on the keystroke path the CLI never acknowledges anything
     * at all -- so what this state reports is the honest gap between those two facts, and it
     * closes when the echo arrives rather than when a timer runs out.
     */
    PENDING,

    /** The machine refused it, having written nothing. Row 12's border recipe, so a refusal here
     * looks like every other refusal in the app. */
    REFUSED,
}

/**
 * One bubble. [mono] draws a `/command` in the machine's own face: the reader typed a machine
 * word and the type says so.
 */
fun messageBubble(
    context: Context,
    text: CharSequence,
    state: BubbleState = BubbleState.SETTLED,
    mono: Boolean = false,
): TextView = Kit.textView(context).apply {
    setTextAppearance(
        if (mono) R.style.TextAppearance_Swarm_Mono_Code else R.style.TextAppearance_Swarm_Body_Message,
    )
    setTextColor(
        Kit.colour(
            context,
            // PENDING dims the INK rather than the surface. Fading the whole bubble would fade
            // the reader's own words, which are not in doubt -- what is in doubt is whether the
            // agent has them.
            if (state == BubbleState.PENDING) R.color.swarm_text_secondary else R.color.swarm_text_primary,
        ),
    )
    this.text = text
    background = bubbleSurface(context, state)
    // Row 26's padding and gap since ADR-020 D2 (2026-08-27): the Slate slab's `space_12` x
    // `space_16` and its 14 dp margin, where the row spent `space_8` x `space_12` and `space_8`.
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_16),
        Kit.dimenPx(context, R.dimen.swarm_space_12),
        Kit.dimenPx(context, R.dimen.swarm_space_16),
        Kit.dimenPx(context, R.dimen.swarm_space_12),
    )
    layoutParams = LinearLayout.LayoutParams(
        LinearLayout.LayoutParams.WRAP_CONTENT,
        LinearLayout.LayoutParams.WRAP_CONTENT,
    ).apply {
        gravity = Gravity.END
        topMargin = Kit.dimenPx(context, R.dimen.swarm_space_14)
    }
}
