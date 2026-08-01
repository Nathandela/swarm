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
     * `.prow .ag`. EMPTY, and deliberately: `swarmmobile.Session` carries ID, Title, Group, Need
     * and Present and no agent (mobile/types.go), so the slot has no source on the wire. An
     * invented one would be simulated data in production code, which is worse than a blank.
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
    ): InboxScreen = TODO("S24: the triage inbox is not composed yet")
}
