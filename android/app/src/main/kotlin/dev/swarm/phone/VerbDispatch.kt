package dev.swarm.phone

import android.os.Handler
import android.os.Looper
import android.view.View
import java.util.concurrent.Executor
import java.util.concurrent.Executors

/**
 * Which of the facade's two send planes a verb resolves through.
 *
 * IT IS THE GO SIDE'S DISTINCTION, restated here because the Android side is where it can be
 * undone. `mobile/commands.go` carries exactly two destination policies and the difference is
 * the requirement rather than an implementation detail:
 *
 *  - [COMMAND] resolves through `sendContext`, which is `resolveSend(a.awaitConn)`. awaitConn
 *    "polls for up to five seconds" so a command issued right after Start is not refused by a
 *    race the caller cannot see. That is right for a command -- idempotent, queued by design.
 *  - [LIVE] resolves through `liveSendContext`, which takes the connection as it stands. Waiting
 *    would append a keystroke to the RECONNECTED link and deliver it seconds later against a
 *    terminal the user has since changed, so ADR-007 D7 makes waiting structurally impossible
 *    and input fails immediately as an undelivered record instead.
 *
 * They take SEPARATE lanes for that reason and not for throughput. One shared background thread
 * would put a keystroke behind whatever command is sitting in awaitConn, which is the queue on
 * live input D7 forbids -- reintroduced from above, where D7 cannot see it.
 */
enum class SendPlane { COMMAND, LIVE }

/**
 * Where a facade verb runs, which is never the thread that drew the control.
 *
 * WHAT THIS REPLACES. `PhoneSurface.invoke` called the verb inside the click listener. Every verb
 * on that surface crosses JNI into a Go network call: a command resolves its destination through
 * awaitConn's five-second poll and then appends to the relay, so each tap froze the UI for a
 * round trip and a tap issued while the link was reconnecting froze it long enough for Android to
 * offer to kill the app.
 *
 * NOTHING IN THE PLATFORM WAS EVER GOING TO SAY SO, which is why the seam is explicit rather than
 * left to a convention. `NetworkOnMainThreadException` comes from Android's own socket
 * implementation; Go opens its sockets below the JVM, so the detector has never seen a byte this
 * app sends. android/gate/s25_mainthread_test.go is the fence that replaces it, and
 * `VerbDispatchTest` states what this object owes.
 *
 * THREAD OWNERSHIP, because everything below depends on it: [press], [inFlight], [attach] and
 * [detach] are all main-thread only, and so is the settle, which is why neither field here is
 * synchronized. The lane threads touch nothing on this object -- they run the caller's `work` and
 * hand the answer straight back to [main].
 */
