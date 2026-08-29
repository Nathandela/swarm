package dev.swarm.phone.ui

import dev.swarm.phone.ui.screens.InboxScreen

/** UI-only single-flight state for an authoritative all-agent inbox refresh. */
class InboxRefreshState {
    private var baseline: Long? = null

    val refreshing: Boolean
        get() = baseline != null

    /** Starts one refresh against [rosterRevision], refusing duplicate pulls until it settles. */
    fun begin(rosterRevision: Long): Boolean {
        if (baseline != null) return false
        baseline = rosterRevision
        return true
    }

    /** Completes only when a later authoritative roster generation has committed. */
    fun observe(rosterRevision: Long): Boolean {
        val startedAt = baseline ?: return false
        if (rosterRevision == startedAt) return false
        baseline = null
        return true
    }

    /** A request refused before a roster could land returns the affordance to idle. */
    fun refused() {
        baseline = null
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
