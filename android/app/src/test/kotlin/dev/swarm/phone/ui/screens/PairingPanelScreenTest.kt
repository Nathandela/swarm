package dev.swarm.phone.ui.screens

import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.PermissionStateResolver
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

    /**
     * @param relayKnown defaults to a phone that HAS one. Every assertion in this file predates
     *  the typed short code (ADR-007 B140), and the relay is learned from a pairing's confirm
     *  step -- so the handset each of them describes is one that has paired at least once. The
     *  fresh install with no relay is stated where it is the subject, in `PairingGuidanceTest`.
     */
    private fun panel(
        step: PairingStep,
        holding: Boolean,
        scanner: ScannerState = ScannerState.SCANNING,
        sas: SasStep? = null,
        origin: String = "",
        localNetwork: Boolean = false,
        interrupted: Boolean = false,
        machine: String = "",
        relayKnown: Boolean = true,
    ) = PairingPanelScreen.of(
        attempt = attempt(step, origin, localNetwork, interrupted),
        scanner = scanner,
        sas = sas,
        holding = holding,
        machine = machine,
        relayKnown = relayKnown,
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
            PairingPanelScreen.of(live, ScannerState.SCANNING, null, holding = true, relayKnown = true)
                .notice,
        )
    }

    // ---- which control is offered, and when --------------------------------

    @Test
    fun `with no attempt in this process the scanner and the collapsed fallback are offered`() {
        // `setOf(SCAN)` UNTIL agents-tracker-qun0: the reveal control was withdrawn wherever
        // the camera was granted, so the owner -- permission held, scanner not decoding --
        // had no way to type the code their machine was printing. The fallback is
        // unconditional now; collapsed behind its reveal here, because beside a working
        // scanner it is the fallback and not the primary.
        assertEquals(
            setOf(PairingControl.SCAN, PairingControl.REVEAL_TYPED_PAYLOAD),
            panel(PairingStep.SCAN, holding = false).controls,
        )
    }

    @Test
    fun `an ordinary denial keeps the typed fallback and still offers the re-askable scanner`() {
        // PB-PAIR-2 HAS TWO CLAUSES AND THIS STATE IS WHERE BOTH LAND. A denied camera must not
        // be a dead end -- hence the fallback -- and the permission must be REQUESTED, which is
        // the clause that was never tested: the app's only `requestPermissions(CAMERA)` is this
        // control's click listener, so withdrawing the control here is what made the ask
        // unreachable (agents-tracker-qx9m). An ORDINARY denial is re-askable, which is the same
        // reason it does not send the user to a Settings screen where nothing is wrong.
        //
        // THIS TEST REPLACED ONE THAT ASSERTED THE OPPOSITE, and the reason is recorded here
        // because the next reader needs the argument and not just the new expectation. The old
        // assertion -- `a denied camera withdraws the scanner and offers the typed fallback`,
        // requiring SCAN to be ABSENT here -- made PB-PAIR-2's "requested" clause literally
        // unimplementable: the only control that can request the permission was withheld until
        // the permission was already held. It also contradicted its own stated intent, which read
        // "a denied camera must not be a dead end" above an assertion that created exactly that
        // dead end. It was not weakened to make an implementation pass; it was re-pointed at the
        // requirement, and its surviving half is asserted in the neighbouring test.
        //
        // NO TEST FOUND THIS. The owner found it on a real handset, on the internal-testing build,
        // because every test in this file handed the panel a ScannerState by hand and none of them
        // ever described a new install.
        //
        // WIDENED FROM AN EXACT SET, DELIBERATELY AND BY ITS AUTHOR. The owner's guided pairing
        // screen puts the paste field behind a quieter "Enter code instead" disclosure, because a
        // field of equal weight beside the scan control is what made the owner read pasting as how
        // this product pairs. That is compatible with PB-PAIR-2, which requires the fallback to be
        // OFFERED and says nothing about whether it starts expanded: a labelled control that is
        // always on screen and reaches the same typed path in one tap is not a dead end. What this
        // test is about -- that the camera can be ASKED FOR in the state a fresh install is in --
        // is unchanged and is the first assertion below.
        val controls = panel(
            PairingStep.SCAN,
            holding = false,
            scanner = ScannerState.PERMISSION_DENIED,
        ).controls

        assertTrue(
            "the scan control is withheld in the state a fresh install is actually in, so the " +
                "only code in the app that can request CAMERA is unreachable",
            PairingControl.SCAN in controls,
        )
        assertTrue(
            "a denied camera can reach no typed fallback at all, which is the dead end PB-PAIR-2 " +
                "forbids. The field may be COLLAPSED behind the reveal control -- the paste path " +
                "is the fallback, not the primary -- but something that reaches it must be ON " +
                "SCREEN, or the fallback exists only in principle.",
            PairingControl.REVEAL_TYPED_PAYLOAD in controls ||
                PairingControl.TYPED_PAYLOAD in controls,
        )
        // THE EXACT SET WAS CARRYING THIS IMPLICITLY, so dropping it means stating it. An ordinary
        // denial is re-askable, and routing it to Settings sends a user with nothing wrong to a
        // screen where nothing is wrong.
        assertTrue(
            "an ordinary denial routes to a system settings screen where nothing is wrong",
            PairingControl.OPEN_SYSTEM_SETTINGS !in controls,
        )
    }

    @Test
    fun `only a permanent denial routes to system settings, and it withdraws the scanner`() {
        // The one state that takes the scan control away, and the only one that may: Android will
        // not show the prompt again, so a scan button here could only fail silently -- and the
        // route that can actually undo it is the one offered instead.
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

    // ---- what a phone that has NEVER BEEN ASKED is offered ------------------

    /**
     * The camera as a REAL PHONE ANSWERS IT, resolved rather than named.
     *
     * Every assertion above hands this file a `ScannerState` by hand, and that is precisely how a
     * screen with no reachable camera shipped: the suite never once asked what state a phone that
     * has never been asked for the permission is actually in. It is DENIED --
     * `PermissionStateResolver` answers `!hasAskedBefore -> DENIED`, and that row exists because
     * `shouldShowRequestPermissionRationale` is false before the first ask as well as after a
     * permanent one.
     */
    private fun camera(granted: Boolean, asked: Boolean, rationale: Boolean) =
        PairingFlow.scannerState(
            PermissionStateResolver.resolve(
                permission = AppPermission.CAMERA,
                sdkInt = 35,
                granted = granted,
                hasAskedBefore = asked,
                showRationale = rationale,
            ),
        )

    @Test
    fun `a fresh install is offered the control that requests the camera`() {
        // THE FIELD REPORT, AS A TEST (agents-tracker-qx9m). The owner installed the
        // internal-testing build on a real handset and found no camera and no scan button -- only
        // the paste field. The catch-22 is closed at both ends: this state has no permission, the
        // only code in the app that can request one is the scan control's listener, and the panel
        // did not draw the control.
        val controls = panel(
            PairingStep.SCAN,
            holding = false,
            scanner = camera(granted = false, asked = false, rationale = false),
        ).controls

        assertTrue(
            "a phone that has never been asked for the camera is offered no way to ask, so QR " +
                "pairing is unreachable for the entire life of the install",
            PairingControl.SCAN in controls,
        )
    }

    @Test
    fun `granting the camera leads to the scanner`() {
        // The other half of the loop, and the half that proves the first one goes somewhere: the
        // press asks, the answer is yes, and the next draw is the scanner itself.
        val granted = camera(granted = true, asked = true, rationale = false)

        assertEquals(ScannerState.SCANNING, granted)
        assertEquals(
            setOf(PairingControl.SCAN, PairingControl.REVEAL_TYPED_PAYLOAD),
            panel(PairingStep.SCAN, holding = false, scanner = granted).controls,
        )
    }

    @Test
    fun `a permanently denied phone is offered the settings route and no scanner`() {
        // The state where a scan button would be a lie: the prompt will not appear again, so the
        // control that requests the permission cannot get it and the system settings screen is
        // the only thing that can.
        val controls = panel(
            PairingStep.SCAN,
            holding = false,
            scanner = camera(granted = false, asked = true, rationale = false),
        ).controls

        assertTrue(
            "a permanently denied camera is offered a scan button that can do nothing",
            PairingControl.SCAN !in controls,
        )
        assertTrue(
            "the one route that can undo a permanent denial is not offered",
            PairingControl.OPEN_SYSTEM_SETTINGS in controls,
        )
    }

    @Test
    fun `the typed fallback survives every state the camera can be in`() {
        // THE GRANTED STATE IS IN THIS LIST BECAUSE THE OWNER GOT STUCK IN IT
        // (agents-tracker-qun0). This test's first version scoped PB-PAIR-2's fallback to the
        // denial paths, reading "a denied camera must not be a dead end" as the whole
        // requirement -- and a GRANTED camera pointed at a symbol that would not decode
        // (agents-tracker-v5qc's 640x480 analyzer) was a dead end with no typed path at all.
        // The permission state knows who may open the camera; it knows nothing about whether
        // frames decode. The fallback is unconditional.
        listOf(
            camera(granted = false, asked = false, rationale = false),
            camera(granted = false, asked = true, rationale = true),
            camera(granted = false, asked = true, rationale = false),
            camera(granted = true, asked = true, rationale = false),
        ).forEach { state ->
            val controls = panel(PairingStep.SCAN, holding = false, scanner = state).controls
            // REACHABLE, COLLAPSED OR OTHERWISE. The guided screen collapses the field behind
            // "Enter code instead" wherever the camera is still askable, and leaves it expanded on
            // a permanent denial, where the typed code is the only thing left that works without
            // leaving the app. Both satisfy PB-PAIR-2; neither is a dead end.
            assertTrue(
                "$state offers no way to pair without a camera, collapsed or otherwise",
                PairingControl.REVEAL_TYPED_PAYLOAD in controls ||
                    (PairingControl.TYPED_PAYLOAD in controls &&
                        PairingControl.USE_TYPED_PAYLOAD in controls),
            )
        }
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
