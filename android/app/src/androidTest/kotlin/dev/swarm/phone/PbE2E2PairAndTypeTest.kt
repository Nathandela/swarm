package dev.swarm.phone

import android.util.Log
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import dev.swarm.phone.PhoneScreenDriver.awaitPressable
import dev.swarm.phone.PhoneScreenDriver.awaitScreen
import dev.swarm.phone.PhoneScreenDriver.press
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
            app.awaitScreen(
                "Scan the code your machine is showing.",
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
            app.awaitScreen(
                "Compare the six symbols with the ones on your machine.",
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
            app.awaitPressable(
                "Take control",
                "the paired phone never rendered a session, so it observed nothing. The roster " +
                    "is drawn from the journal stream, and an empty one after pairing means the " +
                    "phone is not draining the machine's mailbox -- App.Start is what dials it, " +
                    "and PhoneSurface is what calls Start.",
            )

            // ---- takes control -----------------------------------------------------------
            app.press("Take control")

            // ---- types -------------------------------------------------------------------
            app.type(SESSION_PROMPT, "echo swarm-pb-e2e-2")
            app.press("Send line")

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
