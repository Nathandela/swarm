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
     * A denied camera must not be a dead end. PB-PAIR-2 requires a manual-entry fallback, and
     * PB-RUN-2 already names what is lost when CAMERA is withheld.
     */
    @Test
    fun `both denial paths offer manual entry`() {
        assertTrue(PairingFlow.offersManualEntry(ScannerState.PERMISSION_DENIED))
        assertTrue(PairingFlow.offersManualEntry(ScannerState.PERMISSION_PERMANENTLY_DENIED))
        assertFalse(PairingFlow.offersManualEntry(ScannerState.SCANNING))
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
     * PB-PAIR-5: five terminal states, each user-legible and each its own. Collapsed into one
     * "pairing failed" with prose beside it, the screen can only show an error string -- which
     * is the opaque error the requirement exists to remove, and three of these need genuinely
     * different next steps from the user.
     */
    @Test
    fun `every terminal pairing state is explicit and separately legible`() {
        val states = listOf(
            PairingStep.DECLINED,
            PairingStep.SAS_MISMATCH,
            PairingStep.RENDEZVOUS_TIMEOUT,
            PairingStep.QR_EXPIRED,
            PairingStep.ALREADY_PAIRED,
        )
        val messages = states.map { PairingFlow.messageFor(it) }
        messages.forEach { assertTrue("a terminal state with no message is an opaque error", it.isNotBlank()) }
        assertEquals("two states that read identically are one state", states.size, messages.toSet().size)
        states.forEach { assertTrue(PairingFlow.isTerminal(it)) }
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
