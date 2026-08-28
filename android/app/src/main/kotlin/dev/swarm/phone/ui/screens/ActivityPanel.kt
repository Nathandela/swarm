package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.JournalPageView
import dev.swarm.phone.ui.JournalRow
import dev.swarm.phone.ui.kit.ToolCard
import java.text.DateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale

/**
 * Phase B slice S25 -- PB-DS-9: the ACTIVITY screen's model.
 *
 * WHY THERE IS A MODEL BESIDE [JournalPageView]. That one answers what the PAGE is: the records,
 * where the next read resumes, and whether the stream they came from has a hole. This answers what
 * the SCREEN says about it -- the title, the sections it renders under, what each record reads as,
 * which word in it is emphasised, and what the screen says when the page is empty or holed. Every
 * one of those is copy or arrangement, and PB-DS-9 assigns both to the screen.
 *
 * IT IS A PURE FUNCTION OVER [JournalPageView], which is the shape this package already uses
 * ([SettingsPanelScreen], [PeekPanelScreen], [TriageInboxScreen]). No Android import, so the
 * interesting half is checkable without a device.
 *
 * ## What the mock draws and what the journal supplies
 *
 * The retired mock's `renderActivity()` is evidence of INTENT and nothing more; `.arow` is not a
 * Substrate rule. It draws six rows, and each of the things it draws is recorded here against
 * what the wire actually carries, because a screen that is pixel-accurate to its design and still
 * lying is the defect class this project has spent the most effort on (ADR-007 B135, and §8.8 of
 * the design doc).
 *
 * - **The `HH:MM` timestamp** -- SUPPLIED SINCE W7.4. `protocol.JournalRecord` used to drop
 *   `internal/journal.Record`'s `TS`; it now carries it (`ts`, omitted when zero), and
 *   `JournalRow.tsUnixMs` is that stamp, 0 where the wire carried none. The time on a row is
 *   `ToolCard.timestampLabel` over the DAEMON's stamp, "" for 0 -- and the `Cursor` beside it is
 *   still a monotonic sequence number, not a clock: it is never formatted as a time, and a row
 *   with no stamp draws no time rather than one manufactured on the handset.
 * - **The `While you were away` / `Informative` split.** Nothing in the journal marks a record as
 *   seen, acknowledged, salient or slept-through: there is no read cursor for the USER (the
 *   cursor is the transport's), no ack that reaches the phone, and no severity. The first heading
 *   is a claim about time the phone cannot make and the second is a claim about importance nobody
 *   computes. Reproducing them would be a grouping invented to match a drawing. What the wire DOES
 *   support since W7.4 is a grouping by DAY, from the daemon's stamp: `Today`, `Yesterday`, then
 *   the date, newest day first, with the rows the wire did not stamp in one trailing section under
 *   the product's own name for the stream. A salience split could be faked out of `Type` --
 *   `presence` is chatter and a `group_transition` into `needs_input` is not -- and it would be a
 *   different split than the one the mock's words promise, which is worse than no split at all.
 * - **Which session an event is about.** `swarmmobile.JournalEntry` carries `SessionID`, and for
 *   a while `FacadeBridge.journal` dropped it on the way into [JournalRow], so the feed could say
 *   a session was launched and not which -- and the mock's emphasised span, which is the project
 *   name in every row it draws, had nothing real to be. It is one field on [JournalRow] and one
 *   line in `FacadeBridge`, and since W7.4 it is also what tapping the row opens.
 *
 * ## What each row therefore says
 *
 * `session · word` (W7.4), where the word is W5's vocabulary for what happened -- `started`,
 * `finished`, `needs you`, `connection lost` -- and a `group_transition` reads by the Group it
 * names, the inbox's own lookup one register down. A type this build does not know renders
 * VERBATIM, which is [dev.swarm.phone.ui.screens.InboxRow.need]'s own rule -- "the journal record
 * type verbatim, never an invented phrase" -- and the reason behind it is stronger here than
 * there: a table that failed loudly on a value it did not know, the way `Kit.groupColour` does,
 * would let a server that adds a record type take the activity screen down. Rendering what
 * arrived costs a reader some polish and cannot become a lie.
 */
data class ActivityPanel(
    /** The activity tab's own `.pnav .big`. */
    val title: String,
    /**
     * One section per DAY the daemon stamped, newest day first, then one trailing section for the
     * rows it did not stamp (W7.4). See the class comment: the mock's two headings are claims about
     * seen-ness and salience that nothing on the wire supports; a day is a claim the stamp does.
     */
    val sections: List<ActivitySection>,
    /**
     * PB-APP-8, and this surface is the reason the flag exists.
     *
     * `JournalPage.Stale`'s own words: `ReadJournal` "serves PB-APP-3's session detail AND
     * PB-APP-5's activity log, and both render as a chronology -- a shape that reads as complete
     * unless it says otherwise". A holed log shown as a plain list tells the user their agents did
     * nothing in the gap. Empty when the stream is whole; a blank warning line over a healthy feed
     * is a warning nobody wrote.
     */
    val staleNotice: String,
)

