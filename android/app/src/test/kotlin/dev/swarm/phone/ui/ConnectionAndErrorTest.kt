package dev.swarm.phone.ui

import dev.swarm.phone.keys.ConnectionState
import dev.swarm.phone.keys.GoCustodyFailure
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-8 (connection and stale UX), PB-APP-9 (errors reach
 * the user) and PB-APP-10 (revoked and grant-loss states).
 *
 * THIS IS WHERE EVERY ERROR IDENTITY BECOMES A VISIBLE STATE. Other slices spent real effort
 * making failures DISTINGUISHABLE rather than collapsing them -- phonecore.ErrGrantLost was
 * given its own identity for exactly this, and its doc says so.
 *
 * PB-APP-10 NARROWS FROM THREE REMEDIES TO TWO (ADR-007 B133). The row that left is
 * `crypto.ErrKeyAuthRequired -> authenticate`, and it left because BOTH ends of it went: there
 * is no prompt in this app to offer, so `Remedy.AUTHENTICATE` was advice nobody could carry out,
 * and `ConnectionState.REAUTH_REQUIRED` had no producer once `mobile/relay.go` stopped emitting
 * it. The two that remain must still never collapse:
 *
 *   crypto.ErrKeyInvalidated  -> pair this device again (permanent, and the user CAN do it)
 *   phonecore.ErrGrantLost    -> the MACHINE must re-grant  <- the user cannot act on this one
 *
 * Sending a grant-loss user to "pair again" is a BRICK, not a wording problem: BeginPairing
 * fail-fasts while a device is registered, so the advice cannot be carried out and the only
 * exit is physical access to the machine (PB-STATE-10).
 *
 * The Go side proves the classes exist, are exhaustive and are produced by real scenarios
 * (mobile/s16_taxonomy_test.go and mobile/conformance/s16_errorstates_test.go). This side
 * proves the screen does something DIFFERENT with each.
 */
class ConnectionBannerTest {

    /**
     * Every transport state the facade can report has a banner, and they are not all the same.
     *
     * The list is the EXISTING dev.swarm.phone.keys.ConnectionState enum rather than a second
     * one here, for the reason that enum's own doc gives: the mapping must be total in the
     * direction the UI reads it, and `of` errors on a wire string it does not know -- so a
     * seventh state the facade starts reporting crashes the app until someone adds it.
     */
    @Test
    fun `each connection state renders its own banner`() {
        val banners = ConnectionState.entries.associateWith { ConnectionBanner.of(it) }
        banners.forEach { (state, banner) ->
            assertTrue("$state has no banner text", banner.text.isNotBlank())
        }
        assertEquals(
            "two states that render identically cannot be told apart by the user",
            ConnectionState.entries.size,
            banners.values.map { it.text }.toSet().size,
        )
    }

    /**
     * PB-APP-10 needs a SEVENTH state, and this is where its absence is a crash rather than a
     * cosmetic gap. relay.ErrRevoked is the only signal a revoked phone gets; the moment the
     * transport loop reports it, ConnectionState.of("revoked") hits its `error(...)` fallback
     * -- so the enum must grow the entry in the same change that makes the facade emit it.
     */
    @Test
    fun `revoked is a connection state the app understands`() {
        val revoked = ConnectionState.of("revoked")
        assertTrue(revoked.isTerminal)
        assertNotEquals(ConnectionState.REPAIR_REQUIRED, revoked)
    }

    /**
     * ONLINE IS THE ONLY QUIET STATE, and the custody verdict is NOT a transport condition.
     * S14 recorded the defect this asserts against: a refusal presented as "reconnecting" tells
     * the user to wait for a condition that waiting never ends.
     *
     * THE REAUTH ROW IS GONE FROM THIS TEST (ADR-007 B133) AND ITS SHAPE IS NOT. It asserted
     * that a recoverable custody refusal carried `Remedy.AUTHENTICATE` and no spinner; the state
     * and the remedy were removed atomically with `mobile/relay.go`'s `connReauthRequired` and
     * the taxonomy row, because a row kept on one side of that join is a banner nothing can
     * reach. Keeping it "for safety" is the vacuous case this file was most exposed to:
     * `each connection state renders its own banner` iterates the enum, so a producer-less row
     * would have gone on reading as covered. The property it carried -- a terminal state may
     * never show a spinner -- is asserted below over every state that has one.
     */
    @Test
    fun `a custody refusal is never rendered as a network condition`() {
        assertFalse(ConnectionBanner.of(ConnectionState.ONLINE).visible)
        assertTrue(ConnectionBanner.of(ConnectionState.RECONNECTING).visible)

        val repair = ConnectionBanner.of(ConnectionState.REPAIR_REQUIRED)
        assertEquals(Remedy.RE_PAIR, repair.remedy)
        assertFalse(repair.showsSpinner)
    }

