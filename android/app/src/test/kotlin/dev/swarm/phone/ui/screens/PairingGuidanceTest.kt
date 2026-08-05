package dev.swarm.phone.ui.screens

import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.PermissionStateResolver
import dev.swarm.phone.ui.PairingAttempt
import dev.swarm.phone.ui.PairingFlow
import dev.swarm.phone.ui.PairingStep
import dev.swarm.phone.ui.ScannerState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the GUIDED pairing screen -- the owner's design, recorded on
 * agents-tracker-qx9m after they installed the internal-testing build.
 *
 * WHAT THE OWNER FOUND AND WHAT THIS FILE FENCES. `agents-tracker-qx9m`'s first half is the camera
 * catch-22: the scan control was offered only where the permission was already granted, and the
 * app's only `requestPermissions(CAMERA)` is that control's own listener, so nothing could ever
 * ask. `PairingPanelScreenTest` fences that. Its SECOND half is what this file is for and it is a
 * separate defect: even with a working scan button, the screen said nothing about where a code
 * comes from. A person holding an unpaired phone was shown a paste field and expected to already
 * know that a Mac has to run `swarm remote pair` first. A control nobody knows the precondition for
 * is not more usable than a control that does not work.
 *
 * THE FLOW IS THE OWNER'S AND NOT THIS FILE'S. It was chosen from options and handed over drawn:
 * the two numbered steps, the command in a mono well, the scanner as the primary action, and the
 * typed code as a fallback that is REACHABLE but not open. This suite asserts that shape; it does
 * not re-derive it.
 *
 * ## The one place this and `PairingPanelScreenTest` had to be reconciled
 *
 * That suite asserts the paste FIELD is on screen for every denial state, on PB-PAIR-2's "a denied
 * camera must not be a dead end". The owner's design puts the field behind `Enter code instead`.
 * Both cannot be literally true of an ordinary denial, and the reconciliation is that PB-PAIR-2's
 * clause is about a DEAD END: a labelled control that reveals the field in one tap is not one. So
 * the requirement is asserted here as REACHABILITY rather than as the field's presence --
 * [assertManualPathIsReachable] below -- and the two suites now agree on the requirement rather
 * than on one rendering of it.
 *
 * A PERMANENT DENIAL IS THE EXCEPTION AND IT IS DELIBERATE. There the scanner is withdrawn for
 * good, so the typed path is the only thing left that works without leaving the app, and it is
 * open by default. Collapsing it in the one state where it is the whole screen would be the dead
 * end the requirement names.
 */
class PairingGuidanceTest {

    /**
     * @param relayKnown defaults to a phone that HAS one, because that is the phone every test
     *  written before agents-tracker-3fkm was about: the relay is learned from the first
     *  pairing's confirm step, so every assertion here that predates the typed short code
     *  describes a handset on its second pairing or later. The fresh install -- the state the
     *  first-run prompt exists for -- is stated by the tests that are about it, never defaulted
     *  into, because a default is how the state that shipped broken went unconstructed the last
     *  three times (agents-tracker-qx9m, -qun0, -v5qc).
     */
    private fun panel(
        step: PairingStep = PairingStep.SCAN,
        holding: Boolean = false,
        scanner: ScannerState = ScannerState.PERMISSION_DENIED,
        revealed: Boolean = false,
        relayKnown: Boolean = true,
        typedEntry: String = "",
        cameraLive: Boolean = false,
    ) = PairingPanelScreen.of(
        attempt = PairingAttempt(
            step = step,
            originShown = "",
            originIsLocalNetwork = false,
            explainsInterruptedAttempt = false,
        ),
        scanner = scanner,
        sas = null,
        holding = holding,
        machine = "",
        manualEntryRevealed = revealed,
        cameraLive = cameraLive,
        relayKnown = relayKnown,
        // RESOLVED THE WAY THE SURFACE RESOLVES IT, never named by hand: the surface asks
        // PairingFlow what the field holds, and a test that answered the question itself would
        // agree with a screen that had stopped asking.
        typedEntryCarriesItsOwnRelay = PairingFlow.entryCarriesItsOwnRelay(typedEntry),
    )

