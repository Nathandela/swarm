package dev.swarm.phone.ui.screens

import swarmmobile.App
import swarmmobile.Snapshot

/**
 * One fallback session's machine-sanitized grid as the screen takes it.
 *
 * It lives HERE and not on the facade adapter because reading the grid is part of the render path
 * ADR-017's gate note requires to be unreachable without a capability read, and this is the file
 * the allowlist names.
 */
data class TerminalGrid(
    val rows: List<String>,
    val gridRows: Int,
    val ageMs: Long,
    /**
     * Whether the MACHINE'S TERMINAL STREAM ITSELF is stale, as the phone core judges it.
     *
     * It is a different fact from [ageMs] and both are needed: the age says how old this
     * SNAPSHOT is, the stream flag says whether the phone is still hearing from the machine at
     * all. `App.Peek` has always returned it and the adapter discarded it, so the screen asserted
     * freshness about an arbitrarily old terminal -- which is exactly the "the machine went quiet
     * rendered as the terminal is idle" that T4-b forbids.
     */
    val streamStale: Boolean,
) {
    /**
     * Whether this grid is the MACHINE SAYING IT STOPPED RENDERING THIS SESSION.
     *
     * `TerminalWatcher.Reap` and `UnwatchAll` end a watch the phone never asked to end, and both
     * publish a BLANK view so the screen cannot go on presenting a grid the machine stopped
     * rendering as a live terminal. That blank carries no rows, no geometry and no render time,
     * and the GEOMETRY is what identifies it: a live view always carries the columns and rows its
     * emulator resolved (fenced on the machine side by
     * `internal/remotegw/r8r5_reapblank_test.go`), while a zero render time is also what a machine
     * predating the closing round sends, so the age cannot tell the two apart.
     *
     * A grid that never arrived is NOT this. `App.Peek` refuses a session with no cached snapshot,
     * so "the machine has not sent anything yet" reaches the caller as a refusal and never as a
     * blank -- which matters, because treating the arrival window as a lapse would tear down and
     * re-open the peek on every redraw while the first frame was in flight.
     */
    val machineStoppedRendering: Boolean
        get() = gridRows <= 0 && rows.isEmpty()

    companion object {
        /**
         * The grid for a session that was never routed to the fallback: no rows, no geometry,
         * and NOT asserted fresh. A caller that has no binding has nothing to show, and showing
         * nothing must not be spelled the same way as showing a screen the machine just rendered.
         */
        val EMPTY = TerminalGrid(rows = emptyList(), gridRows = 0, ageMs = 0L, streamStale = false)
    }
}

/**
 * WAVE R8 -- THE CAPABILITY-ROUTED TERMINAL FALLBACK's model and its ONE facade binding
 * (ADR-017 T1, T4, T6).
 *
 * **THIS IS THE ONLY FILE IN THE APP THAT MAY ASK FOR A TERMINAL, AND THE ONLY ONE THAT MAY TYPE
 * INTO ONE.** `android/gate/r8_fallback_ui_test.go` names it by path as the single-file allowlist
 * for a watch-shaped verb, for `terminalInput`, and for `terminalControlKeepalive`. The ban is
 * stated over the SHAPE of the verb rather than over three legacy spellings precisely so renaming
 * one is not a way past the rule -- and the allowlist is one file and not a directory, a prefix or
 * a pattern, because what is permitted is "one screen may watch", not "screens may watch".
 *
 * **ADR-009 (1) IS RE-SCOPED, NOT REPEALED.** "No terminal emulation and no raw grid anywhere in
 * the app" remains the rule for every `structured_chat` session; only a session the DAEMON routed
 * here may render the machine-sanitized snapshot. There is no route to this screen from a healthy
 * structured session -- no power-user escape hatch, no long-press, no debug toggle in a release
 * build -- and that absence is enforced in three independent places, because one of them being
 * right is not a property: the daemon refuses `terminal_subscribe` for such a session with the
 * sealed `capability_refused` before any tap opens, the gateway ends the watch on that code instead
 * of reconnecting, and [TerminalFallbackModel.from] routes on the destination the machine chose.
 *
 * **NOTHING HERE IS EMULATED.** The rows are `vt.SnapText`'s output: the machine's own emulator
 * resolved them and its sanitizer stripped them. No VT emulator crosses the gomobile boundary and
 * no raw PTY byte reaches the phone.
 *
 * **WHAT A USER CAN AND CANNOT CONCLUDE FROM THIS SCREEN, stated plainly because the evidence
 * obligation is the same one the UI has.** A row here is what the machine's emulator resolved,
 * sanitized. It is NOT guaranteed byte-for-byte identical to what the owner's terminal draws: the
 * emulator does not implement CSI 5i/4i media copy, and it does not honour 8-bit C1 introducers,
 * so a session using either renders MORE, inert text here than the owner sees. Both divergences are
 * fidelity, never injection -- the phone receives literal text and runs no parser.
 */
