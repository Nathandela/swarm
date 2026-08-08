package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.approvalSheet
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.scrolledHorizontally

/**
 * ADR-009-structured-chat-interaction (1) and (4): the approval card, composed.
 *
 * IT IS THE SHEET'S FIRST COMPOSITION. `kit/ApprovalSheet.kt` shipped in phase O6 with its well and
 * its actions as SLOTS and nothing to fill them -- the phase was recorded PARTIAL for exactly that
 * ("it is built, tested, and has no production caller ... A protocol decision has to land before
 * item 1 can be closed"). The decision landed: `interaction-schema.md` §3.5 carries the question,
 * the literal and the decisions, and IS-LIFE-4's `ActionApprove` answers one.
 *
 * ONE COMPOSITION FOR BOTH MODES, which is (4) in one line: "The approval fallback is a prompt card,
 * never a grid." A `prompt_card` differs only in where [ApprovalSheetPanel.command] came from -- the
 * sanitized prompt region instead of §7's structured action -- so it takes the same well, the same
 * buttons and the same signed op. A second view for it would be the deep-link-to-peek this ADR
 * deletes, wearing a card's clothes.
 *
 * THE WELL IS `monoWell` AND THAT REUSE IS THE RULE RATHER THAN A CONVENIENCE: "every mono block in
 * the app is ONE component" (derivation row 18). It is the DEFAULT ink and not `terminal = true`:
 * the champagne foreground is the grid's, the grid is deleted at this slice's exit, and an accent
 * behind a row of decisions is a second thing on the sheet asking to be looked at first -- ADR-009's
 * visual direction spends `--p-hero` on one meaning at a time.
 */
object SheetTag {

    /** The sheet itself: D4.4's heaviest material, reserved for the moment of decision. */
    const val SHEET = "approval.sheet"

    /** The literal the decision is about -- §7's action line, or IS-APR-3's prompt region. */
    const val WELL = "approval.well"

    /** One offered decision, labelled by `decisions[].label`. */
    const val ACTION = "approval.action"
}

/**
 * The approval card as a view.
 *
 * @param actionFor how one decision becomes a control. It is a SLOT FACTORY and not a fixed list of
 *  slots, which is `SettingsPanelView`'s arrangement and is forced here by the data: `decisions[]`
 *  is 1..8 entries of the CLI's own choosing (§5's `MaxDecisions`), so a surface cannot pre-build
 *  them. The default builds the kit's own button, which is what makes the composition complete and
 *  assertable; a surface that owns the verb passes wired ones instead, because `App.Approve` needs
 *  the operation id and PB-SEC-12 clause 1's touch filter, and both are `PhoneSurface`'s (PB-DS-6).
 *
 *  EVERY DECISION IS `CtaKind.MORE`, and that is IS-APR-4 rather than a taste. `.a2-ok` and `.a2-no`
 *  ARE a polarity claim, and the phone has no field to make one from: the verdict is machine-side,
 *  the wire carries `{id, label}`, and painting a label green would assert a grant the daemon never
 *  told this side about. `.a2-more` is the one variant that asserts nothing, which is also what the
 *  kit's equal-width rule already decided for the same reason.
 */
fun approvalSheetView(
    context: Context,
    panel: ApprovalSheetPanel,
    actionFor: (ApprovalDecision) -> View = { decision ->
        ctaButton(context, decision.label, CtaKind.MORE)
    },
): View = approvalSheet(
    context = context,
    contextLine = panel.contextLine,
    question = panel.question,
    // ABSENT IS NOT EMPTY: an approval whose action names no literal draws no well, rather than a
    // recessed box saying nothing in the shape of a command that is blank.
    //
    // `.scrolledHorizontally()` (agents-tracker-ksvb.7): a long shell command is exactly the
    // line `setHorizontallyScrolling` refuses to wrap, and this sheet is asking the reader to
    // approve it -- reachable, not clipped, is the floor for a command someone is signing off on.
    well = if (panel.hasCommand) {
        monoWell(context, panel.command).apply { tag = SheetTag.WELL }.scrolledHorizontally()
    } else {
        null
    },
    actions = panel.actions.map { decision -> actionFor(decision).tagged(SheetTag.ACTION) },
).apply { tag = SheetTag.SHEET }

/**
 * Tag a control with the part it renders and detach it from whatever last held it.
 *
 * The detach is not tidiness: a surface that owns the verb hands the same button back on every
 * redraw, and one arriving at its next `addView` still claiming a discarded parent is refused by
 * Android with "the specified child already has a parent". `SessionDetailView` carries the same four
 * lines for the same reason.
 */
private fun View.tagged(tag: String): View = apply {
    this.tag = tag
    (parent as? ViewGroup)?.removeView(this)
}
