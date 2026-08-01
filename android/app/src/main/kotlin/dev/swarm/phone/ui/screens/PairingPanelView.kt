package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.navHeader

/**
 * Phase B slice S24 -- PB-DS-6 and PB-DS-9: the pairing screen, composed in inventory C7's order.
 *
 * ## What this file composes, and the honest size of it
 *
 * C7's scaffold is a title, a body line, a mono command well, the QR or the symbols, a CTA and a
 * waiting line. The kit ships ONE of those: `navHeader`, which renders `Display.NavTitle` --
 * and derivation row 18 says the pairing step's title *is* the screen title in exactly that
 * style, so spending it here is the row's own instruction rather than a reuse of convenience.
 *
 * The other five have no factory. Row 18's mono well, row 7's SAS display, row 10's three CTA
 * variants, row 9's text field and the body/waiting copy are all fills, radii, inks or text
 * appearances, and PB-DS-6 puts every one of those in `ui/kit`. So this file does NOT build them:
 * it takes them, already built, from [PairingSlots].
 *
 * **That is a smaller claim than "the pairing screen is recomposed on the kit", and it is stated
 * here rather than left for a reader to discover.** What has actually moved is the part a screen
 * owns and this one had lost: WHICH view is on screen in WHICH step, and in what order. That
 * lived in three functions setting `View.visibility` in `PairingSurface`, where a transposed
 * condition was invisible until the step it governed was reached -- and two of these controls are
 * the only human-in-the-loop security check left in the product (ADR-007 B133). It is now
 * [PairingPanel], which is data, and this, which is one composition.
 *
 * ## Why the slots are the surface's and not this file's
 *
 * `PairingSurface` builds them because it must: `SecureWindow.gate` applies PB-SEC-12 clause 1's
 * touch filter at construction, the listeners belong to the surface's own verbs, and the same
 * control has to survive a redraw or `touchFilteredActions` stops naming the views that are
 * actually on screen. The surface may also spend an `R.style` -- PB-DS-11 permits a text
 * appearance outside the screen package, which is how the destination keeps `Mono.Code` and the
 * symbols keep their size while row 18 and row 7 are unbuilt. Moving those views in here would
 * hand them to a fence that forbids `setTextAppearance`, and the screen would render *worse* than
 * the one it replaced. A gap is not an improvement wearing a fence.
 */
object PairingTag {
    /** C7's step title, in `Display.NavTitle` per derivation row 18. */
    const val NAV = "pairing.nav"

    /** The step's sentence -- `PairingFlow.messageFor`, in row 18's body slot. */
    const val BODY = "pairing.body"

    /** The interrupted-attempt line or PB-PAIR-6's destination notice. */
    const val NOTICE = "pairing.notice"

    /** PB-PAIR-6: the destination, in the design's code face. */
    const val DESTINATION = "pairing.destination"

    /** PB-SAS-3's symbols. */
    const val SAS = "pairing.sas"

    const val SAS_INSTRUCTION = "pairing.sas.instruction"

    /** The camera preview, which has no design source at all -- see [PairingSlots.scanner]. */
    const val SCANNER = "pairing.scanner"

    /** One control. The tag names WHICH control, so an assertion cannot confuse two. */
    fun control(control: PairingControl): String = "pairing.control." + control.name
}

/**
 * The views `PairingSurface` owns, offered to the composition above.
 *
 * @param controls every control the screen can offer, whether or not this step offers it.
 *  [PairingPanel.controls] decides which are added; a control absent from this map is a control
 *  the surface cannot perform, which fails loudly rather than silently drawing a shorter screen.
 * @param scanner the camera preview. It has NO design source: inventory B20's `.qr` is the code
 *  TILE a machine displays, not the viewfinder a phone points at one, and nothing in Substrate or
 *  the mock draws a scanner. It is passed through unstyled and unwrapped.
 */
class PairingSlots(
    val body: View,
    val notice: View,
    val destination: View,
    val sas: View,
    val sasInstruction: View,
    val scanner: View,
    val controls: Map<PairingControl, View>,
)

/**
 * The pairing panel as a view.
 *
 * THE ORDER IS C7'S: heading, body, the destination or the symbols, then the controls. The
 * scanner sits directly under the body because that is where the thing the body is telling the
 * user to do happens.
 *
 * @param below views this slice has NOT recomposed, hosted under the panel.
 */
fun pairingPanelView(
    context: Context,
    panel: PairingPanel,
    slots: PairingSlots,
    below: View? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    // Twelve of the fifteen steps have no recorded heading and get none; see [PairingPanel].
    panel.title?.let { title ->
        column.addView(navHeader(context, title, null).apply { tag = PairingTag.NAV })
    }

    column.addView(slots.body.tagged(PairingTag.BODY))
    if (panel.notice.isNotEmpty()) column.addView(slots.notice.tagged(PairingTag.NOTICE))
    // ALWAYS IN THE TREE, VISIBILITY THE SURFACE'S. The preview is created on the first scan --
    // CameraX allocates a provider the moment it exists and this screen is built on every launch
    // -- so whether it is showing is a fact about the camera rather than about the step, and the
    // surface is what holds it.
    column.addView(slots.scanner.tagged(PairingTag.SCANNER))
    if (panel.destination.isNotEmpty()) {
        column.addView(slots.destination.tagged(PairingTag.DESTINATION))
    }
    if (panel.sas.isNotEmpty()) {
        column.addView(slots.sas.tagged(PairingTag.SAS))
        column.addView(slots.sasInstruction.tagged(PairingTag.SAS_INSTRUCTION))
    }

    // THE ORDER IS THE ENUM'S, not the set's. `PairingPanel.controls` is a Set and a set has no
    // order, so composing it directly would put the controls on screen in whatever order the
    // implementation happened to insert them -- and "They match" / "They do not match" swapping
    // places between draws is the one pair in this product where a mis-tap is a security event.
    PairingControl.entries.filter { it in panel.controls }.forEach { control ->
        column.addView(
            requireNotNull(slots.controls[control]) {
                "PB-DS-9: the pairing panel offers $control and the surface supplied no view for " +
                    "it, so the step would render with one control missing and nothing to say so."
            }.tagged(PairingTag.control(control)),
        )
    }

    below?.let { column.addView(it) }
    return column
}

/**
 * Tag a slot with the part it renders and detach it from whatever last held it.
 *
 * The detach is not tidiness: the panel is rebuilt on every step change, and a slot arriving at
 * its next `addView` still claiming a discarded parent is refused by Android with "the specified
 * child already has a parent".
 */
private fun View.tagged(tag: String): View = apply {
    this.tag = tag
    (parent as? ViewGroup)?.removeView(this)
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