data class TerminalFallbackModel(
    /** The adapter identity the machine authored: `opencode`, `agy`, ... */
    val provider: String,
    /**
     * The DETECTED version of the installed CLI, or empty when the machine could not detect one.
     * Empty renders as "version unknown" and never as a guess: the header's whole job is to be
     * checkable, and an invented version is the one thing that would make it worse than silence.
     */
    val providerVersion: String,
    /**
     * The capability that cost this session structured chat, in the machine's own vocabulary.
     *
     * WITHOUT IT THE HEADER SAYS "this is a terminal" AND NOT "this is a terminal BECAUSE this
     * build of this provider does not do X" -- and the second sentence is the whole of what makes
     * three destinations honest rather than arbitrary (playbook:280).
     */
    val missingCapability: String,
    /** The incarnation this screen is showing. Every generation and every snapshot binds to it. */
    val sessionInstance: String,
    /** Whether the machine authored `terminal_control` -- read, never derived from the destination. */
    val controlOffered: Boolean,
) {
    /** The header's first line: who is running, and which build of them. */
    val headline: String
        get() = if (providerVersion.isEmpty()) provider else "$provider $providerVersion"

    /**
     * The header's second line -- the honest one.
     *
     * A session degraded INTO the fallback and a provider that never had structured chat are
     * different states, and the machine is the only thing that knows which one this is.
     */
    val explanation: String
        get() = if (missingCapability.isEmpty()) {
            NO_STRUCTURED_PLANE
        } else {
            "No chat here: this build of $provider does not support $missingCapability."
        }

    companion object {

        /** The header line when the machine named no missing capability. */
        const val NO_STRUCTURED_PLANE = "No chat here: this session has no structured plane."

        /**
         * ADR-017 T8 / playbook:286-287 -- the INTERLEAVING warning, and it is a warning rather
         * than a fix on purpose.
         *
         * Decision G keeps the owner typing at their own terminal throughout, and ADR-013's
         * co-presence finding proves both streams stay live. The UX must therefore say that
         * simultaneous typing can interleave; it must NOT "fix" this by evicting the terminal user,
         * which would make the phone's convenience cost someone else their session.
         */
        const val INTERLEAVING_WARNING =
            "Someone may be typing at this terminal too. Simultaneous typing can interleave."

        /** The banner's own sentence while a control generation is live. */
        const val CONTROL_LIVE = "You are typing into this terminal."

        /** The in-view release, which is never a drawer entry and never a second navigation step. */
        const val RELEASE_LABEL = "Release control"

        /** What the read-only state says, so "read only" is stated rather than inferred from a missing keyboard. */
        const val READ_ONLY = "Read only. This session's machine did not grant terminal control."

        /**
         * The model for one routed session, or null when the machine did not route it here.
         *
         * NULL IS THE ROUTING RULE, not an error path: [swarmmobile.Session.destination] is
         * `phonecore.RouteSession`'s answer over the daemon-authored capability record AND the
         * machine's remote profile, resolved once in the facade. An absent record, an INCONSISTENT
         * record, a record binding no session instance, and a machine whose profile declares no
         * TerminalView version all answer `status_card` there -- so this screen cannot be built
         * for any of them, whatever a caller intends.
         */
        fun from(session: swarmmobile.Session): TerminalFallbackModel? = of(
            destination = session.destination,
            provider = session.provider,
            providerVersion = session.providerVersion,
            missingCapability = session.missingCapability,
            sessionInstance = session.sessionInstance,
            controlOffered = session.terminalControl,
        )

        /**
         * [from]'s PURE half, over the flat facts rather than the bound handle.
         *
         * It exists because the routing rule is the one thing in this file that MUST be testable
         * without the native library: `swarmmobile.Session` cannot be constructed in a JVM unit
         * test (its static initialiser loads libgojni), so a rule reachable only through it would
         * be a rule asserted only by a source scan. The gate says which files may NAME the verbs;
         * this says what the rule DOES, and both are needed.
         */
        fun of(
            destination: String,
            provider: String,
            providerVersion: String,
            missingCapability: String,
            sessionInstance: String,
            controlOffered: Boolean,
        ): TerminalFallbackModel? {
            if (destination != DESTINATION) return null
            return TerminalFallbackModel(
                provider = provider,
                providerVersion = providerVersion,
                missingCapability = missingCapability,
                sessionInstance = sessionInstance,
                controlOffered = controlOffered,
            )
        }

        /** The one destination string this screen answers to. */
        const val DESTINATION = "terminal_fallback"

        /**
         * How stale a snapshot may be before the screen says so, in milliseconds.
         *
         * IT IS DERIVED FROM THE SNAPSHOT'S OWN AGE and never from arrival time (ADR-017 T4-b): a
         * replayed backlog arrives all at once and a held relay delivers old content at a new
         * instant, so arrival time renders a QUIET MACHINE as an IDLE TERMINAL -- which is the
         * state a user is most likely to type into, and the exact lie this indicator exists to
         * prevent.
         */
        const val STALE_AFTER_MS = 10_000L

        /**
         * What the indicator says once the snapshot is older than [STALE_AFTER_MS], or the empty
         * string while it is fresh.
         *
         * @param snapshotAge how long ago the MACHINE rendered this screen, in milliseconds.
         */
        fun staleness(snapshotAge: Long): String =
            if (snapshotAge < STALE_AFTER_MS) {
                ""
            } else {
                "Last update ${snapshotAge / 1000}s ago -- this screen may be out of date."
            }

        /**
         * What the indicator says when the phone core reports the MACHINE'S TERMINAL STREAM as
         * stale, whatever the snapshot's own age says.
         *
         * The two are independent and the screen must say whichever is true. `App.Peek` carries
         * this verdict and the adapter used to throw it away while hardcoding an age of zero,
         * so a screen showing a grid the machine stopped sending hours ago rendered as FRESH --
         * "the machine went quiet" told to the user as "the terminal is idle", which is the state
         * a user is most likely to act on and the exact lie T4-b names.
         */
        const val STREAM_STALE =
            "Not hearing from this machine. This screen is whatever it last sent, not what is there now."

        /**
         * The one staleness sentence to draw, or the empty string when there is nothing to say.
         *
         * The stream verdict WINS over the age: an old snapshot on a live stream is an idle
         * terminal, and the same snapshot on a dead stream is an unknown one. Reporting the
         * weaker of the two would be the softer form of the same lie.
         */
        fun stalenessLine(snapshotAge: Long, streamStale: Boolean): String =
            if (streamStale) STREAM_STALE else staleness(snapshotAge)

        /**
         * ADR-017 T6's middle property, which the ADR itself names as "the easiest to lose": for
         * the WHOLE LIFE of a generation the screen continuously displays that control is live,
         * its REMAINING HORIZON, and a release in the same view.
         *
         * A sheet that grants control and then disappears is explicit and NOT persistent, and it
         * leaves a user typing into a live generation they have to REMEMBER they opened. So this
         * is a banner the composition draws for as long as the generation lasts, not a dialog.
         *
         * @param remainingHorizon milliseconds left on the generation. Zero or less means the
         *  horizon passed, and the banner says so rather than disappearing quietly -- a banner
         *  that vanishes is indistinguishable from a screen that never had control.
         */
        fun controlBanner(remainingHorizon: Long): String =
            if (remainingHorizon <= 0L) {
                "Control ended. Nothing you type reaches this terminal."
            } else {
                "$CONTROL_LIVE ${remainingHorizon / 60_000}m left."
            }
    }
}

