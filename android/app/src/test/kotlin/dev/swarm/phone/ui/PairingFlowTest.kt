package dev.swarm.phone.ui

import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.DegradedCapability
import dev.swarm.phone.runtime.PermissionState
import dev.swarm.phone.runtime.PermissionStateResolver
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-PAIR-2 (camera capture + decode with denial paths),
 * PB-PAIR-5 (explicit terminal states), PB-PAIR-6 (nothing joined silently) and PB-SAS-3 (the
 * SAS is compared, never typed).
 *
 * NO CAMERA IS OPENED HERE AND NONE IS MODELLED. PB-PAIR-2's decode belongs to a scanner
 * library the ADR has yet to choose (PB-PAIR-3), and whether a real camera produces a frame is
 * PB-E2E-5, which is deferred. What is modelled is the POLICY the requirement actually names:
 * which state the screen is in for granted, denied and permanently-denied, and that a
 * manual-entry fallback exists and is SPECIFIED rather than improvised.
 * android/gate/s16_ui_test.go fences that this package imports no camera API.
 */
class PairingPermissionTest {

    /**
     * The three permission paths PB-PAIR-2 enumerates. The resolution itself is
     * PermissionStateResolver's -- already shipped and already tested (PB-RUN-2) -- and reusing
     * it is the point: a second copy of the fresh-install-versus-permanently-denied rule is a
     * second place to get it wrong, and getting it wrong sends a user with a fresh install to a
     * Settings screen where nothing is wrong.
     */
    @Test
    fun `the scanner screen follows the shipped permission resolution`() {
        val granted = PermissionStateResolver.resolve(
            AppPermission.CAMERA, sdkInt = 35, granted = true, hasAskedBefore = true, showRationale = false,
        )
        assertEquals(ScannerState.SCANNING, PairingFlow.scannerState(granted))

        val denied = PermissionStateResolver.resolve(
            AppPermission.CAMERA, sdkInt = 35, granted = false, hasAskedBefore = true, showRationale = true,
        )
        assertEquals(ScannerState.PERMISSION_DENIED, PairingFlow.scannerState(denied))

        val permanently = PermissionStateResolver.resolve(
            AppPermission.CAMERA, sdkInt = 35, granted = false, hasAskedBefore = true, showRationale = false,
        )
        assertEquals(ScannerState.PERMISSION_PERMANENTLY_DENIED, PairingFlow.scannerState(permanently))
    }

    /**
     * PB-PAIR-2's FIRST clause, which had no test: the permission is REQUESTED.
     *
     * A permission nobody has asked for resolves to DENIED -- `PermissionStateResolver` answers
     * `!hasAskedBefore -> DENIED` and that row is deliberate, because `showRationale` is false
     * before the first ask as well as after a permanent one. So a fresh install arrives at this
     * screen DENIED, and the app's only `requestPermissions(CAMERA)` call is the scan control's
     * own click listener. Offering that control on GRANTED alone makes the button that asks for
     * the permission require the permission it exists to ask for: the owner's handset showed a
     * paste field and no camera, and no sequence of taps could have produced one
     * (agents-tracker-qx9m).
     *
     * THE STATE IS RESOLVED HERE RATHER THAN NAMED. Every test that had this screen's permission
     * behaviour under it passed a `ScannerState` in by hand, so the suite never once asked what a
     * phone that has never been asked actually gets -- which is exactly the state that shipped
     * broken.
     */
    @Test
    fun `a camera nobody has asked for still offers the control that asks for it`() {
        val freshInstall = PermissionStateResolver.resolve(
            AppPermission.CAMERA, sdkInt = 35, granted = false, hasAskedBefore = false, showRationale = false,
        )
        assertEquals(ScannerState.PERMISSION_DENIED, PairingFlow.scannerState(freshInstall))
        assertTrue(
            "a fresh install is offered no scanner, and the only code in the app that can " +
                "request CAMERA is that control's listener -- so the permission can never be granted",
            PairingFlow.offersScanner(PairingFlow.scannerState(freshInstall)),
        )
    }