    /** The camera as a REAL PHONE answers it, never a state named by hand. */
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

    /**
     * PB-PAIR-2's fallback clause, asserted as what it says: the typed path can be REACHED.
     *
     * Either the field is on screen, or a named control that reveals it is. What the clause
     * forbids is a state with neither, which is a camera denial the user cannot pair around.
     */
    private fun assertManualPathIsReachable(controls: Set<PairingControl>, where: String) {
        assertTrue(
            "$where offers no way to reach the typed code at all, so a camera denial is the dead " +
                "end PB-PAIR-2 exists to forbid",
            PairingControl.TYPED_PAYLOAD in controls ||
                PairingControl.REVEAL_TYPED_PAYLOAD in controls,
        )
    }

    // ---- the steps, and the command they name ------------------------------

    @Test
    fun `the scan step carries the two steps the owner drew`() {
        val steps = panel().steps

        assertEquals(
            "the guided steps are not on the one screen that needs them, so an unpaired phone is " +
                "still asking a person to already know where a pairing code comes from",
            2,
            steps.size,
        )
        assertEquals(listOf("1", "2"), steps.map { it.ordinal })
    }

    @Test
    fun `the first step names the exact command and nothing else does`() {
        // THE COMMAND IS ONE STRING IN ONE PLACE. A second spelling of it is a second thing that
        // can be a typo, and this one is typed by a person reading it off a phone screen into a
        // shell -- the failure mode is not a wrong pixel, it is `command not found`.
        assertEquals("swarm remote pair", PairingPanelScreen.COMMAND)

        val carrying = panel().steps.filter { it.command.isNotEmpty() }
        assertEquals("the command is on more than one step, or on none", 1, carrying.size)
        assertEquals(PairingPanelScreen.COMMAND, carrying.single().command)
        assertEquals("1", carrying.single().ordinal)
    }

    @Test
    fun `the steps say computer and never Mac, because this app cannot know which`() {
        // The handset has no way to learn the desktop's OS -- nothing in the pairing payload
        // carries it and the phone has not connected to anything yet. "On your Mac" is correct for
        // the owner and wrong for every Linux user, and a wrong instruction is worse than a
        // general one on the screen whose whole job is telling someone what to do first.
        PairingPanelScreen.GUIDANCE.forEach { step ->
            assertFalse(
                "step ${step.ordinal} names a platform this app cannot know the user is on",
                step.line.contains("Mac"),
            )
        }
        assertTrue(
            "no step says whose computer the command is run on",
            PairingPanelScreen.GUIDANCE.any { it.line.contains("computer") },
        )
    }

    @Test
    fun `the guidance is the screen model's, never the view's`() {
        // PB-DS-9: copy belongs to the screen and exists once, so a suite that asserts what is
        // drawn cannot agree with a re-worded copy of itself.
        assertEquals(PairingPanelScreen.GUIDANCE, panel().steps)
    }

    @Test
    fun `a live attempt drops the steps, because they describe a pairing that has not started`() {
        // "On your computer, run `swarm remote pair`" is an instruction to CREATE a code. A phone
        // that is mid-handshake has one, and repeating the instruction over it would read as an
        // invitation to start again -- which is the dead end PB-PAIR-4 describes: BeginPairing
        // fail-fasts while a device is registered.
        listOf(
            PairingStep.CONFIRM_DESTINATION,
            PairingStep.HANDSHAKING,
            PairingStep.COMPARING_CODES,
            PairingStep.AWAITING_MACHINE_DECISION,
        ).forEach { step ->
            assertEquals(
                "$step still draws the how-to-start-pairing steps",
                emptyList<PairingGuidance>(),
                panel(step, holding = true).steps,
            )
        }
    }

