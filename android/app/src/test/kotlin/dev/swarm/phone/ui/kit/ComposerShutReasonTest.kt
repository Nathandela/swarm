package dev.swarm.phone.ui.kit

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the four reasons a composer can be shut, each with its
 * own sentence. Plan: docs/specifications/chat-surface-plan.md §7 D.8. Beads:
 * agents-tracker-tbpm.8, agents-tracker-tbpm.4.
 *
 * THE DEFECT THE OWNER PHOTOGRAPHED. One screen carried "Read-only -- take control to type."
 * above a "Take control" button, and four elements below it: "This session's structured
 * record broke, so it can no longer be typed into from the phone." The two conditions share
 * no term -- the first is drawn on `!leaseHeld` with no capability input at all, the second
 * on `!structuredChat` -- so both render together, and they contradict each other. That is
 * the DEFAULT state of every status-card session, because the phone's router has only two
 * arms and drops them onto the chat screen.
 *
 * AND THE SURVIVING SENTENCE IS OVER-SPECIFIC. It accuses the record of BREAKING while its
 * condition also covers "no record was ever authored", "the record is inconsistent" and
 * "this machine predates R8". Three of those four are not a break, and the remedy differs:
 * a torn record is permanent for the life of the session instance, an absent one is a
 * machine that never spoke, and neither is anything the reader did.
 *
 * ALL FOUR ARE DERIVABLE TODAY WITH NO WIRE CHANGE, which is why this is a copy-and-routing
 * fix rather than a protocol one. `structureTorn` is already computed from the transcript
 * holding a daemon-authored `structured_gap` element -- and is currently read by NOTHING,
 * which is the shape of a fact built for exactly this and never wired.
 */
class ComposerShutReasonTest {

    @Test
    fun aLiveChatSessionHasNoShutReason() {
        val a = ComposerModel.availabilityFor(
            online = true, structuredChat = true, recordTorn = false, ended = false,
        )
        assertEquals(ComposerAvailability.AVAILABLE, a)
        assertNull("an available composer explains nothing; it just works", ComposerModel.shutCopyFor(a))
    }

    @Test
    fun theFourReasonsAreDistinctStatesWithDistinctSentences() {
        val offline = ComposerModel.availabilityFor(
            online = false, structuredChat = true, recordTorn = false, ended = false,
        )
        val torn = ComposerModel.availabilityFor(
            online = true, structuredChat = false, recordTorn = true, ended = false,
        )
        val noChat = ComposerModel.availabilityFor(
            online = true, structuredChat = false, recordTorn = false, ended = false,
        )
        val ended = ComposerModel.availabilityFor(
            online = true, structuredChat = true, recordTorn = false, ended = true,
        )

        assertEquals(ComposerAvailability.OFFLINE, offline)
        assertEquals(ComposerAvailability.TORN, torn)
        assertEquals(ComposerAvailability.NO_CHAT, noChat)
        assertEquals(ComposerAvailability.ENDED, ended)

        val copies = listOf(offline, torn, noChat, ended).map {
            val c = ComposerModel.shutCopyFor(it)
            assertNotNull("every shut composer says why", c)
            c!!.placeholder
        }
        assertEquals(
            "four reasons, four sentences: one sentence over all of them is the accusation the " +
                "owner photographed",
            copies.size,
            copies.toSet().size,
        )
    }

    @Test
    fun onlyATornRecordAccusesTheRecord() {
        val torn = ComposerModel.shutCopyFor(
            ComposerModel.availabilityFor(online = true, structuredChat = false, recordTorn = true, ended = false),
        )!!
        val noChat = ComposerModel.shutCopyFor(
            ComposerModel.availabilityFor(online = true, structuredChat = false, recordTorn = false, ended = false),
        )!!

        assertEquals("a torn record says the chat is paused here (phone refit W5.4)", "Chat is paused here.", torn.placeholder)
        for (word in listOf("broke", "gap", "record")) {
            assertTrue(
                "a session that simply reports no chat surface must not be told its record " +
                    "$word -- three of the four states behind the old sentence were not a break",
                !noChat.placeholder.contains(word),
            )
        }
    }

    @Test
    fun aPermanentReasonOutranksATransientOne() {
        // Offline AND torn at once. Saying "not connected" would imply the composer returns
        // when the link does, and it never will: the degrade is one-way for the life of the
        // session instance. The permanent fact is the honest one to report.
        assertEquals(
            ComposerAvailability.TORN,
            ComposerModel.availabilityFor(online = false, structuredChat = false, recordTorn = true, ended = false),
        )
        // An ended session outranks everything: there is nothing to type into, whatever the
        // link or the record says.
        assertEquals(
            ComposerAvailability.ENDED,
            ComposerModel.availabilityFor(online = false, structuredChat = false, recordTorn = true, ended = true),
        )
    }

    @Test
    fun noReasonOffersAStepThatCannotProduceTyping() {
        for (a in listOf(
            ComposerAvailability.OFFLINE,
            ComposerAvailability.TORN,
            ComposerAvailability.NO_CHAT,
            ComposerAvailability.ENDED,
        )) {
            val copy = ComposerModel.shutCopyFor(a)!!
            val whole = copy.placeholder + " " + copy.detail
            for (banned in listOf("take control", "Take control")) {
                assertTrue(
                    "a shut composer must never point at $banned: it is a step that produces no " +
                        "typing on any route, and offering it beside a sentence saying typing is " +
                        "impossible is the contradiction this work exists to remove -- got $whole",
                    !whole.contains(banned),
                )
            }
        }
    }

    @Test
    fun anEndedSessionSaysOneThingAndStopsThere() {
        val ended = ComposerModel.shutCopyFor(ComposerAvailability.ENDED)!!
        assertEquals(
            "the drawing tables `composer.ended` as this placeholder alone, and draws no " +
                "composer for the state at all",
            "This session has ended",
            ended.placeholder,
        )
        assertTrue(
            "it is the one shut state with no remedy on the other side -- the other three all " +
                "end in `and you can still type at your machine`, and there is nothing here to " +
                "type AT. A second line can only restate the first in more words, and the one " +
                "that shipped (`Its conversation is kept; there is nothing to type into.`) was " +
                "on screen and on no copy sheet",
            ended.detail.isEmpty(),
        )
    }

    @Test
    fun theOfflineSentenceSaysNothingIsHeld() {
        val copy = ComposerModel.shutCopyFor(ComposerAvailability.OFFLINE)!!
        assertTrue(
            "input is live-only and never queued (ADR-007 B43); a composer that went quiet " +
                "without saying so invites the user to believe their words are waiting",
            copy.detail.isNotEmpty(),
        )
    }
}
