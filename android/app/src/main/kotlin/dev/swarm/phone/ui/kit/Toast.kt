package dev.swarm.phone.ui.kit

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.text.SpannableStringBuilder
import android.text.Spanned
import android.text.style.ForegroundColorSpan
import android.text.style.TextAppearanceSpan
import android.view.Gravity
import android.view.View
import android.widget.FrameLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #1 Toast
 *
 * The one sentence the app says about something that has just happened.
 *
 * There is deliberately no `origin:` line. `.toast` is the RETIRED MOCK's class -- Substrate's
 * directions page draws no toast at all -- so the shared block declares no such rule and citing it
 * as an origin would claim a join to something that does not exist. [readOnlyNote] and
 * [activityRow] are in the same position and say so the same way.
 *
 * **WHAT IT REPLACES IS SILENCE.** Until this existed, every answer to a press landed in one of
 * three persistent text lines -- two of which are children of `PhoneSurface`'s unrecomposed column,
 * visible only at the bottom of the Inbox tab. A refusal produced by a control on the Machines
 * screen was written somewhere the user was not looking, and a SUCCESS was written nowhere at all:
 * `dispatchPress` clears the line before dispatching, so "still crossing" and "done" are the same
 * empty line. The persistent lines stay -- see `PressFeedback`, which puts a refusal on both --
 * and what this adds is the half that lands where the press happened.
 *
 * **IT DOES NOT MOVE, AND THAT IS ADR-007 B134 RATHER THAN AN OMISSION.** The motion budget is
 * three animations -- the bottom sheet, the push banner, the streaming caret -- and row 1 states
 * this one's behaviour as "3200 ms then hidden, NO TRANSITION". A fade would be the obvious thing
 * to add and it is the decorative animation the ADR forbids. What [ToastHost] owns is a TIMER,
 * which interpolates nothing.
 *
 * **THE SUFFIX IS A SPAN AND NOT A SECOND VIEW.** Row 1 states two type roles for one line, and
 * the mock's own template is `msg + " " + <span class="m">mono</span>` -- so the separator is the
 * design's rather than this factory's invention, and the two roles wrap as one sentence instead of
 * laying out as a label and a value.
 *
 * **IT DOES NOT FORBID WRAPPING, WHICH THE MOCK'S `white-space: nowrap` DOES.** PB-DS-12 requires
 * the layout to survive a 1.3x font scale, and §8.7 already names this component as the one whose
 * longest copy is a problem at ordinary scale. A single clipped line is the failure mode a `nowrap`
 * buys, and there is nothing in row 1 -- which is the specification -- that asks for one.
 *
 * @param message the whole sentence, which the SCREEN writes (PB-DS-9 gives copy to the screen and
 *  this component styles it).
 * @param suffix the mono fragment that trails it, or null for the four of the mock's seven toasts
 *  that carry none. It is a separate argument rather than a fragment of [message] because row 1
 *  states it as a separate CELL: unlike the activity row's emphasis, it is not part of the
 *  sentence, it is an identifier appended to one.
 */
fun toast(context: Context, message: CharSequence, suffix: CharSequence? = null): TextView =
    TextView(context).apply {
        setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
        setTextColor(Kit.colour(context, R.color.swarm_text_primary))
        background = toastSurface(context)
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_16),
            Kit.dimenPx(context, R.dimen.swarm_space_10),
            Kit.dimenPx(context, R.dimen.swarm_space_16),
            Kit.dimenPx(context, R.dimen.swarm_space_10),
        )
        // §8.7: the announcement is a LIVE REGION and not a content description. A description is
        // read when a view is focused and nothing ever focuses a toast; a live region is read when
        // its content changes, which is the only moment a toast has. POLITE rather than ASSERTIVE
        // because none of this copy is worth interrupting a reading already in progress.
        accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
        text = toastText(context, message, suffix)
        // Row 1's placement cell, which belongs to the component rather than to whatever hosts it:
        // `bottom toast_bottom = tabbar_height + space_18 = 92; centred`. A screen that positioned
        // this would be choosing a frame constant, which is the decision `s24_screens_test.go`
        // fences a screen out of making.
        layoutParams = FrameLayout.LayoutParams(
            WRAP,
            WRAP,
            Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL,
        ).apply { bottomMargin = Kit.dimenPx(context, R.dimen.swarm_toast_bottom) }
        tag = KitTag.TOAST
    }

