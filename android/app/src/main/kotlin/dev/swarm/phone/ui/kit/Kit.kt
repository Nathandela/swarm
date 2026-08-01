package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import androidx.annotation.ColorRes
import androidx.annotation.DimenRes
import dev.swarm.phone.R
import kotlin.math.roundToInt

/**
 * PB-DS-6: the component kit's foundation -- the only place the kit reads the design system from.
 *
 * "Every visual element is one factory in a single package, STYLED ENTIRELY FROM THE THEME; a
 *  screen composes components and passes data."
 *
 * Every colour is an `R.color.swarm_*` that `android/design-tokens.tsv` joins to the token origin,
 * every spacing step an `R.dimen.swarm_space_*` off PB-DS-1's grid, every text role a
 * `TextAppearance.Swarm.*` off PB-DS-2's scale. Nothing in this package reads a colour or a size
 * from anywhere else, and `android/gate/s23_kit_test.go` fences that: a hex literal, a `Typeface.`
 * reference or a `setTextSize` call in any kit file fails the build.
 *
 * WHAT [KitMetrics] IS FOR, since the sentence above says everything comes from the theme. Six
 * numbers the design states cannot be resources: a 7 dp status dot and a 9 dp glow radius are not
 * spacing (a 2 dp grid has nothing to say about a dot's diameter) and not radii, and `--p-card-fx`
 * and `--p-workbar` are declared `effect` in `tokens.json`, so PB-TOK-6's converters produce no
 * `<color>` or `<dimen>` for them at all. They are named constants carrying a
 * machine-read `origin:` annotation, and the Go gate recomputes every one of them from the design
 * source. A constant here with no annotation fails rather than being skipped -- which is the
 * difference between a small checked set and somewhere to put numbers.
 */
internal object Kit {

    fun colour(context: Context, @ColorRes id: Int): Int = context.getColor(id)

    fun dimen(context: Context, @DimenRes id: Int): Float = context.resources.getDimension(id)

    /**
     * A scale step as a whole number of pixels -- a padding, a margin, a laid-out size.
     *
     * IT IS NOT `dimen(...).toInt()`, AND THE DIFFERENCE IS A PIXEL WHEREVER DENSITY IS
     * FRACTIONAL. A cast truncates; `getDimensionPixelSize` rounds half away from zero, which is
     * what the platform itself does everywhere a dimension becomes a pixel -- `View.setPadding`
     * from XML, `TextView`'s own text size, every `getDimensionPixelSize` in the framework. On a
     * 2.625x handset a 4 dp step is 10.5 px: this kit spent 10 where the platform spends 11, on
     * every gap, in one direction, invisibly. One call rather than an arithmetic rule of our own,
     * because the platform's rule is the whole specification.
     */
    fun dimenPx(context: Context, @DimenRes id: Int): Int =
        context.resources.getDimensionPixelSize(id)

    /** Design px is Android dp at 1:1 -- the artifact is a 386x812 frame at device scale. */
    fun dp(context: Context, value: Float): Float = value * context.resources.displayMetrics.density

    /**
     * A [KitMetrics] constant as a whole number of pixels, quantised the way [dimenPx] is.
     *
     * The six numbers the resource table cannot carry are spent at the same call sites as the
     * steps that can -- a dot's size beside a row's padding -- so they have to reach the layout
     * through the same arithmetic. `roundToInt` is `getDimensionPixelSize`'s rule for the positive
     * lengths a design states.
     */
    fun dpPx(context: Context, value: Float): Int = dp(context, value).roundToInt()

    /**
     * PB-TOK-8 / ADR-007 B134 decision 1: the colour a `status.Group` IS.
     *
     * The Group arrives as `swarmmobile.Session.Group`, which is `internal/status.Group`'s string
     * form. It is derived ONCE, on the server; the phone renders it verbatim and never re-derives
     * it, so this is a lookup and not a decision.
     */
    fun groupColour(context: Context, group: String): Int = colour(context, groupColourRes(group))