    /**
     * A SPINNER IS A PROMISE THAT WAITING IS ENOUGH, over the whole enum rather than the two
     * rows somebody remembered.
     *
     * It matters more now that `reauth_required` is gone: the states left that end only when the
     * user acts are all TERMINAL, and every one of them is a screen the user reaches by doing
     * exactly what the product told them to do (revoking a lost handset, above all). One shown
     * behind a spinner is a phone that looks busy forever.
     */
    @Test
    fun `no terminal state is shown behind a spinner`() {
        for (state in ConnectionState.entries) {
            val banner = ConnectionBanner.of(state)
            if (!state.isTerminal) continue
            assertFalse(
                "$state has stopped retrying and shows a spinner, which tells the user to wait " +
                    "for something that is never going to happen",
                banner.showsSpinner,
            )
            assertTrue("$state is terminal and the banner does not say so", banner.terminal)
        }
    }

    /**
     * PB-APP-10's first half. relay.ErrRevoked is the only signal a revoked phone ever gets, and
     * mobile/relay.go's dial switch does not name it today -- so the phone redials every 250 ms
     * forever behind a spinner, which is the failure LOOP the requirement forbids in as many
     * words.
     */
    @Test
    fun `a revoked device is told to pair again and is not left spinning`() {
        val revoked = ConnectionBanner.of(ConnectionState.of("revoked"))
        assertEquals(Remedy.RE_PAIR, revoked.remedy)
        assertFalse(revoked.showsSpinner)
        assertTrue(revoked.terminal)
        assertNotEquals(
            "a revoked device and a destroyed Keystore key share a remedy and not a cause: the " +
                "owner must clear the machine-side record before a re-pair can succeed",
            ConnectionBanner.of(ConnectionState.REPAIR_REQUIRED).text,
            revoked.text,
        )
    }

    /** PB-APP-8's per-stream half: staleness is a property of one stream, never of the phone. */
    @Test
    fun `staleness is reported per stream and a repair in flight is its own state`() {
        val v = StreamView(stream = "journal", stale = true, resyncPending = false)
        assertEquals(StreamBadge.STALE, v.badge)
        assertTrue(v.notice.isNotBlank())

        val repairing = StreamView(stream = "journal", stale = true, resyncPending = true)
        assertEquals(StreamBadge.RESYNCING, repairing.badge)
        assertTrue(
            "PB-SYNC-3: the stale mark clears when the repair LANDS, never when it is asked for",
            repairing.stale,
        )

        val live = StreamView(stream = "reply", stale = false, resyncPending = false)
        assertEquals(StreamBadge.LIVE, live.badge)
        assertTrue(live.notice.isBlank())
    }

    /**
     * PB-APP-8's clock verdict, which S16 inherits as a residual: the phone must be able to
     * render the CURRENT verdict on a screen that opens after the event, and the banner must
     * clear when the user fixes the clock.
     *
     * It is not a transport state, and that matters: the daemon's refusal of a skewed command
     * reads "not authorized", which sends the user to re-pair when the fix is to correct their
     * clock.
     */
    @Test
    fun `the clock verdict is rendered from a pull and clears when it is empty`() {
        val skewed = ClockBanner.of("this device's clock is 2m0s ahead of the machine")
        assertTrue(skewed.visible)
        assertEquals(Remedy.FIX_CLOCK, skewed.remedy)
        assertNotEquals(Remedy.RE_PAIR, skewed.remedy)

        assertFalse("an empty verdict is a healthy clock, not an unknown one", ClockBanner.of("").visible)
    }
}

class ErrorRoutingTest {

    /**
     * The two remedies, kept apart. Driven from the TOKENS the facade stamps, because that is
     * all gomobile leaves of a Go error at the JNI boundary -- keycustody.go established the
     * shape for the custody verdicts and PB-APP-9 generalises it.
     *
     * IT WAS THREE (ADR-007 B133). `Remedy.AUTHENTICATE` went with its subject. The pair that is
     * left is the pair that was always the dangerous one to merge: both say "the key situation is
     * permanent", and only ONE of them names something the user can do from the handset.
     */
    @Test
    fun `the two remedies never collapse into each other`() {
        val rePair = ErrorRouter.route(SwarmErrorTokens.REPAIR_REQUIRED)
        val grantLost = ErrorRouter.route(SwarmErrorTokens.GRANT_LOST)

        assertEquals(Remedy.RE_PAIR, rePair.remedy)
        assertEquals(Remedy.MACHINE_REGRANT, grantLost.remedy)

        assertEquals(2, setOf(rePair.remedy, grantLost.remedy).size)
        assertEquals(2, setOf(rePair.state, grantLost.state).size)
    }

