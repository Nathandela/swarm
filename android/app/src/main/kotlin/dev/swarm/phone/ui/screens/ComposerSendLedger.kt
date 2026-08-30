package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.kit.SendState

/** One locally sealed composer command, retained until outcome and transcript settle separately. */
data class ComposerSendRecord(
    val operationId: String,
    val sessionId: String,
    val text: String,
    val state: SendState = SendState.PENDING,
    val refusal: String = "",
    val notice: String = "",
    val detail: String = "",
    val answered: Boolean = false,
    val echoed: Boolean = false,
)

/**
 * Ordered per-operation composer state.
 *
 * A command outcome and its transcript echo are independent acknowledgements. The former is
 * still claimed after an early echo; the latter removes only its own provisional bubble. A
 * linked map preserves local sealing order across multiple in-flight sends.
 */
class ComposerSendLedger {
    private val sends = linkedMapOf<String, ComposerSendRecord>()
    private val latestBySession = mutableMapOf<String, ComposerSendRecord>()

    fun sealed(operationId: String, sessionId: String, text: String) {
        if (operationId.isEmpty()) return
        val record = ComposerSendRecord(operationId = operationId, sessionId = sessionId, text = text)
        if (sends.putIfAbsent(operationId, record) == null) latestBySession[sessionId] = record
    }

    fun unansweredOperations(): List<String> = sends.values
        .filterNot { it.answered }
        .map { it.operationId }

    fun settle(operationId: String, verdict: ComposerVerdict) {
        if (!verdict.answered) return
        val current = sends[operationId] ?: return
        val settled = if (current.echoed) {
            // A matching user_message is the provider's own proof that this operation landed.
            // A later outcome_unknown/commit refusal cannot make that delivered message refused.
            current.copy(
                state = SendState.SENT,
                refusal = "",
                notice = "",
                detail = "",
                answered = true,
            )
        } else {
            current.copy(
                state = verdict.state ?: current.state,
                refusal = verdict.refusal,
                notice = verdict.notice,
                detail = verdict.detail,
                answered = true,
            )
        }
        if (latestBySession[current.sessionId]?.operationId == operationId) {
            latestBySession[current.sessionId] = settled
        }
        if (settled.echoed && settled.state == SendState.SENT) {
            sends.remove(operationId)
        } else {
            sends[operationId] = settled
        }
    }

    fun observeEchoes(sessionId: String, operationIds: Set<String>) {
        if (operationIds.isEmpty()) return
        for ((operationId, current) in sends.toMap()) {
            if (current.sessionId != sessionId || operationId !in operationIds) continue
            // The transcript echo is authoritative about delivery. Keep an early echo's outcome
            // claim alive, but clear any provisional refusal already attached to this operation.
            val echoed = current.copy(
                state = SendState.SENT,
                refusal = "",
                notice = "",
                detail = "",
                echoed = true,
            )
            if (latestBySession[sessionId]?.operationId == operationId) {
                latestBySession[sessionId] = echoed
            }
            if (echoed.answered) {
                sends.remove(operationId)
            } else {
                sends[operationId] = echoed
            }
        }
    }

    fun pendingFor(sessionId: String): List<PendingSend> = sends.values
        .filter { it.sessionId == sessionId && !it.echoed }
        .map {
            PendingSend(
                operationId = it.operationId,
                text = it.text,
                refused = it.state == SendState.REFUSED || it.state == SendState.STALE_TURN,
                notice = it.notice,
                detail = it.detail,
            )
        }

    fun latestFor(sessionId: String): ComposerSendRecord? = latestBySession[sessionId]
}
