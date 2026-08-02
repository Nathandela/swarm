package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.TriageInbox

/**
 * Phase B slice S24 -- PB-DS-9: the triage inbox's SCREEN MODEL.
 *
 * WHY THERE IS A SECOND MODEL BESIDE [TriageInbox]. That one answers what the ROSTER is: four
 * Groups, their sections, and whether the list is whole. This answers what the SCREEN says about
 * it -- the heading each Group renders, what an empty section says, what the live counter counts,
 * what the badge counts, which scope chips exist and what a screen reader hears. Every one of
 * those is copy or arithmetic, and PB-DS-9 assigns both to the screen: the kit "takes data, not
 * views or copy".
 *
 * IT IS A PURE FUNCTION OVER [TriageInbox], which is the shape this module already uses for
 * screen logic (`PermissionStateResolver`, `TriageInbox` itself). The interesting behaviour is a
 * mapping from what the phone core knows onto what a person reads, and hiding it behind a view
 * hierarchy makes the mapping untestable while proving nothing about the view.
 *
 * THE ORDER IS [TriageInbox.TRIAGE_ORDER]'S. Inventory C1 draws the artifact's sections as
 * `Needs you / Working / Ready for review / Done`; the model declares needs_input,
 * ready_for_review, completed, working and argues for it -- working is the one Group that needs
 * nothing from the user, so on a triage surface it goes last. The recorded copy is the artifact's;
 * the order is the product's, and this file reads it rather than restating it.
 */
data class InboxScreen(
    /** `.pnav .big`. Inventory C1.1. */
    val title: String,
    /** `.pnav .live`, or null when nothing is in flight -- a counter reading zero says nothing. */
    val live: String?,
    val scopes: List<ScopeChip>,
    val sections: List<InboxSection>,
    val tabs: List<InboxTab>,
    /**
     * PB-APP-8's verdict for this screen, in [TriageInbox]'s own words.
     *
     * IT IS NOT RENDERED BY THE INBOX VIEW and that is a scope statement rather than an omission:
     * inventory C1's composition is four elements and none of them is a notice, and the design's
     * only body-copy block (derivation row 8) is the empty state. `PhoneSurface` renders this on
     * the line it already gives the connection banner and the freshness notice, which is where a
     * user reads everything else the transport has to say.
     */
    val staleNotice: String,
)

/**
 * One scope chip. `machine` is null for the "All machines" scope, which is the only chip that
 * names no machine and therefore the only one with no presence to report.
 */
data class ScopeChip(
    val machine: String?,
    val label: String,
    /** `.chip .pd`, or null where there is no machine for a dot to be about. */
    val present: Boolean?,
    val selected: Boolean,
    /**
     * What a screen reader says instead of [label]. The presence dot is a compound drawable and a
     * drawable cannot be described, so the chip is the only view that can speak for it -- and null
     * rather than the empty string where there is nothing extra to say, because a non-null
     * description is read INSTEAD of the label.
     */
    val description: String?,
)

/** One `status.Group`'s section: its heading, its rows, and what it says when it has none. */
data class InboxSection(
    val group: String,
    val heading: String,
    val rows: List<InboxRow>,
    val emptyCopy: String,
)

/** One session, as the row renders it. */
data class InboxRow(
    val id: String,
    /** `.prow .pj` -- `swarmmobile.Session.Title`, the id's own local part. */
    val project: String,
    /**
     * `.prow .ag` -- the agent identity the machine reported, verbatim from the wire.
     *
     * THIS FIELD USED TO BE THE EMPTY STRING BY CONSTRUCTION, on the ground that "`swarmmobile.
     * Session` carries ID, Title, Group, Need and Present and no agent (mobile/types.go)". That
     * ceased to be true at 5f45f34, which carried `Agent` the whole way from the daemon's
     * `persist.Meta.AgentType` to `swarmmobile.Session` -- and the sentence was left behind citing
     * the very file that refuted it, which is the defect class this project treats most seriously.
     * It is deleted rather than softened.
     *
     * EMPTY STILL MEANS SOMETHING, and it is the opposite of what it used to mean: not "the wire
     * has no such field" but "the machine reported no agent for this session". mobile/types.go says
     * it is never derived on-device, so nothing here fills the gap -- the row draws no agent cell
     * at all (`ui/kit/SessionRow.kt`) rather than a blank one or a plausible substitute.
     */
    val agent: String,
    /** `.prow .ln` -- the journal record type verbatim, never an invented phrase. */
    val need: String,
    val group: String,
    /**
     * What a screen reader says about the dot. The 7 dp mark is the only thing distinguishing the
     * four Groups -- four hues, no text -- so without words a screen reader user gets nothing.
     */
    val stateDescription: String,
    val selected: Boolean,
)

