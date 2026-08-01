package dev.swarm.phone.ui.kit

import android.content.Context
import android.content.res.ColorStateList
import android.graphics.drawable.Drawable
import android.view.Gravity
import android.view.View
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/** One tab. The icon is the caller's asset; everything else about it is the design's. */
data class TabItem(
    val label: CharSequence,
    val icon: Drawable? = null,
    val selected: Boolean = false,
    /** Sessions in `needs_input`. Zero means no badge at all, not a badge reading "0". */
    val badgeCount: Int = 0,
    val badgeDescription: CharSequence? = null,
)

/**
 * origin: .ptabs
 *
 * The bottom bar: four tabs, a 1 dp hairline along the top, and `--p-tabbg` behind them.
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
    // `padding-bottom: 14px` is the home-indicator inset in a 386x812 mock. On a handset the real
    // one comes from WindowInsets and belongs to the screen scaffold (S24); this is the design's
    // value, which is the right default and the right preview.
    setPaddingRelative(0, 0, 0, Kit.dimenPx(context, R.dimen.swarm_space_14))
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
            setImageDrawable(item.icon)
            // `stroke: currentColor` -- the glyph is the item's ink, selected or not.
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
        addView(iconFrame)
        addView(
            TextView(context).apply {
                setTextAppearance(R.style.TextAppearance_Swarm_Label_Tab)
                setTextColor(ink)
                text = item.label
                gravity = Gravity.CENTER
                layoutParams = LinearLayout.LayoutParams(WRAP, WRAP).apply {
                    topMargin = Kit.dimenPx(context, R.dimen.swarm_space_4)
                }
                tag = KitTag.TAB_LABEL
            },
        )
    }
}