/**
 * derived: docs/design/substrate-components.md #1 Toast
 *
 * Where a toast is shown from: the overlay a WINDOW hosts, and the lifetime row 1 gives it.
 *
 * **IT IS A COMPONENT AND NOT THREE LINES IN A SURFACE.** A toast is the only thing in this app
 * that appears without anyone navigating to it, and the three facts that make it one -- where it
 * sits, how long it stays, and that it is announced -- are all row 1's. A surface that owned them
 * would own a timer, a placement and an accessibility decision each, in as many copies as there
 * are surfaces; `PhoneSurface` and `SettingsSurface` already keep two unstyled outcome lines that
 * way, and they disagree about what they are for.
 *
 * **ONE VIEW, BUILT ONCE, HIDDEN RATHER THAN REMOVED.** §8.7 is explicit that the toast's 3200 ms
 * visual lifetime is shorter than a TalkBack reading of its longest copy, so the announcement must
 * have a lifetime of its own. That rules out the obvious implementation -- add a toast, remove it
 * when the timer fires -- because taking the view away is what cancels a reading still in progress.
 * [dismiss] therefore changes visibility and nothing else: the text stays, the live region stays,
 * and the view a screen reader was announced from is still there when the next message arrives.
 *
 * **THE TIMER IS CANCELLED ON EVERY SHOW.** Two messages in a row are the normal case -- a press
 * refused twice, a confirmation followed by an error -- and a second toast that inherited the
 * first's expiry would be on screen for whatever was left of it. `Handler.postDelayed` is what
 * `android/gate/s23_motion_test.go` records as the one frame-driving call it permits, and it is
 * permitted because the pairing screen's state poll is the same program; what makes this one not
 * an animation is that it fires once, changes a visibility and interpolates nothing.
 *
 * IT TAKES NO TOUCHES. It covers the bottom of every screen in the app for 3.2 s at a time, and a
 * view over content a user is reading that could take a tap is what PB-SEC-12 clause 1 exists for.
 * A `FrameLayout` that is neither clickable nor focusable does not consume a touch -- the platform
 * offers the event to the sibling below it -- so the app under it stays usable while a toast is up.
 */
internal class ToastHost(context: Context) : FrameLayout(context) {

    private val line: TextView = toast(context, "").apply { visibility = GONE }

    /** Row 1's "then hidden": one visibility change, on the looper that owns the view. */
    private val expiry = Runnable { line.visibility = GONE }

    private val lifetime = Handler(Looper.getMainLooper())

    init {
        // The overlay is the height of what it holds and sits at the bottom of its host, so what
        // is above it -- every screen in the app -- is neither covered nor measured against it.
        layoutParams = FrameLayout.LayoutParams(MATCH, WRAP, Gravity.BOTTOM)
        addView(line)
    }

    /**
     * Say [message], restarting the lifetime.
     *
     * The text is written even when it is the same words twice, and that is what makes the second
     * announcement happen: `TextView.setText` notifies accessibility on every write, so a live
     * region re-announces a repeated message rather than falling silent on it -- which is the
     * state a user gets when the same refusal happens twice.
     *
     * VISIBLE FIRST, TEXT SECOND, and the order is load-bearing: the platform DROPS the text
     * change's live-region event while the view is not shown (`sendAccessibilityEventUnchecked`
     * early-returns on `!isShown()`), so a write into a GONE view is a first message no screen
     * reader is told about. The wrong order announced only repeats inside the 3200 ms window --
     * the inverse of the burst case above.
     */
    fun show(message: CharSequence, suffix: CharSequence? = null) {
        lifetime.removeCallbacks(expiry)
        line.visibility = VISIBLE
        line.text = toastText(context, message, suffix)
        lifetime.postDelayed(expiry, KitMetrics.TOAST_LIFETIME_MS)
    }

    /** Take it down now, and cancel the expiry so it cannot land on the next message. */
    fun dismiss() {
        lifetime.removeCallbacks(expiry)
        line.visibility = GONE
    }
}

/**
 * [message], with row 1's mono suffix appended in `Mono.CodeSmall` / `--p-ink2`.
 *
 * IT IS A TOP-LEVEL `private fun` because it is not a component: `TestPBDS6_EveryKitFactoryIsAn
 * InboxComponent` reads every top-level `fun` in a claimed file as a factory and refuses one the
 * inventory does not name, and this is the text two of them share -- [toast] builds it once and
 * [ToastHost] rebuilds it on every message.
 *
 * TWO SPANS AND NOT ONE, as everywhere else in this kit: no `TextAppearance` in this app holds a
 * colour (Substrate binds one style to several inks), so the appearance carries the family, the
 * size and the weight, and the ink is stated beside it. Without the second span the suffix would
 * inherit the body's `--p-ink` and row 1's two ink cells would silently become one.
 *
 * THE SEPARATOR IS ONE SPACE AND IT IS THE MOCK'S. Its own template writes
 * `msg + " " + <span class="m">mono</span>`, so this is a transcription rather than a factory
 * inventing punctuation between two pieces of somebody else's copy.
 */
private fun toastText(
    context: Context,
    message: CharSequence,
    suffix: CharSequence?,
): CharSequence {
    if (suffix.isNullOrEmpty()) return message
    val text = SpannableStringBuilder(message).append(" ")
    val start = text.length
    text.append(suffix)
    text.setSpan(
        TextAppearanceSpan(context, R.style.TextAppearance_Swarm_Mono_CodeSmall),
        start,
        text.length,
        Spanned.SPAN_EXCLUSIVE_EXCLUSIVE,
    )
    text.setSpan(
        ForegroundColorSpan(Kit.colour(context, R.color.swarm_text_secondary)),
        start,
        text.length,
        Spanned.SPAN_EXCLUSIVE_EXCLUSIVE,
    )
    return text
}
