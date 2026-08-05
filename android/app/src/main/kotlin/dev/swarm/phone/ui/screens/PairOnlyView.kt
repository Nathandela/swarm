package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.navHeader

/**
 * Phase B -- agents-tracker-64rf: the screen an unpaired phone opens on, as drawn.
 *
 * ONE OFFER TO PAIR, ON THE GROUND, AND NOTHING ELSE. No tab bar, no triage inbox, no empty
 * sections announcing that nothing is waiting on a user who has no machine to be waited on by.
 * What makes an offer unmissable is not that it is drawn large or first -- it is that there is
 * nothing else to look at, which is the requirement the shipped screen failed while composing the
 * pairing block correctly the whole time.
 *
 * THE FLOW REPLACES THE OFFER RATHER THAN STACKING ABOVE IT. `PairingSurface` composes its own
 * heading and its own controls per step -- the SCAN step already offers "Scan the code your
 * machine is showing." -- so a hero CTA left on screen beside it would be a second way in to the
 * same step, one of which the pairing surface does not own. Two entry points to a security flow is
 * one more than the flow was argued with.
 *
 * IT DECIDES NOTHING ABOUT HOW ANY OF THIS LOOKS, including the ground: `--p-bg` is the theme's
 * `android:colorBackground` and reaches this screen as the window's own background, and
 * `android/gate/s24_screens_test.go` fences this package against `background =` so a screen
 * painting its own ground fails the build.
 *
 * **THE CTA IS NOT A GATED ACTION (PB-SEC-12 clause 1), AND THAT IS ARGUED RATHER THAN
 * FORGOTTEN.** The touch filter is for the destructive and the authorising taps -- take control,
 * kill, revoke, and inside the flow the destination confirmation and the SAS answer, which
 * `PairingSurface.touchFilteredActions` carries. Opening the flow authorises nothing: an overlay
 * that stole this tap would have shown the user the pairing screen, which is the screen they were
 * being offered anyway.
 */
object PairOnlyTag {
    /** The offer's heading, on the `.pnav .big` cell itself -- see [pairOnlyView]. */
    const val TITLE = "pairOnly.title"

    /** The sentence saying why the rest of the app is not here. */
    const val BODY = "pairOnly.body"

    /** The one control on the screen. */
    const val CTA = "pairOnly.cta"

    /** Where the pairing flow is hosted once the offer has been taken up. */
    const val PAIRING = "pairOnly.pairing"

    /**
     * What a revoke left behind, when it left anything (agents-tracker-qlf9).
     *
     * IT IS ABOVE EVERYTHING ELSE ON THE SCREEN, which is the rule `sessionDetailView` already
     * follows: a notice goes above what it qualifies, and what this one qualifies is the pairing
     * the user is about to attempt -- one that the machine may refuse for a reason no other screen
     * in this product can state.
     */
    const val NOTICE = "pairOnly.notice"
}

/**
 * The unpaired phone's screen as a view.
 *
 * @param pairing the pairing flow -- `PairingSurface.root`. It is a long-lived view the surface
 *  owns and redraws, so it is passed in rather than built: this screen decides WHERE it goes and
 *  nothing about what is in it.
 * @param started whether the offer has been taken up. The two states are exclusive on purpose;
 *  see the class comment.
 * @param onStartPairing what the one control does.
 * @param notice what a revoke left this phone unable to confirm, or empty where there is nothing
 *  to say. It is drawn in BOTH states and above both, because the state it warns about is the one
 *  the pairing flow is walking into -- see [PairOnlyTag.NOTICE]. It is LAST and defaulted so the
 *  existing call sites are unaffected, which is the shape `phoneScaffoldView`'s banner took.
 */
fun pairOnlyView(
    context: Context,
    pairing: View,
    started: Boolean,
    onStartPairing: () -> Unit,
    notice: String = "",
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    // DRAWN ONLY WHEN IT HAS SOMETHING TO SAY, which is `sessionDetailView`'s rule for its own
    // three notices: a blank warning line over a phone that has revoked nothing is a warning
    // nobody wrote.
    if (notice.isNotEmpty()) {
        column.addView(
            TextView(context).apply {
                tag = PairOnlyTag.NOTICE
                text = notice
                layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            },
        )
    }

    if (started) {
        column.addView(
            FrameLayout(context).apply {
                tag = PairOnlyTag.PAIRING
                layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
                // DETACHED ON THE WAY IN, for `PairingPanelView.tagged`'s reason one level out:
                // the flow outlives every draw of this screen, and a view arriving at its next
                // `addView` still claiming a discarded parent is refused by Android with "the
                // specified child already has a parent".
                (pairing.parent as? ViewGroup)?.removeView(pairing)
                addView(pairing)
            },
        )
    } else {
        column.addView(
            navHeader(context, PairOnlyScreen.TITLE, null).apply {
                // THE TAG GOES ON THE TITLE CELL AND NOT ON THE HEADER ROW, which is the one place
                // this screen differs from every other one's `navHeader`. The others name the row
                // because they assert its position; here the tag names the SENTENCE, and a tag on
                // the row would answer with a `LinearLayout` and no text at all.
                findViewWithTag<View>(KitTag.TITLE).tag = PairOnlyTag.TITLE
            },
        )
        // `emptyState` IS THE SENTENCE COMPONENT HERE, which is the reuse `PhoneSurface` already
        // makes for `MachinesPanelScreen.UNAVAILABLE_COPY`: row 8's block is body copy centred with
        // generous air, and what it says on this screen is what it says on that one -- there is
        // nothing here, and here is why. The alternative was a bare `TextView` with no appearance
        // at all, which is what `LinkPanelView`'s clock line settles for because it sits inside a
        // populated screen; this sentence is half of the only screen this phone has.
        column.addView(emptyState(context, PairOnlyScreen.BODY).apply { tag = PairOnlyTag.BODY })
        column.addView(
            ctaButton(context, PairOnlyScreen.CTA, CtaKind.APPROVE).apply {
                tag = PairOnlyTag.CTA
                setOnClickListener { onStartPairing() }
                // A `TextView` ANNOUNCES ITSELF AS TEXT. The kit records the gap and cannot close
                // it -- it has no click to hang the role on -- so the role is set where the click
                // is, which is `PhoneSurface.ctaAction`'s arrangement for the controls it owns.
                setAccessibilityDelegate(
                    object : View.AccessibilityDelegate() {
                        override fun onInitializeAccessibilityNodeInfo(
                            host: View,
                            info: AccessibilityNodeInfo,
                        ) {
                            super.onInitializeAccessibilityNodeInfo(host, info)
                            info.className = Button::class.java.name
                        }
                    },
                )
            },
        )
    }

    // THE SCROLL IS THIS SCREEN'S BECAUSE THE SCAFFOLD'S IS NOT HERE. Every other destination is
    // hosted inside `phoneScaffoldView`'s, and this screen replaces the scaffold outright -- so
    // without one the flow it hosts would be cut off at the fold on a short handset, and the two
    // controls it would lose are PB-SAS-3's answer buttons, the only human-in-the-loop security
    // step in the product (ADR-007 B133).
    return ScrollView(context).apply {
        // `scrollbar-width: none`, derivation row 20, spelled as the scaffold spells it.
        isVerticalScrollBarEnabled = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
        addView(column)
    }
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
