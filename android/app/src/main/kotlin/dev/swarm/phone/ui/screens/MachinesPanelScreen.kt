package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.MachineFreshness

/**
 * The machine switcher PANEL model (wave R4 follow-on, bead agents-tracker-0ox9): what the
 * composed switcher screen shows, in the module's established shape (PB-DS-9 -- copy lives on the
 * screen model, views take data and copy from it, logic lives where the JVM suite can drive it).
 *
 * [MachinesScreen] stays the pure first-run resolver and row set; this is the panel a draw spends
 * once the resolver has answered [MachinesDestination.MACHINES]. The split mirrors
 * TriageInboxScreen/InboxScreen: the resolver decides whether the world exists, the panel says
 * what is in it.
 *
 * @property title the drill header's title -- the entry's own recorded name.
 * @property back where the drill chevron goes, in words ([SessionDetailPanel]'s precedent).
 * @property rows one row per pairing, folded by machine id ([MachinesScreen.rows] -- never a
 *  second derivation that can drift).
 * @property capNotice ADR-018's documented foreground connection cap, stated ONLY when rows
 *  exceed it. Null inside the cap: a warning about nothing teaches a user to ignore the one that
 *  matters.
 * @property selectedMachineId the machine this phone last switched to, or empty for none
 *  (round 3). IT IS THE SURFACE'S RECORD AND NOT THE FACADE'S, because the facade has none:
 *  `MachineInfo` carries no current-machine fact and the roster's Connected flag only moves when
 *  the roster exceeds the cap -- so a successful switch produced a byte-identical panel, the
 *  draw's equality guard early-returned, and the primary control was indistinguishable from a
 *  dead button. Carrying it HERE is what makes the panel differ, which is what ends that.
 */
data class MachinesPanel(
    val title: String,
    val back: String,
    val rows: List<MachineRowModel>,
    val capNotice: String?,
    val selectedMachineId: String = "",
)

object MachinesPanelScreen {

    /**
     * The navigation entry that leads to the switcher, recorded HERE so the composition spends it
     * rather than typing its own words -- a label typed at a call site is the drift that made the
     * pairing panel exist and be unfindable (defect shape 1, agents-tracker-64rf).
     */
    const val ENTRY_LABEL = "Computers"

    /** The entry row's second line: what is on the other side of it. */
    const val ENTRY_SUBLABEL = "Add, switch, or forget a computer"

    /** Playbook 4.1 step 4's own words: the developer "chooses Add computer". */
    const val ADD_LABEL = "Add computer"

    /** What belongs in the add form's first field. The id is the pairing's identity (MM4). */
    const val ADD_ID_HINT = "Machine id"

    /** The add form's optional display name; a machine may publish none. */
    const val ADD_NAME_HINT = "Computer name (optional)"

    /** The panel's own refusal of an add with no machine id: nothing reached the facade. */
    const val ADD_ID_MISSING = "Add computer needs the machine id; nothing was sent."

    /** Playbook 4.9's own words: phone-side, distinct from machine-side revoke. */
    const val FORGET_LABEL = "Forget this computer"

    /**
     * What Forget ASKS before it acts (round 2). Forgetting a pairing destroys its keys,
     * namespace and caches on this phone with no undo -- and `kill`, which ends ONE session,
     * has asked since S24 (mrq5's own argument for the more destructive control). The question
     * names what is destroyed, what is NOT (the computer itself), and carries MM8's reassurance
     * so the dialog does not read as app-wide data loss.
     */
    const val FORGET_CONFIRM = "Forget this computer? This phone deletes its pairing keys and " +
        "cached sessions for it. The computer itself is untouched, and other computers are " +
        "unaffected."

    /**
     * The resolver's PAIR_ONLY answer AS A SENTENCE (round 2). With zero machines the entry
     * used to bounce back to Settings composing nothing and saying nothing -- the silent no-op
     * shape. An answer the user cannot read is not an answer.
     */
    const val PAIR_FIRST = "No computers are paired yet. Pair this phone with a computer " +
        "first; Computers fills in from the first pairing."

    /**
     * WHAT ADD COMPUTER CANNOT FINISH, on screen and not only in a verification file (round 3).
     *
     * The form takes a machine id and a name and calls `App.AddMachine`, which registers the
     * pairing BESIDE the existing ones -- and that is the whole of what this slice does.
     * `mobile/machines.go:19-21` states the two limits in its own words and they belong where the
     * user is typing: a second machine's namespace lands AWAITING that machine's pairing ceremony
     * (bead agents-tracker-ak2s owns the ceremony; it is deliberately not wired here), and
     * `SelectMachine` records the viewed pairing without re-targeting the live relay session. A
     * form that cannot be completed and says nothing leaves a permanently stale row and a user
     * with no way to know why -- which is the overclaim in the shape of a screen.
     */
    const val ADD_LIMITS = "Adding a computer registers it on this phone. That computer still " +
        "needs its own pairing ceremony before it can answer, and switching to it records which " +
        "computer you are viewing without moving this phone's live session yet."

    /**
     * What ADD ASKS before it acts (round 3), on [FORGET_CONFIRM]'s argument applied to the
     * larger blast radius.
     *
     * Add runs `App.Stop` around the registration, because the MM6 migration must not race a live
     * drain -- and `Stop` is `suspendInput`: every buffered keystroke is resolved UNDELIVERED,
     * every input lease is severed, and the link really drops. That is strictly more destructive
     * than forgetting one pairing, and forgetting one pairing asks. The question names what is
     * briefly lost, and what is not, so it does not read as data loss.
     */
    const val ADD_CONFIRM = "Add this computer now? This phone briefly disconnects from your " +
        "computers while the pairing is registered, and anything typed but not sent is " +
        "discarded. No pairing is removed and nothing already saved is lost."

