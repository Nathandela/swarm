package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.kit.SendState

/** One locally sealed composer command, retained until outcome and transcript settle separately. */
data class ComposerSendRecord(
    val logicalId: String,
    val operationId: String,
    val sessionId: String,
    val expectedTurn: String = "",
    val text: String,
    val state: SendState = SendState.PENDING,
    val refusal: String = "",
    val notice: String = "",
    val detail: String = "",
    val answered: Boolean = false,
    val echoed: Boolean = false,
    val retrying: Boolean = false,
    val retryDispatched: Boolean = false,
    val retryAttempt: Int = 0,
)

/** Content-only durable projection returned by App.ComposerPublications. */
data class DurableComposerPublication(
    val logicalId: String,
    val operationId: String,
    val sessionId: String,
    val expectedTurn: String,
    val text: String,
    val phase: String,
    val terminalCode: String,
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
        sealed(operationId, sessionId, expectedTurn = "", text = text)
    }

    fun sealed(operationId: String, sessionId: String, expectedTurn: String, text: String) {
        if (operationId.isEmpty()) return
        val record = ComposerSendRecord(
            logicalId = operationId,
            operationId = operationId,
            sessionId = sessionId,
            expectedTurn = expectedTurn,
            text = text,
        )
        if (sends.putIfAbsent(operationId, record) == null) latestBySession[sessionId] = record
    }

    /**
     * Adopt the durable Go projection atomically. Exact operations keep any already-claimed UI
     * verdict; a fresh operation with the same LogicalID replaces that one bubble in its durable
     * FIFO slot. Text is content, never identity, so identical words under distinct logical ids
     * remain distinct messages.
     */
    fun hydrate(publications: List<DurableComposerPublication>) {
        val seenOperations = mutableSetOf<String>()
        val seenLogical = mutableSetOf<String>()
        val currentByLogical = sends.values.associateBy { it.logicalId }
        val adopted = linkedMapOf<String, ComposerSendRecord>()
        for (publication in publications) {
            require(
                publication.operationId.isNotEmpty() && publication.logicalId.isNotEmpty() &&
                    publication.sessionId.isNotEmpty() && publication.text.isNotEmpty(),
            ) { "invalid durable composer publication" }
            require(seenOperations.add(publication.operationId)) {
                "duplicate durable composer operation"
            }
            require(seenLogical.add(publication.logicalId)) {
                "duplicate durable logical composer send"
            }
            val current = sends[publication.operationId] ?: currentByLogical[publication.logicalId]
            if (current != null) {
                require(
                    current.logicalId == publication.logicalId &&
                        current.sessionId == publication.sessionId &&
                        current.expectedTurn == publication.expectedTurn &&
                        current.text == publication.text,
                ) { "durable composer identity changed content" }
            }
            val next = when {
                current == null -> ComposerSendRecord(
                    logicalId = publication.logicalId,
                    operationId = publication.operationId,
                    sessionId = publication.sessionId,
                    expectedTurn = publication.expectedTurn,
                    text = publication.text,
                )
                current.operationId == publication.operationId -> current
                else -> current.copy(
                    operationId = publication.operationId,
                    state = SendState.PENDING,
                    refusal = "",
                    notice = "",
                    detail = "",
                    answered = false,
                    echoed = false,
                    retrying = false,
                    retryDispatched = false,
                )
            }
            adopted[next.operationId] = next
        }
        sends.clear()
        sends.putAll(adopted)
        latestBySession.clear()
        for (record in sends.values) latestBySession[record.sessionId] = record
    }

    fun unansweredOperations(): List<String> = sends.values
        .filterNot { it.answered || it.retrying }
        .map { it.operationId }

    /**
     * Claim one terminal input_busy answer for retry without removing or duplicating its bubble.
     * Marking it before the facade call makes repeated event-driven redraws idempotent.
     */
    fun beginRetry(operationId: String): ComposerSendRecord? {
        val current = sends[operationId] ?: return null
        if (current.answered || current.echoed || current.retrying) return null
        val retrying = current.copy(
            retrying = true,
            retryDispatched = false,
            retryAttempt = current.retryAttempt + 1,
        )
        sends[operationId] = retrying
        if (latestBySession[current.sessionId]?.operationId == operationId) {
            latestBySession[current.sessionId] = retrying
        }
        return retrying
    }

    /**
     * Whether a scheduled retry may enter the facade FIFO now. Only an earlier retry that has not
     * entered that serial lane is an ordering barrier. An earlier operation with no answer may
     * have lost its reply during explicit mailbox recovery; treating that unknown outcome as a
     * barrier freezes every later, safely retryable input_busy send forever. Once the earlier retry
     * is dispatched, the command lane itself preserves A-before-B without waiting for A's next
     * machine outcome or transcript echo.
     */
    fun retryReady(operationId: String): Boolean {
        val current = sends[operationId] ?: return false
        if (!current.retrying) return false
        for ((id, earlier) in sends) {
            if (id == operationId) return true
            if (
                earlier.sessionId == current.sessionId &&
                earlier.retrying &&
                !earlier.retryDispatched
            ) {
                return false
            }
        }
        return false
    }

    /** Mark that the delayed attempt crossed into the facade lane and must not be replayed. */
    fun retryDispatched(operationId: String) {
        val current = sends[operationId] ?: return
        if (!current.retrying) return
        val dispatched = current.copy(retryDispatched = true)
        sends[operationId] = dispatched
        if (latestBySession[current.sessionId]?.operationId == operationId) {
            latestBySession[current.sessionId] = dispatched
        }
    }

    /**
     * Release cancelled the clock before these attempts reached the facade. Make only those
     * durable outcomes claimable again; an already-dispatched retry may have committed.
     */
    fun rearmScheduledRetries() {
        for ((operationId, current) in sends.toMap()) {
            if (!current.retrying || current.retryDispatched) continue
            val rearmed = current.copy(retrying = false)
            sends[operationId] = rearmed
            if (latestBySession[current.sessionId]?.operationId == operationId) {
                latestBySession[current.sessionId] = rearmed
            }
        }
    }

    /** Queue admission failed before any facade call ran; make the durable outcome claimable again. */
    fun retryRejected(operationId: String) {
        val current = sends[operationId] ?: return
        if (!current.retrying) return
        val rearmed = current.copy(retrying = false, retryDispatched = false)
        sends[operationId] = rearmed
        if (latestBySession[current.sessionId]?.operationId == operationId) {
            latestBySession[current.sessionId] = rearmed
        }
    }

    /** Replace the spent operation id while retaining the logical bubble's original FIFO slot. */
    fun retrySealed(previousOperationId: String, operationId: String) {
        if (operationId.isEmpty()) return
        val current = sends[previousOperationId] ?: return
        if (!current.retrying) return
        val replacement = current.copy(
            operationId = operationId,
            state = SendState.PENDING,
            refusal = "",
            notice = "",
            detail = "",
            answered = false,
            echoed = false,
            retrying = false,
            retryDispatched = false,
        )
        val ordered = sends.entries.toList()
        sends.clear()
        for ((id, record) in ordered) {
            if (id == previousOperationId) sends[operationId] = replacement else sends[id] = record
        }
        if (latestBySession[current.sessionId]?.operationId == previousOperationId) {
            latestBySession[current.sessionId] = replacement
        }
    }

    /** A facade-local retry refusal is terminal for this attempt and remains on its one bubble. */
    fun retryFailed(operationId: String, refusal: String, notice: String, detail: String) {
        val current = sends[operationId] ?: return
        if (!current.retrying) return
        val failed = current.copy(
            state = SendState.REFUSED,
            refusal = refusal,
            notice = notice,
            detail = detail,
            answered = true,
            retrying = false,
            retryDispatched = false,
        )
        sends[operationId] = failed
        if (latestBySession[current.sessionId]?.operationId == operationId) {
            latestBySession[current.sessionId] = failed
        }
    }

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