    /**
     * AND THE REMOVAL IS ATOMIC ON THIS SIDE TOO.
     *
     * `AUTHENTICATE` was one of `Remedy`'s rows and one of `ErrorRouter`'s answers. If either
     * came back without the other -- a remedy with no producer, or a routed state whose remedy
     * nothing renders -- the user meets a screen telling them to perform an authentication this
     * app has no way to collect. There is no scenario in which that is better than the honest
     * "pair this device again", so the absence is asserted rather than assumed.
     *
     * THE SECOND HALF IS THE ONE THAT KEEPS THE FIRST FROM BEING A RENAME. Deleting a remedy row
     * is worth nothing if the failures it used to answer now route to NONE, which is a screen
     * that states a problem and offers no way out -- PB-APP-10's subject, reached from the other
     * side. Every failure that lost `AUTHENTICATE` has to have landed on something a person or a
     * machine can actually do.
     *
     * IT IS ASSERTED ON THE REMEDY AND NOT ON THE PROSE, deliberately: the REPAIR_REQUIRED
     * message reads "no authentication brings it back", which is exactly right and which a
     * substring scan for "authenticate" flags as a violation. What the user is told to DO is the
     * remedy; the message explains why.
     */
    @Test
    fun `no routed failure asks for an authentication or leaves the user without a remedy`() {
        assertFalse(
            "Remedy still declares AUTHENTICATE. ADR-007 B133 removed every prompt from this " +
                "app, so it is advice that cannot be carried out",
            Remedy.entries.any { it.name == "AUTHENTICATE" },
        )
        for (token in listOf(
            SwarmErrorTokens.REPAIR_REQUIRED,
            SwarmErrorTokens.GRANT_LOST,
            SwarmErrorTokens.AWAITING_KEY,
            SwarmErrorTokens.STATE_CORRUPT,
            SwarmErrorTokens.REVOKED,
        )) {
            val routed = ErrorRouter.route(token)
            assertNotEquals(
                "$token routes to Remedy.NONE. Dropping AUTHENTICATE must not have left its " +
                    "failures with nothing at all: \"${routed.message}\"",
                Remedy.NONE,
                routed.remedy,
            )
            assertTrue("$token routes to a state with nothing to say", routed.message.isNotBlank())
        }
    }

    /**
     * PB-APP-10's second half, stated as the consequence rather than as an enum comparison: a
     * grant-loss screen must not offer the pairing flow, because BeginPairing refuses while this
     * device is still registered and the user would be left pressing a button that cannot work.
     */
    @Test
    fun `a grant-loss screen never offers the pairing flow`() {
        val grantLost = ErrorRouter.route(SwarmErrorTokens.GRANT_LOST)
        assertFalse(grantLost.offersPairing)
        assertTrue(
            "the user's action is on the MACHINE; the screen has to say which",
            grantLost.message.isNotBlank(),
        )
        assertTrue(ErrorRouter.route(SwarmErrorTokens.REPAIR_REQUIRED).offersPairing)
    }

    /**
     * NON-VACUITY. A router that answered one state for everything would satisfy nothing above
     * by accident -- but one that answered a REAL state for an unknown message would, and it is
     * the likelier mistake: an `else -> ErrorState.OFFLINE` branch reads as tidy and turns every
     * future error class into a network problem.
     */
    @Test
    fun `an unrecognised error is rendered as unknown and not as something plausible`() {
        val unknown = ErrorRouter.route("java.lang.IllegalStateException: something else entirely")
        assertEquals(ErrorState.UNKNOWN, unknown.state)
        assertEquals(Remedy.NONE, unknown.remedy)
        assertTrue("an unknown failure still has to say something to the user", unknown.message.isNotBlank())
    }

    /**
     * THE TOKENS ARE NOT RE-SPELLED. dev.swarm.phone.keys.GoCustodyFailure already holds the two
     * custody verdicts, as literals, because the unit-test JVM does not load the AAR -- and
     * mobile/s14_custody_test.go checks those literals against the Go constants in the direction
     * that matters. S16 must extend that arrangement rather than start a third copy: a
     * discriminator string that drifted would degrade a PERMANENT invalidation into a prompt the
     * user can never satisfy, which is the failure PB-KEY-6 already recorded once.
     *
     * android/gate/s16_ui_test.go is the Go-side half, checking every token in
     * mobile/error_taxonomy.tsv appears verbatim in these sources.
     */
    @Test
    fun `the custody tokens are the ones the keys module already owns`() {
        assertEquals(GoCustodyFailure.KEY_INVALIDATED_TOKEN, SwarmErrorTokens.REPAIR_REQUIRED)
        assertNotEquals(SwarmErrorTokens.REPAIR_REQUIRED, SwarmErrorTokens.GRANT_LOST)

        // THE AUTH-REQUIRED TOKEN STILL EXISTS AND NO LONGER HAS A TAXONOMY ROW (ADR-007 B133),
        // and both halves of that matter. `internal/remote/crypto` is FROZEN and still raises
        // `ErrKeyAuthRequired`, so the token has to keep classifying at the custody boundary --
        // `phonecore.openSealedDeviceKeys` refuses a Resume outright for anything it cannot
        // recognise. What it must NOT do is come back as a rendered state, because the state's
        // whole content was "prompt and it will connect".
        assertNotEquals(GoCustodyFailure.AUTH_REQUIRED_TOKEN, SwarmErrorTokens.REPAIR_REQUIRED)
        assertEquals(
            "the auth-required token is routed to a state of its own again. It has no remedy " +
                "this app can offer; PhoneRuntime routes the verdict to the permanent screen",
            ErrorState.UNKNOWN,
            ErrorRouter.route(GoCustodyFailure.AUTH_REQUIRED_TOKEN).state,
        )
    }
}
