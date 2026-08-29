package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.MachineFreshness
import dev.swarm.phone.ui.MachineLabel
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
 * `Needs you / Working / Ready for review / Done`, and the model declares needs_input, working,
 * ready_for_review, completed to match: needs_input first because it blocks on the user, working
 * second so live activity is visible without scrolling, completed last. The recorded copy is the
 * artifact's; the order is the product's, and this file reads it rather than restating it.
 */
data class InboxScreen(
    /** `.pnav .big`. Inventory C1.1. */
    val title: String,
    /**
     * `.pnav .live`, or null when nothing is in flight -- a counter reading zero says nothing.
     *
     * It carries a MARK when the roster it was counted from is holed; [TriageInboxScreen.liveCount]
     * argues why it is qualified rather than dropped.
     */
    val live: String?,
    val scopes: List<ScopeChip>,
    val sections: List<InboxSection>,
    val tabs: List<InboxTab>,
    val rosterReady: Boolean,
    val refreshing: Boolean,
    /**
     * PB-APP-8's verdict for this screen, in [TriageInbox]'s own words.
     *
     * IT IS NOT RENDERED BY THE INBOX VIEW and that is a scope statement rather than an omission:
     * inventory C1's composition is four elements and none of them is a notice, and the design's
     * only body-copy block (derivation row 8) is the empty state.
     *
     * IT IS RENDERED ON THE SCAFFOLD'S BANNER, one line of three, above whichever destination is
     * on screen (agents-tracker-e6mi). It used to be joined to the connection banner and the
     * freshness verdict as one sentence on a line hosted UNDER this screen's own sections, which
     * is to say on one of four tabs -- so the notice qualifying this list was not on screen for a
     * user who had navigated away from it, and [live] went on asserting a count over the same
     * holed roster at the top of the same screen.
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
    /**
     * `.prow .ln` -- the row's second line, always: the STATE WORD, the AGE, and in the All
     * scope the MACHINE, joined by ` · ` (phone-refit-playbook W7.1).
     *
     * The state word is the human phrase for the wire's journal record type
     * (agents-tracker-ksvb.2's vocabulary, [TriageInboxScreen.needCopy]), the type ITSELF where
     * this screen does not recognise it, and the Group's own word where the roster has not yet
     * carried a type at all. THE FALLBACK CARRIES FORWARD [dev.swarm.phone.ui.SessionRow.need]'S
     * OWN RULE rather than replacing it: a token outside the seven this build knows renders as
     * the wire sent it, never a guessed English sentence that would fail silently the day the
     * server adds one.
     *
     * The age is time IN THE CURRENT STATE, counted from the MACHINE's stamp of entering it
     * ([dev.swarm.phone.ui.SessionRow.stateSinceUnixMs]), and is absent, not the epoch's, when
     * the stamp is 0.
     */
    val need: String,
    val group: String,
    /**
     * What a screen reader says about the dot. The 7 dp mark is the only thing distinguishing the
     * four Groups -- four hues, no text -- so without words a screen reader user gets nothing.
     */
    val stateDescription: String,
    val selected: Boolean,
    /**
     * ADR-009 D4's promoted slab: this session is the one blocked on the human, so its row comes
     * forward one ladder step and catches the stronger key-light.
     *
     * **IT IS NAMED HERE AND NOT DERIVED IN THE KIT, and the reason is that it was derived in the
     * kit.** Which Group is blocked on the human is a product decision this model already makes
     * twice -- [TriageInboxScreen.BLOCKED] is what the tab badge counts, and the order puts it
     * first -- and `ui/kit/SessionRow.kt` made it a third time as `group == "needs_input"`. Three
     * copies of one decision is how the three come to disagree: promote a different Group and the
     * slab moves while the badge goes on counting the old one, with every test green because each
     * component was asked for exactly what it drew. The kit renders what it is told.
     *
     * **IT IS ALSO WHAT ADR-009 D5's SWEEP FIRES FROM.** The specular sweep runs once "at the
     * moment a session's Group becomes `NeedsInput`" -- that is this field, changing -- so O4 has
     * a named fact to watch rather than a fourth derivation of the same Group.
     *
     * IT HAS NO DEFAULT, for [agent]'s reason and [JournalRow.sessionId]'s before it: a default
     * makes the field optional at every construction site and it goes unpopulated at whichever one
     * nobody revisited. A row that quietly defaulted to `false` renders as a resting row, which is
     * exactly what a correct resting row renders as -- the two are not ambiguous on screen, they
     * are identical.
     */
    val lit: Boolean,
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
     * draw had to be written.
     *
     * CAPTIONS, NOT SENTENCES (agents-tracker-ksvb.6). Three of these used to be full sentences,
     * stacked on the one screen this app means to be glanceable -- a defect the words themselves
     * caused rather than fixed. What survives at two words is exactly the fact this file's own
     * KDoc argues for: an EMPTY section still says so, distinguishably from a section that has
     * merely scrolled off screen. A caption anchors that distinction as well as a sentence did.
     */
    private val EMPTY_SECTION_COPY: Map<String, String> = mapOf(
        "needs_input" to "Nothing waiting",
        "ready_for_review" to "Nothing to review",
        "completed" to "Nothing finished",
        "working" to "Nothing running",
    )

    /**
     * NEED_VOCABULARY is agents-tracker-ksvb.2's phone-side lookup table: the human phrase for
     * each of the wire's own journal record types (`internal/journal.RecordType`), read here
     * because this is where the screen composes [InboxRow]'s display strings -- the approval
     * sheet's question reads the same phrase off the row unchanged (`ApprovalSheetPanel.kt:85`),
     * so one table answers both surfaces.
     *
     * `group_transition` IS NOT HERE. Its own content beyond the session it names IS the Group it
     * moved to (`internal/journal/journal.go`'s own comment on the field: "set on
     * group_transition"), so its phrase is a lookup over the row's own Group --
     * [GROUP_TRANSITION_VOCABULARY] -- and not a fixed string this map could hold.
     *
     * `presence` CARRIES NO ONLINE/OFFLINE DIRECTION HERE ON PURPOSE. The producer
     * (`internal/daemon/journal.go`'s `RecordGatewayPresence`) is "a daemon-side liveness proxy"
     * for the remote gateway connecting or disconnecting, and the online flag rides in the record's
     * opaque payload -- but `mobile/relay.go`'s `a.needs[rec.SessionID] = rec.Type` keeps only the
     * TYPE, so no direction ever reaches this map to read. "Connection updated" says the one thing
     * this table can know: something about connectivity changed.
     *
     * `roster` IS HERE EVEN THOUGH THE JOURNAL NEVER APPENDS IT. `journal.TypeRoster`'s own comment
     * says it is "emitted ONLY inside Resume.Roster... NEVER appended", and it reaches this need
     * line exactly like an appended record -- `mobile/app.go`'s `a.needs` map does not distinguish
     * the two. "Synced" is what that snapshot IS to a person: the phone catching up to what the
     * machine already knows, not a new event having happened.
     */
    private val NEED_VOCABULARY: Map<String, String> = mapOf(
        "launched" to "Started",
        "exited" to "Ended",
        "lost" to "Connection lost",
        "deleted" to "Deleted",
        "presence" to "Connection updated",
        "roster" to "Synced",
    )

    /**
     * `group_transition`'s own phrase, BY THE GROUP IT NAMES -- a lookup, not a second derivation.
     * [InboxRow.group] already carries `swarmmobile.Session.Group` verbatim (`TriageInbox` states
     * the rule this screen never breaks: the Group is never derived on-device), so this reads it
     * rather than deciding it again.
     */
    private val GROUP_TRANSITION_VOCABULARY: Map<String, String> = mapOf(
        "needs_input" to "Waiting on you",
        "working" to "Working",
        "ready_for_review" to "Ready for review",
        "completed" to "Done",
    )

    private const val GROUP_TRANSITION = "group_transition"

    /**
     * The need line's phrase for one row: [NEED_VOCABULARY] or [GROUP_TRANSITION_VOCABULARY], and
     * the wire's own word where NEITHER table knows it.
     *
     * AN UNRECOGNISED TOKEN RENDERS VERBATIM, [dev.swarm.phone.ui.SessionRow.need]'s own rule
     * carried forward rather than replaced: a table that guessed at a record type it did not know
     * would put an invented phrase on screen the moment the server adds one, and the failure would
     * be silent -- every field still a plain string, nothing to throw on.
     */
    fun needCopy(need: String, group: String): String = when {
        // A session the roster carried before any event did has no record type yet (W7.1); its
        // Group is verbatim from the wire, and the Group IS its state.
        need == GROUP_TRANSITION || need.isEmpty() -> GROUP_TRANSITION_VOCABULARY[group] ?: need
        else -> NEED_VOCABULARY[need] ?: need
    }

    /**
     * The time in the current state since the machine's stamp of entering it, in
     * `MachineFreshness`'s own units (`4m`, `3h`, `2d`), or "" for 0 -- a zero stamp is ABSENT
     * and no age is drawn for it, never the epoch's.
     */
    private fun ageOf(stateSinceUnixMs: Long, nowUnixMs: Long): String =
        MachineFreshness(silent = false, lastHeardUnixMs = stateSinceUnixMs).sinceLastHeard(nowUnixMs)

    /** The separator between the need line's facts. `PeekPanelScreen` sets the idiom. */
    private const val FIELD_SEPARATOR = " · "

    /**
     * `Inbox` (on) - `Activity` - `Settings`.
     *
     * INVENTORY C1.4 DRAWS FOUR AND THIS APP HAS THREE (agents-tracker-nx44.3). The `Machines`
     * destination is deleted -- see [Destination], which carries the argument -- and a label here
     * with no destination behind it is what `Destination.forLabel` throws on, deliberately, so
     * these two lists cannot drift apart quietly.
     */
    private val TAB_LABELS: List<String> = listOf("Inbox", "Activity", "Settings")

    /** The tab the inbox IS. */
    private const val INBOX_TAB = "Inbox"

    /** Inventory C1.1: `.pnav .big`. */
    private const val TITLE = "Inbox"
    const val SYNCING_COPY = "Syncing conversations…"
    const val REFRESH_LABEL = "Refresh"
    const val REFRESHING_LABEL = "Refreshing…"
    const val REFRESH_DESCRIPTION = "Refresh all conversations"
    const val REFRESHING_DESCRIPTION = "Refreshing conversations"

    /** Inventory C1.2: the scope that names no machine. */
    private const val ALL_MACHINES = "All machines"

    fun headingFor(group: String): String? = SECTION_HEADINGS[group]

    fun emptyCopyFor(group: String): String? = EMPTY_SECTION_COPY[group]

    /**
     * @param scope the machine the user has narrowed to, or null for every machine.
     * @param selectedSession the session the surface's controls act on, or null for none.
     */
    /**
     * @param machineNames what each machine CALLS itself, keyed by the endpoint id its sessions
     *  are namespaced under (agents-tracker-ksvb.1). A machine with no entry, or an empty one,
     *  renders its endpoint id -- [MachineLabel.of]'s fallback, and what every chip read before
     *  this parameter existed. It is a MAP rather than a single name because the roster is
     *  namespaced per machine and nothing here may assume there is only one; the phone is paired
     *  to one machine today, and a chip bar that hard-coded that would be wrong silently.
     * @param nowUnixMs this phone's clock, for the row's age alone (W7.1). A parameter so the JVM
     *  suite can freeze the words; the default keeps a caller with no opinion honest rather than
     *  frozen at the epoch.
     */
    fun of(
        inbox: TriageInbox,
        scope: String? = null,
        selectedSession: String? = null,
        machineNames: Map<String, String> = emptyMap(),
        nowUnixMs: Long = System.currentTimeMillis(),
        rosterReady: Boolean = true,
        refreshing: Boolean = false,
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
                            // STATE · AGE · MACHINE (W7.1). The machine only in the All scope,
                            // where rows from different machines share one list; under a
                            // chip it would repeat the chip. It reads the NAME and the row
                            // keeps the id, the scope bar's own rule (agents-tracker-ksvb.1).
                            need = listOfNotNull(
                                needCopy(row.need, row.group),
                                ageOf(row.stateSinceUnixMs, nowUnixMs),
                                if (scope == null) {
                                    machineOf(row.id)?.let { MachineLabel.of(machineNames[it].orEmpty(), it) }
                                } else {
                                    null
                                },
                            ).filter { it.isNotEmpty() }.joinToString(FIELD_SEPARATOR),
                            group = row.group,
                            stateDescription = headingOf(section.group),
                            selected = row.id == selectedSession,
                            // THE SAME CONSTANT THE BADGE COUNTS. One name for one decision is the
                            // whole of InboxRow.lit's argument, and it is spent here rather than
                            // restated as a second `== "needs_input"` four lines from the first.
                            lit = row.group == BLOCKED,
                        )
                    },
            )
        }
        val inFlight = sections.filter { it.group in IN_FLIGHT }.sumOf { it.rows.size }
        val blocked = sections.filter { it.group == BLOCKED }.sumOf { it.rows.size }
        return InboxScreen(
            title = TITLE,
            live = liveCount(inFlight, whole = !inbox.stale),
            scopes = scopesOf(inbox, scope, machineNames),
            sections = sections,
            tabs = tabsOf(blocked),
            rosterReady = rosterReady,
            refreshing = refreshing,
            staleNotice = inbox.staleNotice,
        )
    }

    /**
     * `.pnav .live`, and whether it may be stated as a fact.
     *
     * A HOLED ROSTER LABELLED `LIVE` IS THE ONE CLAIM THIS SCREEN MUST NOT MAKE (agents-tracker-
     * e6mi). The list is rendered from the journal stream, so [TriageInbox.stale] means a session,
     * an exit or a needs_input may be missing -- and the counter over it is arithmetic over an
     * incomplete list presented as a count of what is running. The stale notice said so at the
     * BOTTOM of the column while this asserted `3 LIVE` at the top of the same screen; the notice
     * is now on the scaffold's banner, above every destination, and this is the other half of that
     * sentence agreeing with it.
     *
     * IT IS QUALIFIED RATHER THAN SUPPRESSED, which is [TriageInbox]'s own rule about an empty
     * section applied to a number. Dropping the counter makes "nothing is in flight"
     * indistinguishable from "we are not sure", and the count is still the most useful thing this
     * screen has -- it is a floor a person can act on. [PARTIAL_MARK] is the standard "about", it
     * is one character in a cell the design gives ten sans-serif characters of room, and it says
     * the one thing that is true: this number was counted from a list the phone knows is incomplete.
     *
     * A QUALIFIED ZERO IS STILL NO COUNTER. The mark does not resurrect the readout this model
     * already refuses -- `0 LIVE` is a number nobody needs, and marking it approximate does not
     * make it worth the space.
     */
    private fun liveCount(inFlight: Int, whole: Boolean): String? = when {
        inFlight == 0 -> null
        whole -> "$inFlight LIVE"
        else -> "$PARTIAL_MARK$inFlight LIVE"
    }

    /** What a count carries when the roster it was counted from may be missing rows. */
    private const val PARTIAL_MARK = "~"

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
    private fun scopesOf(
        inbox: TriageInbox,
        scope: String?,
        machineNames: Map<String, String>,
    ): List<ScopeChip> {
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
            // THE CHIP READS THE NAME AND ACTS ON THE ID (agents-tracker-ksvb.1). `machine` is the
            // endpoint id the roster namespaces sessions under, so it stays the filter key and the
            // selection key; only what a person reads changes. Reversing that -- keying the filter
            // on a hostname -- would break the moment two machines shared a name, and quietly.
            val label = MachineLabel.of(machineNames[machine].orEmpty(), machine)
            ScopeChip(
                machine = machine,
                label = label,
                present = present,
                selected = machine == scope,
                description = "$label, ${if (present) "online" else "offline"}",
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

    /**
     * The sessions whose Group BECAME [BLOCKED] between [previous] and [next] -- ADR-009 D5's
     * sweep, as a question about two screens.
     *
     * **A PROMOTION IS A TRANSITION AND `lit` IS A STATE.** `lit` is true for as long as a session
     * waits; this is true for the one draw in which it started waiting. Deriving one from the
     * other would sweep every waiting row on every redraw -- [InboxRow.lit] is recomputed on every
     * journal event -- which is the ambient field-register motion D5 bans in the same paragraph
     * that permits this one.
     *
     * **IT COMPARES SCREENS AND NOT ROSTERS**, which is what makes "in front of the user" true
     * rather than approximately true. What the phone core reports and what the user was looking at
     * are different things: a session filtered out by the scope chips is not on this viewport, and
     * a screen being drawn for the first time has nothing to have transitioned from. Both cases
     * fall out of comparing the drawn screens rather than the wire's rosters, and both are the
     * correct answer -- a sweep is an announcement, and there is nobody to announce to.
     *
     * A SESSION THAT FIRST APPEARS ALREADY BLOCKED DOES NOT SWEEP. It arrived already waiting; the
     * lit slab says so, and the sweep is reserved for the change. This is the case that decides
     * whether this function is a transition or a state, and it is why [previous] is nullable
     * rather than defaulted to an empty screen.
     *
     * EVERY TRANSITION IS NAMED, not only the first. One journal event can promote two sessions,
     * and choosing which of them MOVES is Motion's rule (newest wins, one per viewport) -- a model
     * that reported one would make that rule unfalsifiable and would silently drop a promotion the
     * day the rule changed.
     *
     * @param previous the screen the user was looking at, or null on the first draw.
     */
    fun promotions(previous: InboxScreen?, next: InboxScreen): Set<String> {
        if (previous == null) return emptySet()
        val before = previous.sections.asSequence()
            .flatMap { it.rows.asSequence() }
            .associate { it.id to it.group }
        return next.sections.asSequence()
            .flatMap { it.rows.asSequence() }
            .filter { it.group == BLOCKED }
            .filter { before[it.id]?.let { was -> was != BLOCKED } == true }
            .map { it.id }
            .toSet()
    }

    /**
     * The Group blocked on the human: what the tab badge counts, and the row ADR-009 D4 promotes.
     *
     * IT NAMES A STATE AND NOT A COMPONENT, which is why one constant carries both uses. The badge
     * and the lit slab are two instruments reporting the same fact at two scales -- one across the
     * whole app, one on the row itself -- and giving them a constant each is giving them a chance
     * to disagree.
     *
     * PUBLIC SINCE W7.2, for the same reason: the view keeps exactly one section on screen when
     * it is empty, and which one is this decision a third time, not a fresh `== "needs_input"`.
     */
    const val BLOCKED = "needs_input"
}