    /**
     * The other two answers, and the one that must stay a refusal.
     *
     * A PERMANENT denial is the single state that withdraws the scanner: Android will not show
     * the prompt again, so the control could only fail silently -- and [PairingFlow
     * .routesToSystemSettings] answers that state with the one route that can undo it.
     */
    @Test
    fun `the scanner is offered wherever the camera is still askable and nowhere else`() {
        assertTrue(PairingFlow.offersScanner(ScannerState.SCANNING))
        assertTrue(PairingFlow.offersScanner(ScannerState.PERMISSION_DENIED))
        assertFalse(
            "a permanently denied camera is offered a scan button that cannot work, and Android " +
                "will never show the prompt it would trigger",
            PairingFlow.offersScanner(ScannerState.PERMISSION_PERMANENTLY_DENIED),
        )
    }

    /**
     * A camera that cannot deliver a payload must not be a dead end. PB-PAIR-2 requires a
     * manual-entry fallback, and PB-RUN-2 already names what is lost when CAMERA is withheld.
     */
    @Test
    fun `every state offers manual entry, granted camera included`() {
        assertTrue(PairingFlow.offersManualEntry(ScannerState.PERMISSION_DENIED))
        assertTrue(PairingFlow.offersManualEntry(ScannerState.PERMISSION_PERMANENTLY_DENIED))
        // THIS ASSERTION WAS `assertFalse`, and the owner overruled it from the field
        // (agents-tracker-qun0). The old reading scoped the fallback to the denial states,
        // reasoning that a granted camera has the scanner and needs nothing else -- and the
        // day the scanner ran at 640x480 (agents-tracker-v5qc), a granted camera pointed at a
        // symbol that would not decode had no typed path at all: permission held, preview
        // live, nothing decoding, nothing else on screen. "Not working" is a fact about
        // optics and symbols the permission state cannot see, so the fallback is
        // unconditional.
        assertTrue(PairingFlow.offersManualEntry(ScannerState.SCANNING))
        assertEquals(
            DegradedCapability.QR_PAIRING,
            PermissionStateResolver.degradedCapability(AppPermission.CAMERA, PermissionState.DENIED),
        )
    }

    /**
     * A permanent denial is the only one that sends the user to system settings; an ordinary
     * denial is re-askable. Collapsing them either loops a user on a prompt Android will never
     * show again, or sends a first-launch user to Settings for a permission nobody has refused.
     */
    @Test
    fun `only a permanent denial routes to system settings`() {
        assertTrue(PairingFlow.routesToSystemSettings(ScannerState.PERMISSION_PERMANENTLY_DENIED))
        assertFalse(PairingFlow.routesToSystemSettings(ScannerState.PERMISSION_DENIED))
    }

    /**
     * MANUAL ENTRY IS SPECIFIED, NOT IMPROVISED -- PB-PAIR-2's own words. The typed string is
     * the SAME payload the QR carries (internal/remote/pairing.EncodeQR's encoding), so it goes
     * to the same DecodeQR and reaches the same display-and-confirm step. An improvised
     * "type the relay URL and a code" format would be a second wire encoding, and it would
     * bypass PB-PAIR-6 by arriving as fields the user typed rather than as a destination the
     * phone must show them.
     */
    @Test
    fun `manual entry accepts the QR payload and nothing else`() {
        assertTrue(PairingFlow.manualEntryIsQrPayload)
        assertFalse(
            "a second hand-typed format would be a second wire encoding",
            PairingFlow.manualEntryAcceptsSeparateFields,
        )
    }
}

class PairingStepTest {

    private val honestQr = "swarm-pair:v1:example"

