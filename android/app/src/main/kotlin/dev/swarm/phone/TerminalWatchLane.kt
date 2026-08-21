package dev.swarm.phone

/**
 * The two verbs a held terminal watch is driven by: open the machine-sanitized stream, and
 * close it so the machine stops rendering for a screen nobody is looking at.
 *
 * IT IS AN INTERFACE FOR ONE REASON. The production implementation --
 * `dev.swarm.phone.ui.screens.TerminalFallbackBinding` -- wraps the bound facade, which needs
 * libgojni and cannot be constructed on the unit-test JVM, and the THREADING of these verbs is
 * exactly what agents-tracker-jx1x requires to be testable: `TerminalWatchLaneTest` drives this
 * seam with a fake. It grants nothing the binding does not: the binding's constructor stays
 * private and `forRoutedSession` stays the one place a live handle can come from, so a screen
 * that can name this type still cannot obtain a receiver for it (the r8r4 gate's structural
 * argument, unchanged).
 */
interface TerminalWatchHandle {

    /** Open the machine-sanitized stream. A read, and only a read (ADR-017 T4). */
    fun watch()

    /** Close it. Without this the machine keeps rendering, sealing and appending for nobody. */
    fun unwatch()
}

/**
 * The held terminal watch, with its verbs on [VerbDispatch]'s command lane
 * (agents-tracker-jx1x).
 *
 * WHAT THIS REPLACES. `PhoneSurface.reconcileTerminalWatch` called the binding's `watch()` and
 * `unwatch()` INLINE -- on the main thread, on every redraw of the fallback screen. Each verb
 * crosses JNI into a Go relay append that resolves through `sendContext` -> `awaitConn`, which
 * polls for up to five seconds before an append bounded at ten more; `NetworkOnMainThreadException`
 * never fires for a socket Go opened, so the freeze was a silent ANR. The verbs are commands --
 * idempotent, queued by design -- so they belong on the same lane every other command this app
 * issues already takes.
 *
 * ORDERING IS THE LANE'S, which is the property the inline calls were accidentally providing
 * and this object must keep: the command lane is one serial thread, and both verbs are enqueued
 * from the main thread in program order, so a replaced watch is closed BEFORE its successor is
 * opened and an unwatch-then-watch sequence can never interleave.
 *
 * THE HOLD IS EAGER and the REFUSAL claws it back. State is written at enqueue time, so the
 * redraw that follows a `hold` renews instead of re-watching (one append, not one per redraw);
 * a watch the machine refuses then clears the hold on the settle, so the next reconcile
 * re-watches instead of renewing into nothing forever -- the same behaviour the synchronous
 * `try/catch` had, arriving one settle later. The identity check keeps a slow refusal for a
 * SUPERSEDED watch from clearing its replacement.
 *
 * THREAD OWNERSHIP, [VerbDispatch]'s own rule restated: [hold], [drop] and both fields are
 * main-thread only, which is why nothing here is synchronized. The lane thread touches nothing
 * on this object -- the verb lambdas capture the handle itself.
 */
class TerminalWatchLane<H : TerminalWatchHandle>(private val dispatch: VerbDispatch) {

    /** The handle whose watch this lane holds, or null. Read by the surface's lapse question. */
    var held: H? = null
        private set

    /** The session the held watch is for, or null. Read by the surface's reconcile. */
    var heldSession: String? = null
        private set

    /**
     * Hold [handle]'s watch for [session], closing any previously held watch first.
     *
     * Both verbs ride [SendPlane.COMMAND]. The enqueue is unkeyed on purpose: a keyed enqueue
     * can be REFUSED while an earlier one is crossing, and a dropped watch (or worse, a dropped
     * unwatch) is a machine and a phone that disagree about who is looking.
     */
    fun hold(session: String, handle: H) {
        drop()
        held = handle
        heldSession = session
        dispatch.enqueue(
            plane = SendPlane.COMMAND,
            work = { handle.watch() },
            settle = { answer ->
                // A refused watch is the capability gate answering, and it answers the same way
                // every time: nothing is held. `===` and not the session id, because the screen
                // may have moved on to a NEW hold for the same session while this refusal was
                // crossing, and that hold is not this refusal's to clear.
                if (answer.isFailure && held === handle) {
                    held = null
                    heldSession = null
                }
            },
        )
    }

    /**
     * Close the held watch, if any. The state clears NOW and the verb crosses later, in order,
     * behind whatever the lane already carries.
     *
     * THE RELEASE PATH DEPENDS ON THE VERB OUTLIVING THE SCREEN. `PhoneSurface.release()` drops
     * the watch and then detaches the dispatch; [VerbDispatch.enqueue] gates only the SETTLE on
     * attachment, never the work, so the unwatch still reaches the machine -- a posted verb
     * that survives the teardown is fine, a dropped one leaves the machine rendering against a
     * shared append budget for a screen that no longer exists.
     */
    fun drop() {
        val handle = held ?: return
        held = null
        heldSession = null
        dispatch.enqueue(
            plane = SendPlane.COMMAND,
            work = { handle.unwatch() },
            settle = {
                // A refusal has no user to report to: the socket may already be gone, and the
                // machine's own watch horizon is the backstop.
            },
        )
    }
}
