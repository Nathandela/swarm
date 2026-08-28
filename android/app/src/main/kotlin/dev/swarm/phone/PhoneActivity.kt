package dev.swarm.phone

import android.os.Bundle
import android.view.View
import android.view.WindowInsets
import androidx.activity.BackEventCompat
import androidx.activity.OnBackPressedCallback
import androidx.appcompat.app.AppCompatActivity

/**
 * Phase B slice S18 -- the app's one Activity, and the View hierarchy PB-SEC-12 clause 1 needs
 * in order to have a subject at all.
 *
 * WHY IT EXISTS AND WHY HERE. The module declared no `<activity>` until this slice: S16 shipped
 * the screen MODELS (data classes and enums), so the secure-window flag had no Window and
 * `filterTouchesWhenObscured` had no View, and both halves of PB-SEC-4 and PB-SEC-12 clause 1
 * were unverifiable rather than unsatisfied. The ruling recorded on 2026-07-25 assigns the
 * Activity to S18 as a SCOPE call: S18 is the first slice blocked on it, no slice ever owned
 * it, and PB-E2E-2 independently requires one because an on-emulator smoke that pairs,
 * observes, takes control and types cannot run against data classes. The scope is bounded to
 * exactly that: enough Window and View to carry those assertions and S19's smoke.
 *
 * PB-SEC-4 HAS SINCE BEEN WITHDRAWN AND INVERTED (ADR-007 B65, 2026-07-26): the shipped app
 * allows screenshots and screen recording, so `onCreate` sets nothing on the window and
 * [SecureWindow] no longer has a `protect()` to call. The Activity's other two reasons for
 * existing -- the touch filter's View hierarchy and PB-E2E-2's smoke -- are unaffected.
 *
 * THIS CLASS OWNS THE WINDOW AND NOTHING ELSE, and that is PB-SEC-11 rather than taste. It is
 * exported with a LAUNCHER filter -- the single most reachable surface an Android app has, and
 * unavoidably so, because a launcher is another process and cannot start a non-exported
 * Activity. The requirement's last clause is that no exported component can act on the session,
 * so every facade verb lives in [PhoneSurface], one call away, behind a control a person
 * pressed. android/exported-components.tsv records the decision and
 * android/gate/s18_sec11_exported_test.go enforces it against this file by name.
 *
 * IT READS NOTHING OFF THE INTENT. No extra, no data URI, no action beyond the one the filter
 * matched. Anything on the device can send this Activity an intent with any contents at any
 * time, including before the user has ever opened the app, so a screen selected by an extra is
 * a screen a third-party app chooses. What is shown comes from persisted local state alone.
 */
class PhoneActivity : AppCompatActivity() {

    private lateinit var surface: PhoneSurface

