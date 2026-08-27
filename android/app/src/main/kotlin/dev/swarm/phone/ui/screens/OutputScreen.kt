package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.navHeaderDrill
import dev.swarm.phone.ui.kit.screenAir
import dev.swarm.phone.ui.kit.scrolledHorizontally

/**
 * OWNER RULING R8's SECOND HALF -- one tool run's whole output, on a screen of its own.
 *
 * **IT IS WHAT MAKES THE IN-PLACE BOUND HONEST.** `TranscriptScreen` opens a card to twenty lines
 * and keeps the WHOLE body on the block's route, so the flow shows a head and never drops a tail.
 * Without this screen that arrangement is a truncation the phone invented -- which is precisely
 * what IS-TOOL-3 forbids one field over, where an item "SHALL NOT claim to hold the underlying
 * output". The bound is a reading decision; it is only allowed to be one because the rest is one
 * tap away.
 *
 * **IT AUTHORS NOTHING, AND THAT IS THE DRAWING'S OWN ROW FOR IT**: "one tool run's whole output,
 * in the mono well the app already has, scrollable both ways". No copy is tabled for this screen,
 * so no sentence is written on it. The title arrives from the caller and is the sentence the
 * conversation already drew for the row -- the wire's own `tool` and `target`, joined by
 * `TranscriptScreen` -- because a second phrasing of one tool call would be the same fact in two
 * voices, which is the rule that produced five wordings of one moved turn.
 *
 * **SCROLLABLE BOTH WAYS IS A CORRECTNESS CLAIM.** `monoWell` sets `setHorizontallyScrolling(true)`,
 * which tells it to lay a line out whole rather than wrapping it -- and says nothing about where the
 * part past the visible edge goes. With no scroller it goes nowhere and is silently clipped
 * (agents-tracker-ksvb.7): unreachable content wearing a typography flag's costume, on the one
 * screen whose entire purpose is that the body is reachable. Down is the same argument in the other
 * axis: a body routed here is by construction longer than twenty lines.
 *
 * **THE HEADER DOES NOT SCROLL AND THE BODY DOES, WHICH IS THE DRAWING'S STRUCTURAL RULE FOR THE
 * CONVERSATION SPENT ONE SCREEN DEEPER.** "Only the list scrolls" is what stops a screen becoming a
 * document; a header that scrolled away would leave a reader deep in a test run with nothing on
 * screen saying which run it is, and no way back that does not involve scrolling to the top.
 */
object OutputTag {
    /** The drill header: where this came from, and which tool run it is. */
    const val NAV = "output.nav"

    /** The body, whole, in the app's one mono block. */
    const val WELL = "output.well"
}

/**
 * @param title the sentence the conversation already drew for this tool run. It is the machine's
 *  own words joined by [TranscriptScreen], passed in rather than recomposed here.
 * @param output [TranscriptRoute.Output]'s `text`: the WHOLE body, never the remainder past the
 *  bound. A screen that began at line twenty-one would open on a page whose first line is
 *  mid-sentence, and would make the two renderings of one body disagree about what it is.
 * @param onBack where the chevron goes. Null draws no control at all -- `navHeaderDrill(back =
 *  null)`'s own ruling, and the defect it was written against (agents-tracker-2yb).
 */
fun outputScreen(
    context: Context,
    title: CharSequence,
    output: CharSequence,
    onBack: (() -> Unit)? = null,
): View = literalScreen(
    context = context,
    title = title,
    // THE WELL IS BUILT HERE AND NOT INSIDE THE SHARED ARRANGEMENT, so this screen names its own
    // part rather than passing a tag down to be applied out of sight. It is also what keeps the
    // two screens honestly separate to `android/gate/s24_screens_test.go`: each one spends the kit
    // itself, and its composition table row is a claim about a factory this file actually calls.
    well = monoWell(context, output).apply { tag = OutputTag.WELL },
    onBack = onBack,
    navTag = OutputTag.NAV,
)

/**
 * The arrangement [outputScreen] and [diffScreen] share: a drill header that stays, and one
 * machine-authored literal that scrolls in both axes under it.
 *
 * **IT IS ONE FUNCTION BECAUSE IT IS ONE ARRANGEMENT, AND THE DRAWING SAYS SO IN AS MANY WORDS** --
 * `diffScreen` is tabled as "one file's unified diff, same well, same rules". Two copies of a
 * composition that is deliberately identical would drift on the first edit, and the first edit is
 * the one that matters: the day the well gains a wrap toggle or a line-number gutter, a reader has
 * to be able to see that both screens got it.
 *
 * **WHAT IS NOT SHARED IS THE TAG**, and that is this package's standing rule rather than caution:
 * a single tag over both screens would let a test find either and assert the other's behaviour.
 * Each screen names its own parts and passes them in.
 *
 * **THE SCREEN OWNS ITS SCROLL, so it must be hosted at full height** -- the conversation
 * composition's own arrangement (plan §5, B.1), not the scaffold that wraps a whole page in one
 * `ScrollView`. Nested inside that one the weight below has no height to divide and the body would
 * collapse; recorded here because the host is a different lane's file and a screen cannot enforce
 * where it is placed.
 */
internal fun literalScreen(
    context: Context,
    title: CharSequence,
    well: TextView,
    onBack: (() -> Unit)?,
    navTag: String,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, MATCH)
    }

    column.addView(
        navHeaderDrill(
            context,
            // NO DESTINATION, NO CONTROL. The word is `terminalFallbackView`'s, kept verbatim
            // rather than retyped -- this app spells its back control one way, and a second
            // spelling on a screen the drawing tables no copy for would be an invented string.
            back = if (onBack == null) null else BACK,
            title = title,
        ).apply {
            tag = navTag
            onBack?.let { back ->
                findViewWithTag<View>(KitTag.DRILL_BACK)?.setOnClickListener { back() }
            }
        },
    )

    column.addView(
        ScrollView(context).apply {
            // A BODY SHORTER THAN THE VIEWPORT STILL FILLS IT, so the recessed surface spans the
            // screen rather than stopping at the last line -- `scrolledHorizontally`'s own reason
            // for the same flag one axis over.
            isFillViewport = true
            // WEIGHT AND NOT `MATCH`: the header keeps its measured height and the body takes
            // whatever is left, which is what makes the header the part that does not scroll.
            layoutParams = LinearLayout.LayoutParams(MATCH, 0, 1f)
            addView(
                well.scrolledHorizontally().apply {
                    // `ScrollView` IS A `FrameLayout`, and its layout pass casts a child's params
                    // to its OWN. `scrolledHorizontally` hands back a view carrying
                    // `LinearLayout.LayoutParams` for the column it usually sits in, and that
                    // would be a `ClassCastException` the first time this screen was laid out --
                    // a fault Robolectric's measure/layout surfaces as loudly as a device does.
                    layoutParams = FrameLayout.LayoutParams(MATCH, WRAP)
                }.screenAir(),
            )
        },
    )
    return column
}

/**
 * Where the drill chevron goes. `terminalFallbackView`'s own word, and the app's only one.
 */
private const val BACK = "Back"

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
