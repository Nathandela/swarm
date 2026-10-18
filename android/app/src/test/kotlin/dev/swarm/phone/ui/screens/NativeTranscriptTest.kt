package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

@RunWith(RobolectricTestRunner::class)
class NativeTranscriptTest {
    private fun request(valid: Boolean) = InteractionItem(
        itemId="request",cursor=3,kind="permission_request",status="in_progress",
        body="""{"v":2,"kind":"permission_request","authority_valid":$valid,"provenance":"reconciled","request":{"session":"ep/worker","session_instance":"instance","root_id":"ses_root","generation":"generation","native_session_id":"ses_child","request_id":"per_child","request_hash":"${"a".repeat(64)}","expires_at":"2035-01-01T00:00:00Z","kind":"permission","permission":"bash","patterns":["echo *"],"always":["echo *"],"details_json":"{}","decisions":[{"id":"once","label":"Allow this request once","effect":"single_request"}]}}""",
    )
    @Test fun `native request dispatch exposes exact DTO and gap disables controls`() {
        val active=TranscriptScreen.of(listOf(request(true))).blocks.single()
        assertEquals("per_child",active.nativeRequest!!.requestId)
        assertTrue(active.nativeAuthorityValid)
        assertFalse(active.approval)
        val gap=TranscriptScreen.of(listOf(request(false))).blocks.single()
        assertNotNull(gap.nativeRequest)
        assertFalse(gap.nativeAuthorityValid)
        assertTrue(gap.locked)
    }
    @Test fun `native assistant snapshot renders prose without closing a generic later turn`() {
        val user=InteractionItem(itemId="user",cursor=1,kind="user_message",text="later",turnId="new-turn")
        val reply=InteractionItem(itemId="native",cursor=2,kind="message_snapshot",status="completed",text="**Earlier** answer",turnId="old-turn",body="""{"role":"assistant","provenance":"imported"}""")
        val screen=TranscriptScreen.of(listOf(user,reply))
        assertEquals("Earlier answer",screen.blocks[1].line)
        assertEquals("new-turn",screen.latestTurnId)
    }
    @Test fun `native imported input outcome names execution without a fresh turn completion`() {
        val outcome=InteractionItem(itemId="outcome",cursor=2,kind="input_outcome",status="completed",turnId="old-turn",body="""{"provenance":"imported"}""")
        val screen=TranscriptScreen.of(listOf(outcome))
        assertEquals("Earlier input completed",screen.blocks.single().line)
        assertEquals("",screen.latestTurnId)
    }
    @Test fun `clipped native messages offer retained detail and retire unavailable offers`() {
        for (role in listOf("user", "assistant")) {
            val item = InteractionItem(itemId="clipped", cursor=0, kind="message_snapshot", text="partial", truncated=true, detail=true, body="""{"role":"$role","provenance":"imported"}""")
            assertTrue(TranscriptScreen.of(listOf(item)).blocks.single().offersDetail)
            assertFalse(TranscriptScreen.of(listOf(item), withoutDetail=setOf(item.itemId)).blocks.single().offersDetail)
            assertFalse(TranscriptScreen.of(listOf(item.copy(truncated=false))).blocks.single().offersDetail)
            assertFalse(TranscriptScreen.of(listOf(item.copy(detail=false))).blocks.single().offersDetail)
        }
    }

}
