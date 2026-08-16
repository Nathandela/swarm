package dev.swarm.phone.ui.screens

/**
 * The GLOBAL INBOX screen model (wave R4, inbox.global; bead agents-tracker-0ox9): the aggregate
 * list across every pairing, every row keyed by the TUPLE (machine_id, session_id).
 *
 * THE TUPLE IS THE R4 EXIT CRITERION this surface exists for (ADR-018 MM4, playbook 4.2): two
 * machines may serve the same session id and the same title without colliding, and a display
 * title is never an authority. The fold below is therefore on the tuple and on nothing weaker.
 */
data class GlobalInboxRowModel(
    val machineId: String,
    val machineName: String,
    val sessionId: String,
    val title: String,
    val needsInput: Boolean,
)

object GlobalInboxScreen {

    /** Where the aggregate inbox's chevron goes: the switcher that named its entry. */
    const val BACK = "Back to computers"

    /** Row 8's block when no pairing holds a session yet. */
    const val EMPTY_COPY = "No sessions on any computer yet."

    /**
     * The row set as drawn: one row per (machine_id, session_id) TUPLE, in the order given, a
     * duplicate tuple folded to its first row -- two rows for one identity is the duplicate the
     * machine-switcher row set already refuses for machine ids. Session id alone is deliberately
     * NOT a key, and neither is the title.
     */
    fun rows(items: List<GlobalInboxRowModel>): List<GlobalInboxRowModel> =
        items.distinctBy { it.machineId to it.sessionId }

    /**
     * One row's second line: WHICH machine serves the session -- the half of the tuple a
     * single-machine inbox never had to say -- and whether it is waiting on the user.
     */
    fun meta(row: GlobalInboxRowModel): String =
        if (row.needsInput) "${row.machineName}, needs input" else row.machineName
}