    @Test
    fun `a refusal keeps its own argued sentence and gets no generic steps above it`() {
        // Every terminal state's message was argued line by line under PB-PAIR-5 and each names a
        // different next move -- "Ask your machine for a new one", "Approve it there, then pair
        // again", "Wait a minute". Stacking "1 On your computer, run ..." over that is the second,
        // shorter statement of a refusal that `PairingPanel`'s own comment refuses for headings.
        PairingStep.entries.filter { PairingFlow.isTerminal(it) }.forEach { step ->
            assertEquals(
                "$step drew the guided steps over the sentence PB-PAIR-5 wrote for it",
                emptyList<PairingGuidance>(),
                panel(step, holding = false).steps,
            )
        }
    }

    // ---- the primary control is the scanner ---------------------------------

    @Test
    fun `the scanner is the primary control on the screen a fresh install opens`() {
        // A fresh install resolves to PERMISSION_DENIED -- `PermissionStateResolver` answers
        // `!hasAskedBefore -> DENIED` -- so this IS the state the owner was looking at.
        val controls = panel(scanner = camera(granted = false, asked = false, rationale = false))
            .controls

        assertTrue("the scan control is not offered", PairingControl.SCAN in controls)
        assertEquals("Scan QR code", PairingPanelScreen.SCAN_CTA)
    }

    @Test
    fun `the scan control is offered wherever the camera is still askable`() {
        listOf(
            camera(granted = true, asked = true, rationale = false),
            camera(granted = false, asked = false, rationale = false),
            camera(granted = false, asked = true, rationale = true),
        ).forEach { state ->
            assertTrue(
                "$state offers no scan control, so the primary path is missing",
                PairingControl.SCAN in panel(scanner = state).controls,
            )
        }
    }

    // ---- the typed code: reachable, and not expanded -------------------------

    @Test
    fun `the typed code is reachable and NOT expanded on the screen a fresh install opens`() {
        val controls = panel(scanner = camera(granted = false, asked = false, rationale = false))
            .controls

        assertManualPathIsReachable(controls, "a fresh install")
        assertTrue(
            "the paste field is open before anyone asked for it. It is the fallback, not the " +
                "primary path, and an open field beside a scan button is what made the owner " +
                "read the paste box as the only way to pair",
            PairingControl.TYPED_PAYLOAD !in controls &&
                PairingControl.USE_TYPED_PAYLOAD !in controls,
        )
        assertTrue(
            "nothing on screen offers to open the typed code",
            PairingControl.REVEAL_TYPED_PAYLOAD in controls,
        )
        assertEquals("Enter code instead", PairingPanelScreen.MANUAL_CTA)
    }

    @Test
    fun `revealing the typed code puts the field and its button on screen`() {
        val controls = panel(revealed = true).controls

        assertTrue(
            "the reveal control leads nowhere, so the fallback is unreachable in practice",
            PairingControl.TYPED_PAYLOAD in controls &&
                PairingControl.USE_TYPED_PAYLOAD in controls,
        )
        assertTrue(
            "the reveal control is still on screen beside the field it has already revealed",
            PairingControl.REVEAL_TYPED_PAYLOAD !in controls,
        )
        assertTrue(
            "revealing the fallback took the scanner away, so a person who opened the field to " +
                "look at it can no longer scan",
            PairingControl.SCAN in controls,
        )
    }

    @Test
    fun `the typed path stays reachable in every state a withheld camera can be in`() {
        // PB-PAIR-2's fallback clause, over the same three states `PairingPanelScreenTest` walks.
        // Asserted as reachability rather than as the field's presence; see the class comment.
        listOf(
            camera(granted = false, asked = false, rationale = false),
            camera(granted = false, asked = true, rationale = true),
            camera(granted = false, asked = true, rationale = false),
        ).forEach { state ->
            assertManualPathIsReachable(panel(scanner = state).controls, "$state")
        }
    }

