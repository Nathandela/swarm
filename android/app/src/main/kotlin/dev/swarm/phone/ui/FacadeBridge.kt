package dev.swarm.phone.ui

import dev.swarm.phone.keys.ConnectionState
import dev.swarm.phone.ui.screens.GlobalInboxRowModel
import dev.swarm.phone.ui.screens.GlobalInboxScreen
import dev.swarm.phone.ui.screens.LaunchAvailability
import dev.swarm.phone.ui.screens.LaunchPresetScreen
import dev.swarm.phone.ui.screens.MachineRowModel
import dev.swarm.phone.ui.screens.PresetRowModel
import dev.swarm.phone.ui.screens.SessionCapabilityFacts
import dev.swarm.phone.ui.screens.TerminalFallbackBinding
import dev.swarm.phone.ui.screens.TerminalFallbackModel
import dev.swarm.phone.ui.screens.TerminalGrid
import swarmmobile.App
import swarmmobile.Session

/**
 * Phase B slice S16 -- where the pure screen models meet the bound facade.
 *
 * WHY THIS FILE EXISTS AT ALL. The screen models in this package are pure Kotlin so their
 * mapping is testable without an Activity, which is right and is the shape
 * dev.swarm.phone.runtime.PermissionStateResolver already established here. The hazard is the
 * obvious one, and this project has hit it repeatedly: a beautifully-tested [TriageInbox] that
 * nothing constructs from `swarmmobile.App`, with the real screen reading the facade directly
 * and disagreeing with every assertion. This adapter is the only place the two meet, so there
 * is one mapping to review rather than one per screen.
 *
 * IT IS A MAPPING AND NOTHING ELSE. No policy is decided here -- every question of what to
 * show is answered by the model it hands the inputs to. Anything a call cannot answer is a
 * PARAMETER rather than a default, because a plausible-looking value invented at this seam is
 * indistinguishable on screen from one the machine sent.
 *
 * EVERY MODEL IN THIS PACKAGE HAS A PRODUCTION CALLER HERE. That is the property the file
 * exists for, so it is stated as a claim rather than left to be inferred: a model reachable
 * only from a test is one the real screen may quietly disagree with, which is the failure the
 * fence in android/gate/s16_ui_test.go is pointed at.
 */
class FacadeBridge(private val app: App) {

    /**
     * The roster as rows. gomobile has no list type, so a collection crosses as an opaque
     * handle with Count/At and is walked here once rather than at every screen.
     */
    fun roster(): List<SessionRow> = rosterView().rows

    fun sessionRow(sessionId: String): SessionRow = rowOf(app.session(sessionId))

    /**
     * ADR-017 T2 rule 3: the MACHINE's own capability record for one session, mapped and not
     * derived.
     *
     * IT IS A MAPPING AND NOTHING ELSE, which is this file's whole rule. Every fail-closed
     * decision was already taken machine-side and then once more in `phonecore.RouteSession`,
     * whose answer rides on `Session.destination`; a session with no record, an inconsistent
     * one, one binding no session instance, or a machine whose profile declares no
     * capability-record version all arrive here as `structuredChat = false`, which is the honest
     * status card. Nothing about that is re-decided at this seam.
     */
    fun sessionCapabilities(sessionId: String): SessionCapabilityFacts {
        val s = app.session(sessionId)
        return SessionCapabilityFacts(structuredChat = s.structuredChat)
    }

    /**
     * The routed destination and the honest header's facts, for the one screen ADR-017 T1 routes
     * a `terminal_fallback` session to. Null for every other session, because
     * [TerminalFallbackModel.from] answers null unless the MACHINE chose that destination.
     */
    fun terminalFallback(sessionId: String): TerminalFallbackModel? =
        TerminalFallbackModel.from(app.session(sessionId))

    /**
     * The fallback screen's own binding to the facade, FOR A SESSION THE MACHINE ROUTED THERE
     * AND FOR NO OTHER -- null otherwise.
     *
     * IT USED TO RETURN A LIVE HANDLE FOR ANY SESSION ID, and the closing review walked one line
     * through every R8 gate with it: `bridge.terminalFallbackBinding(id).watch()`, appended to a
     * structured chat screen, opened a real watch with no capability read anywhere on the path.
     * The binding's constructor is now private and
     * [TerminalFallbackBinding.forRoutedSession] performs the read, so this method cannot hand
     * out what it does not have -- the refusal is a `null`, not a gate note.
     */
    fun terminalFallbackBinding(sessionId: String): TerminalFallbackBinding? =
        TerminalFallbackBinding.forRoutedSession(app, sessionId)