class VerbDispatch(
    private val command: Executor,
    private val live: Executor,
    private val main: Executor,
) {

    /**
     * The controls whose press has not settled.
     *
     * IT IS THE DOUBLE-TAP FENCE AND THE RENDER FENCE AT ONCE. A responsive button can be tapped
     * twice, and two Launches is worse than the frozen UI this replaces -- it starts two sessions
     * on the machine, both signed, both real. Disabling the view alone would not hold, because
     * `PhoneSurface.render` runs on every journal event and sets these flags from the roster; so
     * the mark lives here and the surface asks about it (`PhoneSurface.enable`).
     *
     * Views do not override equals, so this is identity-keyed, which is what a control is.
     */
    private val crossing = mutableSetOf<View>()

    /**
     * The same fence for work that has NO control to key on (agents-tracker-0ox9 round 3).
     *
     * [enqueue] exists precisely because the machines controls are rebuilt per draw, so a mark
     * keyed on a view fences nothing there -- and it was deliberately undroppable, for the
     * push-token reconciliation that must never be discarded (agents-tracker-b6iu). But Add
     * computer runs `App.Stop` -> AddMachine -> `App.Start` on that lane, and a rapid double tap
     * ran that twice: two disconnects, two rounds of buffered input resolved undelivered, two
     * lease severances, while the first was still crossing.
     *
     * SO THE FENCE IS OPT-IN, BY KEY. Unkeyed work keeps b6iu's guarantee exactly; keyed work is
     * single-flight per key. Main-thread only, like [crossing], which is why it is not
     * synchronized.
     */
    private val crossingKeys = mutableSetOf<Any>()

    /**
     * Whether a live screen is still holding this dispatch.
     *
     * Process death and rebuild are constant on Android and a command takes a relay round trip,
     * so the answer routinely outlives the screen it was meant for. This is the same mechanism
     * [PhoneEvents] uses for the event plane, and for the same reason: a detached surface must
     * not be redrawn, and its views must not be written to.
     */
    private var attached = true

    /**
     * Run [work] off the main thread and hand its answer to [settle] back on it.
     *
     * A press on a control that is already crossing does NOTHING -- not the verb, and not a
     * settle either. The tap is refused, which is the honest answer for a control whose previous
     * press has not been answered yet.
     *
     * @param control disabled for as long as the press is in flight, so a control that has been
     *  tapped does not look untapped. There is no in-flight state in the design system, so this
     *  is derivation row 24's disabled pair, which the kit already paints off drawable state.
     * @param work the facade call. It must not touch a View.
     * @param settle what the answer changes on screen. It runs on [main], and only while this
     *  dispatch is attached.
     */
    fun <T> press(control: View, plane: SendPlane, work: () -> T, settle: (Result<T>) -> Unit) {
        if (!crossing.add(control)) return
        control.isEnabled = false
        laneFor(plane).execute {
            val answer = runCatching(work)
            main.execute {
                // The control is freed BEFORE the attached check and not inside it. A dropped
                // settle that left the mark standing would leave that control refusing every tap
                // for the life of the surface, with nothing to distinguish it from a dead button.
                crossing.remove(control)
                control.isEnabled = true
                if (attached) settle(answer)
            }
        }
    }

    /**
     * Run [work] on [plane]'s lane and hand its answer back on the looper, with no control to
     * mark (agents-tracker-xla6).
     *
     * IT IS NOT [press] WITH A NULL CONTROL, and the difference is the double-tap fence rather
     * than the disabled pair. [press] DROPS a second press of a control whose first has not
     * settled, which is the honest answer to a finger and the wrong one for work nobody tapped:
     * the push-token reconciliation that follows a machine's refusal must not be discarded
     * because an earlier reconciliation is still crossing -- a dropped one leaves the token
     * disagreeing with the switches, which is agents-tracker-b6iu exactly.
     *
     * ORDER IS THE LANE'S. Each lane is a single serial thread, so a reconciliation enqueued
     * after a preference write runs after it, and two reconciliations run in the order they were
     * asked for. That is the whole of the sequencing this needs; nothing here queues or retries.
     *
     * @param key the single-flight key, or null for work that must never be dropped ([crossingKeys]
     *  has the argument). While work under this key is crossing, a second enqueue under it does
     *  NOTHING -- not the work, and not a settle either -- and says so in its return value, so
     *  the caller can put the refusal on screen instead of swallowing it.
     * @param work the facade call. It runs off the main thread and must not touch a View.
     * @param settle what the answer changes on screen. It runs on [main], and only while this
     *  dispatch is attached -- a reconciliation whose panel has gone has nothing to report to.
     * @return whether the work was accepted onto the lane. False means nothing was sent.
     */
    fun <T> enqueue(
        plane: SendPlane,
        key: Any? = null,
        work: () -> T,
        settle: (Result<T>) -> Unit,
    ): Boolean = enqueueOnMain(
        plane = plane,
        key = key,
        work = work,
        complete = {},
        settle = settle,
    )

    /**
     * Run [work] like [enqueue], but reconcile operation-owned model state even after detach.
     *
     * This is deliberately narrower than changing [enqueue]'s lifecycle contract. A facade call
     * accepted while a surface is attached can finish after that surface is released; dropping
     * its operation-id handoff would strand the durable logical operation forever. [complete]
     * therefore always runs on [main]. It must update model state only and must not touch a View,
     * render, toast, or otherwise address the released surface. [settle] remains attachment-gated
     * and is the only callback for UI effects.
     */
    fun <T> enqueueCompleting(
        plane: SendPlane,
        key: Any? = null,
        work: () -> T,
        complete: (Result<T>) -> Unit,
        settle: (Result<T>) -> Unit,
    ): Boolean = enqueueOnMain(
        plane = plane,
        key = key,
        work = work,
        complete = complete,
        settle = settle,
    )

    private fun <T> enqueueOnMain(
        plane: SendPlane,
        key: Any?,
        work: () -> T,
        complete: (Result<T>) -> Unit,
        settle: (Result<T>) -> Unit,
    ): Boolean {
        if (key != null && !crossingKeys.add(key)) return false
        laneFor(plane).execute {
            val answer = runCatching(work)
            main.execute {
                // The key is freed BEFORE the attached check, for [press]'s recorded reason: a
                // dropped settle that left the mark standing would refuse every later attempt
                // for the life of this dispatch, with nothing to distinguish it from a dead
                // button.
                if (key != null) crossingKeys.remove(key)
                complete(answer)
                if (attached) settle(answer)
            }
        }
        return true
    }

    /** Whether a press of [control] is still crossing to the machine. */
    fun inFlight(control: View): Boolean = crossing.contains(control)

    /** A drawn surface may be handed answers. Idempotent: it is called from every render. */
    fun attach() {
        attached = true
    }

    /** A released surface may not. Idempotent: pause and process death arrive together often. */
    fun detach() {
        attached = false
    }

    private fun laneFor(plane: SendPlane): Executor = when (plane) {
        SendPlane.COMMAND -> command
        SendPlane.LIVE -> live
    }

    companion object {

        /** The shipping wiring: two lanes that cannot block each other, answering on the looper. */
        fun background(): VerbDispatch = VerbDispatch(Lanes.command, Lanes.live, Lanes.main)

        /**
         * Everything on the calling thread, for a test that wants its presses to run to
         * completion before the next line. It is not a shortcut for production: on a handset it
         * would be the defect this class exists to remove.
         */
        fun direct(): VerbDispatch {
            val here = Executor(Runnable::run)
            return VerbDispatch(here, here, here)
        }
    }
}

/**
 * The two lanes and the looper, held for the life of the PROCESS rather than per surface.
 *
 * `PhoneSurface` is rebuilt on every Activity instance, and a pair of threads created with it
 * would be a pair leaked on every rotation. Two threads for the whole app is also the correct
 * count: each lane is serial precisely so the frames on it keep their order, and a pool would
 * reorder keystrokes.
 */
private object Lanes {
    val command: Executor = Executors.newSingleThreadExecutor { work ->
        Thread(work, "swarm-command").apply { isDaemon = true }
    }

    /** Serial, so a burst of keystrokes reaches the relay in the order it was typed. */
    val live: Executor = Executors.newSingleThreadExecutor { work ->
        Thread(work, "swarm-input").apply { isDaemon = true }
    }

    val main: Executor = Handler(Looper.getMainLooper()).let { looper ->
        Executor { work -> looper.post(work) }
    }
}