/** One tab. Only the Inbox tab carries a badge (derivation table 1.4). */
data class InboxTab(
    val label: String,
    val selected: Boolean,
    /** Sessions in `needs_input`. Zero means no badge at all, not a badge reading "0". */
    val badgeCount: Int,
    val badgeDescription: String?,
)

object TriageInboxScreen {

    /**
     * SECTION_HEADINGS is the recorded copy, from inventory C1: "Group labels in order: `Needs
     * you` - `Working` - `Ready for review` - `Done`".
     */
    private val SECTION_HEADINGS: Map<String, String> = mapOf(
        "needs_input" to "Needs you",
        "ready_for_review" to "Ready for review",
        "completed" to "Done",
        "working" to "Working",
    )

    /**
     * EMPTY_SECTION_COPY is what a section says when it has nothing in it.
     *
     * IT IS AUTHORED RATHER THAN RECORDED, and that is worth saying out loud. Inventory C1 records
     * three empty states for this screen -- kill switch on, a scope with zero sessions, and a
     * machine offline -- and no per-section copy at all, because the artifact simply drops empty
     * sections. PB-DS-9 requires the opposite, so the words for the case the artifact does not
     * draw had to be written. They are deliberately flat statements of fact rather than
     * encouragement: this is a triage surface, and "nothing is waiting on you" is the most useful
     * thing it can report.
     */
    private val EMPTY_SECTION_COPY: Map<String, String> = mapOf(
        "needs_input" to "Nothing is waiting on you.",
        "ready_for_review" to "Nothing is waiting to be reviewed.",
        "completed" to "Nothing has finished here yet.",
        "working" to "Nothing is running.",
    )

    /** Inventory C1.4: `Inbox` (on) - `Machines` - `Activity` - `Settings`. */
    private val TAB_LABELS: List<String> = listOf("Inbox", "Machines", "Activity", "Settings")

    /** The tab the inbox IS. */
    private const val INBOX_TAB = "Inbox"

    /** Inventory C1.1: `.pnav .big`. */
    private const val TITLE = "Inbox"

    /** Inventory C1.2: the scope that names no machine. */
    private const val ALL_MACHINES = "All machines"

    fun headingFor(group: String): String? = SECTION_HEADINGS[group]

    fun emptyCopyFor(group: String): String? = EMPTY_SECTION_COPY[group]

    /**
     * @param scope the machine the user has narrowed to, or null for every machine.
     * @param selectedSession the session the surface's controls act on, or null for none.
     */
    fun of(
        inbox: TriageInbox,
        scope: String? = null,
        selectedSession: String? = null,
    ): InboxScreen {
        val sections = inbox.sections.map { section ->
            InboxSection(
                group = section.group,
                heading = headingOf(section.group),
                emptyCopy = emptyCopyOf(section.group),
                rows = section.rows
                    .filter { scope == null || machineOf(it.id) == scope }
                    .map { row ->
                        InboxRow(
                            id = row.id,
                            project = row.title,
                            agent = row.agent,
                            need = row.need,
                            group = row.group,
                            stateDescription = headingOf(section.group),
                            selected = row.id == selectedSession,
                        )
                    },
            )
        }
        val inFlight = sections.filter { it.group in IN_FLIGHT }.sumOf { it.rows.size }
        val blocked = sections.filter { it.group == BLOCKED }.sumOf { it.rows.size }
        return InboxScreen(
            title = TITLE,
            live = if (inFlight == 0) null else "$inFlight LIVE",
            scopes = scopesOf(inbox, scope),
            sections = sections,
            tabs = tabsOf(blocked),
            staleNotice = inbox.staleNotice,
        )
    }

    /**
     * The machine a session belongs to, or null when its id does not name one.
     *
     * `mobile/app.go` derives a session's display title by cutting the id at the first "/" and
     * falling back to the whole id when there is none, so the namespaced form is the wire's own
     * and this reads the half that one throws away. It is a PARSE OF AN IDENTIFIER, not a
     * derivation of state: the Group is never derived on-device (TriageInbox says why), and this
     * is not a Group.
     */
    private fun machineOf(sessionId: String): String? =
        sessionId.substringBefore('/', "").takeIf { it.isNotEmpty() }

