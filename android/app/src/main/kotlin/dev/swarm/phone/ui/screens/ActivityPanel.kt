package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.JournalPageView
import dev.swarm.phone.ui.JournalRow

/**
 * Phase B slice S25 -- PB-DS-9: the ACTIVITY screen's model.
 *
 * WHY THERE IS A MODEL BESIDE [JournalPageView]. That one answers what the PAGE is: the records,
 * where the next read resumes, and whether the stream they came from has a hole. This answers what
 * the SCREEN says about it -- the title, the section it renders under, what each record reads as,
 * which word in it is emphasised, and what the screen says when the page is empty or holed. Every
 * one of those is copy or arrangement, and PB-DS-9 assigns both to the screen.
 *
 * IT IS A PURE FUNCTION OVER [JournalPageView], which is the shape this package already uses
 * ([SettingsPanelScreen], [PeekPanelScreen], [TriageInboxScreen]). No Android import, so the
 * interesting half is checkable without a device.
 *
 * ## What the mock draws that the journal cannot supply
 *
 * The retired mock's `renderActivity()` is evidence of INTENT and nothing more; `.arow` is not a
 * Substrate rule. It draws six rows, and three of the things it draws have no source on the wire.
 * `swarmmobile.JournalEntry` is `(Cursor, SessionID, Type, Group)` and that is the entire record.
 * Each of these is recorded here rather than filled in, because a screen that is pixel-accurate to
 * its design and still lying is the defect class this project has spent the most effort on
 * (ADR-007 B135, and §8.8 of the design doc).
 *
 * - **The `HH:MM` timestamp.** `internal/journal.Record` HAS a `TS time.Time`, and
 *   `protocol.JournalRecord` -- the wire form the phone is served -- DROPS IT. So there is no time
 *   on this handset to render, and the `Cursor` beside it is a monotonic sequence number, not a
 *   clock: formatting one as a time would be a fabricated fact wearing a real field's value.
 *   [ActivityEntry] therefore has no timestamp and the view passes none. The row's cell survives
 *   as an empty column at zero cost, because derivation row 14 makes that column wrap-content
 *   rather than the mock's fixed 52 dp -- see `activityRow`.
 * - **The `While you were away` / `Informative` split.** Nothing in the journal marks a record as
 *   seen, acknowledged, salient or slept-through: there is no read cursor for the USER (the
 *   cursor is the transport's), no ack that reaches the phone, and no severity. The first heading
 *   is a claim about time the phone cannot make and the second is a claim about importance nobody
 *   computes. Reproducing them would be a grouping invented to match a drawing, so this panel
 *   renders ONE section. A salience split could be faked out of `Type` -- `presence` is chatter
 *   and a `group_transition` into `needs_input` is not -- and it would be a different split than
 *   the one the mock's words promise, which is worse than no split at all.
 * - **Which session an event is about.** This is the one gap that is not the wire's fault and it
 *   is the most damaging of the three: `swarmmobile.JournalEntry` DOES carry `SessionID`, and
 *   `FacadeBridge.journal` drops it on the way into [JournalRow]. So the feed can say a session
 *   was launched and cannot say which -- and the mock's emphasised span, which is the project
 *   name in every row it draws, has nothing real to be. Fixing it is one field on [JournalRow] and
 *   one line in `FacadeBridge`, in files this slice does not own.
 *
 * ## What each row therefore says
 *
 * The record's `Type`, and its `Group` where it carries one, VERBATIM. That is not a shortfall of
 * effort, it is [dev.swarm.phone.ui.screens.InboxRow.need]'s own rule -- "the journal record type
 * verbatim, never an invented phrase" -- and the reason behind it is stronger here than there.
 * `swarmmobile` says `Type` and `Group` are "verbatim from the wire"; a table turning them into
 * English would have to fail loudly on a value it did not know, the way `Kit.groupColour` does,
 * and a server that adds a record type would then take the activity screen down. Rendering what
 * arrived costs a reader some polish and cannot become a lie.
 */
