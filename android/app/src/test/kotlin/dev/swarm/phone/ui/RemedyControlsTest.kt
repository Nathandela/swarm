package dev.swarm.phone.ui

import dev.swarm.phone.keys.ConnectionState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-agre -- a remedy is a CONTROL, not a paragraph.
 *
 * ## The defect
 *
 * `RoutedError.remedy`, `RoutedError.offersPairing`, `ConnectionBanner.remedy`,
 * `ConnectionBanner.showsSpinner` and `ConnectionBanner.terminal` had ZERO production readers. Every
 * consumer took `.message` and nothing else, so the whole taxonomy reached the user as prose:
 *
 *  - `offersPairing`'s own KDoc says it exists so that "a screen cannot end up offering the pairing
 *    flow for a remedy that is not one". Nothing asked it, so it gated nothing -- the property was
 *    true of a field no screen consulted, which is a property of the file rather than of the app.
 *  - `NEEDS_LEASE` routes to [Remedy.TAKE_CONTROL] and names the step in words -- "Take control to
 *    type or to stop it" -- beside a control the user cannot press, because the screen's own lease
 *    fact is older than the refusal it just received. `ControlLease`'s KDoc records exactly this
 *    window: a lapsed lease "still reads as confirmed", so what the user gets is a routed refusal
 *    and a Stop button that will earn the same refusal again.
 *  - "A spinner is a promise that waiting is enough" was satisfied VACUOUSLY: no spinner exists, so
 *    nothing read `terminal` to stop looking busy -- and the app looks busy in WORDS. The roster's
 *    stale notice ends "has not arrived yet" and the machine's freshness line ends "yet"; both are
 *    promises of arrival, and under a link the transport has stopped retrying nothing will arrive.
 *
 * ## What this file asserts, and what it deliberately does not
 *
 * Three of the four remedies named in the issue become controls here. THE FOURTH IS LEFT OPEN ON
 * PURPOSE: [Remedy.REFRESH] is the remedy for `swarm/not-found`, and this app has no refresh
 * mechanism at all -- redraws come from `onResume` and from journal events. A control invented for
 * it would be an affordance with no verb behind it, which is the defect this issue is about,
 * wearing the opposite face.
 *
 * NO SPINNER IS BUILT EITHER. [ConnectionBanner.showsSpinner] stays unread, and honestly so: the
 * field says which states a spinner would be truthful in, and there is no spinner. What is wired is
 * its complement, [ConnectionBanner.terminal] -- the fact that the app has STOPPED retrying -- which
 * is the half that can silence a promise without drawing anything new.
 *
 * ## What moved out of this file, and where it went (agents-tracker-nx44.2)
 *
 * `StatusBanner` is deleted, so the eight assertions here that built one are gone with it. NOTHING
 * THEY CLAIMED IS UNCLAIMED: the composed sync status makes the same three claims in `SyncStatusTest`
 * -- a terminal link ranks BROKEN and nothing else does, a broken link's own facts do not also
 * render (`broken outranks every other fact being true at the same time`), and the remedy is a
 * control the detail sheet offers (`a revoked device offers the pairing destination and not a
 * resync`). What is left in this file is the half that was never the banner's: `ConnectionBanner`
 * deriving its own offer, and `PressFeedback` deriving the take-control press.
 */
class RemedyControlsTest {

    // ---- the pairing remedy becomes a control ------------------------------

    @Test
    fun `the connection banner says whether its remedy is the pairing flow`() {
        for (state in ConnectionState.entries) {
            val banner = ConnectionBanner.of(state)
            assertEquals(
                "ConnectionBanner does not derive the offer from its own remedy for $state, so " +
                    "the transport's opinion cannot gate a control the way a routed error's can",
                banner.remedy == Remedy.PAIR || banner.remedy == Remedy.RE_PAIR,
                banner.offersPairing,
            )
        }
    }

    // ---- NEEDS_LEASE offers the take-control press --------------------------

    // MOVED (owner ruling R1): "a refusal for want of a lease offers the take-control press"
    // becomes its opposite, because the press it named is deleted. The row still routes -- an
    // older machine can still answer swarm/no-lease -- so what must be asserted now is that it
    // lands honestly and promises nothing this app cannot do.
    @Test
    fun `a refusal for want of a lease promises nothing this phone can do`() {
        val routed = ErrorRouter.route(SwarmErrorTokens.NEEDS_LEASE)
        val feedback = PressFeedback.ofRefusal(routed)

        assertEquals(
            "the no-lease row still has to reach the reader: the ops this app sends need no " +
                "lease, so arriving here at all means an older machine or a plane this app " +
                "does not use, and silence would be worse than a sentence",
            routed.message,
            feedback.line,
        )
        assertEquals(routed.message, feedback.toast)
        assertFalse(
            "the remedy still names a control. Take control is gone from the product, so a " +
                "remedy that is actionable here would send the reader to a button that is not " +
                "on any screen",
            routed.remedy.actionableHere,
        )
    }

    // DELETED: "no other routed failure offers the take-control press". There is no
    // take-control press for any row to offer, which is the strongest form of that claim and
    // is fenced in Go by TestR1_NoProductionKotlinOffersTakeControl.

    // DELETED: "a refusal built from a bare sentence offers no control". Its subject was
    // that a control must never be INFERRED from prose the router did not classify. With no
    // control left to infer, the guarantee is structural rather than asserted.


    @Test
    fun `a routed error derives its pairing offer from its own remedy`() {
        for (token in everyToken) {
            val routed = ErrorRouter.route(token)
            assertEquals(
                "$token's pairing offer disagrees with its own remedy, so the row can be got " +
                    "wrong in one place and not the other",
                routed.remedy == Remedy.PAIR || routed.remedy == Remedy.RE_PAIR,
                routed.offersPairing,
            )
        }
    }

    private val everyToken = listOf(
        SwarmErrorTokens.UNKNOWN,
        SwarmErrorTokens.INTERNAL,
        SwarmErrorTokens.INVALID_REQUEST,
        SwarmErrorTokens.NOT_FOUND,
        SwarmErrorTokens.APP_CLOSED,
        SwarmErrorTokens.OFFLINE,
        SwarmErrorTokens.NOT_PAIRED,
        SwarmErrorTokens.STATE_CORRUPT,
        SwarmErrorTokens.DEVICE_UNSUPPORTED,
        SwarmErrorTokens.SYNCING,
        SwarmErrorTokens.AWAITING_KEY,
        SwarmErrorTokens.GRANT_LOST,
        SwarmErrorTokens.REPAIR_REQUIRED,
        SwarmErrorTokens.REVOKED,
        SwarmErrorTokens.NEEDS_LEASE,
        SwarmErrorTokens.RATE_LIMITED,
        SwarmErrorTokens.PAIRING_FAILED,
    )
}
