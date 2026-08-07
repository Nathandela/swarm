package dev.swarm.phone.ui.kit

import android.content.Context
import android.text.SpannableStringBuilder
import android.text.Spanned
import android.text.style.ForegroundColorSpan
import android.text.style.TextAppearanceSpan
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
     * Derivation row 12's panel border: `color-mix(in srgb, --p-err 36%, --p-hair)`.
     *
     * **IT SPENDS THE ATTENTION ROW'S SHARE, WHICH IS THE WHOLE OF ROW 12'S CELL.** The row does not
     * declare a number of its own -- its words are "Substrate's own `.prow.attention` border recipe
     * with `--p-err` substituted" -- so the RECIPE is the 36% and the substitution is the base. A
     * second constant here would be a second copy of one share, needing a sixth entry in
     * `internal/design.Derivations()` to be checkable and drifting from the first the day either
     * moves. This is the one place in the kit where two components share a derivation, and it is
     * shared because the design says they are the same derivation.
     *
     * An opaque mix rather than an alpha, for [attentionBorder]'s reason and one of row 12's own:
     * the panel sits on the ground here and would composite differently anywhere else.
     */
    fun killSwitchBorder(context: Context): Int = ColorMix.mix(
        colour(context, R.color.swarm_state_error),
        ATTENTION_BORDER_SHARE,
        colour(context, R.color.swarm_hairline),
    )

    /**
     * `.a2-no`'s fill: `color-mix(in srgb, --p-err 13%, transparent)`.
     *
     * A TINT OVER WHATEVER IS BEHIND IT, which is why it keeps its alpha rather than being flattened
     * over a surface. The deny button sits on the sheet in one place and inside an approval card in
     * another, and 13% of `--p-err` composited over `--p-elev` is not the same colour as 13% over
     * `--p-card` -- resolving it against either would ship the wrong one at the other site.
     */
    fun denyFill(context: Context): Int = ColorMix.mix(
        colour(context, R.color.swarm_state_error),
        DENY_FILL_SHARE,
        ColorMix.TRANSPARENT,
    )

    /**
     * `--p-err`'s share of the deny button's fill, over transparent.
     *
     * origin: derivation deny-fill
     */
    private const val DENY_FILL_SHARE = 0.13f

    /**
     * The toggle's OFF track: `color-mix(in srgb, --p-ink3 40%, transparent)`.
     *
     * THE FIRST DERIVED COLOUR IN THIS KIT THAT SUBSTRATE DID NOT DRAW. The other four are
     * `color-mix()` calls the artifact's own CSS makes; this one comes from
     * `docs/design/substrate-components.md` row 4, for a component the artifact declares no rule
     * for at all. It is consumed from `internal/design.Derivations()` like the rest rather than
     * being resolved into a literal here, which is the whole of PB-TOK-7: a derived colour typed
     * once is a copy of the palette that the token join is structurally blind to, because the
     * resolved value is not any token's value and no row would ever have named it.
     */
    fun toggleTrackOff(context: Context): Int = ColorMix.mix(
        colour(context, R.color.swarm_text_tertiary),
        TOGGLE_TRACK_OFF_SHARE,
        ColorMix.TRANSPARENT,
    )

    /**
     * `--p-ink3`'s share of the toggle's off track, over transparent.
     *
     * origin: derivation toggle-track-off
     */
    private const val TOGGLE_TRACK_OFF_SHARE = 0.40f

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

    /**
     * Give a view row 23's ring and make it something the ring can reach.
     *
     * **A RING NOTHING CAN FOCUS IS A RING THAT NEVER DRAWS**, and joining the two halves into one
     * call is what makes that unbuildable: a component cannot acquire the paint without acquiring
     * the state that shows it, and cannot become focusable without acquiring the ring PB-DS-12
     * requires of every focusable. Row 23's clause is "applies to every focusable" in both
     * directions, and this is that sentence as a function.
     *
     * `isFocusableInTouchMode` IS DELIBERATELY NOT SET, and the row's own selector is why. Row 23
     * cites `:focus-visible`, not `:focus` -- the ring is for the user traversing with a keyboard,
     * a D-pad or switch access, and a control that took focus on touch would leave a champagne ring
     * behind every tap. Android's touch-mode rule produces exactly the pseudo-class's behaviour for
     * free, so what would look like the more thorough call is the wrong one.
     *
     * IT IS HERE AND NOT A TOP-LEVEL FUNCTION for [emphasised]'s reason: every top-level `fun` in
     * this package is read as a component factory by `android/gate/s23_kit_test.go`, and this is a
     * treatment applied to one rather than a thing on screen.
     *
     * @param componentRadiusPx the radius of the surface being surrounded, so the ring is
     *  concentric with it. A control with no surface of its own passes 0.
     */
    fun focusable(view: View, componentRadiusPx: Float) {
        view.foreground = focusRing(view.context, componentRadiusPx)
        view.isFocusable = true
    }

    /**
     * [text] with `.prow .ln b` applied over [span], or [text] unchanged when there is none.
     *
     * IT IS HERE BECAUSE TWO COMPONENTS NEED THE SAME SPAN. Row 14's activity row marks an inline
     * identifier inside its body and row 12's panel marks an inline command inside its subtitle,
     * and both cells are the same one: `Mono.InlineStrong` / `--p-ink` inside a sans line. A second
     * private copy is how the two would drift, and a top-level helper cannot live in this package
     * -- `TestPBDS6_EveryKitFactoryIsAnInboxComponent` reads every top-level `fun` as a component
     * and refuses one the inventory does not name, which is what puts a shared helper inside this
     * object.
     *
     * TWO SPANS AND NOT ONE, even though the ink span is the colour the body already carries. No
     * text appearance in this app holds a colour -- Substrate binds one style to several inks --
     * so the appearance carries the family, the size and the weight, and the ink is stated beside
     * it. Without the second span the emphasis would inherit whatever the line is painted, and a
     * later change to one cell would silently move the other.
     *
     * @param span must OCCUR in [text]. A caller naming a fragment its own sentence does not
     *  contain has a copy bug, and this fails loudly rather than rendering the line unmarked --
     *  which is the failure nobody would see.
     */
    fun emphasised(context: Context, text: CharSequence, span: CharSequence?): CharSequence {
        if (span == null) return text
        val start = text.toString().indexOf(span.toString())
        if (start < 0) {
            error(
                "`$span` is not in `$text`, so a component was asked to emphasise a fragment its " +
                    "own sentence does not contain. This fails loudly rather than dropping the " +
                    "emphasis, which would render a correct-looking line that had quietly lost " +
                    "the one part the design puts the eye on.",
            )
        }
        return SpannableStringBuilder(text).apply {
            setSpan(
                TextAppearanceSpan(context, R.style.TextAppearance_Swarm_Mono_InlineStrong),
                start,
                start + span.length,
                Spanned.SPAN_EXCLUSIVE_EXCLUSIVE,
            )
            setSpan(
                ForegroundColorSpan(colour(context, R.color.swarm_text_primary)),
                start,
                start + span.length,
                Spanned.SPAN_EXCLUSIVE_EXCLUSIVE,
            )
        }
    }
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
    const val KEY_LIGHT_ALPHA = 0.10f

    /**
     * The PROMOTED slab's key light: `--p-lit-fx` is `inset 0 1px 0 rgba(246,243,236,0.22)`.
     *
     * A SECOND ALPHA AND NOT A MULTIPLE OF THE FIRST. `0.22` is not `0.10` scaled by anything
     * meaningful, and writing it as one would make the promoted edge move whenever the resting one
     * did -- which is the opposite of what ADR-009 D4 asks for, since the whole statement is that
     * the two surfaces catch DIFFERENT amounts of the same light. `--p-lit-fx` is its own token
     * for that reason, and this is its alpha.
     *
     * THE RGB IS NOT HERE, exactly as [CTA_BLOOM_ALPHA]'s is not: `rgba(246,243,236, ...)` is
     * `--p-ink` to the digit, so the promoted edge is the linen resource at this alpha rather than
     * a second place the ink is written down. Both surfaces resolve it from `swarm_text_primary`.
     *
     * `effect` in tokens.json, so PB-TOK-6's converters produce no `<color>` and no `<dimen>` for
     * it -- the position every one of these constants is in, and the reason this object exists.
     *
     * origin: --p-lit-fx alpha
     */
    const val LIT_KEY_LIGHT_ALPHA = 0.22f

    /** origin: --p-workbar stop */
    const val WORKBAR_FADE_STOP = 0.85f

    /**
     * The grain's opacity: `--p-grain` is the bare fraction `0.04`.
     *
     * IT IS THE ONE TOKEN IN THE ORIGIN WHOSE WHOLE VALUE IS A NUMBER, which is why the join reads
     * it as `opacity` rather than as `alpha` or `stop`. Those two read a number OUT of a larger
     * value -- an alpha inside an `rgba()`, a stop inside a gradient -- and this token has no
     * larger value to read it out of. `effect`-typed, so PB-TOK-6's converters produce nothing for
     * it and the kit has to carry it as a constant; the Go gate recomputes it from the origin.
     *
     * origin: --p-grain opacity
     */
    const val GRAIN_ALPHA = 0.04f

    /**
     * The badge's box. `--p-chip-r` is 8 dp, so a 16 dp box renders a pill -- the derivation is
     * the row's, and the pill is a consequence rather than a fifth radius.
     *
     * derived: docs/design/substrate-components.md #3 Badge { height }
     */
    const val BADGE_HEIGHT_DP = 16f

    /**
     * PB-DS-12's floor: the smallest a control may be to be hit reliably.
     *
     * SIX ROWS STATE IT AND NONE OF THEM STATES ANYTHING ELSE, which is why one constant serves all
     * of them and why the gate compares every stating row against this one value rather than giving
     * each component a constant of its own. Rows 4, 9, 13, 15 and 22 and §4's drill-down header all
     * write 48, in four different spellings -- `>=48`, `touch target 48`, `48 dp target`, `min 48`
     * -- and a floor that differed between two controls would not be a floor.
     *
     * IT IS CITED TO ROW 4 BECAUSE THAT ROW STATES THE HARD CASE: ">=48 with the visual unchanged".
     * A target is not a size -- growing a control until it is 48 dp satisfies the number and loses
     * the drawing -- and the row that says so is the one worth following the citation to.
     *
     * derived: docs/design/substrate-components.md #4 Toggle { min-target }
     */
    const val MIN_TARGET_DP = 48f

    /**
     * Row 23's focus ring: a 2 dp stroke.
     *
     * IT IS NOT `HAIRLINE_DP` AND NOT `RAIL_DP`, both of which this kit already has at 1 dp and
     * 2 dp. A hairline is structure, a rail is "this row needs you", and a focus ring is neither --
     * three values that would be one constant only until one of them moved. Row 23 states this one.
     *
     * derived: docs/design/substrate-components.md #23 Focus ring { stroke }
     */
    const val FOCUS_RING_DP = 2f

    /**
     * The field's PAINTED height, which row 9 states beside its target and separately from it:
     * "field padding `space_8` x `space_14`, visual height 36, touch target 48".
     *
     * IT EXISTS BECAUSE THE TWO NUMBERS DISAGREE ON PURPOSE. Every other control in this kit is its
     * own target; this one is a 36 dp well inside a 48 dp hit box, so the difference has to be a
     * value rather than a consequence -- and both halves of it are the row's, which is what stops
     * the well from being a height somebody liked.
     *
     * derived: docs/design/substrate-components.md #9 Composer { height }
     */
    const val WELL_HEIGHT_DP = 36f

    /**
     * The toggle's thumb, which is also what its track is built out of.
     *
     * ROW 4 STATES FIVE NUMBERS AND ONLY TWO OF THEM ARE LEAVES. `track 46x28` is
     * `thumb + travel + inset + inset` by `thumb + inset + inset`, and the inset is `space_2` off
     * PB-DS-1's grid -- so the track is arithmetic over this constant, [TOGGLE_TRAVEL_DP] and a
     * resource, and does not need a constant of its own. It could not have one anyway: the
     * derivation-table reader parses `field <number>` and `track 46x28` matches nothing, which is
     * the shape of citation that would have been a value nobody could check.
     *
     * derived: docs/design/substrate-components.md #4 Toggle { thumb }
     */
    const val TOGGLE_THUMB_DP = 24f

    /**
     * How far the toggle's thumb travels between its two rest positions.
     *
     * derived: docs/design/substrate-components.md #4 Toggle { travel }
     */
    const val TOGGLE_TRAVEL_DP = 18f

    /**
     * The CTA's bloom radius: `--p-cta-fx` is `0 0 18px rgba(83, 206, 124, 0.20)`.
     *
     * `effect` in `tokens.json`, so PB-TOK-6's converters produce no `<color>` and no `<dimen>` for
     * it -- the same position `--p-card-fx` and `--p-workbar` are in, and the reason this object
     * exists at all.
     *
     * origin: --p-cta-fx px
     */
    const val CTA_BLOOM_DP = 18f

    /**
     * The CTA bloom's alpha, the other half of the same token.
     *
     * THE RGB IS NOT HERE, and its absence is deliberate. `rgba(83, 206, 124, ...)` is `--p-cta-bg`
     * to the digit, so the bloom is that resource at this alpha rather than a fourth place the
     * phosphor green is written down -- exactly as `--p-card-fx`'s highlight is `Color.WHITE` at
     * [KEY_LIGHT_ALPHA]. The alias is a fact about today's skin: `android/design-tokens.tsv` keeps
     * `--p-cta-bg` on its own row precisely so a future skin can break it, and if one does, the
     * appearance suite reads the expected bloom out of the effect token and notices.
     *
     * origin: --p-cta-fx alpha
     */
    const val CTA_BLOOM_ALPHA = 0.22f

    /**
     * The scanner reticle's square: the size the code should look in the shot.
     *
     * IT IS ROW 6's TILE, which is the whole derivation rather than a size that framed well. Row 6
     * draws the pairing symbol at 180x180, and the mark that says "hold the phone so the code fills
     * this" is that same square seen from the other end of the same pairing. The §4 row states it,
     * because row 6 specifies the tile and says nothing about a viewfinder.
     *
     * A FIXED LENGTH RATHER THAN A FRACTION OF THE PREVIEW, and that is the point of it: the
     * preview is the screen's width, so a fraction would ask the user to hold the phone at a
     * different distance on a different handset. What the reticle actually communicates is an
     * absolute size, and it is clamped rather than scaled when the preview is the smaller of the
     * two -- see [scanReticle].
     *
     * derived: docs/design/substrate-components.md §4 Scanner reticle { frame }
     */
    const val RETICLE_FRAME_DP = 180f

    /**
     * How far each of the reticle's brackets runs from its corner, along both edges.
     *
     * IT IS WHAT MAKES THE FRAME OPEN. Four corners are an aiming mark and a closed rectangle is a
     * border -- the §4 row says so, and this constant is the number that difference is made of.
     *
     * derived: docs/design/substrate-components.md §4 Scanner reticle { arm }
     */
    const val RETICLE_ARM_DP = 24f

    /**
     * How long a toast stays on screen, in milliseconds.
     *
     * **IT IS THE ONE CONSTANT IN THIS OBJECT THAT IS NOT A LENGTH, and it is here for the same
     * reason every length is: the resource table cannot carry it.** There is no `integers.xml` in
     * this app and a duration is not a `<dimen>`, so a toast's lifetime typed at its own call site
     * would be the only number in the kit with no design behind it. Row 1 states it -- "3200 ms
     * then hidden, no transition" -- and the Go gate reads that cell.
     *
     * **IT IS NOT A MOTION CONSTANT AND MUST NOT MOVE TO Motion.kt.** ADR-007 B134 keeps three
     * animations (the sheet, the banner, the caret) and row 1 spends none of them: the toast
     * appears and disappears instantly. What this measures is how long a message is READABLE,
     * which is why §8.7 can say in the same breath that the announcement must outlast it -- a
     * duration nobody interpolates over is not an animation, and putting it beside `NAV_EASE`
     * would invite one.
     *
     * derived: docs/design/substrate-components.md #1 Toast { ms }
     */
    const val TOAST_LIFETIME_MS = 3200L

    /**
     * The reticle's stroke.
     *
     * IT IS NOT [HAIRLINE_DP] AND NOT [RAIL_DP] AND NOT [FOCUS_RING_DP], which is the same argument
     * the focus ring already makes one constant up: this kit now has four values that are 1 dp or
     * 2 dp and they are four because they would be one only until one of them moved. What is
     * particular to this one is what sits behind it -- a moving photograph rather than a flat
     * surface -- which is why a hairline is not enough and the §4 row states the rail's weight.
     *
     * derived: docs/design/substrate-components.md §4 Scanner reticle { stroke }
     */
    const val RETICLE_STROKE_DP = 2f
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

    /**
     * The machine mark, named for the PART rather than for `.pdot`.
     *
     * It is a SEPARATE TAG FROM [DOT] and not a second name for it, for [DRILL_TITLE]'s reason one
     * component over: the two marks are the same drawable at the same 7 dp and they answer to
     * different authorities -- [DOT] to the four-Group binding `group-tokens.tsv` fences, this one
     * to row 11's `--p-ok` / `--p-ink3` boolean. One tag over both would let a test find either and
     * assert the other's binding, which is exactly the confusion the separate factory exists to
     * prevent.
     */
    const val PRESENCE_DOT = "presence dot"
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

    /** The recessed mono block: a pairing command line, or the terminal peek. */
    const val MONO_WELL = ".sheet2 .cmd"

    /** A single-line input. Named for the part: Substrate draws no composer and no form. */
    const val TEXT_FIELD = "text field"

    /**
     * The toggle's two parts.
     *
     * Named for the PART, like [SETTINGS_LABEL] and for the same reason: the shared Substrate block
     * declares no `.toggle` rule, so a tag naming one would point a reader at a selector that does
     * not exist. Row 4 is the whole specification and it calls them the track and the thumb.
     */
    const val TOGGLE_TRACK = "toggle track"
    const val TOGGLE_THUMB = "toggle thumb"

    /**
     * The drill-down header's two parts.
     *
     * Named for the PART, like [SETTINGS_LABEL] and [TOGGLE_TRACK]: `.navhead` is the retired
     * mock's class and the shared Substrate block declares no drill-down header at all, so a tag
     * naming that selector would point a reader at a rule that does not exist. §4 is the whole
     * specification and it calls them the back control and the title.
     *
     * [DRILL_TITLE] is NOT [TITLE], and the difference is a type style. `.pnav .big` is 27 sp
     * `Display.NavTitle`; §4's is 15.5 sp `Title.Sheet`. One tag over both would let an appearance
     * test find either and assert the other's metrics.
     */
    const val DRILL_BACK = "drill back"
    const val DRILL_TITLE = "drill title"

    /** The centred sentence under a block that cannot be typed into. Row 22's, named for the part. */
    const val READ_ONLY_NOTE = "read-only note"

    /**
     * The machine row's three cells, row 11's.
     *
     * THEY ARE NOT [PROJECT], [AGENT] AND [NEED], even though the three type roles are the same
     * three the session row spends. `.prow .pj` is a SESSION's project and `.mrow .name` is a
     * machine; one tag over both would let an appearance test find a session row when it asked for
     * a machine row, and the two rows carry different padding, a different leading mark and a
     * different authority -- row 11 rather than `.prow`. What the shared type roles say is that
     * `.mrow .eid` and `.prow .ag` are the same CELL, which is the reuse the derivation table
     * argues for; it does not make them the same VIEW.
     */
    const val MACHINE_NAME = "machine name"
    const val MACHINE_ENDPOINT = "machine endpoint"
    const val MACHINE_META = "machine meta"

    /**
     * The kill-switch panel's two cells, row 12's.
     *
     * THERE IS NO TAG FOR THE INLINE COMMAND, for the reason the activity row's emphasis has none:
     * `Mono.InlineStrong` over `swarm remote off` is a SPAN inside [KILL_BODY]'s text rather than a
     * view, so it has no `tag` to carry and what finds it is the span range on that text.
     *
     * AND NO TAG FOR A TRAILING CONTROL, because row 12 as amended has none: the kill switch is
     * read-only by design and a toggle there would be a control that cannot act.
     */
    const val KILL_TITLE = "kill switch title"
    const val KILL_BODY = "kill switch body"

    /** Row 13's Revoke: `.a2-no`'s treatment at `.chip`'s metrics. Named for the part. */
    const val DENY_CHIP = "deny chip"

    /**
     * The activity row's two parts.
     *
     * Named for the PART, like [SETTINGS_LABEL] and [TOGGLE_TRACK]: `.arow` is the retired mock's
     * class and the shared Substrate block declares no activity row at all, so a tag naming that
     * selector would point a reader at a rule that does not exist.
     *
     * THERE IS NO TAG FOR THE EMPHASIS, and that is a fact about what it is rather than an
     * omission. `.ln b` is an inline SPAN inside the body, not a view, so it has no `tag` to carry
     * -- what an appearance test finds it by is the span range on [ACTIVITY_BODY]'s text.
     */
    const val ACTIVITY_TIME = "activity time"
    const val ACTIVITY_BODY = "activity body"

    /**
     * The toast. Named for the PART, like [SETTINGS_LABEL] and [TOGGLE_TRACK]: `.toast` is the
     * retired mock's class and the shared Substrate block declares no rule for it at all, so a tag
     * naming that selector would point a reader at something that does not exist.
     *
     * THERE IS NO TAG FOR THE MONO SUFFIX, for the reason the activity row's emphasis has none:
     * row 1's `Mono.CodeSmall` is a SPAN inside the toast's own text rather than a view, so it has
     * no `tag` to carry and what finds it is the span range on that text.
     */
    const val TOAST = "toast"

    /**
     * The pairing step's two cells, row 18's.
     *
     * Named for the PART, like [SETTINGS_LABEL] and [TOGGLE_TRACK]: `.pair` is row 18's mock class
     * and the shared Substrate block declares no rule for it at all, so a tag naming that selector
     * would point a reader at a rule that does not exist.
     *
     * THERE IS NO TAG FOR THE DETAIL, and that is a fact about what it is. The command well under
     * step 1 is a `monoWell`, which arrives already carrying [MONO_WELL] -- a second tag over it
     * would rename the one component row 18 instructs the pairing scaffold to reuse verbatim.
     */
    const val STEP_ORDINAL = "pairing step ordinal"
    const val STEP_LINE = "pairing step line"
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
