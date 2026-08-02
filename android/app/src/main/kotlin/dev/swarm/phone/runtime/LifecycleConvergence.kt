package dev.swarm.phone.runtime

/**
 * PB-RUN-5 -- what the app does after each lifecycle event so that state converges rather
 * than corrupts.
 *
 * The durable half is PB-STATE-1/-2 in the Go core. This is the Android plumbing that decides
 * whether the core is ever given the chance to converge, and its load-bearing property is
 * negative: convergence must not DEPEND on a broadcast arriving. After a force-stop Android
 * puts the package in the stopped state and no implicit broadcast -- BOOT_COMPLETED included
 * -- reaches it until the user launches it by hand, so the plan reached with no broadcast at
 * all must equal the plan reached through one.
 */

enum class LifecycleEvent {
    COLD_START,
    BOOT_COMPLETED,
    PACKAGE_REPLACED,
    NETWORK_LOST,
    NETWORK_AVAILABLE,
}

data class ConvergencePlan(
    val resumesFromPersistedState: Boolean,
    val discardsPersistedState: Boolean,
    val reestablishConnection: Boolean,
    /** Re-establishes are metered by the relay, so the count is part of the plan. */
    val reestablishCount: Int,
    val cancelOutstandingWait: Boolean,
    val triggeredByBroadcast: Boolean,
)

object LifecycleConvergence {

    fun planFor(event: LifecycleEvent, hasPersistedState: Boolean): ConvergencePlan =
        when (event) {
            LifecycleEvent.COLD_START ->
                resume(hasPersistedState, triggeredByBroadcast = false)

            // Both are optimisations: they converge earlier than the next launch would, and
            // reach the same place. A force-stopped package receives neither.
            LifecycleEvent.BOOT_COMPLETED,
            LifecycleEvent.PACKAGE_REPLACED,
            -> resume(hasPersistedState, triggeredByBroadcast = true)

            LifecycleEvent.NETWORK_AVAILABLE ->
                resume(hasPersistedState, triggeredByBroadcast = false)

            // A wait bound to a network that has gone away does not fail; it hangs until some
            // timeout unrelated to the handoff, which on a Wi-Fi to cellular transition is the
            // difference between a keystroke landing and a session that looks alive.
            LifecycleEvent.NETWORK_LOST -> ConvergencePlan(
                resumesFromPersistedState = hasPersistedState,
                discardsPersistedState = false,
                reestablishConnection = false,
                reestablishCount = 0,
                cancelOutstandingWait = true,
                triggeredByBroadcast = false,
            )
        }

    /**
     * One re-establish, never one per stream: each is a metered operation against the relay's
     * tumbling one-minute window.
     */
    private fun resume(hasPersistedState: Boolean, triggeredByBroadcast: Boolean) =
        ConvergencePlan(
            resumesFromPersistedState = hasPersistedState,
            discardsPersistedState = false,
            reestablishConnection = hasPersistedState,
            reestablishCount = if (hasPersistedState) 1 else 0,
            cancelOutstandingWait = false,
            triggeredByBroadcast = triggeredByBroadcast,
        )
}