    /**
     * The machine-sanitized grid for one fallback session.
     *
     * IT DELEGATES, AND THE DELEGATION IS THE POINT. The read used to be `app.peek(sessionId)`
     * right here, and `App.Peek` reads the snapshot cache for ANY session id with no capability
     * check -- so the fallback render path was reachable from a file that is not the fallback
     * screen and does no record read, which is exactly what ADR-017's gate note forbids. The
     * capability read and the peek now live together in the one allowlisted file.
     */
    fun terminalRows(sessionId: String): TerminalGrid =
        terminalFallbackBinding(sessionId)?.grid() ?: TerminalGrid.EMPTY

    /**
     * PB-APP-2's screen, with its PB-APP-8 verdict READ OFF THE HANDLE THAT CARRIED IT.
     *
     * This used to ask `App.StreamState("journal")` beside the roster instead, and the two are
     * the same fact: `App.Roster` sets `SessionList.stale` from exactly that stream. Asking
     * separately is what `SessionList.Stale` exists to stop -- its own Go doc says the flag
     * rides on the handle "rather than being left to the caller ... a screen that has to
     * remember to call StreamState beside every Roster() is a screen that will forget once, and
     * the failure is silent and looks exactly like a working app".
     *
     * It had already gone wrong in the softer way: `SessionList.Stale` was reached by NO Kotlin
     * at all, while a unit test asserting the inbox is never presented as live carried the
     * comment "this is the assertion that makes swarmmobile.SessionList.Stale reach a user".
     * It did not -- that assertion drives `TriageInbox.from`'s parameter, and what fills the
     * parameter is this line.
     */
    fun triageInbox(): TriageInbox {
        val roster = rosterView()
        return TriageInbox.from(roster.rows, journalStale = roster.stale)
    }

    /**
     * Wave R4's machine-switcher snapshot (machines.list): every paired machine's four facts of
     * playbook 4.2:198, WITH the documented connection cap they were arbitrated under.
     *
     * TOGETHER IS THE POINT, [rosterView]'s reason one handle over: the cap rides
     * `MachineList.Cap` "so the switcher can render the limitation honestly" (ADR-018), and a
     * second `App.Machines` call to fetch it would be a cap from a different arbitration than the
     * rows on screen. The broken-pairing fields cross verbatim (MM8): the row's own fault is the
     * screen's to render, never this adapter's to soften.
     */
    fun machines(): MachinesSnapshot {
        val list = app.machines()
        return MachinesSnapshot(
            rows = (0 until list.count()).map { index ->
                val info = list.at(index)
                MachineRowModel(
                    machineId = info.getID(),
                    displayName = info.getDisplayName(),
                    connected = info.getConnected(),
                    lastSyncUnixMs = info.getLastSyncUnixMs(),
                    needsInput = info.getNeedsInput().toInt(),
                    broken = info.getBroken(),
                    brokenReason = info.getBrokenReason(),
                )
            },
            cap = list.cap().toInt(),
        )
    }

    /**
     * Wave R4's aggregate inbox (inbox.global): one list across every pairing, folded by
     * [GlobalInboxScreen.rows] on the TUPLE (machine_id, session_id) -- the R4 exit criterion
     * (MM4). The fold is the MODEL's, applied here the way [triageInbox] applies
     * `TriageInbox.from`, so the screen and its JVM suite drive the same function.
     */
    fun globalInbox(): List<GlobalInboxRowModel> {
        val list = app.globalInbox()
        return GlobalInboxScreen.rows(
            (0 until list.count()).map { index ->
                val item = list.at(index)
                GlobalInboxRowModel(
                    machineId = item.getMachineID(),
                    machineName = item.getMachineName(),
                    sessionId = item.getSessionID(),
                    title = item.getTitle(),
                    needsInput = item.getNeedsInput(),
                )
            },
        )
    }

    /**
     * PB-APP-3's event list -- the ACTIVITY destination's feed.
     *
     * It read "and the machine pane's activity log" until agents-tracker-nx44.3: that pane was
     * deleted with the Machines destination, and it never had a production caller anyway.

     *
     * It returns the PAGE and not the rows: the cursor to read from next and the stream's stale
     * mark are on the handle, and dropping them left a caller unable to advance and unable to
     * say the log has a hole. See [JournalPageView].
     */
    fun journal(afterCursor: Long, limit: Long): JournalPageView {
        val page = app.readJournal(afterCursor, limit)
        return JournalPageView(
            rows = (0 until page.count()).map { index ->
                val entry = page.at(index)
                JournalRow(
                    cursor = entry.getCursor(),
                    sessionId = entry.getSessionID(),
                    type = entry.getType(),
                    group = entry.getGroup(),
                )
            },
            nextCursor = page.nextCursor(),
            stale = page.stale(),
        )
    }

