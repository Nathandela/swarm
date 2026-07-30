package dev.swarm.phone

import android.view.View

/**
 * Phase B slice S18 -- PB-SEC-12 clause 1. THE ONE PLACE the tapjacking defence is applied.
 *
 * THE SCREENSHOT BLOCK IS GONE, AND IT IS A DECISION RATHER THAN DRIFT (ADR-007 B65, ruled by
 * the owner on 2026-07-26: the shipped app ALLOWS screenshots and screen recording). This
 * object used to carry `protect()` -- the platform's secure-window layout flag plus the
 * recents-screenshot opt-out -- applied from [PhoneActivity.onCreate] before the content view
 * was set. Both are removed, and `protect()` was REMOVED WITH THEM rather than left empty: a
 * function still called from onCreate, whose name claims a protection it no longer applies, is
 * worse than no function.
 *
 * NEITHER API IS NAMED IN FULL ANYWHERE IN THIS MODULE, AND THAT IS THE GATE'S DOING. The
 * assertion below is a TEXT SCAN for those two identifiers, and it runs over the raw source
 * with comments included -- because a scan that has to tell code from comment is a scan that
 * can be fooled, and once the assertion is negative being fooled means passing. So the names
 * are spelled where they can do no harm: docs/adr/ADR-007-remote-access.md B65,
 * android/window-security.tsv and android/gate/s18_sec4_windowsecurity_test.go all carry them,
 * and a grep for either lands on the explanation rather than on this file.
 *
 * WHY, IN SHORT; THE FULL ARGUMENT IS IN B65. What the flag bought was already conceded in
 * this file's own previous words -- it is a compositor hint, it is not attested, it stops no
 * camera pointed at the screen, and an accessibility service reads the rendered screen
 * regardless (android/input-path-limits.md records that limit). What it cost was that users of
 * a DEVELOPER TOOL could not share terminal output, which is an ordinary thing to want from
 * the product. android/window-security.tsv records screen by screen what the decision exposes,
 * and answers the two rows that carried a specific argument rather than a generic one: the SAS
 * on the pairing screen, and the terminal grid whose at-rest sealing this undoes one layer up.
 *
 * REINSTATING IT FAILS A GATE, DELIBERATELY. android/gate/s18_sec4_windowsecurity_test.go
 * asserts that no production Kotlin names either API, and PhoneActivityWindowTest drives a real
 * Activity and asserts the window does not carry the flag after onCreate -- which is the half a
 * text scan cannot reach, since a flag set as a raw constant or through a theme names nothing. A
 * requirement deleted leaves nothing behind; a requirement inverted keeps the one property the
 * original bought -- that this is a decision -- and makes the next person to add the flag back,
 * for what will feel at the time like an obvious security improvement, read B65 first.
 *
 * WHAT REMAINS HERE IS A DIFFERENT PROTECTION AGAINST A DIFFERENT ATTACK. PB-SEC-12 clause 1 is
 * tapjacking; it has no screenshot clause and B65 leaves it untouched.
 */
object SecureWindow {

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
