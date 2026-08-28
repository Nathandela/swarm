package dev.swarm.phone.ui.kit

import android.content.Context
import android.content.res.ColorStateList
import android.graphics.drawable.Drawable
import android.view.Gravity
import android.view.View
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * One tab.
 *
 * [icon] OVERRIDES THE DESIGN'S OWN GLYPH RATHER THAN SUPPLYING IT. It used to be the only source
 * of one, and it was null at every call site, so the bar rendered four bare labels -- the four
 * glyphs the artifact draws were "drawables nobody has drawn", and the tab bar shipped without the
 * half of itself that a person recognises at a glance. They are `res/drawable/swarm_tab_*.xml`
 * now, joined path-for-path to the artifact, and [tabGlyph] finds a tab's own by its label because
 * that is the pairing the artifact itself makes: each glyph is inside the div that carries the
 * label. A caller that has a reason to draw something else still can.
 */
data class TabItem(
    val label: CharSequence,
    val icon: Drawable? = null,
    val selected: Boolean = false,
    /** Sessions in `needs_input`. Zero means no badge at all, not a badge reading "0". */
    val badgeCount: Int = 0,
    val badgeDescription: CharSequence? = null,
    /**
     * What pressing this tab does, and WITHOUT IT THE BAR WAS A PICTURE OF A BAR. Every field
     * above describes how a tab looks; none of them made one do anything, so the app shipped four
     * tabs a user could press with nothing behind them -- and two screens that are built, composed
     * from this kit and covered by their own suites (`activityPanelView`, and the since-deleted
     * `machinesPanelView`) had zero production call sites, because nothing could navigate to them.
     *
     * IT IS THE ONLY BEHAVIOUR IN THE KIT AND THAT IS THE COMPONENT'S NATURE rather than an
     * exception being carved out. A tab bar is a CONTROL: `.ptabs` is inventory C1.4 and the
     * destinations are what it is for, so a factory that could not carry the press would leave
     * every caller to find its own tab views and attach listeners by index -- which is the
     * child-index coupling `KitTag` exists to prevent. The destination itself is the screen's:
     * this carries a lambda and knows nothing about where it goes.
     *
     * Null is a tab that does not navigate, which is what a bar drawn for a screenshot is.
     */
    val onTap: (() -> Unit)? = null,
)

/**
 * origin: .ptabs div
 *
 * The artifact's own glyph-to-label pairing, which is the only one there is: `.ptabs` holds four
 * divs and each carries its `<svg>` and its text together.
 *
 * A LABEL IS A WEAK KEY AND IT IS THE RIGHT ONE HERE. The alternative is an identity on [TabItem]
 * that every call site would have to set, which is the arrangement that just shipped four empty
 * icon frames. The screen's tabs are literals the artifact names and the app has no translations,
 * so the miss below is unreachable today; when it stops being unreachable the tab renders as it
 * does now, without its glyph, rather than crashing on a screen a person is holding.
 *
 * **THE TABLE HAS FOUR ENTRIES AND THE APP DRAWS THREE TABS** (agents-tracker-nx44.3). The
 * `Machines` DESTINATION is deleted -- `ui/screens/PhoneScaffoldView.kt`'s [dev.swarm.phone.ui
 * .screens.Destination] carries that argument -- and its pairing stays here because this table is
 * the ARTIFACT's, not the app's: `docs/research/remote-control-design-directions.html` is
 * owner-signed and draws four tabs, and `android/gate/tabbar_test.go` reads that block at test
 * time and requires each glyph it finds to exist as a drawable AND to be bound by name in this
 * file. Deleting the row would fail that join; editing the artifact is forbidden. What the bar
 * renders is decided by the items its caller passes, which is the screen's business, so an unused
 * pairing here costs one map entry and keeps the design join intact.
 */
private val TAB_GLYPHS: Map<String, Int> = mapOf(
    "Inbox" to R.drawable.swarm_tab_inbox,
    "Machines" to R.drawable.swarm_tab_machines,
    "Activity" to R.drawable.swarm_tab_activity,
    "Settings" to R.drawable.swarm_tab_settings,
)

