package dev.swarm.phone

import android.util.Log
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import dev.swarm.phone.PhoneScreenDriver.awaitPressable
import dev.swarm.phone.PhoneScreenDriver.awaitScreen
import dev.swarm.phone.PhoneScreenDriver.press
import dev.swarm.phone.PhoneScreenDriver.selectSession
import dev.swarm.phone.PhoneScreenDriver.textOnScreen
import dev.swarm.phone.PhoneScreenDriver.type
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * PB-E2E-2's five in-app actions, performed through the installed APK's own controls:
 *
 *	"APK installs, pairs against a local relay + daemon, SAS matches, observes, takes control,
 *	 types."
 *
 * The sixth clause -- one real `adb shell am force-stop` mid-session -- is the runbook's, because
 * only adb can issue it; [PbE2E2ResumeTest] is what runs after it.
 *
 * IT IS DRIVEN BY scripts/pbe2e2-emulator-smoke.sh AND NOT BY `./gradlew test`. It needs a relay,
 * a daemon and a minted pairing QR, all of which the runbook stands up on the host; run on its
 * own it fails at the first missing instrumentation argument, which is the honest outcome.
 *
 * WHAT IT DOES NOT CLAIM. PB-E2E-5 stays deferred: the QR arrives through PB-PAIR-2's
 * manual-entry path rather than through the camera, so nothing here is evidence that a physical
 * camera decodes anything, and an emulator is not a handset. See PhoneScreenDriver.
 *
 * THIS TEST CANNOT PASS ON AN EMULATOR, AND NEITHER CAN ANY OTHER TEST IN THIS SOURCE SET
 * (ADR-007 B56). Read this before concluding it is broken.
 *
 * PB-KEY-8 refuses a Keystore KEK the platform reports as software-backed, and every Android
 * emulator image reports exactly that -- measured: the image ships only the AOSP software
 * keymint service, no StrongBox instance exists, no image variant in the sdkmanager catalogue
 * changes the keystore backend, and no emulator feature flag enables a TEE. So
 * PhoneRuntime.attach() throws KeystoreDowngrade, every screen is downstream of it, and this
 * test's first assertion reports the symptom ("the app did not open on the pairing step")
 * rather than the cause.
 *
 * The refusal is PB-KEY-8 working as specified and must NOT be relaxed to make this run: that
 * weakens the control PB-SEC-1's at-rest claim rests on so a demonstration can pass. This
 * whole tier -- this class, PbE2E2ResumeTest, and any future connectedAndroidTest that
 * constructs the runtime -- is coverage that can only execute on a physical handset, which is
 * PB-E2E-5's deferred gate. scripts/pbe2e2-emulator-smoke.sh carries the full measurement.
 */
@RunWith(AndroidJUnit4::class)
class PbE2E2PairAndTypeTest {

