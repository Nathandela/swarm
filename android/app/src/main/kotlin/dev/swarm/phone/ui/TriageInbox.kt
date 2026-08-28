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
    /**
     * The one-line need summary: the journal record type, verbatim from the wire.
     *
     * IT STAYS VERBATIM HERE ON PURPOSE (agents-tracker-ksvb.2's ruling). THIS CLASS IS THE MODEL
     * -- "nothing here is computed on the handset", the class KDoc's own words -- and THE MODEL
     * KEEPS THE WIRE'S OWN WORD; turning a record type into a phrase a person reads is
     * presentation, which PB-DS-9 assigns to the screen that composes what a person reads, not to
     * the model of what the wire said. `TriageInboxScreen.of` is where the seven record types
     * become `Started`, `Waiting on you`, and so on, for the two surfaces the field-test audit
     * rated as human copy -- the inbox row's need line and the approval sheet's question -- and it
     * falls back to this field's OWN value, VERBATIM, for a record type it does not recognise: the
     * honesty fallback, never an invented phrase for a token this build does not know.
     *
     * Activity rows and the Session Detail transcript read this same field directly and get no
     * mapping at all: the ruling names them the machine's own register and rates them clean as
     * they already are.
     */
    val need: String,
    /**
     * Machine reachability, which is NOT staleness. Collapsing the two tells a user whose
     * machine is simply asleep that their view is untrustworthy, and a user whose view really
     * is holed that all is well as long as the machine answers.
     */
    val present: Boolean,
    /**
     * `swarmmobile.Session.Agent`, verbatim from the wire, and EMPTY MEANS THE MACHINE REPORTED
     * NONE -- never that nobody asked. mobile/types.go states both halves: the field is "the agent
     * identity the machine reported for this session", and "unlike Title it is never derived
     * on-device". So nothing downstream may fill a gap here from the title, the id, or a word like
     * "unknown"; the row simply draws no agent cell at all (`ui/kit/SessionRow.kt`).
     *
     * IT HAS NO DEFAULT ON PURPOSE, and [JournalRow.sessionId] one screen over is the same field
     * class deciding the same question the same way -- for a reason that was learned rather than
     * argued. That field was carried by the facade all along and `FacadeBridge.journal` did not
     * read it, so the activity feed could report that a session launched and not which one; its
     * KDoc records that "a default value here would have made the field optional at nine
     * construction sites and it would have gone unpopulated at whichever one nobody revisited,
     * which is how it was lost the first time".
     *
     * THE COLLAPSE A DEFAULT WOULD CAUSE IS THIS FIELD'S OWN SUBJECT, which is what makes the
     * precedent binding here rather than merely similar. A mapping that FORGOT the agent renders
     * exactly what a machine reporting none renders -- and since `sessionRow` now draws no cell for
     * an absent agent, the two are not even ambiguous on screen, they are identical: nothing.
     * Requiring the field spends one line at each construction site to buy a compiler that refuses
     * to let the next mapping be written wrong.
     */
    val agent: String,
    /**
     * `swarmmobile.Session.LastActivityUnixMs`, verbatim: the MACHINE's stamp of the session's
     * last activity in Unix milliseconds, and 0 MEANS NO RECORD HAS CARRIED ONE -- a daemon
     * predating the stamp, or a session the roster has not stamped yet. The inbox row draws no
     * age for 0, never the epoch's (phone-refit-playbook W7.1).
     *
     * IT HAS NO DEFAULT, for [agent]'s reason: a mapping that forgot the stamp would render
     * exactly what an unstamped session renders, and the compiler is the only thing that refuses
     * to let the next mapping be written wrong.
     */
    val lastActivityUnixMs: Long,
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
         * THE ORDER IS PART OF THE REQUIREMENT, not decoration, and it is the recorded design
         * mock's (docs/research/remote-control-mock.html:328-335): needs -> work -> ready -> done.
         * needs_input is first because it blocks on the user; working is second so live activity
         * is visible without scrolling; completed is last.
         */
        val TRIAGE_ORDER: List<String> =
            listOf("needs_input", "working", "ready_for_review", "completed")

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
