package dev.swarm.phone.ui

import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

@RunWith(RobolectricTestRunner::class)
class NativeRequestFieldsTest {
    @Test fun `native question preserves exact choices and custom false`() {
        val body = """{"v":2,"kind":"question_request","authority_valid":true,"request":{"session":"ep/worker","session_instance":"instance","root_id":"ses_root","generation":"generation","native_session_id":"ses_child","request_id":"que_child","request_hash":"${"a".repeat(64)}","expires_at":"2035-01-01T00:00:00Z","kind":"question","details_json":"{\"id\":\"que_child\"}","questions":[{"header":"Choose","question":"Which paths?","multiple":true,"custom":false,"options":[{"label":"src","description":"Source files"},{"label":"test","description":"Test files"}]}],"decisions":[{"id":"question_reply","label":"Send answers","effect":"single_request"}]}}"""
        val fields = InteractionItem(itemId="card",cursor=1,kind="question_request",body=body).fields()
        assertTrue(fields.nativeAuthorityValid)
        val request = fields.nativeRequest!!
        assertEquals("ses_child", request.nativeSessionId)
        assertEquals("que_child", request.requestId)
        assertEquals("{\"id\":\"que_child\"}", request.detailsJson)
        assertTrue(request.questions.single().multiple)
        assertFalse(request.questions.single().custom)
        assertEquals(listOf("src", "test"), request.questions.single().options.map { it.label })
        assertEquals("single_request", request.decisions.single().effect)
    }
    @Test fun `malformed native binding is not a partial actionable card`() {
        val fields = InteractionItem(itemId="card",cursor=1,kind="permission_request",body="""{"authority_valid":true,"request":{"kind":"permission","permission":"bash"}}""").fields()
        assertNull(fields.nativeRequest)
        assertFalse(fields.nativeAuthorityValid)
    }
}
