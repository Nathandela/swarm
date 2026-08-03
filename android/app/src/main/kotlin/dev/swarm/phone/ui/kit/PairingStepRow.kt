package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #18 Pairing scaffold
 *
 * One numbered step of the pairing instructions: an ordinal, the line it introduces, and whatever
 * that line is telling the user to look at.
 *
 * There is deliberately no `origin:` line. `.pair` is row 18's MOCK class and the shared Substrate
 * block declares no rule for it -- which is precisely why row 18 had to derive the scaffold.
 * Citing it as an origin would claim a join to a rule that does not exist.
 *
 * ## Why the kit has this and the screen does not
 *
 * The owner installed the internal-testing build, found the pairing screen, and it gave them a bare
 * text field with no camera and no instructions (agents-tracker-qx9m). The fix is a screen that says
 * where a pairing code comes from, and the shape it says it in is a numbered list --
 * `android/gate/s24_screens_test.go` fences the screens package against `R.dimen.`,
 * `setTextAppearance` and `setTextColor`, so a list built there would have no gutter, no type and
 * no ink. It would render as two unstyled TextViews and the fence would still pass, because the
 * fence is what stops a screen from CHOOSING. PB-DS-6's sentence is that every visual element is
 * one factory; this is that factory.
 *
 * ## What row 18 says, and what it does not
 *
 * The row specifies the body copy this is made of -- `Body.Message` / `--p-ink2` -- and the two
 * steps it spends: `space_18` under a block of body copy, and `space_8` between a line and the
 * thing that follows it.
 *
 * WHAT THE ROW DOES NOT ENUMERATE IS AN ORDINAL, because the artifact draws one pairing step at a
 * time and never numbers them. So the ordinal takes the body's OWN style rather than a second one
 * derived for it. The recessive gutter is the obvious embellishment and it is the one this
 * component must not make: `--p-ink3` is 3.17 to 3.50:1 on every surface in this product, under
 * PB-DS-12's 4.5:1 text floor, and the ordinal is the part that says which step a person is on.
 *
 * `maxEms = 30` IS NOT SPENT HERE, and the omission is recorded rather than silent. Row 18 states
 * it for the pairing BODY, which is `PairingSurface.message` and not this component. Spending it
 * would put the literal 30 in the kit, where `TestPBDS7_EveryNumberInTheKitIsAccountedFor` requires
 * every number to be a [KitMetrics] constant whose `derived:` annotation the metric reader can find
 * in the row -- and row 18 writes it as `maxEms=30` inside a prose cell, which that reader does not
 * parse. The measure is therefore the container's here. Reported rather than worked around.
 *
 * @param detail what the line introduces -- the command well under step 1 -- or null. It is hosted
 *  in the LINE's column and not beside the ordinal, which is the whole reason the parameter exists:
 *  a well hung off the step row itself would start under the ordinal, and the list would stop
 *  reading as a list at exactly the point it carries the thing the user has to type.
 */
fun pairingStep(
    context: Context,
    ordinal: CharSequence,
    line: CharSequence,
    detail: View? = null,
): LinearLayout = LinearLayout(context).apply {
    orientation = LinearLayout.HORIZONTAL
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP).apply {
        // Row 18: "body margin-bottom `space_18`". One step is one block of body copy.
        bottomMargin = Kit.dimenPx(context, R.dimen.swarm_space_18)
    }

    addView(
        body(context, ordinal).apply {
            tag = KitTag.STEP_ORDINAL
            layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
        },
    )

    addView(
        LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            // THE WEIGHT IS WHAT MAKES THE SECOND CELL A COLUMN. A wrap-content line beside a
            // wrap-content ordinal is measured against the parent's whole width and clips at the
            // right edge instead of wrapping -- and PB-DS-12 requires the layout to survive a 1.3x
            // font scale, which is the setting that finds it.
            layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f).apply {
                // Row 18's `space_8`: the step between a line and what it introduces, turned
                // ninety degrees. A second value here would be one nothing could be asked about.
                marginStart = Kit.dimenPx(context, R.dimen.swarm_space_8)
            }

            addView(body(context, line).apply { tag = KitTag.STEP_LINE })
            detail?.let {
                addView(
                    it,
                    LinearLayout.LayoutParams(MATCH, WRAP).apply {
                        topMargin = Kit.dimenPx(context, R.dimen.swarm_space_8)
                    },
                )
            }
        },
    )
}

/**
 * Row 18's body copy: `Body.Message` / `--p-ink2`.
 *
 * It is `private` rather than a factory because it is not a component -- a bare line of body copy
 * is not a thing the inventory names, and `TestPBDS6_EveryKitFactoryIsAnInboxComponent` reads every
 * top-level `fun` in this package as one. Both cells go through it so the two cannot drift into
 * being two type decisions.
 */
private fun body(context: Context, text: CharSequence): TextView = TextView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
    setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
    this.text = text
}