    /**
     * The Group's glow, or null where the design declares none.
     *
     * "Nothing glows unless it is alive", and exactly two of the four Groups are: NeedsInput is
     * blocked on the human and Working is computing. `.pdot.ok` sets `box-shadow: none` explicitly
     * and `--p-ink3` has no `.pdot` rule at all -- ReadyForReview is finished work waiting to be
     * looked at, and Completed is finished.
     *
     * The value is BLENDED rather than read: it is `color-mix(--p-att 70%, transparent)`, which is
     * a function of a token and therefore has no resource and may not be typed (PB-TOK-7).
     */
    fun groupGlow(context: Context, group: String): Int? = glowShare(group)?.let { share ->
        ColorMix.mix(groupColour(context, group), share, ColorMix.TRANSPARENT)
    }

    /**
     * `.prow.attention`'s border: `color-mix(in srgb, --p-att 36%, --p-hair)`.
     *
     * An opaque mix rather than an alpha, so it composites identically over any surface -- the
     * same recipe the derivation table reuses for the kill-switch panel with `--p-err` substituted.
     */
    fun attentionBorder(context: Context): Int = ColorMix.mix(
        colour(context, R.color.swarm_state_attention),
        ATTENTION_BORDER_SHARE,
        colour(context, R.color.swarm_hairline),
    )

    /**
     * `--p-att`'s share of `.prow.attention`'s border, over `--p-hair`.
     *
     * origin: derivation attention-row-border
     */
    private const val ATTENTION_BORDER_SHARE = 0.36f

    /**
     * THE REBINDING. Substrate's demo phone labels the GREEN dot "Done"; B134 moves green to
     * ReadyForReview and gives Completed the recessive grey, because finished work should recede
     * on a triage surface rather than hold the most saturated colour on screen. Reading this off
     * the artifact gives the wrong answer, which is why `android/group-tokens.tsv` is the
     * authority and the gate joins this table to it in both directions.
     */
    private fun groupColourRes(group: String): Int = when (group) {
        "needs_input" -> R.color.swarm_state_attention
        "working" -> R.color.swarm_state_working
        "ready_for_review" -> R.color.swarm_state_ok
        "completed" -> R.color.swarm_text_tertiary
        else -> error(
            "PB-TOK-8: $group is not a status.Group this kit can colour. The phone renders the " +
                "server's Group verbatim; a Group with no colour is a whole inbox section with " +
                "no state, so this fails loudly rather than painting a default.",
        )
    }

    /**
     * The share of its own colour a live Group's glow carries. Null means the design has none.
     *
     * THE TWO NUMBERS ARE NAMED RATHER THAN INLINE, and the reason is a gate rather than taste: a
     * literal in a `when` branch could only be checked by a regexp that recognised the branch's
     * syntax, and a fence that depends on the shape of an expression stops matching the first
     * time the expression is rewritten -- silently, and while still passing. Behind a name each
     * share carries an `origin:` annotation, which is the same join every other number in this
     * package answers to.
     */
    private fun glowShare(group: String): Float? = when (group) {
        "needs_input" -> NEEDS_INPUT_GLOW_SHARE
        "working" -> WORKING_GLOW_SHARE
        else -> null
    }

    /** origin: derivation needs-input-dot-glow */
    private const val NEEDS_INPUT_GLOW_SHARE = 0.70f

    /** origin: derivation working-dot-glow */
    private const val WORKING_GLOW_SHARE = 0.55f
}

/**
 * The numbers the resource table cannot carry. Every one is recomputed from the design source by
 * `android/gate/s23_kit_test.go`; the `origin:` line above each is the join, and it is machine-read.
 */
internal object KitMetrics {
    /** origin: .pdot { width } */
    const val DOT_DP = 7f

    /** origin: .pdot.att { box-shadow } */
    const val GLOW_RADIUS_DP = 9f

    /** origin: .chip .pd { width } */
    const val PRESENCE_DOT_DP = 5f

    /** origin: .prow.attention::before { width } */
    const val RAIL_DP = 2f