    @Test
    fun `a granted camera keeps the typed fallback, collapsed until asked for`() {
        // THIS TEST ASSERTED THE OPPOSITE and the owner overruled it from the field
        // (agents-tracker-qun0). Its argument was simplicity -- "a phone that can scan is
        // offered the scanner, and a second way in is a second thing to explain" -- and the
        // case that argument missed is the one that happened: a granted camera pointed at a
        // symbol that would not decode (agents-tracker-v5qc's 640x480 analyzer) left the
        // owner with a live preview, a code on their terminal, and no way to type it. The
        // permission state cannot see whether frames decode, so the fallback is
        // unconditional -- collapsed behind the reveal here, because beside a working
        // scanner it is the fallback and not the primary.
        assertEquals(
            "a working camera hides the fallback until asked",
            setOf(PairingControl.SCAN, PairingControl.REVEAL_TYPED_PAYLOAD),
            panel(scanner = ScannerState.SCANNING, revealed = false).controls,
        )
        assertEquals(
            "asking for the fallback opens the field and keeps the scanner",
            setOf(
                PairingControl.SCAN,
                PairingControl.TYPED_PAYLOAD,
                PairingControl.USE_TYPED_PAYLOAD,
            ),
            panel(scanner = ScannerState.SCANNING, revealed = true).controls,
        )
    }

    // ---- a permanently denied camera ----------------------------------------

    @Test
    fun `a permanently denied camera offers settings instead of a dead scan button`() {
        val controls = panel(scanner = camera(granted = false, asked = true, rationale = false))
            .controls

        assertTrue(
            "a permanently denied camera is offered a scan button that can do nothing: Android " +
                "will not show the prompt again, so the control could only fail silently",
            PairingControl.SCAN !in controls,
        )
        assertTrue(
            "the one route that can undo a permanent denial is not offered",
            PairingControl.OPEN_SYSTEM_SETTINGS in controls,
        )
    }

    @Test
    fun `a permanently denied camera says WHY the scanner is gone`() {
        // The owner's design: "this control is replaced by one that opens system settings, with
        // copy saying why". Without the sentence the screen simply has a different button on it,
        // and a person who never saw the scan control has no reason to connect `Open this app's
        // settings` to a camera they turned off some time ago.
        val page = panel(scanner = camera(granted = false, asked = true, rationale = false))

        assertEquals(PairingPanelScreen.CAMERA_BLOCKED, page.cameraNotice)
        assertTrue("the explanation is empty", page.cameraNotice.isNotEmpty())
        assertTrue(
            "the sentence does not mention the camera, which is the thing that is wrong",
            page.cameraNotice.contains("camera"),
        )
    }

    @Test
    fun `only a permanent denial explains itself`() {
        // On every other state the sentence would be a warning about a camera nothing is wrong
        // with -- the same defect as sending a fresh install to a Settings screen.
        listOf(
            ScannerState.SCANNING,
            camera(granted = false, asked = false, rationale = false),
            camera(granted = false, asked = true, rationale = true),
        ).forEach { state ->
            assertEquals(
                "$state carries an explanation for a camera that is not blocked",
                "",
                panel(scanner = state).cameraNotice,
            )
        }
    }

    @Test
    fun `a permanently denied camera opens the typed field rather than collapsing it`() {
        // THE ONE STATE WHERE THE FALLBACK IS THE SCREEN. The scanner is withdrawn for good, so
        // the typed code is the only thing left that works without leaving the app; hiding it
        // behind a disclosure there is the dead end PB-PAIR-2 forbids, not a tidier default.
        val controls = panel(scanner = ScannerState.PERMISSION_PERMANENTLY_DENIED).controls

        assertEquals(
            setOf(
                PairingControl.TYPED_PAYLOAD,
                PairingControl.USE_TYPED_PAYLOAD,
                PairingControl.OPEN_SYSTEM_SETTINGS,
            ),
            controls,
        )
    }

    // ---- no camera hardware at all -------------------------------------------

    /**
     * agents-tracker-nz9h: a device with no camera hardware, gated on
     * `PackageManager.FEATURE_CAMERA_ANY` rather than on any permission answer.
     *
     * THE PERMANENT-DENIAL BRANCH IS THE MODEL: the scanner is withdrawn for good, so the typed
     * code is the only thing left that works without leaving the app, and it opens automatically
     * rather than collapsing behind a disclosure a person would have to know to press.
     */
    @Test
    fun `no camera hardware withdraws the scanner and opens the typed field automatically`() {
        assertEquals(
            setOf(PairingControl.TYPED_PAYLOAD, PairingControl.USE_TYPED_PAYLOAD),
            panel(scanner = ScannerState.NO_CAMERA).controls,
        )
    }