    /**
     * ADR-009 (1)'s chat transcript: one session's interaction items, oldest first.
     *
     * IT IS NOT [journal] NARROWED TO A SESSION, and `App.ReadTranscript`'s own doc says why at
     * length: the journal page is an in-memory log of record TYPES, bounded and rebuilt empty by
     * every process death, while the transcript is the folded DURABLE model the core holds --
     * records merged by `item_id`, `agent_message` increments concatenated, the latest plan
     * revision kept. Serving a conversation from the page would show it empty after the SIGKILL
     * Android hands out routinely, with the durable receive high-water refusing the relay's
     * redelivery of the frames that built it.
     *
     * It returns the PAGE for [journal]'s reason: the cursor to read from next and the stream's
     * stale mark ride on the handle, and a caller handed a bare list can neither advance nor say
     * the conversation has a hole in it. The staleness is the JOURNAL's, because an interaction
     * item IS a journal record (IS-LAYER-1) and inherits that repair channel (IS-LAYER-4).
     *
     * @param afterCursor the ordering cursor of the last item the caller already has; 0 reads from
     *  the start of what the phone holds. An item UPDATED in place keeps its first record's cursor
     *  (IS-LAYER-3), so a caller that wants the update re-reads the tail rather than paging past
     *  it -- which is what the `interaction` event exists to prompt.
     * @param limit at most this many items; a non-positive value is the core's own bound.
     */
    fun transcript(sessionId: String, afterCursor: Long, limit: Long): TranscriptPageView {
        val page = app.readTranscript(sessionId, afterCursor, limit)
        return TranscriptPageView(
            items = (0 until page.count()).map { index ->
                val item = page.at(index)
                InteractionItem(
                    sessionId = item.getSessionID(),
                    itemId = item.getItemID(),
                    cursor = item.getCursor(),
                    kind = item.getKind(),
                    status = item.getStatus(),
                    text = item.getText(),
                    body = item.getBody(),
                    truncated = item.getTruncated(),
                    degraded = item.getDegraded(),
                    resolved = item.getResolved(),
                    // Wave R6 (M2.2/M2.4): the additive facts the transcript renders per row.
                    // Mapped here so they cross the boundary instead of dying one hop short of
                    // the screen (android/gate/r6_chat_ui_test.go pins each getter).
                    detail = item.getDetail(),
                    toolKind = item.getToolKind(),
                    turnId = item.getTurnID(),
                    tsUnixMs = item.getTSUnixMs(),
                    source = item.getSource(),
                    // AND THE SIXTH, WHICH THE COMMENT ABOVE ALREADY DESCRIBED AND THE LIST DID
                    // NOT CARRY (owner ruling R6, 2026-08-26). `phonecore.Item.OperationID`,
                    // `mobile.TranscriptItem.OperationID` and screen_coverage.tsv's
                    // `transcript.read` row all argue this field out in full -- the daemon stamps
                    // its own operation id onto the prompt the CLI echoes back, so the phone can
                    // tell WHICH of its sends the agent received -- and `mobile/app.go` assigns
                    // it. Every layer was complete except the one that hands the item to a
                    // renderer, so R6's settle could never fire and a sent bubble stayed pending
                    // forever. This finishes the sentence the comment above was already making.
                    operationId = item.getOperationID(),
                )
            },
            nextCursor = page.nextCursor(),
            stale = page.stale(),
        )
    }

    /**
     * ADR-014 §2's honest floor: the machine has said that nothing older than this session's
     * oldest folded item is still retained, so "load earlier" has nothing left to fetch.
     *
     * GUARDED, the way [pendingApproval] guards its own read and for the same reason: this is
     * reached from [dev.swarm.phone.PhoneSurface]'s draw, which runs on the main looper on every
     * journal event, and a refusal propagating out of a render would take the app down while the
     * user watches an agent work. FALSE is the safe answer -- it offers the control, and a control
     * that comes back empty is a smaller harm than a screen that cannot be drawn.
     */
    fun historyFloor(sessionId: String): Boolean = try {
        app.historyFloor(sessionId)
    } catch (refused: Exception) {
        false
    }

    /**
     * The OTHER end of the same control (Wave R6 review round 2): THIS PHONE could not hold the
     * page the machine sent, so more history exists and this handset cannot show it.
     *
     * IT IS NOT [historyFloor] AND MUST NEVER BE COLLAPSED INTO IT. The floor is the machine's
     * sentence -- "nothing older than this is retained" -- and a screen that dropped its control
     * on THIS fact while showing the floor's silence would be telling the reader they had
     * reached the beginning of a conversation that goes further back. What the phone's own bound
     * replaces is worse than either: `insertLocked` put each page at the front and `trimLocked`
     * evicted oldest-first, so at the retention bound a page landed and was evicted inside the
     * same call and the user tapped forever with nothing moving.
     *
     * GUARDED for [historyFloor]'s reason, and FALSE is the safe answer here too: it goes on
     * offering the control, which is a smaller harm than a screen that cannot be drawn.
     */
    fun historyAtCapacity(sessionId: String): Boolean = try {
        app.historyAtCapacity(sessionId)
    } catch (refused: Exception) {
        false
    }