private fun tabGlyph(context: Context, label: CharSequence): Drawable? =
    TAB_GLYPHS[label.toString()]?.let { context.getDrawable(it) }

/**
 * origin: .ptabs
 *
 * The bottom bar: the tabs it is given, a 1 dp hairline along the top, and `--p-tabbg` behind
 * them. (The artifact draws four; this app passes three -- see [TAB_GLYPHS].)
 *
 * `--p-tabbg` IS SPENT AS A RESOURCE, AND IT WAS NOT. The token was typed `effect` in
 * `tokens.json` -- so PB-TOK-6's converters produced no `<color>` and there was nothing to read --
 * and this bar composed the fill instead, as `--p-bg` at the token's own alpha. That joined the
 * ALPHA to the origin and quietly assumed the RGB: `--p-tabbg` and `--p-bg` share a value today,
 * which is the value-alias hazard the `--p-cta-bg` / `--p-hero` assertion exists to catch, one
 * indirection further down. An audit committee found that the `effect` typing was itself a
 * workaround for a parser that could not read `rgba()`, so the token now converts and
 * `R.color.swarm_tabbar_background` is a real resource with a row in `android/design-tokens.tsv`.
 * Reading it is one hop to the origin instead of two-thirds of one.
 *
 * **THE BACKDROP BLUR IS NOT IMPLEMENTED, AND THE OMISSION IS DELIBERATE.** The design pairs
 * `--p-tabbg` with `backdrop-filter: blur(16px)`, and the recorded conversion for it --
 * `RenderEffect.createBlurEffect` -- does not do that: a `RenderEffect` on a View blurs THAT
 * VIEW'S OWN CONTENT, so applying it here would blur the tab labels and icons and leave the
 * content behind the bar perfectly sharp. That is a visible defect rather than an approximation.
 * Android has no view-level backdrop blur at `minSdk 33` (`Window.setBackgroundBlurRadius` blurs
 * behind the whole window, which is the wrong surface). The 88% translucency ships and does most
 * of the work the token was pinned for -- a hero chip or a line of ink scrolling under the bar
 * still shows through as a tint; what is lost is the softening. Recorded here rather than
 * silently dropped, because the next person to read the design will look for it.
 */
fun tabBar(context: Context, items: List<TabItem>): LinearLayout = LinearLayout(context).apply {
    orientation = LinearLayout.HORIZONTAL
    // The badge overhangs its icon's top-right corner; a clipping bar would cut it in half.
    clipChildren = false
    clipToPadding = false
    background = TopRule(
        fill = Kit.colour(context, R.color.swarm_tabbar_background),
        rule = Kit.colour(context, R.color.swarm_hairline),
        // `Kit.dpPx`, NOT `Kit.dp`, AND THE DIFFERENCE IS A THIRD OF THIS LINE. `cardSurface` and
        // `chipSurface` spend the same constant through `dpPx`, which is the platform's own
        // rounding; this bar spent it through `dp`, which is exact. At density 2.625 that is 3 px
        // against 2.625 -- one design value, the 1 dp `--p-hair` rule, rendered two ways on one
        // screen, and a hairline that lands on a fraction of a pixel is antialiased into a smear
        // rather than drawn. Substrate bans drop shadows, so this line is the ONLY thing
        // separating the bar from the content scrolling under it.
        rulePx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP).toFloat(),
    )
    // THE BAR SPENDS NO PADDING OF ITS OWN, and the 14 dp it used to spend was the double this
    // comment used to describe as a default. `.ptabs { padding-bottom: 14px }` reserves the iPhone
    // home indicator INSIDE the bar's own 74 px box; `PhoneActivity.insetTheSystemBars` already
    // pads the whole surface by the real `WindowInsets.systemBars` bottom, which on a handset is
    // roughly 24 dp under gesture navigation and roughly 48 under three buttons. Both were
    // applied, so the bar sat 14 dp above where the design puts it on every device.
    //
    // Derivation row 19 has already ruled on this class of constant: "`screen_top` 54 is an iPhone
    // notch constant -- on Android it must come from `WindowInsets.statusBars`, with 54 as the
    // design-time preview value only. `screen_bottom` 76 is the same problem against the
    // gesture-nav inset." Row 20 says where the bottom one lands: the scaffold's padding is
    // "bottom `screen_bottom` (or inset + `tabbar_height`)" -- the inset UNDER a bar that is
    // `tabbar_height` tall, not inside it. So the platform's measurement replaces the mock's, and
    // the bar keeps exactly the one box the design gives it.
    layoutParams = LinearLayout.LayoutParams(
        MATCH,
        Kit.dimenPx(context, R.dimen.swarm_tabbar_height),
    )
    items.forEach { addView(tab(context, it)) }
}

