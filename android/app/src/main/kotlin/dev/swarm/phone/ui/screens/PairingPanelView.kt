package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.PairingScaffoldLayout
import dev.swarm.phone.ui.kit.ctaStack
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.pairingStep
import dev.swarm.phone.ui.kit.readOnlyNote
import dev.swarm.phone.ui.kit.scrolledHorizontally
import dev.swarm.phone.ui.kit.sasSequence

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
 * The remaining pieces are either kit factories or long-lived surface-owned slots. Row 7's SAS
 * display is now [sasSequence], while row 18's mono well, row 10's three CTA variants, row 9's text
 * field and the body/waiting copy arrive already built from [PairingSlots].
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
 * `PairingSurface` builds the interactive slots because it must: `SecureWindow.gate` applies
 * PB-SEC-12 clause 1's touch filter at construction, the listeners belong to the surface's own
 * verbs, and the same control has to survive a redraw or `touchFilteredActions` stops naming the
 * views that are actually on screen. The non-interactive SAS sequence is built here from panel
 * data so its geometry and accessibility stay in the kit and no security verb moves with it.
 */
object PairingTag {
    /** C7's step title, in `Display.NavTitle` per derivation row 18. */
    const val NAV = "pairing.nav"

    /** The step's sentence -- `PairingFlow.messageFor`, in row 18's body slot. */
    const val BODY = "pairing.body"

    /** The interrupted-attempt line or PB-PAIR-6's destination notice. */
    const val NOTICE = "pairing.notice"

    /** The numbered steps saying how to get a code at all. */
    const val STEPS = "pairing.steps"

    /** Why the scanner is not on screen. Only a permanent denial has one; see [PairingPanel]. */
    const val CAMERA_NOTICE = "pairing.camera.notice"

    /** Why a first pairing is being asked for a relay address. */
    const val RELAY_NOTICE = "pairing.relay.notice"

    /** PB-PAIR-6: the destination, in the design's code face. */
    const val DESTINATION = "pairing.destination"

    /** PB-SAS-3's symbols. */
    const val SAS = "pairing.sas"

    const val SAS_INSTRUCTION = "pairing.sas.instruction"

    /** The camera preview, which has no design source at all -- see [PairingSlots.scanner]. */
    const val SCANNER = "pairing.scanner"

    /** What the camera has looked at, under the viewfinder it is about. */
    const val SCAN_PROGRESS = "pairing.scan.progress"

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
    val sasInstruction: View,
    val scanner: View,
    /**
     * The frame counter under the preview. It is a SLOT and not a string on the panel because
     * its text changes several times a second while the panel must not: see
     * [PairingPanel.showsScanProgress].
     */
    val scanProgress: View,
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
    // NO `screenColumn` HERE, AND ROW 18'S PADDING IS SPENT NO LESS FOR IT (agents-tracker-2pnu
    // F2). `PhoneSurface.drawPairOnly` is the only production caller of this flow and it hosts
    // the panel inside `pairOnlyView`'s own `screenColumn`, so a second column here spent the
    // row's cell twice: a started pairing rendered at 48 dp sides and 20 dp top against the row's
    // 24 and 10. The row is spent by the screen that HOSTS the flow, once.
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    // Twelve of the fifteen steps have no recorded heading and get none; see [PairingPanel].
    panel.title?.let { title ->
        column.addView(navHeader(context, title, null).apply { tag = PairingTag.NAV })
    }

    if (panel.body.isNotEmpty() || !panel.showsScanProgress) {
        column.addView(PairingScaffoldLayout.body(slots.body).tagged(PairingTag.BODY))
    }
    if (panel.notice.isNotEmpty()) column.addView(slots.notice.tagged(PairingTag.NOTICE))
    // The long-lived camera becomes frame 02's hero only while it is genuinely live. Before the
    // user presses Scan it remains GONE and the existing instruction-first composition is kept.
    if (panel.showsScanProgress) {
        column.addView(slots.scanner.tagged(PairingTag.SCANNER))
        column.addView(slots.scanProgress.tagged(PairingTag.SCAN_PROGRESS))
    }
    // In frame 02's live state the viewfinder has already replaced the scan action. Keep the one
    // alternate route directly under that field, before the setup guidance, as Signal Field draws
    // it. Once revealed, the actual form remains in the normal ordered control stack below.
    val earlyControls = if (
        panel.showsScanProgress && PairingControl.REVEAL_TYPED_PAYLOAD in panel.controls
    ) {
        setOf(PairingControl.REVEAL_TYPED_PAYLOAD)
    } else {
        emptySet()
    }
    if (earlyControls.isNotEmpty()) {
        column.addView(
            ctaStack(context).apply {
                addView(
                    requireNotNull(slots.controls[PairingControl.REVEAL_TYPED_PAYLOAD]) {
                        "Signal Field's live scanner has no manual-entry control to compose."
                    }.tagged(PairingTag.control(PairingControl.REVEAL_TYPED_PAYLOAD)),
                )
            },
        )
    }
    if (panel.steps.isNotEmpty()) column.addView(guidance(context, panel.steps))
    // `emptyState` IS THE SENTENCE COMPONENT HERE, which is the reuse `PairOnlyView` already makes
    // and for its stated reason: row 8's block is body copy centred with generous air, and what it
    // says on this screen is what it says on that one -- there is nothing here, and here is why.
    // The thing that is not here is the scanner.
    if (panel.cameraNotice.isNotEmpty()) {
        column.addView(
            emptyState(context, panel.cameraNotice).apply { tag = PairingTag.CAMERA_NOTICE },
        )
    }
    // ALWAYS IN THE TREE, VISIBILITY THE SURFACE'S. The preview is created on the first scan --
    // CameraX allocates a provider the moment it exists and this screen is built on every launch
    // -- so whether it is showing is a fact about the camera rather than about the step, and the
    // surface is what holds it.
    if (!panel.showsScanProgress) column.addView(slots.scanner.tagged(PairingTag.SCANNER))
    // DIRECTLY UNDER THE PREVIEW, because that is what makes it a caption rather than a number.
    // "N frames analysed, no code found yet" beside the thing the user is aiming reads as "this
    // is looking and not finding"; anywhere else on the screen it reads as an error.
    if (panel.destination.isNotEmpty()) {
        column.addView(slots.destination.tagged(PairingTag.DESTINATION))
    }
    if (panel.sas.isNotEmpty()) {
        column.addView(sasSequence(context, panel.sas).tagged(PairingTag.SAS))
        column.addView(slots.sasInstruction.tagged(PairingTag.SAS_INSTRUCTION))
    }