    /**
     * The roster handle read ONCE: its rows and its own staleness verdict, together.
     *
     * Together is the point. Two calls to `App.Roster` would answer two different questions
     * about two different reads, and the second could report a stream repaired between them --
     * which is a list drawn from the holed read, labelled with the whole one.
     */
    private fun rosterView(): RosterView {
        val list = app.roster()
        return RosterView(
            rows = (0 until list.count()).map { index -> rowOf(list.at(index)) },
            stale = list.stale(),
        )
    }

    private data class RosterView(val rows: List<SessionRow>, val stale: Boolean)

    /**
     * The machine roster read ONCE: its rows and the cap they were arbitrated under, together --
     * see [machines].
     */
    data class MachinesSnapshot(val rows: List<MachineRowModel>, val cap: Int)

    /**
     * WHAT IS LEFT OF PB-APP-4's grid read, and now of PB-INPUT-2's lease state too.
     *
     * `terminalPeek` stood here and asked `App.Peek` for the daemon-rendered snapshot. ADR-009
     * (2)/(3) deleted both ends of that: no phone surface issues a `terminal_watch`, so the cache
     * it read is never filled, and no screen draws a grid. What the call was ALSO carrying was the
     * lease -- and owner ruling R1 (2026-08-26) deleted that in its turn, because composer_send
     * takes none at any layer.
     *
     * SO WHAT CROSSES HERE IS THE LINK, and it is still worth crossing: input is live-only and
     * never queued, so whether the composer may send at all turns on it. The read is a joining,
     * which is what this adapter is for.
     */
    fun sessionLease(sessionId: String): SessionLease =
        SessionLease(sessionId = sessionId, online = isOnline())

    /**
     * PB-INPUT-1's ledger: what this phone took from the user and could not deliver.
     *
     * IT IS THE WHOLE LEDGER AND THE SCREEN NARROWS IT. `App.UndeliveredInputs` is process-wide --
     * a phone loses keystrokes for every session at once when the link drops -- and
     * [UndeliveredLedger.forSession] is where the narrowing is decided, because that decision has
     * a consequence worth testing on a JVM: the overflow count belongs to the bound and to no
     * session, so it survives the filter.
     *
     * IT READS `Dropped`, which nothing did. That method sat in android/unbound-verbs.tsv as one of
     * the four bound receivers that lived in the ledger's own blind spot -- "a fact the facade goes
     * to the trouble of carrying and the adapter dropped on the floor".
     *
     * AN UNREADABLE LEDGER IS EMPTY AND NOT A CRASH: this runs inside a render, and a phone whose
     * core cannot answer has a screen to draw either way. What it must never do is claim a loss
     * that did not happen, which [UndeliveredLedger.EMPTY] does not.
     */
    fun undelivered(): UndeliveredLedger = try {
        val list = app.undeliveredInputs()
        UndeliveredLedger(
            entries = (0 until list.count()).map { index ->
                val entry = list.at(index)
                UndeliveredInput(
                    sessionId = entry.getSessionID(),
                    bytes = entry.getBytes().toInt(),
                    reason = entry.getReason(),
                    atMillis = entry.getAtMillis(),
                )
            },
            dropped = list.dropped().toInt(),
        )
    } catch (unreadable: Exception) {
        UndeliveredLedger.EMPTY
    }

    /**
     * PB-INPUT-1's acknowledgement half.
     *
     * IT IS A SEPARATE VERB AND NOT A DRAINING [undelivered], which is the facade's own argument:
     * "a screen that OPENS must see the backlog, and a user who DISMISSES it says so once, for
     * every screen". It does not disable the ledger -- a clear that stopped recording would
     * satisfy the same assertion and silently drop every future loss.
     */
    fun clearUndelivered() = app.clearUndeliveredInputs()

