package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.NativeDecisionFields
import dev.swarm.phone.ui.NativeQuestionFields
import dev.swarm.phone.ui.NativeQuestionOptionFields
import dev.swarm.phone.ui.NativeRequestFields
import org.json.JSONArray
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import java.time.Instant

@RunWith(RobolectricTestRunner::class)
class NativeRequestViewTest {
    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun request(kind: String = "permission") = NativeRequestFields(
        session = "m/s", sessionInstance = "instance", rootId = "ses_root", generation = "generation",
        nativeSessionId = "ses_child", requestId = "request", requestHash = "hash",
        expiresAt = Instant.now().plusSeconds(60).toString(), kind = kind,
        permission = "bash", patterns = listOf("printf marker"),
        detailsJson = "{\"command\":\"printf marker\"}",
        decisions = listOf(NativeDecisionFields("once", "Allow this request once", "single_request"),
            NativeDecisionFields("reject", "Reject pending permission requests in this session", "reject_session_pending_permissions")),
    )

    private fun block(request: NativeRequestFields, valid: Boolean = true) = TranscriptBlock(
        itemId = "item", kind = request.kind + "_request", line = "Native request",
        nativeRequest = request, nativeAuthorityValid = valid,
    )

    private fun all(view: View): List<View> = listOf(view) +
        if (view is ViewGroup) (0 until view.childCount).flatMap { all(view.getChildAt(it)) } else emptyList()

    @Test fun permissionShowsScopeAndLocksEveryChoiceBeforeCallback() {
        val sent = mutableListOf<String>()
        lateinit var card: View
        card = nativeRequestCard(context, block(request())) { _, item, decision, answers ->
            assertTrue(all(card).filter { it.isClickable }.none { it.isEnabled })
            sent.addAll(listOf(item, decision, answers))
        }
        val text = all(card).filterIsInstance<TextView>()
        assertTrue(text.any { it.text.toString().contains("printf marker") })
        assertTrue(text.any { it.text.toString().contains("Reject pending permission requests in this session") })
        text.single { it.text.toString() == "Allow this request once" }.performClick()
        assertEquals(listOf("item", "once", ""), sent)
    }

    @Test fun questionPreservesGroupsAndSupportsMultipleAndCustomAnswers() {
        val req = request("question").copy(
            questions = listOf(
                NativeQuestionFields("First?", "First", listOf(NativeQuestionOptionFields("one", "first option")), false, true),
                NativeQuestionFields("Second?", "Second", listOf(NativeQuestionOptionFields("x", ""), NativeQuestionOptionFields("y", "")), true, false),
            ),
            decisions = listOf(NativeDecisionFields("question_reply", "Send answers", "single_request"), NativeDecisionFields("question_reject", "Reject this question", "single_request")),
        )
        var answers = ""
        val card = nativeRequestCard(context, block(req)) { _, _, decision, body ->
            assertEquals("question_reply", decision)
            answers = body
        }
        val send = all(card).filterIsInstance<TextView>().single { it.text.toString() == "Send answers" }
        assertFalse(send.isEnabled)
        all(card).filterIsInstance<EditText>().single().setText("custom first")
        all(card).filterIsInstance<TextView>().single { it.text.toString() == "x" }.performClick()
        all(card).filterIsInstance<TextView>().single { it.text.toString() == "y" }.performClick()
        assertTrue(send.isEnabled)
        send.performClick()
        val groups = JSONArray(answers)
        assertEquals("custom first", groups.getJSONArray(0).getString(0))
        assertEquals(listOf("x", "y"), (0 until groups.getJSONArray(1).length()).map { groups.getJSONArray(1).getString(it) })
    }

    @Test fun unqualifiedOrExpiredRequestsHaveNoDecisionControls() {
        for (value in listOf(block(request(), false), block(request().copy(expiresAt = Instant.EPOCH.toString())))) {
            val card = nativeRequestCard(context, value) { _, _, _, _ -> fail("read-only request dispatched") }
            assertTrue(all(card).none { it.isClickable && it.isEnabled })
            assertTrue(all(card).filterIsInstance<TextView>().any { it.text.toString().contains("printf marker") })
        }
    }
}