data class ActivityPanel(
    /** The activity tab's own `.pnav .big`. */
    val title: String,
    /**
     * ONE section, always. See the class comment: the mock's two headings are claims about
     * seen-ness and salience that nothing on the wire supports.
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
 * THERE IS NO TIMESTAMP FIELD, and its absence is the assertion. The wire carries no time (class
 * comment), so a nullable field here would be a slot that is structurally always null and reads,
 * to the next person, as one somebody forgot to fill.
 */
data class ActivityEntry(
    /** `JournalRow.cursor`. Identity and order, never rendered -- it is a sequence, not a time. */
    val cursor: Long,
    /** The sentence the row shows: `Type`, and `Group` after it where the record carries one. */
    val body: String,
    /**
     * The span of [body] that takes `.ln b`, or null where the record has nothing to emphasise.
     *
     * IT IS THE SESSION AND NOT AN INVENTED HIGHLIGHT. The mock emphasises the project name --
     * the thing each row is ABOUT -- and `SessionID` is the identifier this product has for that.
     * It reaches the screen as of the `JournalRow.sessionId` fix; before it, this was the `Group`,
     * because the facade carried the session and `FacadeBridge.journal` dropped it, so the feed
     * could say a session launched and not which one.
     *
     * It suits the role: `.ln b` is MONOSPACE, and a wire token is what a monospace emphasis is
     * for -- an English phrase set in mono reads as a mistake.
     *
     * Falls back to the `Group` where a record carries no session, and to null where it carries
     * neither, rather than promoting the type for want of anything better -- which would put the
     * eye on the one word every row shares.
     */
    val emphasis: String?,
)

object ActivityPanelScreen {

    /** The mock's `.navhead`, verbatim -- the one piece of its activity screen that survives. */
    private const val TITLE = "Activity"

    /**
     * The one section's heading.
     *
     * AUTHORED, AND NAMING THE SOURCE RATHER THAN THE READER'S SITUATION. The mock's two headings
     * are dropped for the reason the class comment gives, and a replacement had to say something
     * true about what is under it. "Journal" is the product's own name for this record stream --
     * `internal/journal`, `App.ReadJournal`, `JournalPage` -- so it is a word the log can be
     * checked against, and it makes no claim about when the user last looked.
     */
    private const val SECTION = "Journal"

    /**
     * What the section says when the page is empty.
     *
     * IT SAYS NOTHING HAS REACHED THIS PHONE, NOT THAT NOTHING HAPPENED. Those are different
     * claims and only the first one is this handset's to make: a phone that has been offline since
     * pairing has an empty journal and a machine that has been busy all morning. The stale notice
     * covers the case where the phone KNOWS it missed something; this copy covers the case where
     * it has no basis for saying either way, which is most of them.
     */
    private const val EMPTY = "No activity has reached this phone yet."

    /**
     * PB-APP-8's sentence, in the register [dev.swarm.phone.ui.TerminalPeek.staleNotice] set.
     *
     * It says what is missing and what that means for what is on screen, because "stale" on its
     * own leaves a reader to guess whether the list is old or incomplete -- and for a chronology
     * it is incomplete, which is the worse of the two and the one they would not assume.
     */
    private const val STALE_NOTICE =
        "Some entries are missing: the event stream from your machine had a gap that has not " +
            "been repaired, so this is not a complete history."

    /** The separator between a record's type and its group. `PeekPanelScreen` sets the idiom. */
    private const val FIELD_SEPARATOR = " · "

    fun of(page: JournalPageView): ActivityPanel = ActivityPanel(
        title = TITLE,
        sections = listOf(
            ActivitySection(
                heading = SECTION,
                // NEWEST FIRST, which is the one arrangement decision this screen makes.
                // `ReadJournal` walks the page cache in ascending cursor order, because that is
                // what a cursor is for; a feed is read from the top and the top is what just
                // happened. The mock draws the same order (09:38 down to 08:55) and it is the
                // only part of its composition this panel keeps unchanged.
                rows = page.rows.sortedByDescending { it.cursor }.map(::entryFor),
                emptyCopy = EMPTY,
            ),
        ),
        staleNotice = if (page.stale) STALE_NOTICE else "",
    )

    /**
     * @return the record as one sentence, plus the span of it the row emphasises.
     *
     * The emphasis is a SUBSTRING of the body rather than a fragment beside it, which is what
     * `activityRow` takes and why: `.ln b` is inline, so the component needs to know which part of
     * the sentence it marks, not two pieces to glue.
     */
    private fun entryFor(row: JournalRow): ActivityEntry {
        val session = row.sessionId.ifEmpty { null }
        val group = row.group.ifEmpty { null }
        return ActivityEntry(
            cursor = row.cursor,
            body = listOfNotNull(session, row.type, group).joinToString(FIELD_SEPARATOR),
            // The SESSION, now that one reaches this screen. It is what the mock emphasises -- the
            // thing the row is about -- and it falls back to the Group only where the record
            // carries no session, so the eye still lands on a wire token rather than on a type.
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
            // `JournalRow` also carries no title to prefer, and that is downstream of the same
            // ruling rather than the reason for it: `swarmmobile.JournalEntry` is the wire's
            // record shape, and a name on it would exist only to be ignored here.
            emphasis = session ?: group,
        )
    }
}
