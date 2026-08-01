package dev.swarm.phone.ui.kit

import android.content.Context
import android.content.res.ColorStateList
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md §4 Drill-down nav header
 *
 * The header a screen BELOW a root screen carries: where it came from, and what it is showing.
 *
 * There is deliberately no `origin:` line. `.navhead` is the retired mock's class and the
 * directions artifact draws only `.pnav`, the root header -- which is why §4 exists at all, and
 * why this is a DIFFERENT COMPONENT rather than a flag on [navHeader]. The two disagree on every
 * cell: `.pnav` is a 27 sp `Display.NavTitle` over a two-step padding with a counter pushed right;
 * this is a 15.5 sp `Title.Sheet` over a three-step padding with a back control on the left. A
 * shared factory taking `drill: Boolean` would put the choice between two type styles in a
 * parameter, and a screen passing that parameter is a screen choosing type -- which is the one
 * thing `android/gate/s24_screens_test.go` fences the screen package against.
 *
 * ## The chevron, and what has no source
 *
 * §4 states the glyph's weight, its box and its ink and does not state its PATH -- neither
 * artifact draws a chevron at all; the mock spells its back control as the character U+2039 in the
 * documentation page's amber, which §2 retires along with every other accent-text affordance.
 * `res/drawable/swarm_nav_back.xml` carries the path, says in its own comment that it is the
 * asset's own, and is held to §4 for the one thing §4 states about it.
 *
 * THE GLYPH TAKES ITS OWN INTRINSIC BOUNDS rather than a size typed here. §4 says 24 dp and the
 * asset declares 24 dp; reading the asset is one statement of that number instead of two that can
 * drift, and it is the reason this file spends no dimension on the glyph at all.
 *
 * ## Two things §4 asks for that this does not do
 *
 * **The right-hand action is not built.** §4 ends "Right-hand action is a `floating` chip (row
 * 10), not accent text". The one screen that has one is inventory C2's session detail, whose right
 * action is `Terminal` -- and C2 is unbuilt. The terminal peek's own right slot (C3.1) is a 44 px
 * BALANCE SHIM, which exists only to centre the title against an action; with no action there is
 * nothing to balance, so the title takes the remaining width the way `.pnav`'s does. A parameter
 * with no call site would be a component shaped by a screen that does not exist.
 *
 * **The 48 dp touch target is not set**, for [textField]'s reason exactly: §4 writes "48 dp
 * target" and rows 4, 10, 13, 15 and 22 write ">=48", and neither is a value this package's
 * annotation grammar can read. It is a WCAG floor rather than a design value. Recorded so the
 * absence reads as a known boundary rather than as an oversight.
 *
 * IT DOES NOT HANDLE ITS OWN TAP, like every other component here -- a screen composes components
 * and passes data. The back control is [KitTag.DRILL_BACK] so the screen that owns a destination
 * can find it.
 */
fun navHeaderDrill(context: Context, back: CharSequence, title: CharSequence): LinearLayout =
    KitStack(
        context,
        LinearLayout.HORIZONTAL,
        Kit.dimenPx(context, R.dimen.swarm_space_10),
    ).apply {
        // Not baseline-aligned, which is where this parts company with `.pnav` a second time. That
        // header aligns a 27 sp title against a 10 sp counter, and baselines are what stops the
        // pair looking dropped; here the tallest thing on the row is a GLYPH, which has no
        // baseline of its own worth aligning a title to.
        gravity = Gravity.CENTER_VERTICAL
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_18),
            Kit.dimenPx(context, R.dimen.swarm_space_6),
            Kit.dimenPx(context, R.dimen.swarm_space_18),
            Kit.dimenPx(context, R.dimen.swarm_space_12),
        )
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)

        addView(backControl(context, back))
        addView(
            TextView(context).apply {
                setTextAppearance(R.style.TextAppearance_Swarm_Title_Sheet)
                setTextColor(Kit.colour(context, R.color.swarm_text_primary))
                text = title
                // The remaining width, for `navHeader`'s reason: a trailing gravity works until
                // the title grows long enough to meet what is beside it, and then they overlap
                // rather than push.
                layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
                tag = KitTag.DRILL_TITLE
            },
        )
    }

/**
 * The back control: the chevron in `--p-ink`, its destination in `Body.Message` / `--p-ink2`.
 *
 * ONE VIEW AND TWO INKS, which is what a compound drawable is for. §4 gives the glyph and the
 * label different colours, and two sibling views would give a screen reader two nodes for one
 * affordance and would put the 48 dp target -- when there is one -- around neither of them.
 * `filterChip` reaches for the same mechanism to put the presence dot before its label.
 *
 * THE GAP BETWEEN GLYPH AND LABEL IS §4's OWN `space_10`. The row states exactly one separation
 * for this header and this is the only other place inside it where two things sit side by side;
 * spending anything else would be a step nobody wrote down.
 */
private fun backControl(context: Context, label: CharSequence): TextView = TextView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
    setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
    text = label
    gravity = Gravity.CENTER_VERTICAL
    setCompoundDrawablesRelativeWithIntrinsicBounds(
        context.getDrawable(R.drawable.swarm_nav_back),
        null,
        null,
        null,
    )
    // `stroke: currentColor` is the artifact's idiom for every glyph in this app and §4 names the
    // colour: the chevron is `--p-ink`, the label beside it is `--p-ink2`. The drawable ships the
    // platform's white so there is something opaque for the tint to replace.
    compoundDrawableTintList =
        ColorStateList.valueOf(Kit.colour(context, R.color.swarm_text_primary))
    compoundDrawablePadding = Kit.dimenPx(context, R.dimen.swarm_space_10)
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
    tag = KitTag.DRILL_BACK
}