private fun tab(context: Context, item: TabItem): View {
    val ink = Kit.colour(
        context,
        if (item.selected) R.color.swarm_hero else R.color.swarm_text_tertiary,
    )
    val iconPx = Kit.dpPx(context, KitMetrics.TAB_ICON_DP)

    val iconFrame = FrameLayout(context).apply {
        clipChildren = false
        clipToPadding = false
        layoutParams = LinearLayout.LayoutParams(iconPx, iconPx)
        tag = KitTag.TAB_ICON
    }
    iconFrame.addView(
        ImageView(context).apply {
            setImageDrawable(item.icon ?: tabGlyph(context, item.label))
            // `stroke: currentColor` -- the glyph is the item's ink, selected or not. The drawable
            // carries the platform's white so there is something for the tint to replace.
            imageTintList = ColorStateList.valueOf(ink)
            layoutParams = FrameLayout.LayoutParams(iconPx, iconPx)
        },
    )
    if (item.badgeCount > 0) {
        iconFrame.addView(
            // The description is passed through AS IT ARRIVES, including absent. `?: ""` was the
            // defensive spelling and it is the harmful one: an empty content description is what
            // a decorative view carries, so it asks a screen reader to skip the badge rather than
            // to read the count on it. Absent means "no words of its own", which leaves the count.
            badge(context, item.badgeCount, item.badgeDescription).apply {
                tag = KitTag.BADGE
                layoutParams = FrameLayout.LayoutParams(
                    WRAP,
                    Kit.dpPx(context, KitMetrics.BADGE_HEIGHT_DP),
                    Gravity.END or Gravity.TOP,
                ).apply {
                    // The mock anchors this at `right: 24%`, which moves under font scaling.
                    marginEnd = -Kit.dimenPx(context, R.dimen.swarm_space_6)
                    topMargin = -Kit.dimenPx(context, R.dimen.swarm_space_4)
                }
            },
        )
    }

    return LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        gravity = Gravity.CENTER
        clipChildren = false
        clipToPadding = false
        layoutParams = LinearLayout.LayoutParams(0, MATCH, 1f)
        // THE WHOLE COLUMN IS THE TARGET, not the label inside it. A listener on the words would
        // leave the glyph half of a 74 dp bar dead, and the label is the shorter half; this is
        // also what makes the target the full `tabbar_height` that PB-DS-12's 48 dp floor needs.
        item.onTap?.let { tap -> setOnClickListener { tap() } }
        addView(iconFrame)
        addView(
            Kit.textView(context).apply {
                Kit.appearance(this, R.style.TextAppearance_Swarm_Label_Tab)
                setTextColor(ink)
                text = item.label
                gravity = Gravity.CENTER
                layoutParams = LinearLayout.LayoutParams(WRAP, WRAP).apply {
                    topMargin = Kit.dimenPx(context, R.dimen.swarm_space_4)
                }
                tag = KitTag.TAB_LABEL
                // The bar's height is a fixed `tabbar_height`, so a label that wrapped inside
                // it had nowhere to go.
                Kit.identityCell(this)
            },
        )
    }
}
