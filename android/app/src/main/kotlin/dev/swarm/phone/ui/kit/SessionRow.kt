package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * origin: .prow
 *
 * The triage row: a status dot, the project, the agent, the one-line need, and -- on a Working
 * session -- the workbar.
 *
 * THE ATTENTION VARIANT IS THE POINT OF THE COMPONENT. `.prow.attention` is the row blocked on the
 * human, and it says so three times over: a 2 dp rail down its leading edge, a border warmed
 * toward `--p-att`, and the dot's glow. The tab badge is the fourth site of the same state. Every
 * one of them is `--p-att`, and none is `--p-err` -- red means denial, failure and destruction in
 * this product, and a session waiting for a person is none of those.
 *
 * IT TAKES DATA, NOT VIEWS OR COPY. The `group` is `swarmmobile.Session.Group` verbatim: the
 * server derives it once and the phone renders it, so this component looks it up rather than
 * deciding anything. The strings are the screen's (PB-DS-9); what is decided here is what they
 * look like.
 *
 * @param stateDescription what a screen reader should say about the Group's dot, which is the
 * only thing on the row carrying the state. It is copy, so it is the screen's; with none, the dot
 * is marked decorative rather than announced as an unlabelled view.
 */
fun sessionRow(
    context: Context,
    project: CharSequence,
    agent: CharSequence,
    need: CharSequence,
    group: String,
    stateDescription: CharSequence? = null,
): View {
    val gap = Kit.dimenPx(context, R.dimen.swarm_space_8)

    val line = LinearLayout(context).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = android.view.Gravity.CENTER_VERTICAL
        // The dot's view is inflated by its halo and pulled back with negative margins, so it
        // extends past this line's bounds on every side. Necessary and NOT sufficient: this
        // governs the parent's clip and can do nothing about a software layer sized to the child,
        // which is what was actually cutting the halo off (see statusDot).
        clipChildren = false
        clipToPadding = false
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        tag = KitTag.LINE
    }
    line.addView(
        statusDot(context, group, stateDescription).apply {
            // ADDED TO, NOT REPLACING. A glowing dot's margins already carry the negative
            // compensation for its halo -- the view is inflated so the software layer has room,
            // and the inflation is given back so the mark still occupies the design's 7 dp.
            // Assigning the gap here would spend that compensation and shift the dot 9 dp right.
            (layoutParams as LinearLayout.LayoutParams).marginEnd += gap
        },
    )
    line.addView(
        TextView(context).apply {
            setTextAppearance(R.style.TextAppearance_Swarm_Title_Row)
            // `.prow .pj` declares no colour: it inherits `.pscreen { color: var(--p-ink) }`.
            setTextColor(Kit.colour(context, R.color.swarm_text_primary))
            text = project
            // `flex: 1` -- the project takes the slack so the agent name sits hard right.
            layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
            tag = KitTag.PROJECT
        },
    )
    line.addView(
        TextView(context).apply {
            setTextAppearance(R.style.TextAppearance_Swarm_Mono_Agent)
            setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
            text = agent
            layoutParams = LinearLayout.LayoutParams(WRAP, WRAP).apply { marginStart = gap }
            tag = KitTag.AGENT
        },
    )

    val row = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        clipChildren = false
        clipToPadding = false
        background = cardSurface(context, attention = group == "needs_input")
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_12),
            Kit.dimenPx(context, R.dimen.swarm_space_10),
            Kit.dimenPx(context, R.dimen.swarm_space_12),
            Kit.dimenPx(context, R.dimen.swarm_space_10),
        )
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }
    row.addView(line)
    row.addView(
        TextView(context).apply {
            setTextAppearance(R.style.TextAppearance_Swarm_Body_Secondary)
            setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
            text = need
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP).apply {
                topMargin = Kit.dimenPx(context, R.dimen.swarm_space_4)
            }
            tag = KitTag.NEED
        },
    )
    if (group == "working") row.addView(workingBar(context))
    return row
}

/**
 * origin: .prows
 *
 * The rows' container. It exists so a screen never types the 12 dp side padding or the gap between
 * rows -- PB-DS-6's whole claim is that a screen composes components and passes data, and a
 * container is where that claim is usually lost.
 */
fun sessionList(context: Context): LinearLayout = KitStack(
    context,
    LinearLayout.VERTICAL,
    Kit.dimenPx(context, R.dimen.swarm_space_8),
).apply {
    clipChildren = false
    clipToPadding = false
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_12),
        0,
        Kit.dimenPx(context, R.dimen.swarm_space_12),
        0,
    )
}