    /**
     * PB-SYNC-1's repair for the channel a CONVERSATION rides on (agents-tracker-upbo).
     *
     * IT TAKES NO CHANNEL NAME, and that is `REPAIR_CHANNELS`' own rule rather than a narrowing
     * chosen here: the four names cross as bare strings, so a caller that typed one would be a
     * second alphabet with nothing joining it to the core's --
     * `android/gate/pbapp8_repairchannels_test.go` refuses exactly that. The channel is the
     * JOURNAL because an interaction item IS a journal record (IS-LAYER-1) and inherits its repair
     * channel (IS-LAYER-4), and because the journal is the only one of the four with a
     * stream-scoped repair verb at all -- `App.Resync`'s own doc says the other three are mended
     * by a snapshot, a durable outcome and a machine-side re-grant. The read position is rewound
     * for every admitted resync regardless of channel, so this one press repairs the transport
     * hole under all four.
     *
     * IT IS NOT WRAPPED IN A `try`, and that is the difference between this and [undelivered]
     * above. A read runs inside a render and must not take the screen down; this is a PRESS, and
     * its refusal is the whole point -- the verb is rate-bounded per section 6.0 and answers
     * ErrClassRateLimited, which PB-APP-9's table renders with its own remedy. Swallowing it here
     * would be a button that reports success for a repair the budget refused.
     */
    fun repairTranscript() = app.resync(REPAIR_CHANNELS.first())

    /**
     * The unresolved `approval_request` this session is blocked on, or null when there is none.
     *
     * IT IS THE FIRST ONE THE MACHINE IS STILL WAITING ON, oldest first, which is `App
     * .PendingApprovals`' own order. One card at a time is the surface's shape rather than the
     * wire's: the sheet is D4.4's heaviest material, "reserved for the moment of decision", and a
     * stack of them would ask the user to answer a question while a second one is on screen.
     *
     * IS-LIFE-2 IS WHAT MAKES THAT SAFE. Every `approval_request` reaches exactly one
     * `approval_resolved` -- including cancelled, superseded, expired and answered at the machine
     * -- and the Go side drops a resolved item out of this list, so a card that has stopped being
     * a question stops being on screen without this side deciding anything.
     *
     * A SESSION WITH NO CARD IS NOT A FAILURE, which is the same absorption [isAwaitingFirstFrame]
     * was written for one verb over: a phone that holds nothing for the session answers null, and
     * every other refusal keeps propagating to the router that renders it with a remedy.
     */
    fun pendingApproval(sessionId: String): ApprovalItem? = try {
        firstApprovalFor(sessionId)
    } catch (refused: Exception) {
        if (!isAwaitingFirstFrame(classOf(refused))) throw refused
        null
    }

    private fun firstApprovalFor(sessionId: String): ApprovalItem? {
        val page = app.pendingApprovals()
        for (index in 0 until page.count()) {
            val item = page.at(index)
            if (item.getSessionID() != sessionId) continue
            // RESOLVED IS ASKED EVEN THOUGH THE LIST IS OF UNRESOLVED CARDS. IS-LIFE-2's guarantee
            // is what dismisses a stale card on every surface, and the flag is durable with the
            // item precisely so it survives the process death Android hands out; reading it here
            // costs nothing and means this side never draws a question the machine has stopped
            // asking, whichever of the two ends resolved it.
            if (item.getResolved()) continue
            val approval = ApprovalItem.of(
                sessionId = item.getSessionID(),
                itemId = item.getItemID(),
                body = item.getBody(),
            )
            if (approval != null) return approval
        }
        return null
    }

    /**
     * What a person calls one session -- `swarmmobile.Session.Title` -- or empty where the roster
     * cannot answer for it (agents-tracker-ksvb.1).
     *
     * GUARDED, for the reason `pendingApproval` above is guarded: `App.Session` REFUSES an id the
     * cached roster does not hold, and a drill-down on a session that has just left the roster is
     * an ordinary race rather than a failure. What a refusal costs here is a LABEL, and the screens
     * fall back to the id -- so propagating it would kill a surface over a nicety.
     *
     * EMPTY IS NEVER A NAME. It is "this phone could not read one", and every caller renders the
     * id instead, which is what all of them rendered before this method existed.
     */
    fun sessionTitle(sessionId: String): String = try {
        app.session(sessionId).getTitle()
    } catch (unknown: Exception) {
        ""
    }

    /**
     * What the GO side calls this refusal, or nothing when it cannot say.
     *
     * `App.ErrorClass` fails through `a.ready()` like every other verb, so a phone whose core has
     * closed cannot classify its own error. AN UNNAMEABLE FAILURE IS NOT THE BENIGN ONE: the empty
     * string is refused by [isAwaitingFirstFrame], so the ORIGINAL refusal propagates rather than
     * being replaced by the secondary one raised while trying to describe it.
     */
    private fun classOf(refused: Exception): String = try {
        app.errorClass(refused.message.orEmpty())
    } catch (unreadable: Exception) {
        ""
    }

    fun connectionBanner(): ConnectionBanner =
        ConnectionBanner.of(ConnectionState.of(app.connectionState()))