    /**
     * PB-PAIR-6: the destination is shown BEFORE anything is joined.
     *
     * mobile/pairing.go dials on its second statement today, so this step has nowhere to sit on
     * the Go side either; mobile/conformance/s16_pairing_test.go measures the dial at a socket
     * and this measures that the screen has a step for it.
     */
    @Test
    fun `the first step after a scan is confirming the destination`() {
        val flow = PairingFlow.begin(
            qr = honestQr,
            origin = "wss://relay.example.com:8443",
            originIsPrivate = false,
        )
        assertEquals(PairingStep.CONFIRM_DESTINATION, flow.step)
        assertEquals("wss://relay.example.com:8443", flow.originShown)
        assertFalse("nothing may be joined before the user says yes", flow.joined)
    }

    /**
     * The LAN case, resolved EXPLICITLY by the requirement: a private destination is allowed
     * after display and confirmation, because a blanket private-address rule would reject the
     * very handset demonstration PB-OPS-1 describes -- a phone reaching the laptop over the LAN.
     * It is labelled so the sheet can say which kind it is showing.
     */
    @Test
    fun `a private destination is allowed after confirmation and is labelled as local`() {
        val flow = PairingFlow.begin(qr = honestQr, origin = "ws://192.168.1.20:8443", originIsPrivate = true)
        assertEquals(PairingStep.CONFIRM_DESTINATION, flow.step)
        assertTrue(flow.originIsLocalNetwork)
        assertTrue(flow.destinationNotice.isNotBlank())

        val confirmed = flow.confirmDestination("ws://192.168.1.20:8443")
        assertEquals(PairingStep.HANDSHAKING, confirmed.step)
    }

    /** A target swapped after display is rejected -- the requirement's own second test. */
    @Test
    fun `confirming a different destination from the one displayed is refused`() {
        val flow = PairingFlow.begin(qr = honestQr, origin = "ws://192.168.1.20:8443", originIsPrivate = true)
        val swapped = flow.confirmDestination("wss://relay.attacker.example:8443")
        assertEquals(PairingStep.REFUSED_ORIGIN_MISMATCH, swapped.step)
        assertFalse(swapped.joined)
    }

    /**
     * PB-SAS-3: the SAS screen is a COMPARISON of two displays. It shows the six emoji, it names
     * the machine's screen as the other half, and it offers exactly two answers -- they match,
     * they do not.
     */
    @Test
    fun `the SAS screen compares two screens and collects no input`() {
        val sas = SasStep(code = "otter anchor lemon prism cactus violin")
        assertEquals(6, sas.symbols.size)
        assertTrue(sas.instruction.isNotBlank())
        assertFalse("a typed code moves the comparison off the two screens", sas.acceptsTypedInput)
        assertEquals(setOf(SasAnswer.MATCHES, SasAnswer.DOES_NOT_MATCH), sas.answers)
    }

    /**
     * "They do not match" is NOT "cancel". A mismatch is the only signal this protocol has for a
     * man-in-the-middle; recording it as "I changed my mind" discards the security event and
     * invites the user to simply try again -- against the same attacker.
     */
    @Test
    fun `a SAS mismatch is a distinct terminal state from a cancellation`() {
        val mismatch = PairingFlow.terminal(SasAnswer.DOES_NOT_MATCH)
        assertEquals(PairingStep.SAS_MISMATCH, mismatch.step)
        assertNotEquals(PairingStep.CANCELLED, mismatch.step)
        assertTrue(mismatch.warnsAboutInterception)
        assertFalse("nothing is pinned when the codes disagree", mismatch.joined)
    }

