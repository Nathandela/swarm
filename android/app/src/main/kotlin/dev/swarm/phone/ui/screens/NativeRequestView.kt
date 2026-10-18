package dev.swarm.phone.ui.screens

import android.content.Context
import android.text.Editable
import android.text.TextWatcher
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.TextView
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.activityRow
import dev.swarm.phone.ui.kit.approvalSheet
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.ctaStack
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.scrolledHorizontally
import dev.swarm.phone.ui.kit.textField
import org.json.JSONArray
import java.time.Instant

internal data class NativeRequestDraft(
    val requestHash: String,
    val requestId: String,
    val selections: List<List<String>>,
    val custom: List<String>,
)

private class NativeRequestForm(
    val snapshot: () -> NativeRequestDraft,
    val restore: (NativeRequestDraft) -> Unit,
    val resetSubmission: () -> Unit,
)

internal fun nativeRequestDraft(view: View?): NativeRequestDraft? =
    (view?.tag as? NativeRequestForm)?.snapshot?.invoke()

internal fun restoreNativeRequestDraft(draft: NativeRequestDraft?, view: View?) {
    if (draft != null) (view?.tag as? NativeRequestForm)?.restore?.invoke(draft)
}

internal fun resetNativeRequestSubmission(pressed: View) {
    var current: View? = pressed
    while (current != null) {
        (current.tag as? NativeRequestForm)?.let { it.resetSubmission(); return }
        current = current.parent as? View
    }
}

internal fun copyNativeRequestDrafts(from: View?, to: View?) {
    fun walk(view: View?, visit: (View) -> Unit) {
        if (view == null) return
        visit(view)
        if (view is ViewGroup) (0 until view.childCount).forEach { walk(view.getChildAt(it), visit) }
    }
    val drafts = mutableMapOf<Pair<String, String>, NativeRequestDraft>()
    walk(from) { view -> nativeRequestDraft(view)?.let { drafts[it.requestHash to it.requestId] = it } }
    walk(to) { view ->
        nativeRequestDraft(view)?.let { current -> restoreNativeRequestDraft(drafts[current.requestHash to current.requestId], view) }
    }
}

/** Native choices stay bound to the complete displayed request; a submitted reply locks the card. */
internal fun nativeRequestCard(
    context: Context,
    block: TranscriptBlock,
    onDecision: ((View, String, String, String, String) -> Unit)?,
): View {
    val request = requireNotNull(block.nativeRequest)
    val contents = ctaStack(context)
    val controls = mutableListOf<View>()
    val selections = request.questions.map { linkedSetOf<String>() }
    val custom = MutableList<EditText?>(request.questions.size) { null }
    val options = MutableList(request.questions.size) { mutableListOf<Pair<String, TextView>>() }
    val answerButtons = mutableListOf<Pair<String, TextView>>()
    var submitted = false
    val expiry = try { Instant.parse(request.expiresAt) } catch (_: Exception) { Instant.EPOCH }
    fun current(): Boolean = block.nativeAuthorityValid && !block.locked && !submitted &&
        request.detailsJson.isNotEmpty() && Instant.now().isBefore(expiry) && onDecision != null
    fun answers(): List<List<String>> = request.questions.mapIndexed { index, question ->
        val selected = question.options.map { it.label }.filter { it in selections[index] }
        val typed = custom[index]?.text?.toString().orEmpty()
        if (typed.isEmpty()) selected else if (question.multiple) selected + typed else listOf(typed)
    }
    fun update() {
        val enabled = current()
        controls.forEach { it.isEnabled = enabled }
        options.forEachIndexed { index, buttons ->
            buttons.forEach { (label, button) ->
                button.isSelected = label in selections[index]
                button.contentDescription = if (button.isSelected) "$label, selected" else label
                button.text = if (button.isSelected) "✓ $label" else label
            }
        }
        answerButtons.forEach { (kind, button) ->
            button.isEnabled = enabled && (kind != "question_reply" || answers().all { it.isNotEmpty() })
        }
    }
    fun send(pressed: View, decision: String) {
        if (!current()) { update(); return }
        val body = if (decision == "question_reply") {
            val groups = answers()
            if (groups.any { it.isEmpty() }) return
            JSONArray(groups.map { JSONArray(it) }).toString()
        } else ""
        submitted = true
        update()
        onDecision?.invoke(pressed, block.itemId, request.requestHash, decision, body)
    }
    contents.addView(monoWell(context, request.detailsJson).scrolledHorizontally())
    if (request.kind == "permission") {
        contents.addView(activityRow(context, request.permission, secondary = request.patterns.joinToString("\n")))
    } else {
        request.questions.forEachIndexed { index, question ->
            contents.addView(activityRow(context, question.question, secondary = question.header))
            question.options.forEach { option ->
                val button = ctaButton(context, option.label, CtaKind.MORE).apply {
                    tag = "native-request-option"
                    filterTouchesWhenObscured = true
                    setOnClickListener {
                        if (!current()) { update(); return@setOnClickListener }
                        val selected = option.label in selections[index]
                        if (!question.multiple) { selections[index].clear(); custom[index]?.setText("") }
                        if (selected) selections[index].remove(option.label) else selections[index].add(option.label)
                        update()
                    }
                }
                controls.add(button)
                options[index].add(option.label to button)
                contents.addView(button)
                if (option.description.isNotEmpty()) contents.addView(activityRow(context, option.description))
            }
            if (question.custom) {
                val field = textField(context, "Your answer").apply {
                    tag = "native-request-input"
                    filterTouchesWhenObscured = true
                    addTextChangedListener(object : TextWatcher {
                        override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
                        override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {
                            if (!question.multiple && !s.isNullOrEmpty()) selections[index].clear()
                            update()
                        }
                        override fun afterTextChanged(s: Editable?) = Unit
                    })
                }
                custom[index] = field
                controls.add(field)
                contents.addView(field)
            }
        }
    }
    val supported = if (request.kind == "permission") setOf("once", "always", "reject")
        else setOf("question_reply", "question_reject")
    request.decisions.filter { it.id in supported }.forEach { decision ->
        val button = ctaButton(context, decision.label, CtaKind.MORE).apply {
            tag = "native-request-action"
            filterTouchesWhenObscured = true
            setOnClickListener { send(this, decision.id) }
        }
        controls.add(button)
        answerButtons.add(decision.id to button)
        contents.addView(button)
    }
    if (!current()) contents.addView(emptyState(context, "This request is read-only or expired. Refresh its current state."))
    update()
    return approvalSheet(context, "", block.line, well = contents).apply {
        tag = NativeRequestForm(
            resetSubmission = { submitted = false; update() },
            snapshot = { NativeRequestDraft(request.requestHash, request.requestId, selections.map { it.toList() }, custom.map { it?.text?.toString().orEmpty() }) },
            restore = { draft ->
                if (draft.requestHash == request.requestHash && draft.requestId == request.requestId && draft.selections.size == selections.size && draft.custom.size == custom.size) {
                    selections.forEachIndexed { index, selected -> selected.clear(); selected.addAll(draft.selections[index]) }
                    custom.forEachIndexed { index, field -> field?.setText(draft.custom[index]) }
                    update()
                }
            },
        )
    }
}