    /**
     * PB-APP-11, and it is a SEPARATE question from [connectionBanner] on purpose.
     *
     * The transport can only report what it knows: the socket is up and the polls are
     * succeeding. A relay that answers every poll with an empty page while withholding the
     * machine's frames leaves that reading "online" forever, with no gap for any staleness
     * mechanism to key on. This asks the one thing the relay cannot fake -- how long since the
     * machine's own AAD-covered timestamp -- and it is PULLED for [clockBanner]'s reason: a
     * screen that opens after the fact was never sent an event.
     */
    fun machineFreshness(): MachineFreshness = app.machineFreshness().let {
        MachineFreshness(silent = it.getSilent(), lastHeardUnixMs = it.getLastHeardUnixMs())
    }

    /**
     * What the MACHINE calls itself (agents-tracker-ksvb.1): `App.MachineName`, the hostname it
     * published in the pairing payload, or empty where it published none.
     *
     * IT IS SAFE FROM A RENDER, which is the question android/unbound-verbs.tsv makes anyone ask
     * of a verb reached from a draw. `App.Presence` is barred here because it is a blocking relay
     * round-trip at a 10 s call timeout; this reads durable state behind a mutex, the same class
     * as `App.ClockVerdict` and `App.StreamState`, which `LinkPanel` already renders on this
     * argument.
     *
     * IT IS NOT LABELLED HERE. `MachineLabel.of` is where the name and the endpoint id become one
     * string a person reads, and doing it at this seam would put a display decision in the
     * adapter that "decides no policy".
     */
    fun machineName(): String = app.machineName()

    /**
     * The RELAY's last word on whether the machine is reachable (PB-APP-5, agents-tracker-nx44.3).
     *
     * IT IS `App.MachinePresence` AND IT MAY NEVER BE `App.Presence`. That verb is a relay
     * round-trip at the transport's 10 s call timeout, and this surface's render is driven by an
     * event stream -- one RPC per journal record, on the main thread. `android/unbound-verbs.tsv`
     * ledgers it barred for exactly that reason. This one is an O(1) read of a cache the relay
     * goroutine fills on its own 15 s cadence, which is the arrangement that made the settings
     * CONNECTION section landable at all.
     *
     * IT RETURNS THE STATE AND NOT THE READING'S AGE, which the facade also carries, and the
     * reason is that the age is the WEAKER of the two qualifications available here. A cached
     * opinion that cannot say how old it is renders staleness as liveness -- so Go resets the
     * cache to `unknown` the moment the link drops (`presenceCache.forget`), which is the case an
     * age would have had to catch. What is left is a reading that ages while the link is up and
     * the poll keeps failing, and PB-APP-11 already requires the answer to that: the section
     * renders `machineFreshness` beside this word, which is the machine's OWN authenticated stamp
     * and the one thing the relay cannot fake. A fresh reading of a withholding relay is worth
     * nothing; a stale reading beside a machine that is demonstrably speaking is corroborated.
     */
    fun machinePresence(): String = app.machinePresence().state

    /**
     * The machines this phone can NAME, keyed by the endpoint id the roster namespaces their
     * sessions under (agents-tracker-ksvb.1). The inbox's scope chips read it.
     *
     * IT HAS AT MOST ONE ENTRY AND IS STILL A MAP. Pairing is to one machine and no facade verb
     * enumerates them -- the settings CONNECTION section states the same fact about its own single
     * row -- but the
     * ROSTER is namespaced per machine, so a chip bar that assumed one would label another
     * machine's sessions with this machine's name. A lookup that misses is the honest shape: the
     * chip renders its endpoint id.
     *
     * EMPTY WHERE THERE IS NOTHING TO SAY, and that covers every degenerate case in one place: no
     * pairing, a machine that published no hostname, or a state this phone cannot read. All three
     * leave the chips exactly as they were before this method existed.
     */
    fun machineNames(): Map<String, String> = try {
        val endpoint = app.stateSummary().takeIf { it.paired }?.machine.orEmpty()
        val name = app.machineName()
        if (endpoint.isEmpty() || name.isEmpty()) emptyMap() else mapOf(endpoint to name)
    } catch (unreadable: Exception) {
        emptyMap()
    }

    /**
     * PB-APP-8 for one repair channel.
     *
     * Stale and repairing are asked for SEPARATELY because they are orthogonal facts: a stream
     * is stale-and-repairing or stale-and-idle, and PB-SYNC-3 forbids expressing the second by
     * clearing the first -- clearing on the request shows the user a known hole as live. So
     * `StreamState` keeps answering "stale" throughout a repair and `ResyncPending` carries the
     * in-flight half.
     */
    fun streamView(stream: String): StreamView = StreamView(
        stream = stream,
        stale = app.streamState(stream) == STREAM_STALE,
        resyncPending = app.resyncPending(stream),
    )