    /**
     * PB-PAIR-5: every terminal state explicit, user-legible and its own. Collapsed into one
     * "pairing failed" with prose beside it, the screen can only show an error string -- which
     * is the opaque error the requirement exists to remove, and each of these needs a genuinely
     * different next step from the user.
     *
     * DIFFERENT_MACHINE IS THE ONE THE AMENDMENT ADDED, and the reason this list changed. The
     * 2026-07-25 amendment retired `already-paired` -- the machine fail-fasts a second pairing
     * before minting any rendezvous, so the phone has nothing to scan and can never reach it --
     * and substituted the QR that belongs to a machine this phone is not pinned to, which the
     * Go core decides mid-handshake from the authenticated machine static. The app kept
     * declaring the retired state and never learned the substituted one, so the state the
     * amendment created was the one state that fell through to the generic message.
     *
     * RATE_LIMITED and FAILED had never had a step at all. android/gate/pairingstates_test.go
     * is the control that fails when the core's alphabet and this enum drift again.
     */
    @Test
    fun `every terminal pairing state is explicit and separately legible`() {
        val states = listOf(
            PairingStep.DECLINED,
            PairingStep.SAS_MISMATCH,
            PairingStep.RENDEZVOUS_TIMEOUT,
            PairingStep.QR_EXPIRED,
            PairingStep.DIFFERENT_MACHINE,
            PairingStep.RATE_LIMITED,
            PairingStep.FAILED,
        )
        val messages = states.map { PairingFlow.messageFor(it) }
        messages.forEach { assertTrue("a terminal state with no message is an opaque error", it.isNotBlank()) }
        assertEquals("two states that read identically are one state", states.size, messages.toSet().size)
        states.forEach { assertTrue(PairingFlow.isTerminal(it)) }
    }

    /**
     * The substituted state's message must tell the user what actually happened, because its
     * next step is unlike every other refusal here: nothing is wrong with this phone, nothing
     * is wrong with the QR, and retrying the SAME code will fail the same way. v1 is
     * single-machine, so the answer is "you scanned a different machine's code".
     *
     * It also has to say that the pairing they already have is intact. The defect the state
     * closes is that `pin()` used to overwrite the pinned machine unconditionally, so a user
     * who did this lost the machine they were working on with an empty roster as the first
     * symptom; a message that does not say "nothing changed" leaves them believing it happened.
     */
    @Test
    fun `the different-machine state names the cause and says nothing was lost`() {
        val message = PairingFlow.messageFor(PairingStep.DIFFERENT_MACHINE)
        assertTrue(
            "the message must name the cause -- another machine's code -- not just report a failure",
            message.contains("machine", ignoreCase = true),
        )
        assertNotEquals(
            "it must not read as the generic failure it used to fall through to",
            PairingFlow.messageFor(PairingStep.FAILED),
            message,
        )
        assertFalse("nothing is joined when the QR names another machine", PairingFlow.restore(PairingStep.DIFFERENT_MACHINE).joined)
    }

    /**
     * PB-PAIR-4: the state machine is PERSISTED, so a process death mid-handshake resumes or
     * fails closed and never leaves a half-paired device.
     *
     * The screen's obligation is the one the user sees: a relaunch during pairing must not
     * present a fresh scanner as though nothing had happened. The machine may have committed --
     * and BeginPairing fail-fasts while a device is registered, so a scanner offered here leads
     * to a refusal the user cannot resolve from the handset (PB-STATE-10).
     */
    @Test
    fun `a relaunch during pairing resumes the recorded step rather than starting over`() {
        val resumed = PairingFlow.restore(persistedStep = PairingStep.AWAITING_MACHINE_DECISION)
        assertNotEquals(PairingStep.CONFIRM_DESTINATION, resumed.step)
        assertTrue(resumed.explainsInterruptedAttempt)
        assertFalse("a half-paired device is the one state neither path can leave", resumed.joined)
    }

    /** No persisted attempt is a first launch, and that path must still work. */
    @Test
    fun `a relaunch with no recorded attempt offers the scanner`() {
        val fresh = PairingFlow.restore(persistedStep = null)
        assertEquals(PairingStep.SCAN, fresh.step)
        assertFalse(fresh.explainsInterruptedAttempt)
    }
}
