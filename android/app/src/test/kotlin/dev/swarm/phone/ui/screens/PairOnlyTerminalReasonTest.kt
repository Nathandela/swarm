package dev.swarm.phone.ui.screens

import dev.swarm.phone.keys.ConnectionState
import dev.swarm.phone.ui.ConnectionBanner
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-w6o3: the two terminal states that reach this
 * screen and had nothing to say on it.
 *
 * THE DEFECT IS A DECISION ORDER, not a missing sentence. `PhoneSurface.renderReady` asks
 * [PairOnlyScreen.presentationOf] FIRST and returns before `FacadeBridge.connectionBanner()` is
 * ever read; `mobile/relay.go`'s `transportEndsPairing` folds `repair_required` and a
 * past-grace `revoked` into `paired = false`. So the two most carefully worded banners in
 * `ConnectionUi.kt` -- the ones that name a cause the user cannot otherwise know -- are
 * unreachable in production, and the handset they describe opens on the screen a FRESH INSTALL
 * opens on: "Pair this phone", one CTA, no reason given.
 *
 * WHY THE GENERIC SCREEN IS NOT MERELY VAGUE HERE, and this is PB-APP-10's own clause: on
 * `repair_required` the machine STILL HOLDS this device's registration, and `swarm remote pair`
 * is refused while it does (PB-STATE-10, single-device v1). The one control on screen therefore
 * leads into a pairing that cannot complete -- the forbidden failure LOOP, reached through the
 * remedy, which is the defect [dev.swarm.phone.ui.Remedy.CLEAR_DATA_AND_RE_PAIR]'s own KDoc
 * argues against in as many words.
 *
 * ## What is asserted, and what is deliberately not
 *
 * WHAT IS ASSERTED is the PRESENTATION per reason: that a revoked phone reads the revocation's
 * own sentence rather than the first-run copy, that a repair_required phone is told what has to
 * happen ON THE MACHINE before it presses anything, and that the mapping from the transport's
 * states is the same one `transportEndsPairing` makes -- exactly two states end a pairing, and
 * every other state on this screen is a phone that was never paired or unpaired itself.
 *
 * WHAT IS NOT ASSERTED is that the CTA disappears. Removing it would be the brick in the other
 * direction: nothing on the handset can un-destroy a Keystore key, so `repair_required` never
 * clears on its own, and a screen that withheld the pairing control until it did would withhold
 * it forever -- while the recovery PB-STATE-10 documents ENDS with a pairing on this handset.
 * What the requirement forbids is a BARE control with no statement of the machine-side step, and
 * that is what is checked.
 *
 * These are screen-model tests because the decision is a model's. The wiring half -- that
 * `PhoneSurface` consults the transport before it draws -- is `android/gate/w6o3_terminalpaironly
 * _test.go`, for the reason `d0b8_unpair_test.go` gives: `PhoneRuntime.phone()` answers
 * `Unavailable` under Robolectric, so no JVM test can drive a real phone core to a terminal state.
 */
class PairOnlyTerminalReasonTest {

    /** The two transport states `transportEndsPairing` folds into `paired = false`. */
    private val terminal = listOf(PairOnlyReason.REVOKED, PairOnlyReason.REPAIR_REQUIRED)

    @Test
    fun `a revoked phone is not shown the screen a fresh install opens on`() {
        val firstRun = PairOnlyScreen.copyFor(PairOnlyReason.FIRST_RUN)

        for (reason in terminal) {
            val copy = PairOnlyScreen.copyFor(reason)
            assertNotEquals(
                "a phone in $reason reads the same heading as a fresh install. The handset had " +
                    "a machine until this morning and the app is now empty; a screen saying only " +
                    "\"Pair this phone\" leaves the one fact the user cannot discover -- WHY -- " +
                    "on no screen in the product",
                firstRun.title,
                copy.title,
            )
            assertNotEquals(
                "a phone in $reason reads the first-run sentence, which explains an empty app by " +
                    "the absence of a pairing and says nothing about the pairing that ENDED",
                firstRun.body,
                copy.body,
            )
        }
    }

    @Test
    fun `a revoked phone reads the revocation's own sentence and not a second wording of it`() {
        val copy = PairOnlyScreen.copyFor(PairOnlyReason.REVOKED)
        val banner = ConnectionBanner.of(ConnectionState.REVOKED).text

        // AN IDENTITY AND NOT A PARAPHRASE. `ConnectionUi` argues that wording -- the owner
        // removed this device, and the machine-side registration has to be cleared before a
        // re-pair can succeed -- and two wordings for one pair of facts is two things for a
        // reader to reconcile at the moment they are least able to.
        assertTrue(
            "the revoked screen says \"${copy.body}\", which does not carry the revocation " +
                "sentence \"$banner\". That sentence is the only place this product states that " +
                "the machine still has a registration to clear, and the banner carrying it is " +
                "unreachable on a phone the revoke has unpaired",
            copy.body.startsWith(banner),
        )
    }

