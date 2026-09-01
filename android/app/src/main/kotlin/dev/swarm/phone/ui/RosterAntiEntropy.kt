package dev.swarm.phone.ui

/**
 * Foreground-only, non-destructive roster anti-entropy.
 *
 * The machine reply is authoritative only when [rosterRevision] advances, so completion is not
 * the facade call returning. Foreground and an observed offline-to-online edge each arm one
 * request; redraws cannot. A lost request expires to idle without arming itself again: another
 * lifecycle/network edge may retry, but a timer cannot turn an unavailable relay into a hot loop.
 */
class RosterAntiEntropy(private val timeoutMillis: Long) {
    private var pending = false
    private var lastOnline: Boolean? = null
    private var baselineRevision: Long? = null
    private var admittedAtMillis = 0L

    init {
        require(timeoutMillis > 0L)
    }

    /** Arm one attempt for the current foreground generation. */
    fun foreground() {
        pending = true
    }

    /**
     * Observe current authority and transport. Returns true exactly when the caller should issue
     * the passive facade verb now.
     */
    fun observe(online: Boolean, rosterRevision: Long, nowMillis: Long): Boolean {
        val wasOnline = lastOnline
        lastOnline = online
        if (wasOnline == false && online) pending = true

        val baseline = baselineRevision
        if (baseline != null) {
            if (rosterRevision != baseline) {
                baselineRevision = null
                pending = false
            } else if (nowMillis - admittedAtMillis >= timeoutMillis) {
                // Expiry returns to idle. It does not re-arm itself: foreground/network recovery
                // are the only automatic triggers, which bounds a silent or hostile relay. Keep
                // a trigger that actually arrived while this attempt was crossing.
                baselineRevision = null
            }
        }
        if (!online || !pending || baselineRevision != null) return false

        baselineRevision = rosterRevision
        admittedAtMillis = nowMillis
        pending = false
        return true
    }

    /** A facade/dispatch refusal authored no request and must not schedule an automatic retry. */
    fun refused() {
        baselineRevision = null
        pending = false
    }

    /** Prevent a background/replaced surface from spending an unissued foreground trigger. */
    fun release() {
        pending = false
    }
}
