package dev.swarm.phone

import android.os.Handler
import android.os.Looper
import swarmmobile.Event
import swarmmobile.EventListener

/**
 * The app's one `swarmmobile.EventListener`, and the reason PB-APP-3/4/5 now do something.
 *
 * `App.SetEventListener` appeared ZERO times in all Kotlin -- main, test and androidTest alike
 * -- so no listener was ever installed and no journal event could reach a screen (residuals
 * §2.9). Everything downstream looked wired: the roster rendered, the peek had a model, the
 * journal had a page reader. What none of it had was anything telling it something changed.
 *
 * IT IS A SINGLETON HOLDING A REPLACEABLE SINK, and both halves are deliberate.
 *
 *  - Process-lived, because [PhoneRuntime] caches the built `App` across Activity instances and
 *    the bound facade has no un-set that does not cross a null through JNI. A listener that
 *    captured the Activity would outlive it -- the screen goes, the Go core keeps the
 *    reference, and every rotation adds another.
 *  - Replaceable, because the SINK is what a screen owns. `PhoneSurface.release` clears it on
 *    the way to the background (ADR-007 B16), so a paused screen stops being redrawn without
 *    anything having to un-install a listener.
 *
 * DELIVERY IS MARSHALLED HERE, once. `OnEvent` runs on a GO GOROUTINE, which on Android is not
 * a Looper thread at all: the facade's own package doc says the UI must marshal, and a sink
 * that touched a View on that thread is the class of bug Android sometimes tolerates and
 * sometimes crashes on -- so it survives every emulator run and appears on a handset.
 *
 * IT MUST NOT BLOCK. The core drains the relay on the delivering goroutine's other side; a slow
 * sink stalls the keystroke path, which is why the Go dispatcher is a bounded drop-oldest queue
 * (mobile/events.go). Posting and returning is what keeps that bound meaningful.
 *
 * THE EVENT IS NOT READ. Every accessor on `swarmmobile.Event` is native, and this surface has
 * exactly one response to any event -- redraw -- so there is nothing to branch on. When a
 * screen needs the KIND it is a parameter added here, not a second listener.
 */
object PhoneEvents : EventListener {

    private val main = Handler(Looper.getMainLooper())

    /** Written from a Go goroutine's callback and from the main thread. */
    @Volatile
    private var sink: (() -> Unit)? = null

    /** Install the redraw a live screen wants. A second call replaces the first. */
    fun observe(redraw: () -> Unit) {
        sink = redraw
    }

    /** Stop redrawing. Idempotent: pause and process death arrive together often enough. */
    fun stopObserving() {
        sink = null
    }

    /**
     * @param event unused, and typed nullable because gobind declares it as a platform type.
     *  Nothing here dereferences it: a throw crossing back into Go is recovered by `deliver`'s
     *  panic barrier, so the cost of getting it wrong would be silence.
     */
    override fun onEvent(event: Event?) {
        if (sink == null) return
        // Re-read inside the post rather than capturing: the screen may have paused between the
        // core emitting and the looper getting to it, and a redraw is exactly what a paused
        // screen must not be given.
        main.post { sink?.invoke() }
    }
}