    /**
     * All four repair channels, for the one screen whose subject is the link itself.
     *
     * PER STREAM AND NEVER GLOBAL, which is PB-APP-8's whole discipline: the journal can have an
     * unrepaired hole while the terminal is live, and a single mark either understates the first
     * or slanders the second. So this returns four verdicts and not a boolean, and the screen
     * renders four rows.
     *
     * IT IS EIGHT LOCAL READS AND NO ROUND TRIP. `App.StreamState` reads the core's stale map and
     * `App.ResyncPending` reads a mutex-guarded map on the facade; neither goes near the relay.
     * That is what makes this callable from a render at all, and it is the distinction
     * android/unbound-verbs.tsv draws around `App.Presence`, which is a blocking relay round-trip
     * and stays unbound for exactly that reason.
     */
    // `map { streamView(it) }` AND NOT `map(::streamView)`. The two are the same call and only one
    // of them is visible to android/gate/boundverbledger_test.go, which matches a call by NAME
    // followed by a paren -- a method reference carries no paren, so the bound-method reachability
    // walk reported `streamView` as reached by nothing while this line called it four times. The
    // gate's limit is written down in its own header; the fix is to write the call, not to widen
    // the matcher until a shadowed local wrapper satisfies it too.
    fun streamViews(): List<StreamView> = REPAIR_CHANNELS.map { streamView(it) }

    /**
     * PB-TIME-1's verdict, PULLED rather than remembered from an event.
     *
     * A screen that opens after the measurement -- which on Android is most of them, because the
     * process is killed and rebuilt constantly -- was never told, and a screen that latched the
     * event has nothing to clear it with. An empty verdict is a healthy clock, and the banner
     * treats it as one.
     */
    fun clockBanner(): ClockBanner = ClockBanner.of(app.clockVerdict())

    /**
     * `App.KillSwitchEngaged`: whether the machine has refused a remote op because its owner
     * turned remote control off (agents-tracker-2pnu F5).
     *
     * **READ ONLY, AND THE ASYMMETRY IS A SECURITY DECISION** (PB-SEC-6). There is deliberately no
     * setter here and there never may be: `protocol/server.go handleRemoteSetControl` refuses the
     * remote tier BEFORE consulting its backend, on the stated grounds that a remote device must
     * never re-enable a switch its owner turned off. So the phone may SHOW the state and never
     * move it, which is derivation row 12's 2026-08-01 amendment in one sentence.
     *
     * IT IS SAFE FROM A RENDER, which is the question `android/unbound-verbs.tsv` makes anyone ask
     * of a verb reached from a draw: it reads a bool the relay goroutine set, behind a mutex, in
     * the same class as `clockVerdict` above.
     */
    fun killSwitchEngaged(): Boolean = app.killSwitchEngaged()

    /**
     * PB-APP-9's classifier, for a message that has already crossed as an exception.
     *
     * The Android side holds the tokens as literals ([SwarmErrorTokens]) because the unit-test
     * JVM does not load the AAR, and this is the other direction: the same classification made
     * by the Go side, so a screen with a live App never has to be the one that gets it right.
     */
    fun routeFacadeError(message: String): RoutedError = ErrorRouter.route(app.errorClass(message))

    /**
     * Wave R5's preset launch snapshot (round 2; launch.presets): the machine-published rows AND
     * the availability verdict they were resolved under, together -- [machines]' one-read rule.
     * The DECISION is [LaunchPresetScreen.launchAvailabilityFor]'s (the model the JVM suite
     * drives); this seam only feeds it facts the facade carries:
     *
     *  - `online` is the transport state [sessionLease] already reads;
     *  - `tier` is `App.LaunchCapability` -- the machine's own registry-pinned word for THIS
     *    device, stamped on its last launch_presets reply, "" until one arrived (the model's
     *    first-run FETCHING state; never a value invented at this seam);
     *  - `killSwitchOn` is the negation of [killSwitchEngaged]'s fact;
     *  - the rows are `App.LaunchPresets` verbatim: what the machine published and nothing else
     *    (ADR-007 B135), each row carrying the REVISION the confirm must echo.
     */
    fun launchPresetFlow(): LaunchPresetFlow {
        val list = app.launchPresets()
        val rows = (0 until list.count()).map { index ->
            val p = list.at(index)
            PresetRowModel(
                id = p.getID(),
                displayName = p.getDisplayName(),
                provider = p.getProvider(),
                workspacePath = p.getWorkspacePath(),
                revision = p.getRevision(),
                worktree = p.getWorktree(),
            )
        }
        return LaunchPresetFlow(
            rows = rows,
            availability = LaunchPresetScreen.launchAvailabilityFor(
                online = isOnline(),
                tier = app.launchCapability(),
                killSwitchOn = !app.killSwitchEngaged(),
                presetCount = rows.size,
            ),
            machineLabel = MachineLabel.of(
                app.machineName(),
                app.stateSummary().takeIf { it.paired }?.machine.orEmpty(),
            ),
        )
    }

