package dev.swarm.phone

import android.os.Bundle
import android.view.View
import androidx.appcompat.app.AppCompatActivity

/**
 * Phase B slice S18 -- the app's one Activity, and the window PB-SEC-4 needs in order to have a
 * subject at all.
 *
 * WHY IT EXISTS AND WHY HERE. The module declared no `<activity>` until this slice: S16 shipped
 * the screen MODELS (data classes and enums), so `FLAG_SECURE` had no Window and
 * `filterTouchesWhenObscured` had no View, and both halves of PB-SEC-4 and PB-SEC-12 clause 1
 * were unverifiable rather than unsatisfied. The ruling recorded on 2026-07-25 assigns the
 * Activity to S18 as a SCOPE call: S18 is the first slice blocked on it, no slice ever owned
 * it, and PB-E2E-2 independently requires one because an on-emulator smoke that pairs,
 * observes, takes control and types cannot run against data classes. The scope is bounded to
 * exactly that: enough Window and View to carry those assertions and S19's smoke.
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

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // BEFORE the content view. A window that is briefly unprotected is a window a
        // screenshot, a screen recorder or the recents thumbnailer can catch.
        SecureWindow.protect(this)

        surface = PhoneSurface(this, (application as SwarmApplication).phoneRuntime)
        setContentView(surface.root)
    }

    /**
     * Redraw on every resume rather than only on creation. The phone core is built lazily and
     * FAILABLY (PhoneRuntime): the commonest refusal is "the user has not authenticated yet",
     * and the whole remedy is that they then do -- which happens while this Activity is
     * stopped, not while it is being created.
     */
    override fun onResume() {
        super.onResume()
        surface.render()
    }

    /**
     * The controls PB-SEC-12 clause 1 protects, for the assertion in
     * `PhoneActivityWindowTest`. Exposed by name because a test that went looking for "the
     * buttons" in a view hierarchy would keep passing after the last one was removed.
     */
    internal fun gatedActionViews(): List<View> = surface.gatedActions
}
