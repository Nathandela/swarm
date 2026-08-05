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
 */
class RemedyControlsTest {

    private val holedRoster = TriageInbox.from(emptyList(), journalStale = true).staleNotice

    private val silentMachine = MachineFreshness(silent = true, lastHeardUnixMs = 0L)
        .notice { "09:14" }

    private fun bannerFor(state: ConnectionState) = StatusBanner.of(
        connection = ConnectionBanner.of(state),
        freshness = silentMachine,
        staleNotice = holedRoster,
    )

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

    @Test
    fun `a link whose remedy is pairing offers a control and not only a sentence`() {
        // The two states a phone stays IN THE APP for. `PairOnlyReason` records why the other two
        // terminal states never reach this banner: `transportEndsPairing` folds a revoked and a
        // repair_required handset into `paired = false`, so they get the pair-only screen. These
        // two do not end a pairing, so the banner is the only thing that speaks for them -- and it
        // said "Pair this phone again" with nothing on screen to press.
        for (state in listOf(ConnectionState.RELAY_UNTRUSTED, ConnectionState.RELAY_INSECURE)) {
            assertEquals(
                "$state tells the user to pair again and offers no control to do it with, which " +
                    "is PB-APP-10's failure loop written as advice",
                StatusBanner.PAIR_AGAIN,
                bannerFor(state).pairAgain,
            )
        }
    }

    @Test
    fun `a link the app is still retrying offers no pair-again control`() {
        for (state in listOf(
            ConnectionState.OFFLINE,
            ConnectionState.CONNECTING,
            ConnectionState.RECONNECTING,
        )) {
            assertEquals(
                "$state offers a pair-again control. Its remedy is to WAIT -- the link comes back " +
                    "on its own -- and a control offered here sends a user through a destructive " +
                    "re-pair for a dropped connection",
                "",
                bannerFor(state).pairAgain,
            )
        }
    }

    @Test
    fun `a quiet link offers nothing at all`() {
        val banner = StatusBanner.of(
            connection = ConnectionBanner.of(ConnectionState.ONLINE),
            freshness = null,
            staleNotice = "",
        )

        assertEquals(
            "a healthy phone carries a pairing control in the place its warnings go",
            "",
            banner.pairAgain,
        )
        assertTrue(banner.silent)
    }

    @Test
    fun `the control is not a fourth line of the banner`() {
        val banner = bannerFor(ConnectionState.RELAY_UNTRUSTED)

        assertFalse(
            "the control's words were folded into `lines`, so a view that draws one TextView per " +
                "line renders the button as a sentence -- prose again, one level down",
            banner.lines.contains(StatusBanner.PAIR_AGAIN),
        )
        assertTrue("a banner offering a control has nothing to say", banner.lines.isNotEmpty())
    }

    @Test
    fun `the empty banner offers no control`() {
        assertEquals("", StatusBanner.NONE.pairAgain)
    }

    // ---- a terminal link stops the app promising ---------------------------

    @Test
    fun `a terminal link carries its own line alone`() {
        val banner = bannerFor(ConnectionState.RELAY_UNTRUSTED)

        assertEquals(
            "a link that has STOPPED RETRYING still carries \"$holedRoster\" and " +
                "\"$silentMachine\" -- two promises of arrival under a transport that will never " +
                "deliver another byte. That is \"a spinner is a promise\", in words, and nothing " +
                "read `terminal` to end it",
            listOf(ConnectionBanner.of(ConnectionState.RELAY_UNTRUSTED).text),
            banner.lines,
        )
    }

    @Test
    fun `every terminal state suppresses the waiting facts and none of the others do`() {
        for (state in ConnectionState.entries) {
            val lines = bannerFor(state).lines
            if (state.isTerminal) {
                assertEquals(
                    "$state has stopped retrying and the banner still carries a fact that says " +
                        "\"yet\"",
                    1,
                    lines.size,
                )
            } else if (state != ConnectionState.ONLINE) {
                assertEquals(
                    "$state is a link the app is still working on, and the machine's own clock " +
                        "and the roster's completeness were suppressed anyway -- the fix " +
                        "swallowed two facts it was not about",
                    3,
                    lines.size,
                )
            }
        }
    }

    @Test
    fun `an online link still reports a silent machine and a holed roster`() {
        // ADR-007 D9's adversary answers every poll and withholds the frames, so ONLINE beside a
        // silent machine is the NORMAL pairing. ONLINE is not terminal and must not be swept up.
        val banner = bannerFor(ConnectionState.ONLINE)

        assertEquals(listOf(silentMachine, holedRoster), banner.lines)
    }

    // ---- NEEDS_LEASE offers the take-control press --------------------------

    @Test
    fun `a refusal for want of a lease offers the take-control press`() {
        val routed = ErrorRouter.route(SwarmErrorTokens.NEEDS_LEASE)
        val feedback = PressFeedback.ofRefusal(routed)

        assertTrue(
            "the machine refused this press for want of a lease and the screen offers nothing but " +
                "the sentence. The remedy names a control this app already has -- take control -- " +
                "and the screen's own lease fact is older than the refusal, so Stop stays labelled " +
                "Stop and the next press earns the same refusal",
            feedback.offersTakeControl,
        )
        assertEquals(
            "the refusal stopped reaching the outcome line once it started carrying an offer",
            routed.message,
            feedback.line,
        )
        assertEquals(routed.message, feedback.toast)
    }

    @Test
    fun `no other routed failure offers the take-control press`() {
        for (token in everyToken) {
            val routed = ErrorRouter.route(token)
            if (routed.remedy == Remedy.TAKE_CONTROL) continue
            assertFalse(
                "$token offers a take-control press. Its remedy is ${routed.remedy}, so the " +
                    "control would act on a lease that is not what is wrong",
                PressFeedback.ofRefusal(routed).offersTakeControl,
            )
        }
    }

    @Test
    fun `a refusal built from a bare sentence offers no control`() {
        // The three call sites that hand this model a screen's own words rather than a routed
        // error -- a refused kill, a severed lease, a refused push preference. None of them is a
        // remedy the router classified, and a control inferred from prose would be a guess.
        assertFalse(
            PressFeedback.ofRefusal("Your machine did not end this session.").offersTakeControl,
        )
        assertFalse(PressFeedback.ofSuccess("Interrupt sent").offersTakeControl)
        assertFalse(PressFeedback.ofUnsent(SessionDetail.NOT_SENT_NOTICE).offersTakeControl)
    }

    @Test
    fun `a routed error derives both offers from the one remedy`() {
        for (token in everyToken) {
            val routed = ErrorRouter.route(token)
            assertEquals(
                "$token's pairing offer disagrees with its own remedy, so the row can be got " +
                    "wrong in one place and not the other",
                routed.remedy == Remedy.PAIR || routed.remedy == Remedy.RE_PAIR,
                routed.offersPairing,
            )
            assertEquals(
                "$token's take-control offer disagrees with its own remedy",
                routed.remedy == Remedy.TAKE_CONTROL,
                routed.offersTakeControl,
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
