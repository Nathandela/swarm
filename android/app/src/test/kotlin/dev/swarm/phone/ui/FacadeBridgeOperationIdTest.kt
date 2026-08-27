package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for owner ruling R6 -- **the one fact that can settle a sent
 * bubble crossed the whole system and stopped one hop short of the screen.**
 *
 * R6 IS: a sent bubble is PENDING until the agent's own transcript echoes it back. That rule
 * exists because the acknowledgement this app already had cannot carry it. `composer_send` is
 * acknowledged when the daemon wrote bytes into a PTY, not when the CLI accepted them, so a
 * bubble that settled on the acknowledgement would claim a delivery the wire cannot back --
 * ADR-009 (6)'s "delivery unknown rendered as sent is a lie", stated as a ruling. The echo, and
 * only the echo, is the delivery.
 *
 * MATCHING ON TEXT IS THE THING THAT MAY NOT BE DONE, and the daemon has already probed why:
 * an owner typing "yes" at the machine while a phone send of "yes" is pending produces two
 * indistinguishable prompts, and the phone would settle the wrong one. `skeleton/chat.go`
 * refuses to rely on it, which is why the daemon stamps its own operation id onto the prompt
 * -- it is the only party that watched the injection -- and why `phonecore.Item.OperationID`
 * and `mobile.TranscriptItem.OperationID` both carry the R6 argument out in full.
 *
 * WHERE IT DIED. `FacadeBridge.transcript()` mapped `detail`, `toolKind`, `turnId`, `tsUnixMs`
 * and `source` under a comment saying they were mapped there *"so they cross the boundary
 * instead of dying one hop short of the screen"* -- and `operationId` was not in the list. The
 * comment described the fix; the field was missing from it. `mobile/screen_coverage.tsv`'s
 * `transcript.read` row already argues at length that this field rides that read for exactly
 * this purpose, and `mobile/app.go` already assigns it. Every layer was complete except the one
 * that hands the item to a renderer, so the settle could never fire and every sent bubble stayed
 * pending forever.
 *
 * WHAT THIS FILE CAN AND CANNOT SEE, stated plainly, as [FacadeBridgeTest] states its own limit:
 * [FacadeBridge] takes a `swarmmobile.App`, a gomobile class backed by .so files cross-compiled
 * for Android ABIs, so it CANNOT BE CONSTRUCTED on the unit-test JVM and nothing here calls
 * `transcript()`. What is asserted is the SHAPE the mapping fills -- that the field exists on
 * the item, that an unclaimed item's absence stays absent, and that a matcher written against it
 * settles exactly one send. That the bridge's `item.getOperationID()` call is present is a
 * source fact, and `android/gate/r6_chat_ui_test.go` is where source facts about this mapping
 * are fenced -- it already loops over the other five getters for this reason.
 *
 * IT READS NO JSON. Nothing here calls [InteractionItem.fields], which parses with Android's
 * `org.json` and throws outside the sandbox -- a test that did would need
 * `@RunWith(RobolectricTestRunner::class)` or it would silently assert nothing. The operation id
 * is a TOP-LEVEL field for that family of reasons: it is stamped by the daemon rather than
 * authored by the CLI, so it never rides in the item object the way section 3's fields do.
 */
class FacadeBridgeOperationIdTest {

    private fun item(itemId: String, operationId: String) = InteractionItem(
        sessionId = "m/s1",
        itemId = itemId,
        cursor = 1L,
        kind = "user_message",
        operationId = operationId,
    )

    @Test
    fun `an item carries which of this phones sends the agent echoed back`() {
        assertEquals(
            "the transcript item does not carry the operation id, so the phone has nothing to " +
                "match an echo against and no sent bubble can ever settle (owner ruling R6)",
            "op-7",
            item("i1", "op-7").operationId,
        )
    }

    @Test
    fun `an item nobody claimed carries an absence and not a match`() {
        val ownersOwnPrompt = InteractionItem(itemId = "i2", cursor = 2L, kind = "user_message")
        assertEquals(
            "the default must be empty. An item the daemon did not stamp -- an owner's own " +
                "prompt typed at the machine, or any item from a machine that predates R6 -- has " +
                "no operation to name, and an absent fact stays absent rather than being " +
                "invented at this seam",
            "",
            ownersOwnPrompt.operationId,
        )
    }

    @Test
    fun `an echo settles exactly the send it names`() {
        val sent = "op-7"
        val transcript = listOf(
            item("i1", ""),
            item("i2", "op-6"),
            item("i3", sent),
            item("i4", ""),
        )
        val settled = transcript.filter { it.operationId == sent }
        assertEquals(
            "the echo matched ${settled.size} items. R6 settles ONE bubble -- the one this " +
                "phone sent -- and a match that hit more than one would settle a message the " +
                "agent never received",
            1,
            settled.size,
        )
        assertEquals("i3", settled.single().itemId)
    }

    /**
     * The absence is SHARED, which is the trap a matcher has to be written around: every item
     * nobody claimed carries the same empty string, so an equality that does not guard on
     * non-empty settles on whatever the agent said first. Asserted here because the guard belongs
     * to a renderer this lane does not own, and the fact it must be written against is this one.
     */
    @Test
    fun `every unclaimed item carries the same absence, so a matcher must guard on it`() {
        val transcript = listOf(item("i1", ""), item("i2", ""), item("i3", "op-7"))
        val unguarded = transcript.count { it.operationId == "" }
        assertEquals(
            "an unnamed send compared by bare equality matches " + unguarded + " items at once. " +
                "The absence is SHARED -- an owner's own prompt and an item from a machine that " +
                "predates R6 carry the same empty string -- so the matcher that settles a bubble " +
                "must require a non-empty id BEFORE it compares, or a send this phone could not " +
                "name settles on whatever the agent happened to say first",
            2,
            unguarded,
        )
        assertEquals(
            "a claimed item is not swept up by the absence",
            1,
            transcript.count { it.operationId.isNotEmpty() },
        )
    }

    /**
     * The R6 argument's negative half, kept as an assertion rather than left in prose: two items
     * carrying the same words are still two items, and only the stamp tells them apart.
     */
    @Test
    fun `two identical prompts are told apart by the stamp and never by the text`() {
        val phoneSend = InteractionItem(
            itemId = "i1", cursor = 1L, kind = "user_message",
            text = "yes", source = "phone", operationId = "op-7",
        )
        val ownerTyped = InteractionItem(
            itemId = "i2", cursor = 2L, kind = "user_message",
            text = "yes", source = "owner",
        )
        assertEquals(phoneSend.text, ownerTyped.text)
        assertNotEquals(
            "the two prompts are indistinguishable, which is the mis-attribution the daemon has " +
                "probed and refuses to rely on -- an owner-typed \"yes\" while a phone send of " +
                "\"yes\" was pending",
            phoneSend.operationId,
            ownerTyped.operationId,
        )
        assertTrue(
            "only the phone's own send may be settled by its id",
            phoneSend.operationId.isNotEmpty() && ownerTyped.operationId.isEmpty(),
        )
    }
}
