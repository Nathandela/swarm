package dev.swarm.phone.ui

import dev.swarm.phone.ui.screens.InboxScreen

/** UI-only single-flight state for an authoritative all-agent inbox refresh. */
class InboxRefreshState {
    private var baseline: Long? = null
    private var inFlight = false

    val refreshing: Boolean
        get() = inFlight

    /** Starts one refresh against [rosterRevision], refusing duplicate pulls until it settles. */
    fun begin(rosterRevision: Long): Boolean {
        if (inFlight) return false
        baseline = rosterRevision
        inFlight = true
        return true
    }

    /** Completes only when a later authoritative roster generation has committed. */
    fun observe(rosterRevision: Long): Boolean {
        val startedAt = baseline ?: return false
        if (rosterRevision == startedAt) return false
        baseline = null
        inFlight = false
        return true
    }

    /** Stops showing an unanswered request while retaining enough state to recognise a late reply. */
    fun expire(): Boolean {
        if (!inFlight) return false
        inFlight = false
        return true
    }

    /** A request refused before a roster could land returns the affordance to idle. */
    fun refused() {
        baseline = null
        inFlight = false
    }
}

/** Keeps the last successfully constructed inbox visible across transient read failures. */
class InboxScreenCache {
    private var last: InboxScreen? = null

    fun remember(screen: InboxScreen): InboxScreen = screen.also { last = it }

    fun fallback(fallback: InboxScreen): InboxScreen = last?.copy(
        rosterReady = fallback.rosterReady,
        refreshing = fallback.refreshing,
    ) ?: fallback

    fun clear() {
        last = null
    }
}