    @Test
    fun `a repair required phone is told what the machine must do before it presses anything`() {
        val copy = PairOnlyScreen.copyFor(PairOnlyReason.REPAIR_REQUIRED)

        for (step in listOf("swarm remote devices", "swarm remote revoke")) {
            assertTrue(
                "the repair_required screen says \"${copy.body}\", which never names `$step`. " +
                    "The machine still holds this device's registration and `swarm remote pair` " +
                    "is refused while it does (PB-STATE-10), so a user who presses the only " +
                    "control on this screen walks into a pairing that cannot complete -- the " +
                    "failure loop PB-APP-10 forbids, reached through the remedy",
                copy.body.contains(step),
            )
        }
        assertTrue(
            "the repair_required screen never says that pairing is refused until the machine " +
                "side is cleared, so the order of the two steps is left for the user to discover " +
                "by failing at it",
            copy.body.contains("refused"),
        )
    }

    @Test
    fun `the two terminal reasons do not read as each other`() {
        val revoked = PairOnlyScreen.copyFor(PairOnlyReason.REVOKED)
        val repair = PairOnlyScreen.copyFor(PairOnlyReason.REPAIR_REQUIRED)

        // They share a remedy and NOT a cause, which is the distinction `ConnectionState.REVOKED`
        // was split from `REPAIR_REQUIRED` to keep: the owner removed this device, or this
        // handset's key was destroyed. A user shown one sentence for both is told to go looking
        // for a device registration that a destroyed key did not create.
        assertNotEquals(
            "both terminal states read the same on the pair-only screen, so the phone cannot " +
                "tell its user whether the owner removed it or its own key was destroyed",
            revoked.body,
            repair.body,
        )
        assertNotEquals(revoked.title, repair.title)
    }

    @Test
    fun `no reason leaves this phone without the control that ends the recovery`() {
        for (reason in PairOnlyReason.entries) {
            val copy = PairOnlyScreen.copyFor(reason)
            assertEquals(
                "the pair-only screen offers a different control in $reason. The recovery ENDS " +
                    "with a pairing on this handset -- `swarm remote pair` shows a code and this " +
                    "phone scans it -- and a screen that renamed or withheld that control in a " +
                    "state nothing on the device can clear would withhold it forever",
                PairOnlyScreen.CTA,
                copy.cta,
            )
            assertTrue(
                "the only screen this phone has carries an empty string where a sentence should " +
                    "be, in $reason",
                copy.title.isNotBlank() && copy.body.isNotBlank(),
            )
        }
    }

    @Test
    fun `the screen a fresh install opens on is unchanged`() {
        // The constants are what `PairOnlyViewTest` asserts are drawn, and what the first-run
        // screen says is not this issue's subject: a phone with no machine is told the same thing
        // it was told before.
        assertEquals(
            PairOnlyCopy(PairOnlyScreen.TITLE, PairOnlyScreen.BODY, PairOnlyScreen.CTA),
            PairOnlyScreen.copyFor(PairOnlyReason.FIRST_RUN),
        )
    }

    @Test
    fun `exactly the two transport states that end a pairing name themselves on this screen`() {
        val ends = mapOf(
            ConnectionState.REVOKED to PairOnlyReason.REVOKED,
            ConnectionState.REPAIR_REQUIRED to PairOnlyReason.REPAIR_REQUIRED,
        )

        // TOTAL OVER THE ENUM, so a state added to the transport is a decision made here rather
        // than a screen that silently keeps its first-run copy. The two that map are the two
        // `transportEndsPairing` folds into `paired = false`; RELAY_UNTRUSTED and RELAY_INSECURE
        // are terminal and do NOT end a pairing, so a phone in them stays in the app with the
        // banner that names them -- which is why they are not on this screen at all.
        for (state in ConnectionState.entries) {
            assertEquals(
                "the transport reports $state on a phone that has been sent to the pair-only " +
                    "screen, and the screen answers the wrong reason for it",
                ends[state] ?: PairOnlyReason.FIRST_RUN,
                PairOnlyScreen.reasonFor { state },
            )
        }
    }

    @Test
    fun `a phone that cannot read its transport state gets the screen that promises least`() {
        // The same asymmetry `presentationOf` takes for the same kind of failure: guessing a
        // terminal reason would tell a user their device was removed on the strength of a read
        // that failed, which is a claim about the MACHINE this phone is in no position to make.
        val unreadable = listOf<() -> ConnectionState>(
            { throw IllegalStateException("the phone core is closed") },
            { error("swarmmobile reported an unknown connection state: quantum") },
            { throw RuntimeException("the facade refused") },
        )

        for (reader in unreadable) {
            assertEquals(
                "a phone whose transport state could not be read is told the owner removed it, " +
                    "or that its key was destroyed, on the strength of a read that threw",
                PairOnlyReason.FIRST_RUN,
                PairOnlyScreen.reasonFor(reader),
            )
        }
    }
}