    /**
     * The one difference from a permanent denial: nothing in Settings can add hardware a
     * handset does not have, so this state's sentence must not send anyone there.
     */
    @Test
    fun `no camera hardware says so, and offers no settings route`() {
        val page = panel(scanner = ScannerState.NO_CAMERA)

        assertEquals(PairingPanelScreen.NO_CAMERA_NOTICE, page.cameraNotice)
        assertTrue("the explanation is empty", page.cameraNotice.isNotEmpty())
        assertTrue(
            "a camera-less handset reads the same sentence as a permission denial, which " +
                "offers a settings route that fixes nothing here",
            page.cameraNotice != PairingPanelScreen.CAMERA_BLOCKED,
        )
        assertTrue(
            PairingControl.OPEN_SYSTEM_SETTINGS !in panel(scanner = ScannerState.NO_CAMERA).controls,
        )
    }

    // ---- a viewfinder that is looking, and says so ---------------------------

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-av7k's on-screen half.
     *
     * WHAT THE OWNER CANNOT TELL APART, AND IT IS THE WHOLE BUG REPORT. They hold the phone over
     * the terminal symbol and nothing happens. "Nothing" is the same picture for a camera that
     * never started, an analysis pipeline delivering no frames, and a decoder reading thirty
     * frames a second and finding no symbol in any of them -- and those are three different
     * defects with three different fixes. The screen showed a live preview and said nothing else,
     * so the one number that separates them was never on it.
     *
     * IT IS A COUNT AND NEVER A PROGRESS BAR. There is nothing to be a fraction of: a scan
     * succeeds on the frame it succeeds on, and a bar filling up would be an invention that
     * implies an end. What this reports is the thing that is actually happening.
     */
    @Test
    fun `a running camera reports what it has looked at`() {
        assertTrue(
            "a live camera says nothing about whether it is receiving frames, which is the one " +
                "fact separating a dead pipeline from a symbol that will not decode",
            panel(cameraLive = true).showsScanProgress,
        )
        assertEquals("1 frame analysed, no code found yet", PairingPanelScreen.scanProgress(1))
        assertEquals("50 frames analysed, no code found yet", PairingPanelScreen.scanProgress(50))
    }

    @Test
    fun `a camera nobody has started reports nothing`() {
        // NO FAKE PROGRESS. Before the scan control is pressed there is no pipeline and no count,
        // and a line reading "0 frames analysed" would be a claim that something is looking.
        assertTrue(
            "the frame counter is on screen before anyone opened the camera",
            !panel(cameraLive = false).showsScanProgress,
        )
        assertEquals("", PairingPanelScreen.scanProgress(0))
    }

    @Test
    fun `the counter goes with the scanner when an attempt begins`() {
        // A payload has been read by this point and the camera is released. A frame count left
        // on screen would be reporting on a pipeline that has stopped.
        PairingStep.entries.forEach { step ->
            assertTrue(
                "$step reports frame counts over a live attempt, whose camera is already closed",
                !panel(step = step, holding = true, cameraLive = true).showsScanProgress,
            )
        }
    }

