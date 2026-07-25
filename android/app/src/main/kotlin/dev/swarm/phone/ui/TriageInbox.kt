package dev.swarm.phone.ui

/**
 * Phase B slice S16 -- PB-APP-2: the triage inbox.
 *
 * THE SCREEN MODEL IS A PURE FUNCTION, which is the shape
 * dev.swarm.phone.runtime.PermissionStateResolver already established in this module, and its
 * reason holds here: the interesting behaviour is a mapping from what the phone core knows
 * onto what the user sees, and hiding it behind a view hierarchy makes the mapping untestable
 * while proving nothing about the view. [FacadeBridge] is what keeps that honest -- it builds
 * these inputs from the bound facade, so the model the tests drive is the model the screen
 * gets.
 *
 * THE GROUP IS NEVER DERIVED ON-DEVICE. `swarmmobile.Session.Group` is internal/status.Group's
 * string form, and a second derivation here would be a second source of truth about what a
 * session is doing.
 */

/** One session, as the wire describes it. Nothing here is computed on the handset. */
data class SessionRow(
    val id: String,
    val title: String,
    /** Verbatim from `swarmmobile.Session.Group`. One of [TriageInbox.TRIAGE_ORDER]. */
    val group: String,
    /** The one-line need summary: the journal record type verbatim, never an invented phrase. */
    val need: String,
    /**
     * Machine reachability, which is NOT staleness. Collapsing the two tells a user whose
     * machine is simply asleep that their view is untrustworthy, and a user whose view really
     * is holed that all is well as long as the machine answers.
     */
    val present: Boolean,
)

/**
 * One Group's section. An EMPTY section is still a section and says so: dropping it is the
 * obvious implementation and it is wrong for a triage screen, because the sections then move
 * under the user as sessions change group, and "nothing is waiting on me" -- the most useful
 * fact this screen can report -- becomes indistinguishable from "that section scrolled away".
 */
data class TriageSection(
    val group: String,
    val rows: List<SessionRow>,
) {
    val isEmpty: Boolean get() = rows.isEmpty()
}

/**
 * The four Groups as sections, in triage order, over a roster that says whether it is whole.
 */
data class TriageInbox(
    val sections: List<TriageSection>,
    /**
     * PB-APP-8 at this screen: the roster is rendered from the JOURNAL stream, so an inbox
     * drawn while that stream has an unrepaired hole may be missing a session, an exit or a
     * needs_input, and must not be presented as live.
     */
    val stale: Boolean,
) {
    val isEmpty: Boolean get() = sections.all { it.isEmpty }

    val staleNotice: String
        get() = if (stale) {
            "This list may be incomplete: some of your machine's activity has not arrived yet."
        } else {
            ""
        }

    companion object {

        /**
         * THE ORDER IS PART OF THE REQUIREMENT, not decoration. This is a triage screen, so the
         * group a user must act on has to be the one they see without scrolling: needs_input is
         * the agent blocked ON THEM, and working is the one group that needs nothing -- push
         * deliberately ignores it (internal/remotegw/push.go isWakeWorthy) -- so it goes last.
         */
        val TRIAGE_ORDER: List<String> =
            listOf("needs_input", "ready_for_review", "completed", "working")

        /**
         * @throws IllegalStateException on a group outside [TRIAGE_ORDER]. Loud, following the
         *  precedent dev.swarm.phone.keys.ConnectionState.of sets, and for a sharper reason
         *  here: a session quietly dropped from a triage inbox is a session the user never
         *  triages, which is the exact failure this screen exists to prevent.
         */
        fun from(sessions: List<SessionRow>, journalStale: Boolean): TriageInbox {
            sessions.forEach { row ->
                check(row.group in TRIAGE_ORDER) {
                    "PB-APP-2: session ${row.id} is in group ${row.group}, which is not one of " +
                        "$TRIAGE_ORDER. internal/status.Group grew a value this screen cannot place"
                }
            }
            return TriageInbox(
                sections = TRIAGE_ORDER.map { group ->
                    TriageSection(group = group, rows = sessions.filter { it.group == group })
                },
                stale = journalStale,
            )
        }
    }
}
