package dev.swarm.phone

import android.app.Activity
import android.view.View
import android.view.WindowManager

/**
 * Phase B slice S18 -- PB-SEC-4 and PB-SEC-12 clause 1. THE ONE PLACE the window and touch
 * protections are applied.
 *
 * ONE SINK, NOT ONE CALL PER SCREEN. Per-screen application is how a screen gets missed, and
 * the screen that gets missed is always the one added last, with nothing failing: a per-screen
 * test can only enumerate the screens that exist. Applying the flags where the window is
 * created is what makes the NEXT screen protected by default.
 * android/gate/s18_sec4_windowsecurity_test.go asserts there is exactly one such site.
 *
 * WHAT THE TWO WINDOW PROTECTIONS DO, and they are not the same protection.
 *
 *  - FLAG_SECURE tells the system compositor to refuse screenshots and screen recording for
 *    this window, and blanks it in the recents thumbnail.
 *  - setRecentsScreenshotEnabled(false) is the API-33 way to drop the thumbnail specifically.
 *    It is applied as well as, not instead of: it states the recents decision separately from
 *    the screenshot one, so a later screen that needs screenshots for some reason cannot
 *    silently reacquire the thumbnail by clearing a single flag.
 *
 * android/window-security.tsv records WHICH screens are sensitive and why. The requirement
 * names two by role, pairing and terminal peek; the table is what stops the next screen
 * opting out by default.
 *
 * WHAT IS NOT CLAIMED. FLAG_SECURE is a platform hint the compositor honours. It does not stop
 * a camera pointed at the screen, it is not attested, and an accessibility service can still
 * read the rendered screen (android/input-path-limits.md records that limit). PB-E2E-5 stays
 * deferred: nothing here is evidence about a physical handset.
 */
object SecureWindow {

    /**
     * Apply the window protections. Called from [PhoneActivity.onCreate] BEFORE the content
     * view is set: a window that is briefly unprotected is a window a screenshot can catch.
     */
    fun protect(activity: Activity) {
        activity.window.setFlags(
            WindowManager.LayoutParams.FLAG_SECURE,
            WindowManager.LayoutParams.FLAG_SECURE,
        )
        // API 33 is the app's minSdk (android/toolchain.env SWARM_ANDROID_MIN_SDK), so this
        // needs no version guard.
        activity.setRecentsScreenshotEnabled(false)
    }

    /**
     * Mark a control as a GATED ACTION: one an overlay attack is worth mounting against.
     *
     * `filterTouchesWhenObscured` makes the framework discard a touch that arrived while
     * another window was covering this view, which is the whole of the tapjacking defence
     * Android offers. The actions that need it are the destructive and the authorising ones:
     * take control of a live shell, kill a session, revoke this device. A tap the user did not
     * see themselves make must not reach any of them.
     *
     * It returns the view so a call site reads as a decoration of the construction rather than
     * a separate statement someone can forget to write.
     */
    fun <V : View> gate(view: V): V {
        view.filterTouchesWhenObscured = true
        return view
    }
}