    // ---- the relay a ten-character code cannot carry -------------------------

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-3fkm.
     *
     * WHAT THE CLI PROMISES AND A FRESH INSTALL CANNOT KEEP. `swarm remote pair` prints "Type
     * this code on your phone to pair: XXX-XXXX-XXX", and the phone routes any entry without the
     * wire prefix to `BeginPairingWithCode(code, knownRelay())`. On a fresh install that second
     * argument is empty, and the facade refuses -- correctly, because ten characters name a
     * rendezvous and no address to reach it at. The screen therefore had one path the desktop
     * advertises and the handset cannot walk.
     *
     * WHY A FIELD AND NOT A SECOND PAIRING FORM. [PairingFlow.manualEntryAcceptsSeparateFields]
     * stays false and its test stays with it: the typed CODE is one spelling of the QR's payload,
     * and a form that took a URL and a code as equal fields would be a second wire encoding
     * arriving as things the user asserted. The relay address is not part of that encoding -- it
     * is REMEMBERED CONFIGURATION this phone lacks, asked for once, outside the ceremony, and
     * still displayed and confirmed through PB-PAIR-6 like every other destination.
     */
    @Test
    fun `a fresh install typing a code is asked for the relay address`() {
        val page = panel(revealed = true, relayKnown = false, typedEntry = "K73-M2QF-9TD")

        assertTrue(
            "a phone with no relay is offered nowhere to put one, so the ten-character code the " +
                "desktop just printed cannot be used on the install that most needs it",
            PairingControl.RELAY_URL in page.controls,
        )
        assertTrue(
            "the field appears with no sentence saying what it wants or that it is asked once",
            page.relayNotice.isNotEmpty(),
        )
        assertEquals(PairingPanelScreen.RELAY_ASK, page.relayNotice)
    }

    @Test
    fun `a phone that already knows its relay is never asked for it again`() {
        // THE ASK IS ONCE. The URL is written on the PB-PAIR-6 confirm and read back at the next
        // launch, so a second ask would be this screen asking for something it is already holding
        // -- and the obvious way to answer it is to retype it slightly differently.
        val page = panel(revealed = true, relayKnown = true, typedEntry = "K73-M2QF-9TD")

        assertTrue(
            "a phone that knows its relay is asked for it anyway",
            PairingControl.RELAY_URL !in page.controls,
        )
        assertEquals("", page.relayNotice)
    }

    @Test
    fun `a pasted long payload carries its own relay, so nothing is asked for`() {
        // The other spelling. `swarm-pair:` announces the payload that CONTAINS the relay URL, so
        // asking for one beside it would be asking the user to supply a value the string they
        // just pasted already holds -- and whichever they typed, the payload's is what gets used.
        val page = panel(
            revealed = true,
            relayKnown = false,
            typedEntry = "swarm-pair:v1:example",
        )

        assertTrue(
            "the long payload was asked for a relay address it already carries",
            PairingControl.RELAY_URL !in page.controls,
        )
        assertEquals("", page.relayNotice)
    }

    @Test
    fun `the relay ask never appears without the field it belongs to`() {
        // A field for the relay beside a screen with nowhere to type the CODE is a form with one
        // half missing. Collapsed, and mid-attempt, are the two states where that would happen.
        assertTrue(
            "the collapsed fallback shows a relay field with no code field under it",
            PairingControl.RELAY_URL !in panel(revealed = false, relayKnown = false).controls,
        )
        PairingStep.entries.forEach { step ->
            assertTrue(
                "$step offers the relay ask over a live attempt, whose destination is already " +
                    "decided and already on screen",
                PairingControl.RELAY_URL !in
                    panel(step = step, holding = true, relayKnown = false).controls,
            )
        }
    }

    @Test
    fun `a permanently denied camera on a fresh install offers the whole typed path`() {
        // THE TWO FIRST-RUN DEFECTS MEET HERE and this is the screen that has to work: no camera
        // ever, no relay yet, and a code on the terminal. It is the one state where the typed
        // path is the entire product, so every part of it has to be on screen at once.
        assertEquals(
            setOf(
                PairingControl.TYPED_PAYLOAD,
                PairingControl.RELAY_URL,
                PairingControl.USE_TYPED_PAYLOAD,
                PairingControl.OPEN_SYSTEM_SETTINGS,
            ),
            panel(
                scanner = ScannerState.PERMISSION_PERMANENTLY_DENIED,
                relayKnown = false,
                typedEntry = "K73-M2QF-9TD",
            ).controls,
        )
    }

    // ---- the reveal control cannot leak into a live attempt ------------------

    @Test
    fun `no step with a live attempt offers the reveal control`() {
        PairingStep.entries.forEach { step ->
            assertTrue(
                "$step offers to open the typed code over a live attempt, which would begin a " +
                    "second pairing while the first is mid-handshake",
                PairingControl.REVEAL_TYPED_PAYLOAD !in panel(step, holding = true).controls,
            )
        }
    }
}
