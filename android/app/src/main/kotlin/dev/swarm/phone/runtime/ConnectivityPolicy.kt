package dev.swarm.phone.runtime

/**
 * PB-RUN-3 -- what the relay socket does in every runtime condition the platform can put the
 * app in.
 *
 * This is the executable form of android/connectivity-policy.tsv, and the two are asserted
 * equal by ConnectivityPolicyTest. The table alone would be a document; this alone would be a
 * decision made in code review.
 *
 * The decision is ADR-007 B16: the foreground holds a connected socket and issues ADR-007
 * B7's bounded server-side waits; every background state closes the socket and relies on a
 * high-priority FCM wake. No foreground service ships in v1, because holding the socket would
 * force a foregroundServiceType of dataSync -- capped from API 34 at roughly six hours a day,
 * after which the system force-stops the service -- or specialUse, which is a Play-review
 * dependency on a personal tool.
 */

/** A runtime condition the policy has an answer for. [tableKey] is its row in the TSV. */
enum class RuntimeState(val tableKey: String) {
    FOREGROUND("foreground"),
    BACKGROUND("background"),
    DOZE("doze"),
    APP_STANDBY("app_standby"),
    BATTERY_SAVER("battery_saver"),
}

/** What happens to the relay socket in a state. */
enum class SocketDisposition(val tableKey: String) {
    CONNECTED("connected"),
    SUSPENDED("suspended"),
    CLOSED("closed"),
}

/** How the machine reaches the phone in a state. */
enum class WakePath(val tableKey: String) {
    SOCKET("socket"),
    PUSH("push"),
    NONE("none"),
}

/** What happens to an ALREADY-OUTSTANDING wait when the state is entered. */
enum class WaitOnEntry(val tableKey: String) {
    KEEP("keep"),
    CANCEL("cancel"),
}

data class ConnectivityRule(
    val socket: SocketDisposition,
    val maxWaitSeconds: Int,
    val foregroundService: Boolean,
    /** An Android foregroundServiceType token, or null when no service is held. */
    val foregroundServiceType: String?,
    val wakePath: WakePath,
    val waitOnEntry: WaitOnEntry,
)

object ConnectivityPolicy {

    /**
     * The server-side wait ceiling from ADR-007 B7 and section 6.0, chosen to sit under the
     * common 30-60 s idle-proxy timeout. No state may ask for longer than the relay will hold.
     */
    const val RELAY_MAX_WAIT_SECONDS = 25

    private val backgrounded = ConnectivityRule(
        socket = SocketDisposition.CLOSED,
        maxWaitSeconds = 0,
        foregroundService = false,
        foregroundServiceType = null,
        wakePath = WakePath.PUSH,
        waitOnEntry = WaitOnEntry.CANCEL,
    )

    fun ruleFor(state: RuntimeState): ConnectivityRule = when (state) {
        RuntimeState.FOREGROUND -> ConnectivityRule(
            socket = SocketDisposition.CONNECTED,
            maxWaitSeconds = RELAY_MAX_WAIT_SECONDS,
            foregroundService = false,
            foregroundServiceType = null,
            wakePath = WakePath.SOCKET,
            waitOnEntry = WaitOnEntry.KEEP,
        )
        // Backgrounded, Doze, App Standby and battery saver differ in how aggressively the
        // platform enforces them, not in what the app does: all four close the socket, so all
        // four share one rule rather than four copies that could drift apart.
        RuntimeState.BACKGROUND,
        RuntimeState.DOZE,
        RuntimeState.APP_STANDBY,
        RuntimeState.BATTERY_SAVER,
        -> backgrounded
    }
}