/** One `.plabel` and the rows under it. */
data class ActivitySection(
    val heading: String,
    val rows: List<ActivityEntry>,
    /**
     * What the section says when it holds nothing. PB-DS-9's rule that an empty section is still a
     * section applies here for the reason it applies to the inbox: a heading over nothing is a
     * section that has lost its contents, and a screen that drops the heading instead makes "no
     * activity" indistinguishable from "the feed failed to load".
     */
    val emptyCopy: String,
)

/**
 * One journal record as the row renders it: derivation table row 14.
 *
 * THE TIME IS A CELL AND NOT PART OF [body], for `activityRow`'s reason: the sentence is copy the
 * screen wrote and the cell is the daemon's stamp, and a test reading the body for a cursor must
 * not mistake a clock for a number.
 */
data class ActivityEntry(
    /** `JournalRow.cursor`. Identity and order, never rendered -- it is a sequence, not a time. */
    val cursor: Long,
    /** `JournalRow.sessionId`, verbatim: what tapping the row opens (W7.4); "" for a session-neutral record. */
    val sessionId: String,
    /** `ToolCard.timestampLabel` over the daemon's stamp; "" where the wire carried none (W7.4). */
    val time: String,
    /** The sentence the row shows: the session, then W5's word for what happened. */
    val body: String,
    /**
     * The span of [body] that takes `.ln b`, or null where the record has nothing to emphasise.
     *
     * IT IS THE SESSION AND NOT AN INVENTED HIGHLIGHT. The mock emphasises the project name --
     * the thing each row is ABOUT -- and `SessionID` is the identifier this product has for that.
     *
     * It suits the role: `.ln b` is MONOSPACE, and a wire token is what a monospace emphasis is
     * for -- an English phrase set in mono reads as a mistake.
     *
     * Null where a record carries no session (W7.4; it used to fall back to the Group, which the
     * body no longer holds as a span of its own), rather than promoting the word for want of
     * anything better -- which would put the eye on the one thing every row of its kind shares.
     */
    val emphasis: String?,
)

object ActivityPanelScreen {

    /** The mock's `.navhead`, verbatim -- the one piece of its activity screen that survives. */
    private const val TITLE = "Activity"

    /** The day headings a stamp supports (W7.4). Any other day is its date. */
    private const val TODAY = "Today"
    private const val YESTERDAY = "Yesterday"

    /**
     * W5's vocabulary for what happened, by record type; a `group_transition` reads by the Group
     * it names ([GROUP_WORDS]). The inbox's own table (`TriageInboxScreen.needCopy`) is the same
     * lookup one register up; this one is lower case because it sits mid-sentence after the id.
     */
    private val WORDS: Map<String, String> = mapOf(
        "launched" to "started",
        "exited" to "finished",
        "lost" to "connection lost",
        "deleted" to "deleted",
        "presence" to "connection updated",
        "roster" to "synced",
    )

    private val GROUP_WORDS: Map<String, String> = mapOf(
        "needs_input" to "needs you",
        "working" to "working",
        "ready_for_review" to "ready for review",
        "completed" to "done",
    )

    private const val GROUP_TRANSITION = "group_transition"

    /**
     * What the section says when the page is empty.
     *
     * IT SAYS NOTHING HAS REACHED THIS PHONE, NOT THAT NOTHING HAPPENED. Those are different
     * claims and only the first one is this handset's to make: a phone that has been offline since
     * pairing has an empty journal and a machine that has been busy all morning. The stale notice
     * covers the case where the phone KNOWS it missed something; this copy covers the case where
     * it has no basis for saying either way, which is most of them.
     */
    private const val EMPTY = "No activity yet."

    /**
     * PB-APP-8's sentence, in the register [dev.swarm.phone.ui.TerminalPeek.staleNotice] set.
     *
     * It says what is missing and what that means for what is on screen, because "stale" on its
     * own leaves a reader to guess whether the list is old or incomplete -- and for a chronology
     * it is incomplete, which is the worse of the two and the one they would not assume.
     */
    private const val STALE_NOTICE = "Some entries are missing."

    /** The separator between a record's session and its word. `PeekPanelScreen` sets the idiom. */
    private const val FIELD_SEPARATOR = " · "