    /** The preset rows and the availability they were resolved under, together -- see [launchPresetFlow]. */
    data class LaunchPresetFlow(
        val rows: List<PresetRowModel>,
        val availability: LaunchAvailability,
        /** What to call the machine on the confirm sheet: [MachineLabel.of]'s name-else-id. */
        val machineLabel: String,
    )

    /** PB-SYNC-2: outcomes are claimed BY OPERATION ID, never by proximity. */
    fun launchOutcome(operationId: String): OperationOutcome {
        val outcome = app.outcome(operationId)
        return OperationOutcome(
            operationId = outcome.getOperationID(),
            code = outcome.getCode(),
            message = outcome.getMessage(),
        )
    }

    /** PB-APP-7's screen: the two push preferences, and nothing this seam has to invent. */
    fun pushSettings(): SettingsScreen {
        val preference = app.pushPreference()
        return SettingsScreen(
            alerts = preference.getAlerts(),
            mentions = preference.getMentions(),
        )
    }

    private fun isOnline(): Boolean =
        ConnectionState.of(app.connectionState()) == ConnectionState.ONLINE

    private fun rowOf(session: Session) = SessionRow(
        id = session.getID(),
        title = session.getTitle(),
        group = session.getGroup(),
        need = session.getNeed(),
        present = session.getPresent(),
        // VERBATIM, AND WITH NO FALLBACK. The daemon puts `persist.Meta.AgentType` on the journal
        // record and phonecore folds it through unchanged, so this is the machine's own word for
        // what is running. An empty one means the session's records carried none -- which is a
        // fact, not a gap to fill: substituting the title or the id here would put a fabricated
        // identity in the one cell a reader trusts to name the agent.
        agent = session.getAgent(),
    )

    /**
     * The pure halves of this adapter, lifted out of the instance ON PURPOSE.
     *
     * NONE TAKES AN `App`, AND THAT IS WHAT MAKES THEM TESTABLE. `swarmmobile.App` is a gomobile
     * class over .so files cross-compiled for Android ABIs, so it cannot be constructed on the
     * unit-test JVM. The decisions that can be got wrong are pure Kotlin and are asserted in
     * `FacadeBridgeTest`; what stays untested is where the calls are placed, which is review's.
     */
    internal companion object {
        /** `App.StreamState` answers "stale" or "live". */
        const val STREAM_STALE = "stale"

        /**
         * PB-SYNC-1's four repair channels, spelled ONCE.
         *
         * THE NAMES CROSS AS BARE STRINGS AND THE READ FAILS OPEN, which is why they are gathered
         * here rather than typed at the call site. `App.StreamState` does not validate its
         * argument: it falls through to a stale map that was never given the key and answers
         * "live", so a mistyped channel renders as healthy forever over a stream nobody is
         * watching. Its sibling `App.Resync` validates the same four and FAILS CLOSED, in its own
         * words -- "a caller that mistyped one of the four would see exactly what a working resync
         * looks like" -- and that argument is stronger for the read, because the read is what a
         * screen draws.
         *
         * Kotlin cannot see `internal/phonecore.StreamJournal` and the Go toolchain cannot see
         * this list, so the two spellings are joined by
         * `android/gate/pbapp8_repairchannels_test.go`, which set-compares them and fails in
         * either direction. That is `android/gate/pairingstates_test.go`'s shape, and it exists
         * for the same reason: PB-PAIR-5's alphabet moved on the Go side and the screen's did not,
         * with every check on both sides green.
         *
         * THE JOURNAL IS FIRST AND [repairTranscript] SPENDS IT BY POSITION. That is a coupling
         * and it is the cheaper of the two available ones: the alternative is a second `"journal"`
         * typed somewhere, which is the drift this whole declaration exists to prevent, and the
         * order here is joined to the core by the set comparison above -- a reordering that broke
         * it would leave the set identical, so the note is written here rather than fenced.
         */
        val REPAIR_CHANNELS: List<String> = listOf("journal", "terminal", "reply", "grant")

        /**
         * Whether a refusal means this phone simply holds nothing yet for the session.
         *
         * IT IS AN EQUALITY AND NOT A SET, because exactly one class in the taxonomy means "not
         * yet" and every widening of it is a real failure rendered as a quiet screen.
         *
         * IT OUTLIVED THE GRID IT WAS WRITTEN FOR. It guarded `App.Peek`, whose cache is empty for
         * the whole round trip after a watch; the well is deleted (ADR-009 (3)) and the same shape
         * of refusal now arrives from [pendingApproval], where a card the phone has already
         * answered or never received is `NOT_FOUND` and is not a failure to report.
         */
        fun isAwaitingFirstFrame(errorClass: String): Boolean =
            errorClass == SwarmErrorTokens.NOT_FOUND
    }
}
