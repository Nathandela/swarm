package dev.swarm.phone.push

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android slice, scope item 1's Kotlin half:
 * the TOKEN-EVENT ROUTING decision table (ADR-015 P5, push-gateway-api.md sections 3.1-3.2,
 * PG-AUTH-5).
 *
 * Android hands this app an FCM token at exactly two moments -- `SwarmApplication.onCreate`'s
 * initial `getToken` and `SwarmMessagingService.onNewToken` -- and both already funnel into
 * one `PushTokens.register` (its own KDoc records why: two copies of "hand it on" is two
 * places to get the ordering wrong). What R3 adds underneath is the GATEWAY decision: a
 * token event is a fresh registration, a rotation, or a PG-AUTH-5 refresh, and which one it
 * is depends only on what this installation durably holds. That decision must be a named,
 * testable object -- not an if-ladder inside a Firebase callback -- because the failure
 * modes differ in kind: a REGISTER where a ROTATE belonged mints a second durable
 * installation that holds a live token for 180 days; a ROTATE where a REGISTER belonged
 * retries an unauthorized PUT forever while the phone silently never receives a wake.
 *
 * [PushRegistrationRouting] is the frozen contract this file names and GREEN supplies.
 * It is pure policy (no Context, no network, no Firebase), which is what makes the table
 * exhaustively testable on the JVM: the Go core owns the durable state and the wire calls
 * (phonecore's EnsurePushRegistration); this object owns only the decision's vocabulary so
 * the Kotlin callers and the facade agree on WHICH verb a token event is.
 *
 * WHAT ROBOLECTRIC/JVM CANNOT SAY, stated before the assertions: no real FCM, no real
 * rotation, no handset. PB-E2E-5 and R3's physical exit are untouched by this file.
 */
class PushRegistrationRoutingTest {

    /** First run: nothing durable, no installation -- the only honest verb is REGISTER. */
    @Test
    fun firstRunRegisters() {
        assertEquals(
            PushRegistrationRouting.Action.REGISTER,
            PushRegistrationRouting.decide(null, "token-alpha"),
        )
    }

    /**
     * A rotated token against a live installation is a ROTATE -- the single authenticated
     * PUT that touches no address, capability, wake key or pairing (PG-ROT-1). This is the
     * scope's "FCM token rotation triggers re-registration" at the decision layer: the
     * event always reaches the gateway, and it reaches it as a rotation, never as a second
     * REGISTER.
     */
    @Test
    fun rotationRotatesInsteadOfReRegistering() {
        assertEquals(
            PushRegistrationRouting.Action.ROTATE,
            PushRegistrationRouting.decide("token-alpha", "token-beta"),
        )
    }

    /**
     * The same token re-presented is the PG-AUTH-5 refresh: the app's periodic PUT with
     * its CURRENT token is what resets the 180-day inactivity clock, and it is idempotent
     * at the gateway. It must not be dropped as a no-op -- a phone that never refreshes is
     * expired and silently unreachable at day 180.
     */
    @Test
    fun sameTokenIsTheInactivityRefresh() {
        assertEquals(
            PushRegistrationRouting.Action.REFRESH,
            PushRegistrationRouting.decide("token-alpha", "token-alpha"),
        )
    }

    /**
     * An empty previous token is the first-run state however it was persisted: an empty
     * string must route exactly as null does, because two spellings of "nothing durable"
     * that route differently is a fresh install that rotates against an installation it
     * never registered.
     */
    @Test
    fun emptyPreviousTokenIsFirstRun() {
        assertEquals(
            PushRegistrationRouting.Action.REGISTER,
            PushRegistrationRouting.decide("", "token-alpha"),
        )
    }
}
