package dev.swarm.phone.runtime

/**
 * PB-RUN-4 -- the FCM priority decision, in executable form. The recorded half is
 * android/fcm-priority.tsv and FcmPriorityPolicyTest asserts the two agree.
 *
 * Under [ConnectivityPolicy] the socket is closed in every background state, so push is the
 * only path from the machine to a phone that is not in the user's hand. Normal-priority
 * messages are deferred until Doze ends, which makes the cheap priority useless in exactly
 * the state the wake exists for.
 */

/** A class of push message. A new class added by a later slice needs a row in the TSV. */
enum class PushMessageClass(val tableKey: String) {
    WAKE("wake"),
}

enum class FcmPriority(val tableKey: String) {
    HIGH("high"),
    NORMAL("normal"),
}

/**
 * What happens when the high-priority quota is exhausted. The quota is per app per device and
 * Google publishes no number, so this half of the decision cannot be left implicit.
 */
enum class QuotaExhaustedBehaviour(val tableKey: String) {
    DEGRADE_TO_NORMAL("degrade_to_normal"),
    DROP("drop"),
    COALESCE("coalesce"),
    NOT_APPLICABLE("n/a"),
}

object FcmPriorityPolicy {

    fun priorityFor(messageClass: PushMessageClass): FcmPriority = when (messageClass) {
        PushMessageClass.WAKE -> FcmPriority.HIGH
    }

    fun onQuotaExhausted(messageClass: PushMessageClass): QuotaExhaustedBehaviour =
        when (messageClass) {
            // Pending wakes collapse into one message, so a burst of agent activity spends
            // the quota once. This is what D6 and PB-PUSH-0's debounce already do.
            PushMessageClass.WAKE -> QuotaExhaustedBehaviour.COALESCE
        }
}