    /**
     * @param nowUnixMs this phone's clock, for naming a day `Today` or `Yesterday` alone (W7.4);
     *  the stamps themselves are the daemon's. A parameter so the JVM suite can freeze the words.
     */
    fun of(page: JournalPageView, nowUnixMs: Long = System.currentTimeMillis()): ActivityPanel {
        // NEWEST FIRST, which is the one arrangement decision this screen makes. `ReadJournal`
        // walks the page cache in ascending cursor order, because that is what a cursor is for;
        // a feed is read from the top and the top is what just happened. The mock draws the same
        // order (09:38 down to 08:55) and it is the only part of its composition this panel keeps
        // unchanged. W7.4 adds the day above it: the days are ordered by the stamp and the rows
        // within a day still by cursor, so two clocks never argue over one list.
        val newestFirst = page.rows.sortedByDescending { it.cursor }
        val (stamped, unstamped) = newestFirst.partition { it.tsUnixMs > 0L }
        val days = stamped.groupBy { dayOf(it.tsUnixMs) }.entries
            .sortedByDescending { it.key }
            .map { (day, rows) ->
                ActivitySection(
                    heading = dayHeading(day, rows.first().tsUnixMs, nowUnixMs),
                    rows = rows.map(::entryFor),
                    emptyCopy = EMPTY,
                )
            }
        // THE UNSTAMPED ROWS TRAIL, with no heading (phone refit W5.3): a daemon predating the
        // stamp sends none, and its whole page lands here. The day headings are W7.4's, from the
        // stamp; the phone has no word of its own to put over rows it cannot date.
        val trailing = if (unstamped.isNotEmpty() || days.isEmpty()) {
            listOf(ActivitySection(heading = "", rows = unstamped.map(::entryFor), emptyCopy = EMPTY))
        } else {
            emptyList()
        }
        return ActivityPanel(
            title = TITLE,
            sections = days + trailing,
            staleNotice = if (page.stale) STALE_NOTICE else "",
        )
    }

    /** The calendar day a stamp falls on, in this phone's zone: `year * 1000 + dayOfYear`. */
    private fun dayOf(unixMs: Long): Long = Calendar.getInstance().run {
        timeInMillis = unixMs
        get(Calendar.YEAR) * 1000L + get(Calendar.DAY_OF_YEAR)
    }

    private fun dayHeading(day: Long, unixMs: Long, nowUnixMs: Long): String {
        val yesterday = Calendar.getInstance().run {
            timeInMillis = nowUnixMs
            add(Calendar.DAY_OF_YEAR, -1)
            timeInMillis
        }
        return when (day) {
            dayOf(nowUnixMs) -> TODAY
            dayOf(yesterday) -> YESTERDAY
            else -> DateFormat.getDateInstance(DateFormat.MEDIUM, Locale.getDefault()).format(Date(unixMs))
        }
    }

    private fun wordFor(row: JournalRow): String = when (row.type) {
        GROUP_TRANSITION -> GROUP_WORDS[row.group] ?: row.type
        else -> WORDS[row.type] ?: row.type
    }

    /**
     * @return the record as one sentence, its time, plus the span of it the row emphasises.
     *
     * The emphasis is a SUBSTRING of the body rather than a fragment beside it, which is what
     * `activityRow` takes and why: `.ln b` is inline, so the component needs to know which part of
     * the sentence it marks, not two pieces to glue.
     */
    private fun entryFor(row: JournalRow): ActivityEntry {
        val session = row.sessionId.ifEmpty { null }
        return ActivityEntry(
            cursor = row.cursor,
            sessionId = row.sessionId,
            time = ToolCard.timestampLabel(row.tsUnixMs),
            body = listOfNotNull(session, wordFor(row)).joinToString(FIELD_SEPARATOR),
            // The SESSION: what the mock emphasises -- the thing the row is about.
            //
            // IT STAYS THE RAW SESSION ID, and that is agents-tracker-ksvb.1's explicit ruling
            // rather than a site nobody got to. Every other surface in this app now prefers the
            // human name -- the inbox row, both nav headers, the machine row, the scope chips, the
            // settings pairing row -- and this one does not, because Activity is a MONO LOG: the
            // machine's own register, read when something has gone wrong and one line has to be
            // matched against a daemon log, a journal cursor or a signed command. Those all carry
            // the id. A label here would make the one surface whose job is correlation the one
            // surface that cannot be correlated.
            //
            // NULL WHERE A RECORD CARRIES NO SESSION (W7.4). It used to fall back to the Group, and
            // the Group is now folded into the word, so a Group emphasis would name a span the body
            // no longer holds -- which `activityRow` refuses, loudly. Promoting the word instead
            // would put the eye on the one thing every row of its kind shares.
            emphasis = session,
        )
    }
}
