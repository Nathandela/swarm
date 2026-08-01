package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.PairingAttempt
import dev.swarm.phone.ui.PairingFlow
import dev.swarm.phone.ui.PairingStep
import dev.swarm.phone.ui.SasStep
import dev.swarm.phone.ui.ScannerState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-9 over the PAIRING screen's model.
 *
 * WHAT IS ACTUALLY BEING TESTED HERE THAT WAS NOT TESTED BEFORE. `PairingFlow` is well covered and
 * this suite does not re-cover it. What had no test at all is WHICH CONTROL IS ON SCREEN IN WHICH
 * STEP: that lived in three `render*Step` functions setting `View.visibility`, and the shape of
 * that code -- `show(a, x); show(b, x); show(c, y)` repeated eight times -- is one where a
 * transposed condition is invisible in review and invisible on screen until the step it governs
 * is reached. The consequence is specific and it is a security one: the two SAS answers and the
 * confirm-destination control are the two places a human is the only check, and a screen that
 * offered them in the wrong step would be offering them over the wrong facts.
 *
 * THE COPY IS ASSERTED AS AN IDENTITY, NOT AS A STRING. Where the panel carries a sentence
 * `PairingFlow` or `PairingAttempt` already owns, the expectation below is that same call rather
 * than a transcription of its output -- so a suite that agreed with a re-worded copy of a
 * security sentence cannot exist.
 */
class PairingPanelScreenTest {

    private fun attempt(
        step: PairingStep,
        origin: String = "",
        localNetwork: Boolean = false,
        interrupted: Boolean = false,
    ) = PairingAttempt(
        step = step,
        originShown = origin,
        originIsLocalNetwork = localNetwork,
        explainsInterruptedAttempt = interrupted,
    )

    private fun panel(
        step: PairingStep,
        holding: Boolean,
        scanner: ScannerState = ScannerState.SCANNING,
        sas: SasStep? = null,
        origin: String = "",
        localNetwork: Boolean = false,
        interrupted: Boolean = false,
        machine: String = "",
    ) = PairingPanelScreen.of(
        attempt = attempt(step, origin, localNetwork, interrupted),
        scanner = scanner,
        sas = sas,
        holding = holding,
        machine = machine,
    )

    // ---- the heading -------------------------------------------------------

    @Test
    fun `the three steps the artifact draws carry the artifact's own headings`() {
        assertEquals("Pair a computer", PairingPanelScreen.titleFor(PairingStep.SCAN))
        assertEquals("Check both screens", PairingPanelScreen.titleFor(PairingStep.COMPARING_CODES))
        assertEquals(
            "Paired with nathans-mbp",
            PairingPanelScreen.titleFor(PairingStep.PAIRED, machine = "nathans-mbp"),
        )
    }

    @Test
    fun `a completed pairing whose machine is unreadable is not titled with a dangling with`() {
        assertEquals("Paired", PairingPanelScreen.titleFor(PairingStep.PAIRED, machine = ""))
    }

    @Test
    fun `a step the artifact never drew carries no invented heading`() {
        // Twelve of fifteen. Each already has a full sentence from PairingFlow that PB-PAIR-5
        // argued line by line; a heading here would be a second, shorter statement of the same
        // refusal, placed above the reviewed one.
        PairingStep.entries
            .filter {
                it != PairingStep.SCAN &&
                    it != PairingStep.COMPARING_CODES &&
                    it != PairingStep.PAIRED
            }
            .forEach { step ->
                assertNull(
                    "$step acquired a heading the design never recorded",
                    PairingPanelScreen.titleFor(step),
                )
            }
    }

    // ---- the copy is the flow's, never a second wording --------------------

    @Test
    fun `the body is the flow's own wording for the step`() {
        PairingStep.entries.forEach { step ->
            assertEquals(
                "the panel re-worded $step. Every one of these sentences was argued under " +
                    "PB-PAIR-5, and the SAS mismatch is the only warning this product has about " +
                    "an interception.",
                PairingFlow.messageFor(step),
                panel(step, holding = !PairingFlow.isTerminal(step)).body,
            )
        }
    }

    @Test
    fun `the destination notice is the attempt's own, not the panel's`() {
        val live = attempt(PairingStep.CONFIRM_DESTINATION, origin = "wss://relay.example", localNetwork = true)

        assertEquals(
            live.destinationNotice,
            PairingPanelScreen.of(live, ScannerState.SCANNING, null, holding = true).notice,
        )
    }

    // ---- which control is offered, and when --------------------------------

    @Test
    fun `with no attempt in this process the scanner is offered and nothing else is`() {
        assertEquals(
            setOf(PairingControl.SCAN),
            panel(PairingStep.SCAN, holding = false).controls,
        )
    }

