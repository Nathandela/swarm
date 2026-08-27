package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #27 Conversation header
 *
 * WHO YOU ARE TALKING TO, ON WHICH MACHINE, DOING WHAT RIGHT NOW -- and the way back out.
 *
 * IT IS NOT [navHeaderDrill] WITH EXTRAS. That component is a screen TITLE and a way back: it
 * answers "where am I". This answers "who is this", which is why it carries a subtitle at all --
 * with several machines paired, the machine name is the only thing telling two identically named
 * sessions apart, and the state is the thing a reader came to check.
 *
 * ITS TITLE TAKES THE ROW RUNG AND NOT THE DISPLAY RUNG, deliberately reversing ADR-012 phase 2
 * P4 for this one header. P4 ruled that a screen is a screen and depth is the chevron's job, and
 * it is right about screens; a conversation is less a screen the reader navigated TO than a thing
 * they are inside, and a 27 sp session name over a message list reads as a document heading.
 *
 * THE DOT IS [statusDot] AND NOT [presenceDot]. Presence answers ONLINE / OFFLINE / UNKNOWN,
 * which is a fact about a MACHINE; this reports what a SESSION is doing, in the roster's own
 * Group vocabulary -- so a session reads the same here as on the row the reader tapped.
 */
fun conversationHeader(
    context: Context,
    title: CharSequence,
    subtitle: CharSequence,
    /**
     * The roster's Group for this session, or **empty where the roster has none** -- an orphaned
     * row, or a Group a newer daemon authored and this build cannot name. Empty draws no dot
     * rather than a substituted one; see the argument at the call site below.
     */
    group: String,
    back: View?,
    menu: View?,
): LinearLayout = KitStack(
    context,
    LinearLayout.HORIZONTAL,
    Kit.dimenPx(context, R.dimen.swarm_space_10),
).apply {
    tag = KitTag.CONVERSATION_HEADER
    gravity = Gravity.CENTER_VERTICAL
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_18),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_18),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
    )
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    // A FILL AND ONE HAIRLINE ACROSS THE WHOLE WIDTH -- the tab bar's own construction, upside
    // down. Not a box: a header with four corners floats over the conversation instead of being
    // the top of the screen.
    background = headerSurface(context)

    // BOTH CONTROLS ARE SLOTS THE SURFACE FILLS, which is `approvalSheet`'s precedent and is
    // structural here rather than reuse: each reaches a verb, carries an operation id and
    // PB-SEC-12 clause 1's touch filter, and none of those is the kit's to own. Null draws
    // nothing rather than a control that does nothing -- navHeaderDrill's ruling, and the
    // dead-chevron defect it was written against.
    //
    // THE FLOOR IS SPENT HERE, ON WHATEVER IS HANDED IN. Row 27 states a 48 dp minimum and the
    // header is what carries the claim, exactly as row 15's settings row carries the toggle's:
    // a slot cannot assert its own size, and a floor assigned to a view the kit never sees is a
    // floor nobody is answerable for.
    back?.let { addView(it.atMinimumTarget(context)) }

    addView(
        KitStack(context, LinearLayout.VERTICAL, 0).apply {
            // WEIGHT 1: the identity takes the row, so a long session name ellipsizes rather
            // than pushing the overflow off the trailing edge.
            layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
            addView(
                Kit.textView(context).apply {
                    setTextAppearance(R.style.TextAppearance_Swarm_Title_Row)
                    setTextColor(Kit.colour(context, R.color.swarm_text_primary))
                    text = title
                    isSingleLine = true
                    // THE END AND NOT THE MIDDLE. A session name is a project name, and what
                    // distinguishes two of them is the front.
                    ellipsize = android.text.TextUtils.TruncateAt.END
                },
            )
            addView(
                KitStack(context, LinearLayout.HORIZONTAL, Kit.dimenPx(context, R.dimen.swarm_space_6)).apply {
                    gravity = Gravity.CENTER_VERTICAL
                    // NO GROUP, NO DOT (Wave H, H.8). An empty [group] is the roster having no
                    // fact about this session -- the orphan race where the row is gone, or a
                    // Group from a newer daemon this build does not know -- and the absence of a
                    // record is drawn as the absence of a mark (ADR-009 D2's `.pdot.unknown`).
                    //
                    // IT MAY NOT BE SUBSTITUTED, and `presenceDot`'s KDoc already ruled on this
                    // exact move: rendering an unknown as `statusDot(ctx, "completed")` gives
                    // every correct pixel and is the phone INVENTING a Group. Groups are derived
                    // server-side and rendered verbatim (PB-TOK-8), so there is nothing here to
                    // derive one from. And the substitute is not neutral -- grey is `completed`,
                    // the claim that the agent FINISHED, which would be drawn beside a state word
                    // that may read `working`.
                    if (group.isNotEmpty()) addView(statusDot(context, group))
                    addView(
                        Kit.textView(context).apply {
                            setTextAppearance(R.style.TextAppearance_Swarm_Mono_Meta)
                            setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
                            text = subtitle
                            isSingleLine = true
                            ellipsize = android.text.TextUtils.TruncateAt.END
                        },
                    )
                },
            )
        },
    )

    menu?.let { addView(it.atMinimumTarget(context)) }
}

/**
 * PB-DS-12's floor, applied to a slot this component did not build.
 *
 * A CONTROL SMALLER THAN A FINGER REFUSES TAPS THAT WERE AIMED AT IT, and a slot cannot assert
 * its own size -- so the container that placed it is where the claim lands.
 */
private fun View.atMinimumTarget(context: Context): View = apply {
    val floor = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    minimumWidth = floor
    minimumHeight = floor
}