    /** origin: .workbar { height } */
    const val WORKBAR_HEIGHT_DP = 3f

    /** origin: .workbar { border-radius } */
    const val WORKBAR_RADIUS_DP = 2f

    /**
     * Every `1px solid` in the design. Android's minimum stroke is 1 dp and there is no sub-dp
     * form worth having, so the artifact's four 0.5 px rules double -- partly offset by colour,
     * because `--p-hair` is darker than what the retired mock composited to.
     *
     * origin: .prow { border }
     */
    const val HAIRLINE_DP = 1f

    /** origin: .ptabs svg { width } */
    const val TAB_ICON_DP = 22f

    /** origin: --p-card-fx px */
    const val KEY_LIGHT_DP = 1f

    /** origin: --p-card-fx alpha */
    const val KEY_LIGHT_ALPHA = 0.045f

    /** origin: --p-workbar stop */
    const val WORKBAR_FADE_STOP = 0.85f

    /**
     * The badge's box. `--p-chip-r` is 8 dp, so a 16 dp box renders a pill -- the derivation is
     * the row's, and the pill is a consequence rather than a fifth radius.
     *
     * derived: docs/design/substrate-components.md #3 Badge { height }
     */
    const val BADGE_HEIGHT_DP = 16f
}

/**
 * The design selector each part of a component renders, carried as a `View.tag`.
 *
 * Android has no stable child identity without an `@id`; `res/values/ids.xml` is S22's file and
 * closed, and `View.generateViewId` is not stable across instances. Tagging each part with the CSS
 * rule it renders gives the appearance suite a subject to find AND says, in the component itself,
 * which rule that view answers to.
 */
internal object KitTag {
    const val PROJECT = ".prow .pj"
    const val AGENT = ".prow .ag"
    const val NEED = ".prow .ln"
    const val DOT = ".pdot"
    const val WORKBAR = ".workbar"
    const val LINE = ".prow .t"
    const val TITLE = ".pnav .big"
    const val LIVE = ".pnav .live"
    const val TAB_LABEL = ".ptabs div"
    const val TAB_ICON = ".ptabs svg"
    const val BADGE = "badge"

    /**
     * The settings row's three parts.
     *
     * They name the PART and not a CSS rule, because Substrate declares none -- `.setrow` is the
     * retired mock's class, and a tag naming it would point a reader at a rule that does not
     * exist. [BADGE] is the precedent: a derived component's tag says what it is.
     *
     * They do not cite "#15" either, and the literal-accounting fence is why. It blanks string
     * contents before counting numbers, so a digit inside one is a number no fence can see --
     * `("1" + "1").toFloat()` is 11f that nothing reads. A row number in a tag is harmless and
     * indistinguishable from a metric hidden in copy, so the fence refuses both and it is right to.
     */
    const val SETTINGS_LABEL = "settings label"
    const val SETTINGS_SUBLABEL = "settings sublabel"
    const val SETTINGS_STATUS = "settings status"
}

/**
 * A `LinearLayout` that puts the design's `gap` BETWEEN its children and nowhere else.
 *
 * CSS `gap` has no Android equivalent and the obvious substitute is wrong in a way that shows: a
 * per-child margin puts a gap outside the first and last items too, so a list inherits a leading
 * indent its container never asked for and the design's own side padding is silently doubled at
 * one edge. Applying it on add, to every child but the first, is what `gap` means.
 */
internal class KitStack(
    context: Context,
    stackOrientation: Int,
    private val gapPx: Int,
) : LinearLayout(context) {

    init {
        orientation = stackOrientation
    }

    override fun onViewAdded(child: View) {
        super.onViewAdded(child)
        val params = child.layoutParams as? MarginLayoutParams ?: return
        val gap = if (indexOfChild(child) == 0) 0 else gapPx
        if (orientation == VERTICAL) {
            params.topMargin = gap
        } else {
            params.marginStart = gap
        }
        child.layoutParams = params
    }
}

internal const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
internal const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
