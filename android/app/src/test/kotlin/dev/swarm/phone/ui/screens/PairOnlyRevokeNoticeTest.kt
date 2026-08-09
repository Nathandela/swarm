package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.OperationOutcome
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-qlf9's third verb: what an unpaired phone is
 * told about the revoke that unpaired it.
 *
 * **THE ORDERING IS NOT THE DEFECT AND MUST NOT BE "FIXED".** `SettingsSurface.onReplace` purges
 * both key tiers in a `finally`, whether or not the command reached the machine, and that is a
 * recorded decision (ADR-007 B133 decision 3): the situation the panic action exists for is one
 * where the phone may not reach its machine at all, and a purge that ran only on success would
 * leave the live keys on the very handset whose registration its owner has just disowned. The Go
 * side makes it structural as well -- `App.RevokeThisDevice`'s own doc records that a revoke "is
 * the one command whose success DESTROYS the path its own reply would come back on", because the
 * daemon removes the device and rotates the epoch in one transaction and the gateway then severs
 * and exits. Waiting for the outcome before purging would therefore hang forever on the path that
 * WORKED.
 *
 * **SO THE DIVERGENCE IS SURFACED INSTEAD.** What was missing is that nothing told the user which
 * of the two states they are in. A refused revoke leaves this phone locally unpaired while the
 * machine still has the device registered -- and `swarm remote pair` is refused while it is
 * (PB-STATE-10, single-device v1), so the next thing that user does is fail to pair with no
 * explanation anywhere. The sentence belongs on [PairOnlyScreen] because that is the screen the
 * revoke drops them on and the screen the pairing attempt starts from.
 */
@RunWith(RobolectricTestRunner::class)
class PairOnlyRevokeNoticeTest {