/**
 * The screen's ONE binding to the facade: the watch, the horizon renewal, and -- only while a
 * generation is live and only from the live foreground composition -- the raw-input plane.
 *
 * **A WATCH IS A READ AND GRANTS NO INPUT AUTHORITY** (ADR-017 T4). [watch] takes no lease, mints
 * no generation and touches no input buffer; gating a read on the input plane is how a monitoring
 * surface becomes a control surface by accident.
 *
 * **THE HORIZON IS WHY [renew] EXISTS** (T4-b). Watch/unwatch were the whole lifecycle, so a phone
 * that simply stopped reading left the machine rendering, sealing and appending full screens
 * against an append budget shared with every other session's transcript -- indefinitely -- and
 * building a backlog it then replayed. The renewal is the machine's only evidence that anyone is
 * still looking, so it is emitted by the composition that is doing the looking.
 *
 * **[keepAlive] IS BOUND TO THE LIVE FOREGROUND SCREEN EXACTLY AS [type] IS** (T6-c). "Only the
 * active fallback screen may send raw input" is unenforceable if a background coroutine, a
 * scheduled job or a service-hosted timer may hold a generation open for the full horizon with no
 * screen displaying it -- which would defeat the persistent banner and the leave-screen trigger
 * together. There is deliberately no WorkManager, no PeriodicWorkRequest, no AlarmManager and no
 * foreground service anywhere near this class. The daemon's own idle expiry is what holds when the
 * app does not; this is the app's contract.
 */