    /**
     * The refusal a SECOND add earns while the first is still crossing (round 3). The controls
     * here are rebuilt per draw, so `VerbDispatch.press`'s per-control fence cannot hold them;
     * the lane's keyed fence does, and what it refuses must be SAID -- a tap dropped in silence
     * is the silent no-op shape on the app's most destructive control.
     */
    const val ADD_IN_FLIGHT = "Add computer is still running; nothing was sent."

    /**
     * The row mark for the machine this phone last switched to (round 3): the first word of that
     * row's status line, so the mark is where the row's other facts already are.
     */
    const val SELECTED_MARK = "selected"

    /**
     * What a SUCCESSFUL switch says (round 3). It used to say nothing at all: `switchComputer`
     * settled with the default no-op, so the one honest signal a user had was a screen that did
     * not change.
     *
     * IT STATES THE LIMIT IN THE SAME BREATH, because the alternative is a confirmation that
     * overstates what happened: `mobile/machines.go:19-21` -- SelectMachine records the viewed
     * pairing and feeds the least-recently-viewed connection policy; it does NOT re-target the
     * App's live relay session yet.
     */
    fun switchedTo(displayName: String): String =
        "Selected $displayName. This phone's live session has not moved to it yet."

    /** The aggregate inbox destination across every pairing (inbox.global). */
    const val GLOBAL_INBOX_LABEL = "All sessions"

    /** Where the switcher's chevron goes: the screen that named the entry. */
    private const val BACK = "Back to settings"

    /**
     * The panel over the model's OWN row set -- [MachinesScreen.rows] folds duplicate ids, and a
     * panel that re-derived the fold would be the second copy that drifts.
     *
     * @param cap the documented foreground connection cap the rows were arbitrated under
     *  (MachineList.Cap, ADR-018): rendered honestly when exceeded, silent when not.
     * @param selected the machine the surface last switched to, or empty for none. Defaulted so
     *  a caller with no selection to report says none rather than being frozen into one.
     */
    fun of(machines: List<MachineRowModel>, cap: Int, selected: String = ""): MachinesPanel {
        val rows = MachinesScreen.rows(machines)
        return MachinesPanel(
            title = ENTRY_LABEL,
            back = BACK,
            rows = rows,
            capNotice = capNoticeFor(rows.size, cap),
            selectedMachineId = selected,
        )
    }

    /**
     * One row's second line: the reachability word the MODEL computes (so no view invents its own
     * vocabulary) and the needs-input count, playbook 4.2:198's fourth fact.
     */
    fun statusLine(row: MachineRowModel): String =
        listOfNotNull(row.reachability, waitingLine(row)).joinToString(", ")

    /**
     * The clocked overload (round 2): all FOUR of playbook 4.2:198's row facts on one line --
     * reachability, last-sync age, needs-input -- because only three reached the screen and a
     * parked row must VISIBLY show its last-sync age (ADR-018 MM3, playbook 4.2:200-202).
     * The one-argument [statusLine] is untouched, so every existing caller and assertion stays
     * exactly as true as it was.
     *
     * @param nowUnixMs this phone's clock, taken rather than read so the JVM suite freezes the
     *  words without a clock -- [MachineFreshness.sinceLastHeard]'s own arrangement, and the
     *  same seam PhoneSurface already spends into `SyncStatus.of` one screen over.
     * @param selected whether this row is [MachinesPanel.selectedMachineId]'s machine (round 3).
     *  The mark leads the line because it is the fact a user came to the screen to change, and
     *  it is DEFAULTED so every existing caller and assertion stays exactly as true as it was.
     */
    fun statusLine(row: MachineRowModel, nowUnixMs: Long, selected: Boolean = false): String =
        listOfNotNull(
            SELECTED_MARK.takeIf { selected },
            row.reachability,
            lastSyncLine(row, nowUnixMs),
            waitingLine(row),
        ).joinToString(", ")

    /**
     * The age spends the app's ONE elapsed-duration model rather than a second formatter that
     * can drift; a zero stamp is "never synced", because "synced 19700d ago" reports the epoch
     * as a fact about this pairing (SyncStatus.NEVER's own reasoning).
     */
    private fun lastSyncLine(row: MachineRowModel, nowUnixMs: Long): String {
        val since = MachineFreshness(silent = false, lastHeardUnixMs = row.lastSyncUnixMs)
            .sinceLastHeard(nowUnixMs)
        return if (since.isEmpty()) "never synced" else "synced $since ago"
    }

    private fun waitingLine(row: MachineRowModel): String? = when {
        row.needsInput == 1 -> "1 session needs input"
        row.needsInput > 1 -> "${row.needsInput} sessions need input"
        else -> null
    }

    /**
     * The broken pairing's notice: its OWN fault, named, and the sentence that stops a user
     * reaching for the wholesale remedy that destroys every pairing (MM8, machines.recovery --
     * the same sentence App.SelectMachine's refusal carries). Null on a healthy row: a fault
     * sentence over a working pairing is the dishonest rendering in the other direction.
     */
    fun brokenNotice(row: MachineRowModel): String? {
        if (!row.broken) return null
        val fault = row.brokenReason.ifEmpty { "this pairing's saved state failed to open" }
        return "$fault. Forget this computer or pair it again; other computers are unaffected."
    }

    /** ADR-018's cap sentence, stating the number the rows were arbitrated under. */
    private fun capNoticeFor(count: Int, cap: Int): String? =
        if (count > cap) {
            "Only $cap computers stay connected at once; the rest are parked and shown stale."
        } else {
            null
        }
}
