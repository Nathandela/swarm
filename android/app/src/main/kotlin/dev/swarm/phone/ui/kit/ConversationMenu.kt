package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * One line of [conversationMenu]: what it says, what it answers to, and whether it destroys
 * something.
 *
 * THE ID AND THE LABEL ARE TWO FIELDS AND NOT ONE, which is the whole reason this is a type
 * rather than a `List<String>`. What comes back through `onChoose` has to be stable enough for a
 * screen to route on, and what is drawn has to be copy -- PB-DS-9 puts every string on the screen
 * that composes it, and copy changes. A menu keyed on its own visible words would re-route itself
 * the day `Kill session` becomes `End session`.
 *
 * [destructive] IS A BOOLEAN AND NOT AN INK, which is [NoticeKind]'s and [CtaKind]'s arrangement
 * for their reason: a caller handed a colour would be a caller deciding what `--p-err` means, and
 * the two inks here are not interchangeable -- `--p-ink` is a route and `--p-err` is a thing that
 * cannot be undone. It says what the row IS; the kit says what that looks like.
 *
 * IT DOES NOT CARRY THE CONFIRMATION. Kill keeps the question it already ships
 * (*"End this session? The agent stops and the session is gone; this cannot be undone."*) and that
 * question is a screen's, drawn after this menu closes -- ruling R2 moves the control and leaves
 * its ceremony exactly where it was.
 */
data class MenuChoice(val id: String, val label: CharSequence, val destructive: Boolean = false)

/**
 * derived: docs/design/substrate-components.md #28 Conversation menu
 *
 * The header's trailing control: the mark that says there is more here than reading and typing.
 *
 * **IT IS THE 48 dp SQUARE THAT REPLACES 160 dp OF BUTTONS.** Owner ruling R2: Take control, Stop
 * and Kill shipped as three stacked full-width CTAs above the transcript, on a viewport that had
 * roughly 150 dp left for the conversation after the chrome. Two of the three leave the product
 * outright and the third moves in here, where it costs no vertical space in the reading flow at
 * all.
 *
 * IT IS A SEPARATE FACTORY FROM [conversationMenu] and not a mode of it, which is [syncPill] and
 * [syncStrip]'s split for their reason: this is on screen whenever the header is and the menu is
 * on screen only while it is open. One component that was both would be a menu with a permanent
 * row of its own.
 *
 * **IT DRAWS THE GLYPH AND NOTHING ELSE** -- no tap, no anchor, no popup. What an overflow OPENS
 * is the screen's, exactly as [navHeaderDrill] leaves its back control's destination to the
 * destination that has one, and a kit that owned the popup would own where it is positioned, what
 * dismisses it and what happens to the session while it is up.
 *
 * THE GLYPH IS THE CHARACTER AND NOT AN ASSET, which is the opposite of the back chevron's
 * arrangement and is decided by the same rule. §4 could give the chevron a path because
 * `res/drawable/swarm_nav_back.xml` holds one and says in its own comment that the path is the
 * asset's; there is no overflow asset in this app, and drawing one here would be inventing a
 * design value in code. The drawing spells this control as U+22EE and so does this.
 *
 * IT SETS NO CONTENT DESCRIPTION, and that is [notice]'s ruling rather than an oversight: a
 * glyph-only control needs one, the words are copy, and copy belongs to the screen (PB-DS-9). The
 * drawing's own copy table records no string for it, so authoring one here would put a sentence on
 * screen that nobody signed.
 */
fun overflowControl(context: Context): TextView = Kit.textView(context).apply {
    // Row 27's own cell: "the overflow glyph `--p-ink2`". The rung is the header's row rung, so
    // the mark and the session name beside it are the same size -- a heavier glyph would pull the
    // eye off the identity, which is what the header is for.
    setTextAppearance(R.style.TextAppearance_Swarm_Title_Row)
    setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
    text = OVERFLOW_GLYPH
    gravity = Gravity.CENTER
    // PB-DS-12's floor, on BOTH edges, and it costs nothing visually: this control has no fill of
    // its own, so what grows is the empty space a finger may land in. `backControl`'s reasoning at
    // the other end of the same header.
    minimumWidth = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    // Row 23, radius 0 for `backControl`'s reason: this paints no surface, so the ring's `space_2`
    // offset is measured against the 48 dp box rather than against a fill on the same edge.
    Kit.focusable(this, componentRadiusPx = 0f)
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
}

