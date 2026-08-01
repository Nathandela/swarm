package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
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
 * ## The notice line has no component, and that is an absence rather than a decision
 *
 * There is no notice or status component in the kit. Row 8's empty state is centred with 48 dp of
 * vertical air and means something else; row 22's read-only note is the terminal peek's and means
 * something else again. So the notice is a bare `TextView` carrying the model's copy and no
 * appearance at all -- `SettingsPanelView` reached the same place for the same reason. Reaching
 * for `Body.Secondary` here would be a screen choosing type, which the fence refuses and is right
 * to.
 */
object LaunchTag {
    /** The section the form sits under, in `.plabel`. */
    const val HEADING = "launch.heading"

    /** The control that starts the session. Supplied by the surface that owns the verb. */
    const val SUBMIT = "launch.submit"

    /** The machine's answer, or the form's own refusal. Drawn only when there is one. */
    const val NOTICE = "launch.notice"

    /** One field. The tag names WHICH field, so an assertion cannot confuse two. */
    fun field(id: LaunchFieldId): String = "launch.field." + id.name

    /** The parts whose ON-SCREEN ORDER is the composition. */
    val COMPOSITION: Set<String> =
        setOf(HEADING) + LaunchFieldId.entries.map { field(it) } + setOf(SUBMIT, NOTICE)
}

/**
 * The launch form as a view.
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
        column.addView(fieldFor(field.id).tagged(LaunchTag.field(field.id)))
    }
    column.addView(submit.tagged(LaunchTag.SUBMIT))

    // Drawn only when the form has something to report. An empty notice is not "nothing yet": a
    // form that has launched nothing has not been refused and has not succeeded, and a line
    // reserved for a status that does not exist is a status about an operation nobody issued.
    if (panel.notice.isNotEmpty()) {
        column.addView(
            TextView(context).apply {
                tag = LaunchTag.NOTICE
                text = panel.notice
                layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            },
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
