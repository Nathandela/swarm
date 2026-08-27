package dev.swarm.phone.ui

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) -- **a resolution could not be paired to the decision it
 * resolves, because the one wire fact that pairs them was decoded away.**
 *
 * THE DEFECT. `interaction-schema.md` §3.6 gives `approval_resolved` an `interaction_id`, and
 * IS-APR-1 says what it is: *"The item's `item_id` **is** the `interaction_id` of ADR-007 D7."*
 * The phone RECEIVES it on every resolution and [ItemFields] threw it on the floor. A settled
 * decision card could therefore say only that it was settled -- never that it was answered at the
 * machine rather than here, and never at what time -- because the record that knows is a
 * DIFFERENT item, and nothing on this side could say which.
 *
 * WHY IT IS ONE KEY HERE AND NOT A PARSE AT THE PANEL. PB-DS-9 gives copy and composition to the
 * screen and `android/gate/i1_sheetandwell_test.go` fences the file: a screen that parsed JSON
 * would be a composition that also owned a wire format. So the decode belongs in exactly one
 * place, which is this one, and the panel gets a flat string.
 *
 * **THE NAMING TRAP IS THE POINT OF THE SECOND AND THIRD TESTS.** [ItemFields.interaction] and
 * [ItemFields.interactionId] are adjacent names for unrelated facts. `interaction` is §3.8's
 * `session_status` dimension -- `none` | `prompt` | `permission` | `unknown` -- a claim about what
 * the SESSION is currently waiting on. `interaction_id` is §3.6's identity of one REQUEST. A
 * reader who takes the second for a typed-up spelling of the first will pair a resolution to
 * whatever card is on screen and it will look plausible, which is the worst kind of wrong: the
 * card settles, the words are the CLI's, and only the pairing is fiction. So the decoder is
 * checked in BOTH directions -- neither key may fill the other's field -- rather than only in the
 * direction that was missing.
 *
 * IT RUNS UNDER ROBOLECTRIC AND IT MUST. [InteractionItem.fields] parses with `org.json`, which
 * the unit-test android.jar stubs to throw; without the runner every assertion below would pass
 * through [ItemFields]'s own catch and assert nothing at all, silently. That has bitten this wave
 * already.
 */
@RunWith(RobolectricTestRunner::class)
class InteractionItemFieldsTest {

    private fun item(kind: String, body: JSONObject) = InteractionItem(
        sessionId = "m/s1",
        itemId = "res-1",
        cursor = 2L,
        kind = kind,
        body = body.toString(),
    )

    /** §3.6, as the daemon sends it. */
    private fun resolution(requestItemId: String) = item(
        "approval_resolved",
        JSONObject()
            .put("item_id", "res-1")
            .put("kind", "approval_resolved")
            .put("interaction_id", requestItemId)
            .put("decision", "allowed")
            .put("by", "owner")
            .put("v", 1),
    )

    @Test
    fun `a resolution names the request it resolves`() {
        assertEquals(
            "the resolution's `interaction_id` is decoded away, so a settled card can say it is " +
                "settled and never WHERE it was answered -- the block that knows is a different " +
                "block and nothing pairs them (interaction-schema.md 3.6, IS-APR-1)",
            "req-9",
            resolution("req-9").fields().interactionId,
        )
    }

    @Test
    fun `a session status dimension never fills the request identity`() {
        val status = item(
            "session_status",
            JSONObject()
                .put("item_id", "st-1")
                .put("kind", "session_status")
                .put("process", "running")
                .put("turn", "active")
                .put("interaction", "permission")
                .put("v", 1),
        ).fields()

        assertEquals("3.8's dimension is still read", "permission", status.interaction)
        assertEquals(
            "`interaction` filled `interactionId`. They are adjacent names for unrelated facts -- " +
                "one says what the SESSION is waiting on, the other identifies one REQUEST -- and " +
                "a decoder that conflated them would pair every resolution to the string " +
                "\"permission\"",
            "",
            status.interactionId,
        )
    }

    @Test
    fun `a request identity never fills the status dimension`() {
        val resolved = resolution("req-9").fields()
        assertEquals(
            "`interaction_id` filled `interaction`, so a resolution would report the session as " +
                "waiting on an interaction named \"req-9\" -- a status value that is not one of " +
                "3.8's four and that no screen can render",
            "",
            resolved.interaction,
        )
        assertNotEquals(
            "the two fields hold the same value, so nothing about this decoder tells them apart",
            resolved.interaction,
            resolved.interactionId,
        )
    }

    /**
     * Both keys on one body. It is synthetic -- §3.6 and §3.8 are different kinds and no real
     * record carries both -- and that is exactly why it is worth asserting: [decode] is ONE flat
     * function over ANY body, so the only thing keeping the two apart is that it reads two keys.
     */
    @Test
    fun `a body carrying both keeps them apart`() {
        val both = item(
            "approval_resolved",
            JSONObject()
                .put("interaction", "permission")
                .put("interaction_id", "req-9")
                .put("v", 1),
        ).fields()
        assertEquals("permission", both.interaction)
        assertEquals("req-9", both.interactionId)
    }

    @Test
    fun `the pairing is an equality against the requests own item id`() {
        val request = InteractionItem(
            sessionId = "m/s1", itemId = "req-9", cursor = 1L, kind = "approval_request",
            body = JSONObject().put("item_id", "req-9").put("v", 1).toString(),
        )
        assertEquals(
            "the resolution does not pair to its request by IS-APR-1's equality -- the item's " +
                "`item_id` IS the `interaction_id`, so this is the whole of the join and there is " +
                "no other",
            request.itemId,
            resolution(request.itemId).fields().interactionId,
        )
    }

    @Test
    fun `a resolution that names nothing yields an absence and never an exception`() {
        val bare = item("approval_resolved", JSONObject().put("decision", "expired").put("v", 1))
        assertEquals(
            "an absent key must decode to empty. IS-ENV-3 and IS-COMPAT-2 make this the whole " +
                "posture at this boundary, and PhoneEvents posts a redraw on every interaction " +
                "event -- one throw out of a render takes the app down while an agent works",
            "",
            bare.fields().interactionId,
        )
        assertTrue("the rest of the fold still decodes", bare.fields().decision == "expired")
    }

    /**
     * The Robolectric guard, and it is first in intent even though it is last in the file: every
     * assertion above is "a string came back", which is precisely what a swallowed
     * `JSONException` also produces. If `org.json` is not real here, this file proves nothing.
     */
    @Test
    fun `the sandbox parses JSON at all, so the assertions above are about something`() {
        assertEquals(
            "org.json returned nothing for a body this test wrote itself, so `fields()` is " +
                "answering from its catch and every assertion in this file is vacuous",
            "allowed",
            resolution("req-9").fields().decision,
        )
    }
}