class TerminalFallbackBinding private constructor(
    private val app: App,
    private val sessionId: String,
) {

    companion object {

        /**
         * THE ONLY WAY TO GET ONE, AND IT IS THE CAPABILITY READ.
         *
         * The closing review's probe was one line appended to `SessionDetailPanel.kt`, a
         * STRUCTURED CHAT screen:
         *
         *     bridge.terminalFallbackBinding(id).watch()
         *
         * and every R8 gate stayed green. The bans in `android/gate/r8_fallback_ui_test.go` are
         * stated over the SHAPE OF A FACADE CALL SITE -- `.terminalViewWatch(`, `.peek(` -- and
         * round 3 moved those call sites behind this class, whose verbs are named `watch`,
         * `unwatch` and `renew`. Bare names match no shape in that list, and the public
         * constructor handed a live watch handle to any file for any session id. That is this
         * wave's own finding 8 ("renaming the verb is evasion") reopened by the fix for it.
         *
         * EXTENDING A BAN LIST IS THE LOSING MOVE: the next indirection gets a name the list does
         * not have either. So the fence is structural. The constructor is private, this is the
         * only factory, and it performs the read -- so `.watch()` on a session the MACHINE did not
         * route to the fallback is not something a gate forbids, it is something with no receiver.
         *
         * [TerminalFallbackModel.from] answers null unless the machine chose this destination, so
         * a structured session, an unknown id and a session the phone has not heard of all yield
         * NO BINDING rather than a handle that would open a real watch on the relay. The daemon
         * refuses such a watch with `capability_refused` regardless -- this is the phone's own
         * half of the same rule, and the ADR's gate note requires the render path to be
         * unreachable without a capability read on THIS side too.
         */
        fun forRoutedSession(app: App, sessionId: String): TerminalFallbackBinding? {
            if (TerminalFallbackModel.from(app.session(sessionId)) == null) {
                return null
            }
            return TerminalFallbackBinding(app, sessionId)
        }

        /**
         * The machine's watch horizon, mirrored on the phone.
         *
         * It is `remotegw.DefaultWatchHorizon` -- sixty seconds -- and it is spelled here rather
         * than derived because the phone has no way to read the machine's constant and a wrong
         * guess in the SAFE direction (re-watching early) costs one unsigned append, while a
         * wrong guess in the other direction is the frozen screen this whole fix is about.
         */
        const val WATCH_HORIZON_MS = 60_000L

        /**
         * How old the screen on the glass is, in milliseconds, or ZERO for "the machine sent no
         * render time".
         *
         * ZERO IS UNKNOWN AND NOT FRESH, and the difference is load-bearing: a machine that
         * predates the closing round sends no `rendered_at`, and reporting its screens as
         * rendered-just-now is exactly the assertion that made the frozen screen invisible. The
         * staleness sentence treats zero as "nothing to say", which leaves such a machine no
         * worse off than before and never better.
         *
         * A NEGATIVE AGE IS CLAMPED TO ZERO. The machine's clock and the phone's are different
         * clocks; a phone running behind would otherwise compute a screen rendered in the future
         * and, arithmetically, a very stale one.
         */
        fun ageOf(renderedAtMillis: Long, nowMillis: Long): Long {
            if (renderedAtMillis <= 0L) {
                return 0L
            }
            val age = nowMillis - renderedAtMillis
            return if (age < 0L) 0L else age
        }

        /**
         * Whether the phone's OWN watch has LAPSED, so the screen must RE-WATCH rather than renew.
         *
         * THE DEFECT IT ANSWERS (closing review, finding 6). `reconcileTerminalWatch` opened a
         * watch only when the DISPLAYED SESSION CHANGED; for the same session it always renewed.
         * `TerminalWatcher.Renew` is a documented no-op for a session with no live watch, so one
         * lapsed horizon -- three missed 20 s ticks on a descheduled UI thread, or a short offline
         * window -- ended the stream PERMANENTLY for that screen, and the phone renewed into
         * nothing forever.
         *
         * The evidence of a lapse is the SCREEN'S OWN AGE, because that is the one fact the phone
         * has that the machine authored: past the horizon the machine has reaped the watch and
         * stopped rendering, so nothing newer is coming and nothing renewed will bring it.
         *
         * AN UNKNOWN AGE IS NOT A LAPSE. Zero means the machine sent no render time, and
         * re-watching on that would re-watch every tick against a pre-R8 machine forever --
         * an unsigned append per tick, per watched session, spent from a shared budget.
         */
        fun watchLapsed(ageMs: Long, horizonMs: Long = WATCH_HORIZON_MS): Boolean =
            ageMs > 0L && horizonMs > 0L && ageMs >= horizonMs

        /**
         * Whether the watch behind THIS GRID is over -- and this, not the age alone, is what the
         * surface asks.
         *
         * THE DEFECT IT ANSWERS (closing review, finding 6, second pass). Round 3 made the machine
         * BLANK the phone's copy when it reaps a watch, and round 4 wrote the lapse detector over
         * the snapshot's age. The blank carries a ZERO rendered_at, [ageOf] reads zero as UNKNOWN
         * rather than as old, and so the age rule answered NOT LAPSED for the one frame that
         * PROVES the watch is over: the machine reaped, the blank landed, and the screen renewed
         * into nothing forever while the user looked at a blank terminal. Round 3's blank actively
         * defeated round 4's detector.
         *
         * The two evidences are independent and both are read. The blank is the machine SAYING it
         * stopped; the age is the phone INFERRING it from a screen older than the machine's own
         * horizon, which is what happens when the blank itself never arrives.
         */
        fun watchLapsed(grid: TerminalGrid, horizonMs: Long = WATCH_HORIZON_MS): Boolean =
            grid.machineStoppedRendering || watchLapsed(grid.ageMs, horizonMs)
    }

    /** Open the machine-sanitized stream. A read, and only a read. */
    fun watch() {
        app.terminalViewWatch(sessionId)
    }

    /**
     * The machine-sanitized grid, READ ONLY AFTER THE CAPABILITY RECORD SAYS SO.
     *
     * ADR-017's gate note requires that "the fallback render path is unreachable without a
     * capability read". `App.Peek` reads the phone's snapshot cache for ANY session id with no
     * record check of any kind, so a call site outside this file could reach the grid of a
     * session the machine never routed here. The daemon gate keeps that cache empty for a
     * structured session -- so this was one-layer defence rather than a live leak -- but the
     * property the ADR states is that the read is gated HERE, and this is where it is gated.
     *
     * A session the machine did not route to this screen yields an EMPTY grid, never an
     * exception and never someone else's screen.
     *
     * The rows are `vt.SnapText`'s output and nothing is parsed: the phone splits on the row
     * separator the machine wrote and counts, which is not emulation and reads nothing out of
     * the content.
     */
    fun grid(): TerminalGrid {
        if (TerminalFallbackModel.from(app.session(sessionId)) == null) {
            return TerminalGrid.EMPTY
        }
        val snap: Snapshot = app.peek(sessionId)
        val rows = if (snap.text.isEmpty()) emptyList() else snap.text.split("\n")
        return TerminalGrid(
            rows = rows,
            gridRows = snap.rows.toInt(),
            ageMs = ageOf(snap.renderedAtMillis, System.currentTimeMillis()),
            streamStale = snap.stale,
        )
    }

    /** Close it. Without this the machine renders for a screen the user has left. */
    fun unwatch() {
        app.terminalViewUnwatch(sessionId)
    }

    /** Renew the watch horizon: the evidence that someone is still looking. */
    fun renew() {
        app.terminalViewRenew(sessionId)
    }

    /**
     * Enter control, binding the generation to the INCARNATION this screen is showing.
     *
     * The instance is not decoration: a generation bound to a session id alone survives the
     * session's replacement, and would authorise raw bytes into the PTY that replaced the one the
     * user was reading. A session replaced between rendering and signing is refused
     * `stale_instance` rather than silently re-pointed.
     */
    fun beginControl(sessionInstance: String) {
        app.terminalControlBegin(sessionId, sessionInstance)
    }

    /** Release it. In view, one tap, never a drawer entry and never a second navigation step. */
    fun releaseControl() {
        app.terminalControlEnd(sessionId)
    }

    /**
     * Type raw bytes under the live generation.
     *
     * LIVE-ONLY AND NEVER BUFFERED: there is no place in this app to hold a byte on the way to
     * `terminal_input` -- not a refused one, none. The facade refuses without a generation and
     * records the bytes as undelivered, so the user is told they did not land and can never have
     * them land later.
     */
    fun type(text: String) {
        app.terminalInput(sessionId, text)
    }

    /** Hold the generation open, from this composition and from nowhere else. */
    fun keepAlive() {
        app.terminalControlKeepalive(sessionId)
    }
}