    // THE ORDER IS THE ENUM'S, not the set's. `PairingPanel.controls` is a Set and a set has no
    // order, so composing it directly would put the controls on screen in whatever order the
    // implementation happened to insert them -- and "They match" / "They do not match" swapping
    // places between draws is the one pair in this product where a mis-tap is a security event.
    //
    // agents-tracker-nx44.1: `ctaStack` AND NOT `column`, so the relay sentence, the field and
    // the controls carry `.acts2`'s own `space_8` gap between them rather than the zero addView
    // gave them -- which on `CtaKind.APPROVE`'s bloom variant was worse than zero, since the
    // button's own negative margin pulled a bare neighbour closer than its visible edge.
    //
    // ONE STACK PER RUN OF CONSECUTIVE CONTROLS (agents-tracker-2pnu F1). `KitStack` writes
    // `.acts2`'s gap onto every child it adopts, and the relay sentence is not an action: row 22
    // gives it a `space_10` top margin of its own, which a stack that adopted it overwrote with
    // 8. So the note lands on the COLUMN, between two stacks, and the row's cell survives at the
    // one site that renders it.
    var controls: LinearLayout? = null
    fun controlStack(): LinearLayout = controls ?: ctaStack(context).also {
        controls = it
        column.addView(it)
    }

    PairingControl.entries.filter { it in panel.controls && it !in earlyControls }.forEach { control ->
        // THE RELAY SENTENCE IS COMPOSED WITH ITS FIELD AND NOT ABOVE THE STACK. Every other
        // block on this screen belongs to the STEP; this one belongs to one box, and a sentence
        // about a relay address drawn above `Scan QR code` is a sentence about nothing the reader
        // can see yet. So the control loop is where it lands, immediately before the field.
        //
        // `readOnlyNote` IS BORROWED FOR ITS FORM AND THE DIFFERENCE IS STATED. Row 22 derives it
        // as the note under a block a user cannot type into, and this one labels a field they
        // must; what carries over is the shape the row actually specifies -- a small centred
        // `Body.Secondary` line bound to the block it is about, with the row's own margins. The
        // alternative in the kit is `emptyState`, which this file already spends on the camera
        // notice, and its 48 dp of vertical air is written for a section that holds nothing
        // rather than for a caption on a text field.
        //
        // AND THE FIELD SITS DIRECTLY UNDER IT, which is row 22's own arrangement: the row states
        // a top margin and no bottom one, because "the trailing air belongs to whatever the
        // screen puts next" -- and what this screen puts next is the box the sentence is about.
        if (control == PairingControl.RELAY_URL && panel.relayNotice.isNotEmpty()) {
            column.addView(
                readOnlyNote(context, panel.relayNotice).apply { tag = PairingTag.RELAY_NOTICE },
            )
            controls = null
        }
        controlStack().addView(
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
 * The numbered steps saying how to get a pairing code, and the command well under the one that
 * names a command.
 *
 * IT IS BUILT HERE AND NOT TAKEN FROM [PairingSlots], which is the opposite of every other block on
 * this screen and is the point. The slots exist because `PairingSurface` MUST own those views --
 * `SecureWindow.gate` applies PB-SEC-12 clause 1's touch filter at construction, the listeners are
 * its own verbs, and `touchFilteredActions` has to name the views actually on screen. None of that
 * is true of a step: it carries no click, authorises nothing, and holds no state across a redraw.
 * What it needs is a fill, a type and an ink, and the kit is where a screen gets those -- so this
 * is composed from `pairingStep` and `monoWell` rather than passed in.
 *
 * THE WELL IS THE KIT'S `.cmd` WELL AND NOT A SECOND ONE, which is row 18's own instruction:
 * "Command line reuses the `.cmd` mono well verbatim ... so every mono block in the app is one
 * component". It is the same component the destination confirmation spends one step later.
 */
private fun guidance(context: Context, steps: List<PairingGuidance>): View =
    LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        tag = PairingTag.STEPS
        steps.forEach { step ->
            addView(
                pairingStep(
                    context = context,
                    ordinal = step.ordinal,
                    line = step.line,
                    // Empty is not a placeholder: a step with no command gets no well, rather than
                    // a well with nothing in it under a sentence that never mentioned one.
                    //
                    // `.scrolledHorizontally()` (agents-tracker-ksvb.7): a relay pairing command
                    // can run past the step's own width, and this is what makes the rest of it
                    // reachable instead of clipped.
                    detail = step.command.takeIf { it.isNotEmpty() }
                        ?.let { monoWell(context, it).scrolledHorizontally() },
                ),
            )
        }
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
