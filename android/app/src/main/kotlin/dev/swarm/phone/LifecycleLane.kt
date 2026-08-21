package dev.swarm.phone

/**
 * The four verbs the phone's foreground/background lifecycle is driven by: sever every
 * authority on the way out (ADR-017 T8-b), withdraw journal delivery, drop the link, and dial
 * it again on the way back in.
 *
 * IT IS AN INTERFACE FOR [TerminalWatchHandle]'s recorded reason. The production
 * implementation -- [AppLifecycle] -- wraps the bound facade, which needs libgojni and cannot
 * be constructed on the unit-test JVM, and the THREADING of these verbs is exactly what the
 * committee's round-3 onPause finding requires to be testable: `LifecycleLaneTest` drives this
 * seam with a fake. THE NAMES ARE THE FACADE'S OWN, deliberately: the s25r3 gate judges
 * teardown verbs BY NAME, so a wrapper that kept the name is a wrapper the fence still sees,
 * and one that renamed it is the laundering the fence rejects
 * (android/gate/s25r3_releasepath_test.go).
 */
interface LifecycleHandle {

    /** ADR-017 amendment T8-b: withdraw every authority, with no transport event assumed. */
    fun enterBackground()

    /** Stop journal delivery into a queue nothing is draining (ADR-007 B16). */
    fun unsubscribeJournal()

    /**
     * Disconnect. `App.Stop` cancels the relay session and then WAITS on the drain goroutine
     * (`<-s.done`, mobile/app.go:480), whose teardown performs a five-second graceful close
     * (internal/remote/relay/client.go:411) -- which is why this verb may never run on the
     * thread that called [LifecycleLane.background].
     */
    fun stop()

    /** Dial the relay and begin draining the mailbox. Idempotent; spawns and returns. */
    fun start()
}

/**
 * The phone's lifecycle verbs on [VerbDispatch]'s command lane (committee round 3, the
 * onPause finding).
 *
 * WHAT THIS REPLACES. `PhoneSurface.release()` called `live.enterBackground()`,
 * `live.unsubscribeJournal()` and `live.stop()` INLINE -- on the main thread, inside
 * `PhoneActivity.onPause`. Stop joins the relay drain's five-second graceful close, so every
 * pause froze the looper for the teardown; `NetworkOnMainThreadException` never fires for a
 * socket Go opened, so the freeze was a silent ANR. `converge()` called `app.start()` inline
 * on the same thread. The verbs are commands -- idempotent, queued by design -- so they
 * belong on the same serial lane every other command this app issues already takes.
 *
 * ORDERING IS THE LANE'S, which is the property the inline calls were accidentally providing
 * and this object must keep: the command lane is one serial thread and both members enqueue
 * from the main thread in program order, so a start asked for while a stop is still draining
 * runs BEHIND it. That sequencing is load-bearing, not cosmetic: `App.Start` no-ops while
 * `a.sess != nil`, so a start that ran BEFORE the queued stop would be swallowed against the
 * dying session and the stop would then land on the fresh state -- a foregrounded phone,
 * disconnected, with nothing on screen saying why.
 *
 * THE SEVERANCE IS NEVER DROPPED (the owner-locked rule). [background] enqueues UNKEYED, and
 * [VerbDispatch.enqueue] gates only the SETTLE on attachment, never the work -- so the T8-b
 * severance still reaches the core after `release()` detaches the dispatch, exactly as
 * [TerminalWatchLane.drop]'s unwatch does. A posted severance that runs promptly in onPause's
 * background window is acceptable; a dropped one is a raw-input generation outliving the
 * screen that owns it.
 *
 * THE HOLD IS EAGER and the REFUSAL claws it back, [TerminalWatchLane]'s idiom -- with one
 * deliberate difference: the clawback is keyed on a PER-ATTEMPT TOKEN, not on handle
 * identity, because the handle is cached per App (`PhoneSurface.lifecycleFor`), so identity
 * cannot tell a superseded start attempt from its successor. A stale refusal that cleared the
 * successor's hold would make the next `release()` skip the severance entirely.
 *
 * THREAD OWNERSHIP, [VerbDispatch]'s own rule restated: [foreground], [background] and both
 * fields are main-thread only, which is why nothing here is synchronized. The lane thread
 * touches nothing on this object -- the verb lambdas capture the handle itself.
 */
class LifecycleLane<H : LifecycleHandle>(private val dispatch: VerbDispatch) {

    /** The handle this lane has started, or null. Read by the surface's converge and release. */
    var started: H? = null
        private set

    /** The start attempt whose answer is still owed, so a stale refusal spares its successor. */
    private var attempt: Any? = null

    /**
     * Start [handle]'s phone, eagerly held: a redraw that follows enqueues nothing --
     * `converge` runs on every render, and a lane full of idempotent no-op starts is still a
     * lane a real command has to queue behind.
     *
     * @param refused what a refused start changes on screen. It runs on the looper, only
     *  while the dispatch is attached, and never for an attempt a later foreground has
     *  superseded.
     */
    fun foreground(handle: H, refused: (Throwable) -> Unit) {
        if (started === handle) return
        started = handle
        val token = Any()
        attempt = token
        dispatch.enqueue(
            plane = SendPlane.COMMAND,
            work = { handle.start() },
            settle = { answer ->
                val refusal = answer.exceptionOrNull() ?: return@enqueue
                // The eager hold is clawed back so the next render retries -- but only by the
                // attempt that owns it. `started === handle` alone cannot carry this check:
                // the same cached handle is re-held after every background/foreground pair.
                if (attempt === token && started === handle) {
                    started = null
                    refused(refusal)
                }
            },
        )
    }

    /**
     * Leave the foreground: sever always, disconnect when the connectivity policy closes the
     * socket. The state clears NOW and the verbs cross later, in order, behind whatever the
     * lane already carries.
     *
     * EACH VERB SWALLOWS ITS OWN REFUSAL, as the inline try/catch blocks did: there is no
     * user present on this path, the process may be going away regardless, and a severance
     * that throws must not take the journal withdrawal and the stop down with it.
     */
    fun background(disconnect: Boolean) {
        val handle = started ?: return
        if (disconnect) started = null
        dispatch.enqueue(
            plane = SendPlane.COMMAND,
            work = {
                try {
                    handle.enterBackground()
                } catch (refused: Exception) {
                    // Severance is local state; a refusal here has no user to report to.
                }
                if (disconnect) {
                    try {
                        handle.unsubscribeJournal()
                    } catch (refused: Exception) {
                        // The socket is closing either way; the next Start re-subscribes.
                    }
                    try {
                        handle.stop()
                    } catch (refused: Exception) {
                        // Stop is idempotent and the process may be going away regardless.
                    }
                }
            },
            settle = {
                // No user on this path and no screen left to report to.
            },
        )
    }
}