    /**
     * The system back gesture, and the ONE screen state it is allowed to touch.
     *
     * WHY IT EXISTS. The session detail is a drill-down reached by tapping a row, and without this
     * the gesture every Android user reaches for to go back one step would instead leave the app --
     * from a screen they got to by tapping something, which is the moment back is most expected to
     * work. It is disabled by default and armed only while a session is open, so on the inbox it
     * does not exist and the gesture leaves the app exactly as it always has.
     *
     * ITS BOUNDARY IS PB-SEC-11 AND IT IS HARD. It may clear LOCAL SCREEN STATE and nothing else:
     * no facade verb, no key custody, nothing reaching the Go core. This class is exported with a
     * LAUNCHER filter, so anything on the device can start it -- and a back callback that reached a
     * verb would be session-acting code on the single most reachable surface this app has.
     * [PhoneSurface.closeSessionDetail] sets a nullable String and redraws;
     * android/gate/s18_sec11_exported_test.go scans this file by name for every session verb. The
     * three predictive members below hold the same line: what crosses from here is a float and a
     * cancellation, and every view property they move is written in [PhoneSurface].
     *
     * IT WATCHES THE GESTURE NOW AND NOT ONLY ITS END (migration plan O6.3). `handleOnBackPressed`
     * is the pre-predictive contract: it fires ONCE, after the user has committed, so a gesture
     * dragged halfway across the screen showed nothing and the user could not tell whether letting
     * go would leave the session they were reading. That question is the whole point of a
     * predictive preview, and it is sharpest on exactly this screen -- the drill-down is where a
     * person is READING rather than scanning, and where an accidental edge-touch costs them their
     * place. The manifest's `android:enableOnBackInvokedCallback="true"` is what makes the platform
     * dispatch the progress at all; without it these three are dead code with a green test suite.
     */
    private val backOutOfTheSessionDetail = object : OnBackPressedCallback(false) {

        /**
         * The gesture began. Nothing is previewed yet: `progress` is 0 at the start and the frame
         * for 0 is the screen untouched, so drawing one here would be a redundant write on the
         * frame where a person's thumb has moved furthest least.
         *
         * IT EXISTS ANYWAY, and not for symmetry. Without a `handleOnBackStarted` override, the
         * androidx dispatcher still routes the gesture, but this callback has no place to reset a
         * preview left behind by a gesture that ended in a way nothing else caught -- and a
         * drill-down that starts a second gesture at 90% and half-faded is the state a user
         * reports as "the app went weird". Resetting at the START is the one point in the gesture
         * where the correct value is known unconditionally.
         */
        override fun handleOnBackStarted(backEvent: BackEventCompat) {
            surface.cancelBackPreview()
        }

        /**
         * The frame the user actually sees: scale toward 90%, never inside 8dp of the edge, and
         * the crossfade past 35% (migration plan O6.3). [PhoneSurface.previewBack] owns the view
         * work; what crosses from here is a float.
         *
         * `backEvent.progress` IS THE CLOCK AND THERE IS NO ANIMATOR. The gesture is the timeline,
         * so an animator would be a second one racing the thumb -- which is why this is the one
         * moving thing in the app that `ui/kit/Motion.kt` expresses as a pure function rather than
         * as an `Animator`, and why PB-DS-8's fence is untroubled by it.
         */
        override fun handleOnBackProgressed(backEvent: BackEventCompat) {
            surface.previewBack(backEvent.progress)
        }

        /**
         * The finger let go short of the threshold. The screen the user DECIDED TO STAY ON has to
         * be put back exactly as it was, and "exactly" is the operative word: a preview left at
         * 97% and alpha 0.98 is invisible in review and permanent on the device, because nothing
         * else in this app ever writes those properties.
         */
        override fun handleOnBackCancelled() {
            surface.cancelBackPreview()
        }

        override fun handleOnBackPressed() {
            // WHICHEVER DRILL-DOWN IS OPEN IS THE ONE THE GESTURE LEAVES (round 2): the machine
            // switcher and the aggregate inbox are drill-downs too, and a commit that only ever
            // popped the session detail closed the Activity out from under both. The surface
            // owns the which; what crosses from here is still nothing but the commit.
            surface.closeDrillDown()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // Nothing is set on the window. ADR-007 B65 withdrew PB-SEC-4: screenshots, screen
        // recording and the recents thumbnail are all allowed, and the gate that used to
        // require the secure-window flag here now requires its absence. See [SecureWindow].
        surface = PhoneSurface(this, (application as SwarmApplication).phoneRuntime)
        // The surface PUSHES rather than being asked. The drill-down opens when a row is tapped,
        // which is between resumes -- so a callback armed from onResume would be armed at the one
        // moment there is nothing to pop and never again.
        surface.onDrillDownChanged = { open -> backOutOfTheSessionDetail.isEnabled = open }
        onBackPressedDispatcher.addCallback(this, backOutOfTheSessionDetail)
        setContentView(surface.root)
        insetTheSystemBars()
    }

    /**
     * ADR-007 B132: on a real handset the status bar sat on top of the first line of text.
     *
     * PB-RUN-1 pins targetSdk to 35, and from Android 15 the platform lays every app out
     * edge-to-edge whether it asked to or not -- so a window that consumes no insets draws its
     * top view underneath the status bar and its bottom view underneath the navigation bar.
     * Nothing in this app is positioned; the whole content is one scrolling column, so padding
     * the root by the system-bar insets is the entire fix.
     *
     * The insets are RETURNED rather than consumed: this Activity hosts one view, but a listener
     * that swallowed them would silently stop any child that grows a listener later from seeing
     * them, and the failure would be a layout bug nobody could trace back to here.
     *
     * THE BOTTOM ONE IS NOW LOAD-BEARING FOR THE TAB BAR, which is a dependency worth stating
     * because it is invisible from here. `.ptabs` reserves 14 px for the iPhone home indicator
     * inside its own box, and the kit deliberately does not spend it: derivation row 19 rules that
     * an iPhone frame constant yields to the platform's own measurement, and row 20 puts the inset
     * UNDER a bar that is `tabbar_height` tall rather than inside it. So this line is the tab
     * bar's only bottom air, and removing it does not merely un-inset the window -- it drops the
     * bar onto the navigation bar with nothing left to compensate.
     * `android/gate/tabbar_test.go` asserts both halves, because either alone is correct.
     */
    private fun insetTheSystemBars() {
        surface.root.setOnApplyWindowInsetsListener { view, insets ->
            val bars = insets.getInsets(WindowInsets.Type.systemBars())
            val ime = insets.getInsets(WindowInsets.Type.ime())
            // THE TOP IS THE PLATFORM'S OWN INSET AND NOTHING ELSE (phone refit W1.1). It used to
            // be floored at the design's 54dp `screen_top` -- derivation row 19's iPhone preview
            // value -- which is taller than a real status bar, so the header sat in dead space.
            view.setPadding(bars.left, bars.top, bars.right, bottomInsetPx(bars.bottom, ime.bottom))
            insets
        }
    }

    /**
     * Redraw on every resume rather than only on creation. The phone core is built lazily and
     * FAILABLY (PhoneRuntime), and a refusal is not cached -- so a construction that failed
     * while the screen was away is retried here rather than latched for the life of the process.
     * It is also where a phone rebuilt after a completed pairing is first drawn.
     */
    override fun onResume() {
        super.onResume()
        surface.render()
    }

    /**
     * Give back what the surface holds while the screen is not in front of anyone -- the camera
     * above all. A viewfinder left bound is a camera light left on.
     *
     * IT REACHES EXACTLY ONE FACADE VERB, and only one shape of verb may ever be added here.
     * PB-SEC-11's concern is that this class is exported with a LAUNCHER filter, so any app on the
     * device can start it and force this callback. `App.EnterBackground` (ADR-017 amendment T8-b)
     * is safe under exactly that threat because everything it can do is WITHDRAW authority: the
     * worst a hostile app achieves by forcing an onPause is costing the user their own control
     * lease and terminal control generation. A verb that GRANTED anything would still be refused
     * here.
     */
    override fun onPause() {
        super.onPause()
        surface.release()
    }

    /**
     * The controls PB-SEC-12 clause 1 protects, for the assertion in
     * `PhoneActivityWindowTest`. Exposed by name because a test that went looking for "the
     * buttons" in a view hierarchy would keep passing after the last one was removed.
     *
     * It was `gatedActionViews`. The touch filter is what it always meant and is what survives
     * ADR-007 B133; "gated" now names a protection this app no longer has.
     */
    internal fun touchFilteredViews(): List<View> = surface.touchFilteredActions
}

/**
 * The window's bottom inset: the navigation bar, or the KEYBOARD when one is up.
 *
 * NOTHING IN THIS APP HAD EVER MET A KEYBOARD until the conversation gained a pinned composer.
 * The listener above read `systemBars()` only, so an open IME covered whatever was at the bottom
 * of the window -- which did not matter while the composer was the last child of a scroll and
 * could simply be scrolled into view, and matters completely once it is pinned and has promised
 * to stay above the keyboard.
 *
 * THE INSETS ARE ALREADY BEING DISPATCHED. targetSdk 35 forces edge-to-edge whatever the app
 * asks for, so the IME inset arrives on every dispatch and was being ignored rather than
 * withheld. Reading it is additive inside the one listener this app has, which is why no second
 * mechanism appears here.
 *
 * A MAX AND NOT A SUM. The keyboard is drawn OVER the navigation bar rather than above it, so
 * adding the two would inset the window by a strip of screen the keyboard already occupies --
 * the doubling agents-tracker-2pnu F2 records in the other axis.
 *
 * IT IS A TOP-LEVEL FUNCTION so `ConversationScaffoldViewTest` can assert the arithmetic without
 * a `WindowInsets` dispatch.
 */
internal fun bottomInsetPx(barsBottomPx: Int, imeBottomPx: Int): Int =
    maxOf(barsBottomPx, imeBottomPx)
