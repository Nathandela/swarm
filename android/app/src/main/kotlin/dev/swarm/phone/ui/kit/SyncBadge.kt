package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * Which sync state a mark carries.
 *
 * IT IS THREE WORDS AND NOT A COLOUR, which is [CtaKind]'s and [NoticeKind]'s arrangement and for
 * their reason: a factory taking an ink would put the choice back at the call site, and the three
 * inks are not interchangeable -- `--p-work` is *the app is doing something*, `--p-att` is *this
 * wants you*, `--p-err` is *this has failed*. There is deliberately no LIVE member: a live phone
 * draws no pill at all, so a fourth value here would be a colour for a component that is not on
 * screen.
 */
enum class SyncTone { SYNCING, QUIET, BROKEN }

/**
 * derived: docs/design/substrate-components.md §4 Sync status pill and strip
 *
 * The nav row's sync mark: a `.chip` carrying a state dot and one short upper-case word.
 *
 * **WHAT IT REPLACES.** Up to four sentences of `--p-ink2` body copy stacked above every
 * destination -- the transport's, the write-hold's, the machine's clock and the roster's -- which
 * field test 3 photographed sitting where the screen's title should be and pushing the whole app
 * down. One mark in the nav row says the same thing in the place a reader is already looking, and
 * says nothing at all when there is nothing to say.
 *
 * **IT IS `.chip` PLUS `.pdot`, AND NEITHER IS RE-DRAWN HERE.** [chipSurface] is row 10's floating
 * chip, unchanged, and the mark is the same 7 dp disc [statusDot] paints for a session's Group,
 * built as a compound drawable rather than a second view -- one TextView, so the pill cannot
 * measure its dot and its word on different baselines. §2's reuse rule is why this is a
 * composition and not a fourth surface recipe.
 *
 * **THE STATE IS IN THE DOT AND NOT IN THE WORD.** The label takes `Label.Chip` / `--p-ink2`, which
 * is row 10's own cell and is 6.21:1 on the chip's `--p-card`; the tone colours are indicator
 * colours, held to WCAG's 3:1 non-text floor rather than to a text floor, and a word painted in one
 * of them would be prose taking an ink that was measured as a dot. It is also what keeps the three
 * states legible to a reader who cannot separate teal from champagne: the WORD differs in every
 * state, and the colour is the second signal rather than the only one.
 *
 * **IT IS A CONTROL AND CARRIES THE 48 dp FLOOR** ([denyChip]'s ruling, PB-DS-12): the pill opens
 * the detail behind it, and a hugging mark sized to `QUIET 18h` is exactly the shape that measures
 * too small to hit. A minimum rather than a size, so the drawing is unchanged wherever the metrics
 * already clear it.
 *
 * @param description what a screen reader announces INSTEAD of [label]. It is required rather than
 *  optional, which is [badge]'s rule sharpened: `QUIET 18h` read aloud is not a sentence, and a
 *  view that takes a tap while announcing itself as a label is a control nobody can find. The words
 *  are the CALLER'S because they are copy (PB-DS-9).
 */
fun syncPill(
    context: Context,
    label: CharSequence,
    tone: SyncTone,
    description: CharSequence,
): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Label_Chip)
    setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
    text = label
    contentDescription = description
    background = chipSurface(context, selected = false)
    gravity = Gravity.CENTER
    // The 7 dp mark, flat. NO GLOW: the two glows in this kit say a session is ALIVE (row §4's
    // B134 mapping gives them to NeedsInput and Working and to nothing else), and none of the
    // three states here is a running agent -- a haloed BROKEN would read as a live failure.
    setCompoundDrawablesRelativeWithIntrinsicBounds(
        StatusDotDrawable(
            fill = Kit.colour(context, toneInk(tone)),
            glow = null,
            diameterPx = Kit.dp(context, KitMetrics.DOT_DP),
            glowRadiusPx = 0f,
        ),
        null,
        null,
        null,
    )
    compoundDrawablePadding = Kit.dimenPx(context, R.dimen.swarm_space_6)
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
    )
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    // HUGGING, for [denyChip]'s reason: this sits at the trailing edge of a nav row whose title is
    // weighted, and a MATCH_PARENT child there leaves the title at zero width.
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
    tag = KitTag.SYNC_PILL
}

/**
 * derived: docs/design/substrate-components.md §4 Sync status pill and strip
 *
 * The escalation: one opaque line across the top of the app, above the nav row.
 *
 * **IT IS OPAQUE AND IN LAYOUT, AND BOTH HALVES ARE THE POINT.** The banner it replaces was
 * transparent body copy in a slot the destination was drawn under, so on a busy screen the two
 * overlapped -- which is what field test 3 shows. An overlay cannot be made not to overlap; it can
 * only be positioned so that it usually does not. A sibling ABOVE the nav in the same column cannot
 * overlap anything by construction, and an opaque `--p-elev` fill means nothing reads through it.
 * What it costs is exactly its own height, taken from the destination, for as long as it is up --
 * which is why only one state draws it.
 *
 * **`--p-elev` WITH A HAIRLINE ALONG THE BOTTOM, WHICH IS THE TAB BAR'S RULE MIRRORED.** [tabBar]
 * and [composerBar] both paint `--p-tabbg` under a 1 dp `--p-hair` TOP rule, because a bar's rule
 * goes on the edge that faces the content. This bar is at the other end of the column, so its rule
 * faces down. The fill is `--p-elev` and not `--p-tabbg` because those two bars are the only
 * translucency §2 keeps, and translucency is the property this component must not have.
 *
 * **THE INK IS `--p-ink` AND NOT `--p-err`.** §4's notice line moves the ink to `--p-err` when the
 * machine is the one speaking, and the temptation here is obvious. It is refused: this is a
 * sentence a person has to read to the end to know what to do, `--p-err` on `--p-elev` is not a
 * pair the contrast ladder declares, and the state is already carried in colour by the pill in the
 * nav row directly beneath. One accent per statement.
 *
 * **ONE LINE, ELLIPSISED** ([Kit.identityCell]): the strip sits outside the scroll, so a sentence
 * that wrapped would move the destination by however long the transport's refusal happens to be.
 * The mark is the platform's, so a truncated warning does not read as a complete one -- and the
 * whole sentence is one tap away in the detail this strip opens.
 */
fun syncStrip(context: Context, text: CharSequence): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
    setTextColor(Kit.colour(context, R.color.swarm_text_primary))
    this.text = text
    background = BottomRule(
        fill = Kit.colour(context, R.color.swarm_surface_elevated),
        rule = Kit.colour(context, R.color.swarm_hairline),
        // The same dp the two bars spend, rounded to whole pixels once: a 1 dp rule rendered two
        // ways on one screen antialiases into a smear. [TopRule]'s call sites record it.
        rulePx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP).toFloat(),
    )
    Kit.identityCell(this)
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_18),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_18),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
    )
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    tag = KitTag.SYNC_STRIP
}

/**
 * The three tone colours.
 *
 * THEY ARE THE STATUS TOKENS AND NOT NEW ONES. `--p-work` already means *something is being worked
 * on* (the Working Group's dot and the working bar), `--p-att` already means *this wants you*, and
 * `--p-err` already means *this failed*. The sync states are the same three claims made about the
 * LINK rather than about a session, so a fourth colour would be a second vocabulary for one idea.
 */
private fun toneInk(tone: SyncTone): Int = when (tone) {
    SyncTone.SYNCING -> R.color.swarm_state_working
    SyncTone.QUIET -> R.color.swarm_state_attention
    SyncTone.BROKEN -> R.color.swarm_state_error
}
