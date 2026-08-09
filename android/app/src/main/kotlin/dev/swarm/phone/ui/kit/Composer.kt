package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #9 Composer
 *
 * Row 9's BAR: `--p-tabbg` under a 1 dp `--p-hair` top rule, `space_8` x `space_14` of padding and
 * a `space_8` gap between the field and the control that sends what is in it.
 *
 * IT IS THE HALF OF ROW 9 NO FACTORY HAD BUILT. `textField` has cited this row since S23 and it is
 * the row's FIELD -- the well, its radius, its `--p-ink2` placeholder. The bar around it was never
 * spent anywhere, so the app's only composer was an `EditText` and a button added to a bare column
 * under the triage inbox, with none of row 9's own surface, rule, padding or gap.
 *
 * **THE FIELD AND THE SEND CONTROL ARE SLOTS**, which is `approvalSheet`'s ruling one component
 * over: `textField` is already every field in this app and `ctaButton` already every action, so a
 * bar that built its own would be a second copy of both. It is also structural rather than tidy --
 * the send control reaches a facade verb, carries PB-SEC-12 clause 1's touch filter and must
 * survive a redraw, and all three of those are `PhoneSurface`'s and cannot be a factory's.
 *
 * **THE BAR TAKES NO HEIGHT OF ITS OWN, AND ROW 9 STATES TWO NUMBERS THAT CANNOT BOTH BE SPENT.**
 * The row gives `composer_height` 52 and, in the same cell, "visual height 36, touch target 48" for
 * the field inside it. 52 measures the mock's 36 dp field between `space_8` above and below; this
 * kit's field is a 48 dp TARGET with the well inset inside it, which is `textField`'s own recorded
 * decision and PB-DS-12's floor -- so pinning 52 here would clip exactly the target that decision
 * bought. The bar wraps its content instead and spends the padding the row names, which is 48 + 16.
 *
 * **`tabbar_height` IS NOT SPENT HERE EITHER**, and for a reason that is about siting rather than
 * size: row 9 measures the composer's BOTTOM up from the tab bar, which is the scaffold's frame and
 * not this component's. The session detail composes the bar as the last thing in its own column,
 * above the bar the scaffold already draws.
 *
 * **THE BACKDROP BLUR IS NOT IMPLEMENTED, AND THIS IS THE SECOND SITE OF THAT OMISSION**
 * (agents-tracker-hxv records it, disposition on agents-tracker-dw8). `RenderEffect` blurs the view
 * it is set on rather than the content behind it, so applying row 9's 16 dp here would blur the
 * field and the send control and leave the transcript behind them sharp -- a visible defect rather
 * than an approximation. `tabBar` is the first site and carries the same paragraph; the 88%
 * translucency ships and does most of what the token was pinned for.
 *
 * @param field row 9's well. It holds what the user typed, so a caller builds it once and hands the
 *  same view to every bar it composes -- see the detach below.
 * @param send what puts those bytes on the wire. Row 9 draws a voice glyph and a stop glyph beside
 *  it and NEITHER IS BUILT: no facade verb takes dictation, which is the call the quick-reply chips
 *  row already made ("a control whose behaviour the wire does not define"), and the stop is the
 *  session's own Stop -- `App.Interrupt`, drawn by the screen with a confirmation on it -- so a
 *  second one inside this bar would be two controls for one verb.
 */
fun composerBar(context: Context, field: View, send: View): LinearLayout = KitStack(
    context,
    LinearLayout.HORIZONTAL,
    Kit.dimenPx(context, R.dimen.swarm_space_8),
).apply {
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    background = TopRule(
        fill = Kit.colour(context, R.color.swarm_tabbar_background),
        rule = Kit.colour(context, R.color.swarm_hairline),
        // `dpPx` and not `dp`, which is `tabBar`'s own line and the same third of a pixel: one
        // design value rendered two ways on one screen antialiases the hairline into a smear.
        rulePx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP).toFloat(),
    )
    val vertical = Kit.dimenPx(context, R.dimen.swarm_space_8)
    val horizontal = Kit.dimenPx(context, R.dimen.swarm_space_14)
    setPaddingRelative(horizontal, vertical, horizontal, vertical)
    // The field is taller than the label beside it, and a send control anchored to the top of the
    // bar would sit above the line the user is typing on.
    gravity = Gravity.CENTER_VERTICAL
    // THE DETACH IS NOT TIDINESS. The bar is rebuilt whenever the screen holding it is, and the
    // field is built once and re-parented because it holds what the user typed; a child arriving
    // at its next addView still claiming a discarded parent is refused by Android with "the
    // specified child already has a parent". `sessionDetailView.tagged` carries the same four lines.
    (field.parent as? ViewGroup)?.removeView(field)
    (send.parent as? ViewGroup)?.removeView(send)
    // The field takes the bar's spare room and the control keeps its own, so a one-word label and a
    // four-word one leave the same field.
    addView(field, LinearLayout.LayoutParams(0, WRAP, 1f))
    addView(send, LinearLayout.LayoutParams(WRAP, WRAP))
}
