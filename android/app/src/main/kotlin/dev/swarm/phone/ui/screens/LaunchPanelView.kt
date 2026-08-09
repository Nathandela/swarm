package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.noticeDetail
import dev.swarm.phone.ui.kit.screenAir
import dev.swarm.phone.ui.kit.sectionLabel

/**
 * Phase B slice S24 -- PB-DS-6 and PB-DS-9: the launch form, composed.
 *
 * IT HAS NO SCREEN INVENTORY ENTRY, which [LaunchPanel] states first and this file restates
 * because it changes what "composed as recorded" can mean here. The eight screens the artifacts
 * draw are the inbox, the session detail, the terminal peek, machines, activity, settings, pairing
 * and the approval sheet. Starting a session is PB-APP-6's requirement and the mock never drew it,
 * so there is no recorded composition to follow -- what there is, is the reuse rule: a section
 * label over a stack of `.cmd`-family fields with a `.a2-ok` under them, every one of which is a
 * component the design already specifies for somewhere else.
 *
 * ## Why so much of it is a parameter
 *
 * This file composes ONE factory and takes the rest. That is `PairingPanelView`'s arrangement and
 * the reasons are the same, restated because a short composition invites the wrong conclusion:
 *
 * - **The three fields hold state.** They are `textField` -- derivation row 9, built in
 *   `PhoneSurface` -- and what a user has typed into them is read back on submit. A screen that
 *   constructed them would hand a new, empty field to the user on every redraw.
 * - **The submit control carries a facade call and a touch filter.** It is
 *   `ctaButton(kind = APPROVE)`, PB-SEC-12 clause 1's `filterTouchesWhenObscured`, and
 *   `App.Launch`. A screen owning a native call is not what a screen is.
 *
 * Both are still built out of the kit; `android/gate/s24_screens_test.go` reads the call sites
 * where they actually are.
 *
 * ## The notice line is the kit's, and the absence it used to record was not an absence
 *
 * This section read "there is no notice or status component in the kit ... so the notice is a bare
 * `TextView` carrying the model's copy and no appearance at all". `§4 Notice line` now specifies
 * one and `ui/kit/Notice.kt` builds it (agents-tracker-ksvb.4). What the old paragraph got wrong is
 * that no appearance is itself an appearance: a `TextView` with no `TextAppearance` renders at the
 * platform's ~14 sp, and the largest body style in this app's ladder is 12.5 sp -- so the form's
 * report about a refused launch was the biggest text on the form. Row 8's empty state and row 22's
 * read-only note still mean something else, which is why §4 gained a row rather than this file
 * borrowing one. Reaching for `Body.Secondary` HERE would still be a screen choosing type, and the
 * fence still refuses it.
 */
object LaunchTag {
    /** The section the form sits under, in `.plabel`. */
    const val HEADING = "launch.heading"

    /** The control that starts the session. Supplied by the surface that owns the verb. */
    const val SUBMIT = "launch.submit"

    /** The machine's answer, or the form's own refusal. Drawn only when there is one. */
    const val NOTICE = "launch.notice"

    /**
     * The machine's own words under [NOTICE], in `.sheet2 .ctx` (agents-tracker-ksvb.10).
     *
     * IT IS ITS OWN PART for [DetailTag.LEASE_DETAIL]'s reason: it is drawn only for a refusal,
     * and a test that could not tell it from the sentence could not say which of the two was
     * carrying the wire string.
     */
    const val NOTICE_DETAIL = "launch.notice.detail"

    /** One field. The tag names WHICH field, so an assertion cannot confuse two. */
    fun field(id: LaunchFieldId): String = "launch.field." + id.name

    /** The parts whose ON-SCREEN ORDER is the composition. */
    val COMPOSITION: Set<String> =
        setOf(HEADING) + LaunchFieldId.entries.map { field(it) } +
            setOf(SUBMIT, NOTICE, NOTICE_DETAIL)
}

/**
 * The launch form as a view.
 *
 * **THE COLUMN IS BARE AND ITS FLUSH CHILDREN CARRY THE SCREEN'S AIR** (owner ruling 2026-08-09,
 * agents-tracker-nx44.10). The heading is a `sectionLabel` and holds itself off the glass; the two
 * boxes, the control and both notice lines carry nothing of their own, so `screenAir` gives each
 * of them the ruled `swarm_space_12`, once. A padding on the column would spend it on the heading
 * too, which is agents-tracker-2pnu F2's doubling -- `ui/kit/ScreenColumn.kt` argues it in full and
 * `ScreenAirSweepTest` holds every screen to it.
 *
 * @param fieldFor the input for one field. It is a function of the field id rather than a list so
 *  a caller cannot pair the working directory's box with the agent's hint, which is the one defect
 *  [LaunchField]'s named id exists to prevent.
 * @param submit the control that starts the session.
 * @param below views this slice has NOT recomposed, hosted under the panel.
 */
fun launchPanelView(
    context: Context,
    panel: LaunchPanel,
    fieldFor: (LaunchFieldId) -> View,
    submit: View,
    below: View? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    column.addView(sectionLabel(context, panel.heading).apply { tag = LaunchTag.HEADING })

    // THE ORDER IS THE MODEL'S, and it is a list rather than a set for that reason: agent, then
    // working directory, then the first message. The two the daemon refuses a launch without come
    // first, so a user who stops reading has still answered them.
    panel.fields.forEach { field ->
        column.addView(fieldFor(field.id).tagged(LaunchTag.field(field.id)).screenAir())
    }
    column.addView(submit.tagged(LaunchTag.SUBMIT).screenAir())

    // Drawn only when the form has something to report. An empty notice is not "nothing yet": a
    // form that has launched nothing has not been refused and has not succeeded, and a line
    // reserved for a status that does not exist is a status about an operation nobody issued.
    if (panel.notice.isNotEmpty()) {
        column.addView(
            notice(context, panel.notice).apply { tag = LaunchTag.NOTICE }.screenAir(),
        )
    }

    // THE MACHINE'S OWN WORDS, UNDER THE FORM'S OWN SENTENCE (agents-tracker-ksvb.10). They used to
    // BE the notice -- `noticeFor` returned `rendering.reason` and nothing else -- so a daemon
    // error string was the whole of what a refused launch said, in this form's own body type. Same
    // condition as the line above and for the same reason: only a refusal has one.
    if (panel.noticeDetail.isNotEmpty()) {
        column.addView(
            noticeDetail(context, panel.noticeDetail)
                .apply { tag = LaunchTag.NOTICE_DETAIL }
                .screenAir(),
        )
    }

    below?.let { column.addView(it) }
    return column
}

/**
 * Tag a slot with the part it renders and detach it from whatever last held it.
 *
 * The detach is not tidiness: the panel is rebuilt when the notice changes, and a slot arriving at
 * its next `addView` still claiming a discarded parent is refused by Android with "the specified
 * child already has a parent".
 */
private fun View.tagged(tag: String): View = apply {
    this.tag = tag
    (parent as? ViewGroup)?.removeView(this)
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