    /**
     * The scope bar: "All machines", then every machine the roster names, ALPHABETICALLY.
     *
     * THE ORDER IS SORTED RATHER THAN THE ROSTER'S, and that is the same argument PB-DS-9 makes
     * about empty sections. [TriageInbox] groups the roster before this screen sees it, so
     * "roster order" here would really be "order of first appearance walking the Groups" -- and
     * the chips would then swap places when a session changed group, under the finger of someone
     * reaching for one. Inventory C1 draws two chips in the mock's declaration order and states no
     * rule; a stable order is the one property a filter control actually needs.
     *
     * A machine's presence is true when ANY of its sessions reports the machine reachable.
     * `Session.Present` is a fact about the MACHINE carried on each of its rows, so they agree;
     * the fold is there so a roster in mid-update cannot report a machine offline because one
     * stale row says so.
     */
    private fun scopesOf(inbox: TriageInbox, scope: String?): List<ScopeChip> {
        val machines = sortedMapOf<String, Boolean>()
        inbox.sections.asSequence().flatMap { it.rows.asSequence() }.forEach { row ->
            val machine = machineOf(row.id) ?: return@forEach
            machines[machine] = (machines[machine] ?: false) || row.present
        }
        val all = ScopeChip(
            machine = null,
            label = ALL_MACHINES,
            present = null,
            selected = scope == null,
            // No dot, so nothing the label does not already say. A non-null description would be
            // read INSTEAD of the label, which would silence it.
            description = null,
        )
        return listOf(all) + machines.map { (machine, present) ->
            ScopeChip(
                machine = machine,
                label = machine,
                present = present,
                selected = machine == scope,
                description = "$machine, ${if (present) "online" else "offline"}",
            )
        }
    }

    private fun tabsOf(blocked: Int): List<InboxTab> = TAB_LABELS.map { label ->
        InboxTab(
            label = label,
            selected = label == INBOX_TAB,
            // Derivation table 1.4: the badge is the CROSS-SCREEN attention carrier and it counts
            // NeedsInput only, which is what keeps it a different instrument from the header's
            // in-flight counter. It rides the Inbox tab because that is where the list is.
            badgeCount = if (label == INBOX_TAB) blocked else 0,
            badgeDescription = if (label == INBOX_TAB && blocked > 0) announcement(blocked) else null,
        )
    }

    /**
     * Derivation table row 3 states the form: "N sessions need you".
     *
     * THE SINGULAR IS WRITTEN OUT rather than left to the recorded plural. "1 sessions need you"
     * is what the recorded form produces at the count this badge spends most of its life at, and
     * it is the one string in this product that a screen reader user hears in place of a number
     * whose whole job is to be understood.
     */
    private fun announcement(count: Int): String =
        if (count == 1) "1 session needs you" else "$count sessions need you"

    /**
     * @throws IllegalStateException on a Group with no copy. LOUD, and for the reason
     *  [TriageInbox.from] gives about the same class of gap: a section rendered with a blank
     *  heading is a section the user cannot name, and silence here would be indistinguishable
     *  from a design decision.
     */
    private fun headingOf(group: String): String = checkNotNull(SECTION_HEADINGS[group]) {
        "PB-DS-9: no section heading for the status.Group $group. TriageInbox.TRIAGE_ORDER places " +
            "it and this screen cannot name it, so it would render as an unlabelled block of rows."
    }

    private fun emptyCopyOf(group: String): String = checkNotNull(EMPTY_SECTION_COPY[group]) {
        "PB-DS-9: no empty copy for the status.Group $group. An empty section is still a section " +
            "and says so; without copy it is a heading over nothing."
    }

    /**
     * What the live counter counts. Derivation table 8.1: the artifact renders `3 LIVE` over 1
     * NeedsInput + 2 Working + 1 Done and omits ReadyForReview entirely, so its recommendation --
     * NeedsInput + Working -- reproduces the artifact's own arithmetic. A session waiting on a
     * human is not running, and a finished one is not either.
     */
    private val IN_FLIGHT: Set<String> = setOf("needs_input", "working")

    /** The Group the tab badge counts, and the only one it counts. */
    private const val BLOCKED = "needs_input"
}
