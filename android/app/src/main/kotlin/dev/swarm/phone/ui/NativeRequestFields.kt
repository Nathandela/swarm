package dev.swarm.phone.ui

import org.json.JSONArray
import org.json.JSONObject

data class NativeRequestFields(
    val session: String,
    val sessionInstance: String,
    val rootId: String,
    val generation: String,
    val nativeSessionId: String,
    val requestId: String,
    val requestHash: String,
    val expiresAt: String,
    val kind: String,
    val permission: String = "",
    val patterns: List<String> = emptyList(),
    val always: List<String> = emptyList(),
    val questions: List<NativeQuestionFields> = emptyList(),
    val decisions: List<NativeDecisionFields> = emptyList(),
    val detailsJson: String = "",
)
data class NativeQuestionFields(
    val question: String,
    val header: String,
    val options: List<NativeQuestionOptionFields>,
    val multiple: Boolean,
    val custom: Boolean,
)
data class NativeQuestionOptionFields(val label: String, val description: String)
data class NativeDecisionFields(val id: String, val label: String, val effect: String)

internal fun nativeRequestOf(item: JSONObject): NativeRequestFields? = try {
    if (item.optInt("v") != 2 || item.optString("kind") !in setOf("permission_request", "question_request")) null
    else item.optJSONObject("request")?.let { request ->
        fun required(key: String): String = request.getString(key).also { require(it.isNotEmpty()) }
        val kind = required("kind")
        require((kind == "permission" && item.getString("kind") == "permission_request") ||
            (kind == "question" && item.getString("kind") == "question_request"))
        val questions = request.optJSONArray("questions") ?: JSONArray()
        require(questions.length() <= 16)
        val choices = (0 until questions.length()).map { index ->
            val question = questions.getJSONObject(index)
            val options = question.getJSONArray("options")
            require(options.length() <= 64)
            require(question.get("multiple") is Boolean && question.get("custom") is Boolean)
            NativeQuestionFields(
                question = question.getString("question"), header = question.getString("header"),
                options = (0 until options.length()).map { option ->
                    val value = options.getJSONObject(option)
                    NativeQuestionOptionFields(value.getString("label"), value.getString("description"))
                }, multiple = question.getBoolean("multiple"), custom = question.getBoolean("custom"),
            )
        }
        val decisions = request.optJSONArray("decisions") ?: JSONArray()
        require(decisions.length() <= 8)
        NativeRequestFields(
            session = required("session"), sessionInstance = required("session_instance"),
            rootId = required("root_id"), generation = required("generation"),
            nativeSessionId = required("native_session_id"), requestId = required("request_id"),
            requestHash = required("request_hash"), expiresAt = required("expires_at"), kind = kind,
            permission = request.optString("permission"), patterns = nativeStrings(request, "patterns"),
            always = nativeStrings(request, "always"), questions = choices,
            decisions = (0 until decisions.length()).map { index ->
                val value = decisions.getJSONObject(index)
                NativeDecisionFields(value.getString("id"), value.getString("label"), value.getString("effect"))
            }, detailsJson = request.optString("details_json"),
        )
    }
} catch (_: Exception) { null }

private fun nativeStrings(value: JSONObject, key: String): List<String> {
    if (!value.has(key)) return emptyList()
    val array = value.getJSONArray(key)
    require(array.length() <= 128)
    return (0 until array.length()).map { array.getString(it) }
}