/**
 * derived: docs/design/substrate-components.md #28 Conversation menu
 *
 * Everything that is not reading or typing, in a block that costs the conversation nothing until
 * it is opened.
 *
 * **WHAT IS NEVER HERE, and both absences are refusals rather than omissions.** There is no
 * TERMINAL VIEW: ADR-017:60-65 forbids a raw-terminal route on a session with a structured
 * record, so a row offering one would be a door onto a room that is not there. And there is no
 * REPAIR: the tear has a position and the repair is drawn at it ([gapDivider]), because two
 * affordances for one live-only operation are two pending states for one act, competing over
 * which of them is in flight.
 *
 * **THE CHOICES ARE THE CALLER'S, AND WHICH ONES EXIST IS A FACT ABOUT THE SESSION.** An ended
 * session cannot be killed and a fully loaded conversation has nothing older to fetch; a menu that
 * held its own list would draw both anyway and refuse them on tap, which is the dead-affordance
 * defect `navHeaderDrill` already has a nullable parameter to avoid.
 *
 * **IT IS THE ONE COMPONENT IN THIS KIT THAT HANDLES ITS OWN TAPS**, and the reason is structural
 * rather than a relaxation of PB-DS-9. Every other component is a view a screen composed and can
 * therefore reach; these rows are built FROM data inside this function, so a screen cannot attach
 * a listener to a row it did not build. The two alternatives were a screen composing the rows
 * itself -- a screen authoring a component's composition, which is what the kit exists to prevent
 * -- and returning a list of views for the caller to wire, which is the same thing with an extra
 * step. What a choice MEANS still leaves here through [onChoose]; only the wiring stays.
 *
 * **THE SURFACE IS [toastSurface] UNCHANGED**, which is §2's reuse rule doing exactly what it is
 * for. Row 1 already specifies a floating elevated block -- `--p-elev`, the card's hairline, the
 * card's radius, the card's key light, opaque -- and a menu is the same kind of object over the
 * same ground. A fourth surface recipe here would differ from row 1's in no cell.
 *
 * THE ROWS ARE SEPARATED BY A HAIRLINE AND NOT BY A GAP. `.menu div + div` is a rule between
 * lines, and the distinction matters on a destructive menu: a gap says these are three items in a
 * list, a rule says these are three separate acts, and the last of them ends a session.
 */
fun conversationMenu(
    context: Context,
    choices: List<MenuChoice>,
    onChoose: (String) -> Unit,
): LinearLayout = KitStack(context, LinearLayout.VERTICAL, 0).apply {
    background = toastSurface(context)
    // The block's own inset, `space_4`, which is the smallest step on the ladder and is all a menu
    // needs: what holds a row off the edge is the ROW's padding, and this is the margin that stops
    // a row's focus ring sitting on the block's hairline.
    val inset = Kit.dimenPx(context, R.dimen.swarm_space_4)
    setPaddingRelative(inset, inset, inset, inset)
    // HUGGING, not MATCH_PARENT: the menu is anchored under a control at the trailing edge of the
    // header, and a full-width block there would be a sheet. The width it takes is its longest
    // row's, which is the drawing's own arrangement.
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)

    choices.forEachIndexed { index, choice ->
        if (index > 0) addView(menuRule(context))
        addView(menuRow(context, choice, onChoose))
    }
}

/**
 * One row: a label, its ink, and the 48 dp line the finger lands on.
 *
 * THE FLOOR IS LOAD-BEARING HERE RATHER THAN GENEROUS. The label is 12.5 sp inside `space_8` of
 * padding, so the DRAWN line is under 34 dp -- and the row directly under a mis-aimed tap on
 * `Load earlier messages` is `Kill session`. Growing the target changes nothing about the drawing
 * and everything about which act a near-miss performs.
 */
private fun menuRow(
    context: Context,
    choice: MenuChoice,
    onChoose: (String) -> Unit,
): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
    // `--p-ink` for a route, `--p-err` for the one act that cannot be undone. It is the notice
    // line's pair of inks and it is the same claim: what changes is who is speaking, not how
    // loudly, so the size and the weight are the same in both rows.
    setTextColor(
        Kit.colour(
            context,
            if (choice.destructive) R.color.swarm_state_error else R.color.swarm_text_primary,
        ),
    )
    text = choice.label
    // ONE LINE, ELLIPSISED. The block hugs its longest row, so nothing here truncates in
    // practice -- what this buys is that a row can never become TWO lines, which would break the
    // 48 dp rhythm and make one act taller than the one under it. On a menu whose last row is
    // `Kill session`, rows of different heights is not a cosmetic problem.
    Kit.identityCell(this)
    gravity = Gravity.CENTER_VERTICAL
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
    )
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    // Radius 0, `backControl`'s reason a third time: the row paints no fill, so its ring is the
    // offset around the box the floor above just gave it.
    Kit.focusable(this, componentRadiusPx = 0f)
    setOnClickListener { onChoose(choice.id) }
    // MATCH, so every row in the block is one width and the ink is the only difference between
    // them. A hugging row would make `Kill session` narrower than `Load earlier messages` and the
    // block would read as ragged rather than as a list.
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
}

/**
 * The rule between two rows: `--p-hair`, one hairline tall, edge to edge inside the block.
 *
 * `Kit.dpPx` AND NOT `Kit.dp`, which is the rule every other hairline in this kit already obeys
 * and the one that is easy to break here because this one is a LAYOUT height rather than a stroke
 * on a drawable. At density 2.625 the unrounded value lays out 2 px where the header's own rule
 * directly above paints 3, and two weights of the same 1 dp line on one screen is what the metric
 * gate is right to refuse.
 */
private fun menuRule(context: Context): View = View(context).apply {
    setBackgroundColor(Kit.colour(context, R.color.swarm_hairline))
    layoutParams = LinearLayout.LayoutParams(MATCH, Kit.dpPx(context, KitMetrics.HAIRLINE_DP))
}

/**
 * U+22EE, the mark the drawing spells this control with.
 *
 * IT IS NOT PICTOGRAPHIC, which is the distinction `ToolCard`'s ASCII glyph vocabulary turns on:
 * that file refuses a picture because a picture would be copy this repository's no-emoji rule
 * forbids. A vertical ellipsis is punctuation -- the platform's own overflow mark, and the one the
 * drawing draws.
 */
private const val OVERFLOW_GLYPH = "⋮"
