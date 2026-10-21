package dev.swarm.phone

/** Context needed to apply one machine-authored interaction read after it resolves. */
internal sealed interface PendingInteractionRead {
    data class History(val target: String, val speaks: Boolean) : PendingInteractionRead
    data class Detail(val itemId: String) : PendingInteractionRead
}

internal data class InteractionReadClaim(
    val operationId: String,
    val read: PendingInteractionRead,
)

/** Keeps overlapping interaction reads claimable until their exact, answered operation arrives. */
internal class InteractionReadLedger {
    private val reads = linkedMapOf<String, PendingInteractionRead>()

    fun record(operationId: String, read: PendingInteractionRead): Boolean {
        if (operationId.isEmpty() || reads.containsKey(operationId)) return false
        reads[operationId] = read
        return true
    }

    fun pending(): List<InteractionReadClaim> = reads.map { (operationId, read) ->
        InteractionReadClaim(operationId, read)
    }

    /** Retire only a matching operation with a nonblank machine code; unresolved answers stay claimable. */
    fun answered(
        operationId: String,
        outcomeOperationId: String,
        code: String,
    ): PendingInteractionRead? {
        if (operationId.isEmpty() || operationId != outcomeOperationId || code.isBlank()) return null
        return reads.remove(operationId)
    }
}
