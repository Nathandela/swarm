package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
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
 * @param lit ADR-009 D4's promotion: this session is the one blocked on the human, so the row is
 *  drawn on the elevated slab under the stronger key-light.
 *
 *  **IT IS TAKEN AND NOT DERIVED, AND IT USED TO BE DERIVED HERE.** This component wrote
 *  `group == "needs_input"`, which is a product decision -- WHICH Group is blocked on the human --
 *  made a third time, beside the two `TriageInboxScreen` already makes with the same string. Three
 *  copies disagree the day one moves: a skin that promoted a different Group would move the slab
 *  and leave the tab badge counting the old one, every test green because each component was asked
 *  for exactly what it drew. `InboxRow.lit` is the one name now, and this renders it.
 *
 *  IT HAS NO DEFAULT, for the reason `InboxRow.agent` gives about the same class of field: a
 *  default makes it optional at every call site and it goes unpopulated at whichever one nobody
 *  revisited -- and a row that quietly defaulted to `false` renders exactly what a correct resting
 *  row renders, so the two are not ambiguous on screen, they are identical.
 * @param promoted ADR-009 D5's moment: this session's Group BECAME NeedsInput since the last draw,
 *  so the row plays the specular sweep once as it appears.
 *
 *  **IT IS A TRANSITION AND [lit] IS A STATE**, which is the whole difference between the two
 *  flags and the reason there are two. `lit` is true for as long as a session waits; `promoted` is
 *  true for the one draw in which it started waiting. A component that derived the second from the
 *  first would sweep every waiting row on every redraw -- the ambient field-register motion D5
 *  bans in the same paragraph that permits this one, arrived at by forgetting a comparison. Who
 *  transitioned is `TriageInboxScreen.promotions`, which is the only place that can know: it
 *  compares the screen against the one the user was actually looking at.
 *
 *  IT HAS NO DEFAULT, for [lit]'s reason exactly.
 */
fun sessionRow(
    context: Context,
    project: CharSequence,
    agent: CharSequence,
    need: CharSequence,
    group: String,
    lit: Boolean,
    promoted: Boolean,
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
        Kit.textView(context).apply {
            setTextAppearance(R.style.TextAppearance_Swarm_Title_Row)
            // `.prow .pj` declares no colour: it inherits `.pscreen { color: var(--p-ink) }`.
            setTextColor(Kit.colour(context, R.color.swarm_text_primary))
            text = project
            // `flex: 1` -- the project takes the slack so the agent name sits hard right.
            layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
            tag = KitTag.PROJECT
            // A project is a path, not a sentence: a monorepo name that wrapped turned this
            // two-line card into a four-line one and moved every row under it.
            Kit.identityCell(this)
        },
    )
    // NO AGENT MEANS NO CELL, not an empty one. `swarmmobile.Session.Agent` is verbatim from the
    // wire and mobile/types.go states what an empty one means: "the session's records carried
    // none". It is never derived on-device. So the honest rendering of a session the machine said
    // nothing about is no view at all -- an empty `TextView` here still spent the 8 dp gap before
    // it, which is a cell claiming to be the agent's while naming nobody.
    //
    // IT IS THE ROW'S OWN RULE, not a new one: the workbar below exists only on a Working session,
    // and the tab badge renders nothing at zero rather than a badge reading "0". What must never
    // happen in this cell is the other repair -- a placeholder, or a fall back to the project or
    // the id -- which would put a fabricated identity where a reader trusts to find the agent's
    // (ADR-007 B135).
    if (agent.isNotBlank()) {
        line.addView(
            Kit.textView(context).apply {
                setTextAppearance(R.style.TextAppearance_Swarm_Mono_Agent)
                setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
                text = agent
                layoutParams = LinearLayout.LayoutParams(WRAP, WRAP).apply { marginStart = gap }
                tag = KitTag.AGENT
                Kit.identityCell(this)
            },
        )
    }

    val row = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        clipChildren = false
        clipToPadding = false
        background = cardSurface(context, attention = lit)
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
        Kit.textView(context).apply {
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
    // THE ONE CALL SITE THE SWEEP HAS, and it is here rather than in the screen because a screen
    // composes components and passes data (PB-DS-6). The rule that at most one of these plays per
    // viewport lives in Motion, so a list that builds two promoted rows in one pass gets one
    // sweep -- the newest -- without this component knowing anything about the other row.
    if (promoted) Motion.specularSweep(context, row)
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