    @Test
    fun `a denied camera withdraws the scanner and offers the typed fallback`() {
        // PB-PAIR-2: a denied camera must not be a dead end. And an ORDINARY denial is re-askable,
        // so it does not send the user to a Settings screen where nothing is wrong.
        assertEquals(
            setOf(PairingControl.TYPED_PAYLOAD, PairingControl.USE_TYPED_PAYLOAD),
            panel(
                PairingStep.SCAN,
                holding = false,
                scanner = ScannerState.PERMISSION_DENIED,
            ).controls,
        )
    }

    @Test
    fun `only a permanent denial routes to system settings`() {
        assertEquals(
            setOf(
                PairingControl.TYPED_PAYLOAD,
                PairingControl.USE_TYPED_PAYLOAD,
                PairingControl.OPEN_SYSTEM_SETTINGS,
            ),
            panel(
                PairingStep.SCAN,
                holding = false,
                scanner = ScannerState.PERMISSION_PERMANENTLY_DENIED,
            ).controls,
        )
    }

    @Test
    fun `the destination step offers exactly the confirm control and the way out`() {
        val page = panel(
            PairingStep.CONFIRM_DESTINATION,
            holding = true,
            origin = "wss://relay.example",
        )

        assertEquals(setOf(PairingControl.CONFIRM_DESTINATION, PairingControl.STOP), page.controls)
        assertEquals("wss://relay.example", page.destination)
    }

    @Test
    fun `no destination is on screen until it is the step that shows one`() {
        // PB-PAIR-6 is about a destination DISPLAYED and then carried back. A screen that left the
        // last one visible through the handshake would be showing a string the confirm control is
        // no longer about.
        listOf(PairingStep.HANDSHAKING, PairingStep.COMPARING_CODES, PairingStep.PAIRED)
            .forEach { step ->
                assertEquals(
                    "$step still shows a destination",
                    "",
                    panel(step, holding = true, origin = "wss://relay.example").destination,
                )
            }
    }

    @Test
    fun `the comparison shows the six symbols and offers exactly the two answers`() {
        val code = SasStep("owl microscope galaxy anchor lock key")
        val page = panel(PairingStep.COMPARING_CODES, holding = true, sas = code)

        assertEquals(code.symbols, page.sas)
        assertEquals(code.instruction, page.sasInstruction)
        assertEquals(
            setOf(
                PairingControl.CODES_MATCH,
                PairingControl.CODES_DO_NOT_MATCH,
                PairingControl.STOP,
            ),
            page.controls,
        )
    }

    @Test
    fun `no symbols are shown until the handshake has derived them`() {
        // The core stays in `pairing` from the dial until the user answers, so a screen that drew
        // an empty comparison would be asking a person to confirm nothing -- and this comparison
        // is the only human-in-the-loop check left in the product.
        val page = panel(PairingStep.COMPARING_CODES, holding = true, sas = null)

        assertEquals(emptyList<String>(), page.sas)
        assertTrue(
            "the two SAS answers are offered over no symbols",
            PairingControl.CODES_MATCH !in page.controls &&
                PairingControl.CODES_DO_NOT_MATCH !in page.controls,
        )
    }

    // ---- the way out, and the resumed attempt ------------------------------

    @Test
    fun `stop is offered while an attempt is live and withdrawn once it has stopped`() {
        PairingStep.entries.forEach { step ->
            val live = PairingControl.STOP in panel(step, holding = true).controls
            assertEquals(
                "$step disagrees with PairingFlow.isTerminal about whether there is anything " +
                    "left to stop",
                !PairingFlow.isTerminal(step),
                live,
            )
        }
    }

    @Test
    fun `an interrupted attempt says so, and only while there is no live handshake`() {
        assertEquals(
            "This pairing was interrupted before it finished. Nothing was joined.",
            panel(PairingStep.SCAN, holding = false, interrupted = true).notice,
        )
        assertEquals(
            "the interrupted line survived into a live attempt, where it contradicts the step " +
                "the handshake is actually in",
            "",
            panel(PairingStep.HANDSHAKING, holding = true, interrupted = true).notice,
        )
    }

    @Test
    fun `a live attempt offers no scanner, whatever the camera says`() {
        // Offering a fresh scan mid-handshake is the dead end PB-PAIR-4 describes from the other
        // direction: BeginPairing fail-fasts while a device is registered, so the scan leads to a
        // refusal the user cannot resolve from the handset.
        listOf(
            PairingStep.CONFIRM_DESTINATION,
            PairingStep.HANDSHAKING,
            PairingStep.COMPARING_CODES,
            PairingStep.AWAITING_MACHINE_DECISION,
        ).forEach { step ->
            val controls = panel(step, holding = true, scanner = ScannerState.SCANNING).controls
            assertTrue(
                "$step offers the scanner over a live attempt",
                PairingControl.SCAN !in controls && PairingControl.TYPED_PAYLOAD !in controls,
            )
        }
    }
}