    private val id = "op-revoke-1"

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun verdict(code: String, message: String = "") = CommandVerdict.of(
        OperationOutcome(operationId = id, code = code, message = message),
        id,
        CommandVerdict.ACCEPTED_OK,
    )

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }

    private fun View.controls(): List<View> = flatten().filter { it.hasOnClickListeners() }

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    // ---- the sentence ------------------------------------------------------

    @Test
    fun `a revoke the machine confirmed leaves nothing to say`() {
        assertEquals(
            "an unpaired phone is warned about a divergence that did not happen, which teaches " +
                "the user to ignore the warning that matters",
            "",
            PairOnlyScreen.revokeNoticeFor(verdict("ok", "ok")),
        )
    }

    /**
     * THE ORDINARY CASE, and it is the honest one rather than the reassuring one. `signedCommand`
     * seals, appends and returns; the reply lands later, if it lands at all. So at the moment the
     * phone has purged itself the machine's answer is genuinely unknown, and a screen that said
     * nothing would be asserting the revoke worked.
     */
    @Test
    fun `an unanswered revoke says the machine has not confirmed it`() {
        val notice = PairOnlyScreen.revokeNoticeFor(CommandVerdict.UNANSWERED)

        assertTrue(
            "a revoke whose outcome nobody has read is presented as a completed one. This phone " +
                "has destroyed both key tiers and cannot ask again",
            notice.isNotEmpty(),
        )
        assertTrue(
            "the notice does not say the machine has not confirmed the removal, which is the " +
                "whole of what this phone knows",
            notice.contains("not confirmed"),
        )
        assertTrue(
            "the notice names no way out. `swarm remote pair` is refused while the machine still " +
                "has this device registered, so a user who cannot pair has no way to discover why",
            notice.contains("swarm remote revoke"),
        )
    }

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.10 on this verb. The machine's reason
     * moves OUT of the notice and into [PairOnlyScreen.revokeDetailFor], which the screen draws
     * mono and tertiary beneath it.
     *
     * THE SENTENCE IS THE ONE THAT MATTERS HERE MORE THAN ANYWHERE. What the user has to act on is
     * that the machine still holds this device -- `swarm remote pair` is refused while it does --
     * and that fact was competing for the same line as a daemon error string.
     */
    @Test
    fun `a refused revoke says the machine kept the registration, with its words beside it`() {
        val refused = verdict("kill_switch", "remote control is disabled (kill switch off)")
        val notice = PairOnlyScreen.revokeNoticeFor(refused)

        assertFalse(
            "the machine's raw reason sits inside the screen's own sentence, so a wire string " +
                "reads as copy this product wrote about a registration",
            notice.contains("remote control is disabled (kill switch off)"),
        )
        assertEquals(
            "the machine's reason for refusing to remove this device was dropped",
            "remote control is disabled (kill switch off)",
            PairOnlyScreen.revokeDetailFor(refused),
        )
        assertEquals(
            "a confirmed or unanswered revoke prints a detail, which is a diagnostic under a " +
                "sentence that reports no refusal",
            "",
            PairOnlyScreen.revokeDetailFor(verdict("ok", "ok")) +
                PairOnlyScreen.revokeDetailFor(CommandVerdict.UNANSWERED),
        )
        assertTrue(
            "a refused revoke reads exactly like an unconfirmed one, so the user is not told " +
                "that the machine has definitely kept this device",
            notice != PairOnlyScreen.revokeNoticeFor(CommandVerdict.UNANSWERED),
        )
        assertTrue(notice.contains("swarm remote revoke"))
    }

    /**
     * The revoke that never left the handset at all. The routed sentence PB-APP-9 produced is
     * carried verbatim -- it is the one that names a remedy -- and the divergence is stated after
     * it, because the purge in the `finally` ran either way.
     */
    @Test
    fun `a revoke that never reached the wire still reports the divergence`() {
        val routed = "No link to your machine right now."
        val notice = PairOnlyScreen.revokeUnsentNotice(routed)

        assertTrue(
            "the routed failure was re-worded here rather than carried, so two files now decide " +
                "what a transport failure reads as",
            notice.startsWith(routed),
        )
        assertTrue(
            "a revoke that never reached the machine leaves the device registered for certain, " +
                "and the screen does not say so",
            notice.contains("swarm remote revoke"),
        )
    }

    // ---- the screen --------------------------------------------------------

    @Test
    fun `the notice is drawn on the screen the revoke drops the user on`() {
        val notice = PairOnlyScreen.revokeNoticeFor(CommandVerdict.UNANSWERED)
        val root = pairOnlyView(
            context,
            pairing = View(context),
            started = false,
            onStartPairing = {},
            revokedNotice = notice,
        )

        assertEquals(
            "the pair-only screen has nowhere to put what a revoke left behind, so the one " +
                "message that explains why pairing is about to fail is shown on no screen at all",
            notice,
            textOf(root.kitRequire(PairOnlyTag.NOTICE)),
        )
    }

    /**
     * IT SURVIVES THE OFFER BEING TAKEN UP, which is when it matters most: the user is inside the
     * flow that is about to be refused.
     */
    @Test
    fun `the notice stays on screen while the pairing flow is up`() {
        val notice = PairOnlyScreen.revokeNoticeFor(CommandVerdict.UNANSWERED)
        val root = pairOnlyView(
            context,
            pairing = View(context),
            started = true,
            onStartPairing = {},
            revokedNotice = notice,
        )

        assertEquals(notice, textOf(root.kitFind(PairOnlyTag.NOTICE)))
    }

    @Test
    fun `a phone that has revoked nothing is shown no notice and no extra control`() {
        val root = pairOnlyView(context, pairing = View(context), started = false, onStartPairing = {})

        assertNull(
            "a blank notice line is drawn over a phone that has never revoked anything, which is " +
                "a warning nobody wrote",
            root.kitFind(PairOnlyTag.NOTICE),
        )
        assertEquals(
            "the unpaired screen no longer offers exactly one control",
            1,
            root.controls().size,
        )
    }

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.10 as DRAWN: the machine's own words
     * are a second view, in the machine's own register, and there is none where it sent none.
     */
    @Test
    fun `the machine's own reason is drawn under the notice, and only when there is one`() {
        val refused = verdict("kill_switch", "remote control is disabled (kill switch off)")
        val root = pairOnlyView(
            context,
            pairing = View(context),
            started = false,
            onStartPairing = {},
            revokedNotice = PairOnlyScreen.revokeNoticeFor(refused),
            revokedDetail = PairOnlyScreen.revokeDetailFor(refused),
        )

        assertEquals(
            "the machine's reason reaches no view on the one screen that could state it",
            "remote control is disabled (kill switch off)",
            textOf(root.kitRequire(PairOnlyTag.NOTICE_DETAIL)),
        )
        assertNull(
            "a blank mono line is drawn under a revoke the machine never answered, which is a " +
                "cell reserved for a reply that does not exist",
            pairOnlyView(
                context,
                pairing = View(context),
                started = false,
                onStartPairing = {},
                revokedNotice = PairOnlyScreen.revokeNoticeFor(CommandVerdict.UNANSWERED),
            ).kitFind(PairOnlyTag.NOTICE_DETAIL),
        )
    }

    @Test
    fun `the notice is not a control`() {
        val root = pairOnlyView(
            context,
            pairing = View(context),
            started = false,
            onStartPairing = {},
            revokedNotice = PairOnlyScreen.revokeNoticeFor(CommandVerdict.UNANSWERED),
        )

        assertFalse(
            "the divergence notice is pressable, so the only screen an unpaired phone has now " +
                "offers two things to press and one of them does nothing",
            root.kitRequire(PairOnlyTag.NOTICE).hasOnClickListeners(),
        )
        assertEquals(1, root.controls().size)
    }
}