    @Test
    fun the_app_pairs_compares_the_sas_observes_takes_control_and_types() {
        val arguments = InstrumentationRegistry.getArguments()
        val qr = PhoneScreenDriver.require(
            arguments, "swarmQr",
            "The runbook mints it with `swarm remote pair` on the host and passes the payload " +
                "through; without it there is no machine to pair with.",
        )
        val relay = PhoneScreenDriver.require(
            arguments, "swarmRelay",
            "The destination the confirm sheet must display -- the emulator's route to the " +
                "host, ws://10.0.2.2:8787. PB-PAIR-6 is the clause being demonstrated: this " +
                "test fails if the app joins something it did not first show.",
        )

        ActivityScenario.launch(PhoneActivity::class.java).use { app ->
            // ---- pairs -------------------------------------------------------------------
            // agents-tracker-ksvb.6: `PairingFlow.messageFor(SCAN)` -- "Scan the code your
            // machine is showing." -- was deleted for duplicating the numbered guidance's step
            // 2 below it. That guidance is now the SCAN step's own waypoint.
            app.awaitScreen(
                "It shows a QR code. Scan it below.",
                "the app did not open on the pairing step, so there is nothing to pair with",
            )
            app.type("Paste the pairing code your machine printed", qr)
            app.press("Use this code")

            // ---- PB-PAIR-6: the destination is DISPLAYED before anything is joined --------
            app.awaitScreen(
                "Check the destination below before this phone joins anything.",
                "the app did not stop to show the destination. BeginPairing decodes and stops " +
                    "precisely so that it can; a screen that went straight to the handshake " +
                    "would have disclosed this handset's connection to whatever the QR named",
            )
            assertTrue(
                "PB-PAIR-6: the confirm step does not display the destination it is about to " +
                    "join. Expected $relay on screen, which said:\n${app.textOnScreen()}",
                app.textOnScreen().contains(relay),
            )
            app.press("Join this destination")

            // ---- SAS matches -------------------------------------------------------------
            // agents-tracker-ksvb.6: `PairingFlow.messageFor(COMPARING_CODES)` -- "Compare the
            // six symbols with the ones on your machine." -- was deleted for duplicating
            // `SasStep.instruction`, rendered beside the symbols. That instruction is now the
            // waypoint.
            app.awaitScreen(
                "Check these six against the ones on your machine's screen.",
                "the handshake never reached the SAS gate, so \"SAS matches\" has no subject",
            )
            val symbols = sasOnScreen(app.textOnScreen())
            assertEquals(
                "PB-SAS-1: the phone displayed ${symbols.size} symbol(s), not six. The " +
                    "comparison is with the machine's own display and both ends derive it from " +
                    "the Noise channel binding, so a different count is not a formatting " +
                    "difference -- it is not the same code.",
                6, symbols.size,
            )
            // The six the PHONE derived, in the run log beside the six the machine printed, so
            // the comparison this clause is named for is auditable after the fact rather than
            // taken on trust. Nothing secret: the SAS is a public function of a binding both
            // ends already hold, and it is dead the moment the handshake finishes.
            Log.i("SwarmE2E2", "phone SAS: " + symbols.joinToString(" "))
            app.press("They match")

            app.awaitScreen(
                "This phone is paired with your machine.",
                "the pairing did not complete after both ends confirmed the SAS",
            )

            // ---- observes ----------------------------------------------------------------
            // AND OPENS THE SESSION, which is the step this test never took (agents-tracker-
            // nx44.6). It pressed `Take control` straight from the inbox, and that control has
            // been on the drill-down since 4b4cde0 -- so this run has been unrunnable as written
            // since 2026-08-01, failing on a missing label rather than on anything it asserts.
            app.selectSession(
                "the paired phone never rendered a session row, so it observed nothing. The " +
                    "roster is drawn from the journal stream, and an empty one after pairing " +
                    "means the phone is not draining the machine's mailbox -- App.Start is what " +
                    "dials it, and PhoneSurface is what calls Start.",
            )

            // ---- takes control -----------------------------------------------------------
            app.awaitPressable(
                "Take control",
                "the session detail opened without the control that acquires a lease. It is " +
                    "drawn while SessionLease.showsTakeControl is true and raised by " +
                    "setActionsEnabled once the roster has a row, so an absent one is a " +
                    "drill-down that did not arrive.",
            )
            app.press("Take control")

            // PB-INPUT-2: the keyboard is shut until the MACHINE confirms the lease, so this is
            // not a courtesy wait -- it is the requirement, asserted. The take_control reply is
            // what opens it, it arrives on a Go goroutine after this press returns, and the
            // screen redraws on the outcome event that carries it.
            app.awaitDescribedPressable(
                "Send",
                "the machine never confirmed the control lease, so the keyboard stayed shut. " +
                    "PB-INPUT-2 refuses every keystroke until the confirmation lands, and the " +
                    "confirmation is the take_control operation's own outcome (PB-SYNC-2 claims " +
                    "it by operation id) -- an unconfirmed lease here is a take_control the " +
                    "daemon refused, or a reply the phone is not draining",
            )

            // ---- types -------------------------------------------------------------------
            app.type(SESSION_PROMPT, "echo swarm-pb-e2e-2")
            app.pressDescribed("Send")

            // A refusal would be on screen: every gated control routes its failure through
            // PB-APP-9's table rather than swallowing it, so an empty outcome line is the
            // assertion that the keystroke was accepted.
            val after = app.textOnScreen()
            for (refusal in ROUTED_REFUSALS) {
                assertTrue(
                    "PB-INPUT-3: the typed line was refused. The screen said:\n$after",
                    !after.contains(refusal),
                )
            }
        }
    }

    /**
     * The six symbols as the screen laid them out. [dev.swarm.phone.ui.SasStep] splits the
     * core's display string on whitespace and the surface joins it with spaces, so this reverses
     * exactly that and nothing else -- it does not re-implement the alphabet, which lives in the
     * shared Go core (internal/remote/crypto/sas.go) and exists in one place on purpose.
     */
    private fun sasOnScreen(screen: String): List<String> {
        val instruction = "Check these six against the ones on your machine's screen."
        val line = screen.lines()
            .map { it.trim() }
            .filter { it.isNotEmpty() && it != instruction }
            .lastOrNull { it.split(Regex("\\s+")).size == 6 }
            ?: return emptyList()
        return line.split(Regex("\\s+"))
    }

    private companion object {
        /** The keyboard's hint, and the marker that a session is on screen at all. */
        const val SESSION_PROMPT = "Type into the session you hold"

        /**
         * The routed messages that mean the machine refused this phone's input. Matched as text
         * because that is what the user sees; ErrorRouter owns the wording.
         */
        val ROUTED_REFUSALS = listOf(
            "has not given this phone control",
            "No link to your machine right now",
            "This phone is not paired with a machine yet",
        )
    }
}
