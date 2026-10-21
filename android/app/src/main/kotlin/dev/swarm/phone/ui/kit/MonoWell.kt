package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.HorizontalScrollView
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * origin: .sheet2 .cmd
 * derived: docs/design/substrate-components.md #18 Pairing scaffold
 *
 * A recessed block of monospace text, shared by every mono block in the app.
 */
fun monoWell(
    context: Context,
    text: CharSequence,
): TextView = Kit.textView(context).apply {
    Kit.appearance(this, R.style.TextAppearance_Swarm_Mono_Code)
    setTextColor(Kit.colour(context, R.color.swarm_text_primary))
    this.text = text
    background = wellSurface(context)
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
    )
    setHorizontallyScrolling(true)
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    tag = KitTag.MONO_WELL
}

/**
 * The well's own scroller, ready to take its place in a caller's layout.
 *
 * NOT INSIDE [monoWell] ITSELF (agents-tracker-ksvb.7). `monoWell` has to keep returning a bare,
 * unparented `TextView` -- `ApprovalSheetTest` and `PairingStepRowTest` both take its return
 * value and hand it STRAIGHT to another component's own `addView`, and a well that arrived
 * pre-parented would make that throw ("the specified child already has a parent"). So the wrap is
 * a second, OPT-IN step: `column.addView(well.scrolledHorizontally())` in place of
 * `column.addView(well)`, wherever a well is hosting content long enough to need it -- the
 * terminal peek, the session detail's snapshot, the pairing destination, the pairing command line.
 *
 * WHY THERE WAS ANYTHING TO FIX. `setHorizontallyScrolling(true)` tells the well to lay a line out
 * whole rather than wrapping it; it says nothing about where the part past the visible edge goes.
 * With `MATCH_PARENT` width and no scroller, it went nowhere -- silently clipped, a correctness
 * bug (unreachable content) wearing a typography flag's costume. This function is the somewhere:
 * a [HorizontalScrollView] measures its child with no width ceiling, so the well takes exactly the
 * width its longest line needs and hands back whatever slice of that the viewport can show.
 *
 * `fillViewport`, SO A WELL SHORTER THAN THE ROW STILL FILLS IT. Without it a short well -- the
 * pairing command line, most terminal frames -- would be only as wide as its own text, and the
 * recessed surface behind it would stop at the last character rather than spanning the row the
 * way every well drawn before this ticket did.
 *
 * THE WELL'S OWN `layoutParams` ARE OVERWRITTEN, AND HAVE TO BE. [monoWell] sets them for the row
 * it used to sit in directly (`LinearLayout.LayoutParams`); `HorizontalScrollView` extends
 * `FrameLayout`, whose layout pass casts a child's params to its OWN `LayoutParams`, and a
 * `LinearLayout.LayoutParams` surviving the wrap would be a `ClassCastException` the first time
 * the screen actually laid this out -- a fault Robolectric's `measure`/`layout` calls surface just
 * as loudly as a device would.
 *
 * NOT A KIT FACTORY, so it carries no `origin:`/`derived:` annotation and no `s23Inbox` row: it
 * makes no visual decision `monoWell` has not already made -- the fill, the ink, the type are all
 * set before this is ever called, and this only gives the finished well somewhere to overflow
 * into. `android/gate/s23_kit_test.go`'s reverse check does not see it either way, and that is
 * worth stating rather than quietly relying on: an extension function's `fun Type.name(` shape
 * never matches that check's `fun name(` pattern. The gate exists to catch a factory that chooses
 * a colour, a size or a face outside the inventory; this function chooses none of those, so
 * nothing it polices is here to miss.
 */
fun TextView.scrolledHorizontally(): View {
    layoutParams = ViewGroup.LayoutParams(WRAP, WRAP)
    return HorizontalScrollView(context).apply {
        isFillViewport = true
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        addView(this@scrolledHorizontally)
    }
}
