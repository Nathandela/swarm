package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.navHeaderDrill
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.readOnlyNote

/**
 * Phase B slice S24 -- PB-DS-6 and PB-DS-9: the terminal peek, composed in inventory C3's order.
 *
 * ## What it composes
 *
 * C3 is three things and the kit now ships all three: the drill-down header (§4), the recessed
 * mono well the machine's grid is printed in (`.sheet2 .cmd`, in `terminal_peek.fg`), and the
 * read-only note (row 22). Nothing here decides how any of them looks --
 * `android/gate/s24_screens_test.go` fences this package to component calls plus layout, so an
 * `R.color`, an `R.dimen`, an `R.style`, a `setTextAppearance`, a `setPadding` or a `background =`
 * in this file fails the build.
 *
 * ## What it composes that C3 does not draw, and why it is here anyway
 *
 * PB-INPUT-2's lease sentence. The requirement's own word is that the state must be VISIBLY
 * confirmed, and its recorded failure mode is precise: the surface showed the same Take control
 * button and the same live keyboard whether the machine had granted a lease or not, so a user
 * could not tell until a keystroke vanished. [PeekPanel] chooses the sentence; this puts it on
 * screen, in [readOnlyNote] -- row 22's component, spent a second time.
 *
 * **THAT REUSE IS A DECISION AND NOT AN ACCIDENT.** Row 22 specifies a centred `Body.Secondary`
 * note in `--p-ink2` under the terminal, which is exactly what this sentence is and exactly where
 * it goes. §2's reuse rule is the whole reason the remaining 24 components are tractable; the
 * alternative was a second component with the same four values, or a bare `TextView` with no
 * appearance at all. What it costs is that a screen reader and a designer both see two notes where
 * the derivation table describes one, so they carry different tags and this comment says so.
 *
 * ## What it deliberately does not compose
 *
 * **The keyboard.** [PeekPanel.keyboardEnabled] is not read here, and the field is not unused: the
 * field and the Send control it governs are inventory C2's COMPOSER (derivation row 9), C2 is
 * unbuilt, and both are `PhoneSurface`'s -- they hold what the user typed and they carry the
 * facade call. A peek panel that enabled views it does not own would be the second statement of a
 * fact the model already makes.
 *
 * **The back control.** IT IS NOT DRAWN AT ALL, and that is this issue's resolution
 * (agents-tracker-joe7) rather than the omission that stood here. What stood here was the chevron
 * drawn "because §4 and the model both state it" with no listener attached, "recorded as unmet
 * rather than wired to something plausible" -- and the record did not help the user: the kit gives
 * that control a 48 dp target, a focus ring and a destination label, so what shipped on the Inbox
 * tab of every paired phone with a session was a fully-drawn control that does nothing, beside a
 * visually identical one on the session detail that works.
 *
 * THERE IS STILL NOWHERE TO SEND IT, which is why the fix is subtraction. This screen is composed
 * UNDER the inbox list rather than pushed over it, so nothing was pushed and nothing can be popped;
 * the label named the Inbox, which is the screen the user is standing on. Inventory C2 -- the
 * screen C3 actually drills down FROM -- now exists, and it is a DIFFERENT screen with its own
 * wired header ([sessionDetailView]), reached by tapping a session row. Wiring this control to
 * scroll somewhere would be the invented navigation the old note already refused; wiring it to the
 * detail would be a "back" that goes forward.
 *
 * [navHeaderDrill] takes a null destination for exactly this, so the header keeps §4's type, its
 * three padding steps and its title, and loses only the promise it could not keep.
 */
object PeekTag {
    /** C3.1 -- the drill-down header, per derivation §4. */
    const val NAV = "peek.nav"

    /** C3.2 `.term` -- the escape-filtered VT snapshot, and only the snapshot. */
    const val WELL = "peek.well"

    /** PB-APP-8: what the screen says when the grid on it is not the current one. */
    const val STALE = "peek.stale"

    /** C3.3 `.ro-note` -- derivation row 22's first line. */
    const val NOTE = "peek.note"

    /** Row 22's standalone tertiary button, supplied by the surface that owns the verb. */
    const val TAKE_CONTROL = "peek.control.take"

    /** PB-INPUT-2's "visibly", in row 22's component. */
    const val LEASE = "peek.lease"

    /** The parts whose ON-SCREEN ORDER is the recorded composition. */
    val COMPOSITION: Set<String> = setOf(NAV, STALE, WELL, NOTE, TAKE_CONTROL, LEASE)
}

/**
 * The terminal peek as a view.
 *
 * @param takeControl row 22's `[Take control]` button. It is a parameter and not a construction
 *  for [PairingSlots]' reason: `PhoneSurface` owns the verb, the operation id the lease is claimed
 *  by, and PB-SEC-12 clause 1's touch filter, and the same control has to survive a redraw. It is
 *  built out of the kit there, as `ctaButton(kind = MORE)`, which is what row 22 asks for.
 * @param below views this slice has NOT recomposed, hosted under the panel.
 */
fun peekPanelView(
    context: Context,
    panel: PeekPanel,
    takeControl: View,
    below: View? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    column.addView(
        // `back = null`: this screen has no destination, so it draws no back control. See the
        // paragraph above -- the affordance was the promise, and it was one this composition could
        // not keep.
        navHeaderDrill(context, back = null, title = panel.title)
            .apply { tag = PeekTag.NAV },
    )
    // THE STALE MARK IS A NOTICE ABOVE THE WELL AND NOT A LINE INSIDE IT (agents-tracker-0qe7).
    // The banner used to be joined to the grid and printed with it, which put a sentence about the
    // view inside the view -- and this well is the one place in the app that renders the machine's
    // output byte for byte, so English in it reads as something the agent typed. It is drawn only
    // when there is something to warn about, which is the call every other notice here makes.
    //
    // IT IS THE KIT'S `notice` NOW (agents-tracker-ksvb.4). It was a bare TextView carrying no
    // appearance, recorded here as rendering "at the theme's default until there is one" -- and
    // the theme's default for a TextView is the PLATFORM's ~14 sp, larger than every body style in
    // this app's ladder, so a warning about a stale grid was set bigger than the grid. `§4 Notice
    // line` specifies the type and the ink; the choosing stays out of this package, which
    // `android/gate/s24_screens_test.go` still fences against `setTextAppearance`.
    if (panel.staleNotice.isNotEmpty()) {
        column.addView(notice(context, panel.staleNotice).apply { tag = PeekTag.STALE })
    }
    // `terminal = true` is where `tokens.json`'s `terminal_peek.fg` pin finally reaches a pixel.
    column.addView(monoWell(context, panel.snapshot, terminal = true).apply { tag = PeekTag.WELL })
    column.addView(readOnlyNote(context, panel.note).apply { tag = PeekTag.NOTE })

    // THE BUTTON SITS DIRECTLY UNDER THE NOTE, which is row 22's own arrangement -- it is the
    // sentence's `[Take control]` promoted out of the prose, not a control that happens to be
    // nearby. It is added only while the model offers it: `TerminalPeek.showsTakeControl` is
    // false once the machine has confirmed the lease, and a screen that composed it anyway and
    // then hid it would be the second, contradictable statement PB-DS-9 fences against.
    if (panel.offersTakeControl) column.addView(takeControl.tagged(PeekTag.TAKE_CONTROL))

    column.addView(readOnlyNote(context, panel.leaseNotice).apply { tag = PeekTag.LEASE })

    below?.let { column.addView(it) }
    return column
}

/**
 * Tag a slot with the part it renders and detach it from whatever last held it.
 *
 * The detach is not tidiness: the panel is rebuilt whenever the snapshot changes, and a slot
 * arriving at its next `addView` still claiming a discarded parent is refused by Android with
 * "the specified child already has a parent". `PairingPanelView` carries the same four lines for
 * the same reason.
 */
private fun View.tagged(tag: String): View = apply {
    this.tag = tag
    (parent as? ViewGroup)?.removeView(this)
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
