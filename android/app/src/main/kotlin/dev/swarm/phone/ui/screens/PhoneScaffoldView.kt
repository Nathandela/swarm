package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import dev.swarm.phone.ui.kit.TabItem
import dev.swarm.phone.ui.kit.tabBar

/**
 * PB-DS-9: the window's scaffold -- one destination, above one tab bar.
 *
 * WHY IT EXISTS, WHICH IS A STRUCTURAL FACT AND NOT A PREFERENCE. `tabBar` was composed inside
 * [triageInboxView], and [machinesPanelView] and [activityPanelView] compose none -- so a tab that
 * merely swapped the screen would land the user on Machines with no bar to come back with. A bar
 * that belongs to one of four destinations is a bar the other three do not have. Lifting it out is
 * what makes inventory C1.4 a navigation control instead of a picture of one, and it is what gave
 * the machines and activity screens their first production call site: both were built, composed
 * from the kit, covered by their own suites, and reachable from nothing.
 *
 * IT IS DERIVATION ROW 20, and the row is why this file spends no dimension. The screen scaffold's
 * padding is "top `screen_top` (or the real inset), bottom `screen_bottom` (or inset +
 * `tabbar_height`)" -- the platform inset UNDER a bar that is `tabbar_height` tall, not inside it.
 * Each of those three lengths is somebody else's already: the insets are
 * `PhoneActivity.insetTheSystemBars`, which is the only place in this app that reads
 * `WindowInsets` (row 19: an iPhone frame constant yields to the platform's own measurement), and
 * the bar's box is `tabbar_height`, which `tabBar` spends. What is left here is the arrangement --
 * a weighted column and a scroll -- which is layout rather than appearance, and is all
 * `android/gate/s24_screens_test.go` leaves a screen.
 *
 * THE SCROLL IS THE SCAFFOLD'S AND IT USED TO BE THE INBOX'S. Row 20 gives `.pscreen` the vertical
 * scroll and `scrollbar-width: none`; the inbox carried both because it was the only screen there
 * was. Here it serves all four, which is what lets [machinesPanelView], [activityPanelView] and
 * the settings panel be the wrap-height columns they already are -- none of them composes a scroll,
 * and without one they would be cut off at the fold on a long journal or a small handset.
 */
object ScaffoldTag {
    /** Row 20's `.pscreen` -- whichever destination is on screen, and its scroll. */
    const val CONTENT = "scaffold.content"

    /** C1.4 `.ptabs`. It was `InboxTag.TABS` while the bar was the inbox's. */
    const val TABS = "scaffold.tabs"

    /**
     * The parts whose ON-SCREEN ORDER is the recorded composition: the content, then the bar. A
     * tab bar that scrolled with the content would be a different screen, which is the assertion
     * `PhoneScaffoldViewTest` inherited from the inbox's suite along with the bar.
     */
    val COMPOSITION: Set<String> = setOf(CONTENT, TABS)
}

/**
 * Where a tab goes. Inventory C1.4: `Inbox` (on) - `Machines` - `Activity` - `Settings`.
 *
 * THE LABEL IS THE JOIN AND IT IS CHECKED RATHER THAN ASSUMED. `TriageInboxScreen` owns the four
 * tab labels as recorded copy, and this enum is the four places they lead; the two are separate
 * because one is copy and the other is navigation, and they are joined by [forLabel], which
 * REFUSES a label it cannot place. A weak key is right here for `TabBar`'s own recorded reason --
 * the alternative is an identity on every tab that each call site sets, which is the arrangement
 * that shipped four tabs with nothing behind them -- and the refusal is what stops the two copies
 * drifting silently: a fifth tab, or a renamed one, fails loudly at the first composition instead
 * of rendering a tab that quietly navigates nowhere.
 */
enum class Destination(val label: String) {
    INBOX("Inbox"),
    MACHINES("Machines"),
    ACTIVITY("Activity"),
    SETTINGS("Settings"),
    ;

    companion object {
        /**
         * @throws IllegalStateException on a tab this app cannot navigate to. LOUD, for the reason
         *  `TriageInboxScreen.headingOf` is loud about a Group with no heading: a tab rendered
         *  without a destination is indistinguishable, on screen, from one whose destination has
         *  not been built yet.
         */
        fun forLabel(label: String): Destination = checkNotNull(entries.find { it.label == label }) {
            "PB-DS-9: the tab bar draws a tab labelled \"$label\" and there is no destination for " +
                "it. Inventory C1.4's four tabs are this enum; a fifth would render as a control " +
                "that goes nowhere."
        }
    }
}

/**
 * The scaffold as a view.
 *
 * @param content the destination on screen. It is a view rather than a `when` over [destination]
 *  because what each destination shows is the SURFACE's business -- the inbox needs its callbacks,
 *  the activity screen needs a journal page, and the settings panel is a live object that redraws
 *  itself -- and a scaffold that built them would need the facade.
 * @param tabs the four tabs as `InboxScreen` records them: the labels, and the NeedsInput badge
 *  the inbox counts (derivation table 1.4). Their [InboxTab.selected] is not read here --
 *  see [destination].
 * @param destination which of the four is on screen. IT IS THE PARAMETER AND `InboxTab.selected`
 *  IS NOT, because selection is a fact about navigation and that model was written when the inbox
 *  was the only screen: it answers `label == "Inbox"` for every tab list it builds, which on the
 *  other three destinations would tell a user standing on Machines that they are in the Inbox.
 * @param onSelectDestination the destination a tapped tab names.
 */
fun phoneScaffoldView(
    context: Context,
    content: View,
    tabs: List<InboxTab>,
    destination: Destination,
    onSelectDestination: (Destination) -> Unit,
): View {
    val scroll = ScrollView(context).apply {
        tag = ScaffoldTag.CONTENT
        // The content is shorter than the screen on a quiet inbox, and without this the tab bar
        // would ride up under the last section instead of sitting at the bottom.
        isFillViewport = true
        // A glowing dot is inflated past its own bounds and the tab badge overhangs its icon, so
        // every container between them and the window has to be told not to clip. Necessary at
        // each level: a parent that clips undoes what its child allowed.
        clipChildren = false
        clipToPadding = false
        // `scrollbar-width: none` (derivation row 20).
        isVerticalScrollBarEnabled = false
        // Weight 1: the destination takes whatever is left after the fixed bar below it.
        layoutParams = LinearLayout.LayoutParams(MATCH, 0, 1f)
        addView(content)
    }

    return LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
        addView(scroll)
        addView(
            tabBar(
                context,
                tabs.map { tab ->
                    val target = Destination.forLabel(tab.label)
                    TabItem(
                        label = tab.label,
                        // NO ICON OVERRIDE. `TabItem.icon` overrides the design's own glyph rather
                        // than supplying it, and the kit finds each tab's own by its label.
                        selected = target == destination,
                        badgeCount = tab.badgeCount,
                        badgeDescription = tab.badgeDescription,
                        onTap = { onSelectDestination(target) },
                    )
                },
            ).apply { tag = ScaffoldTag.TABS },
        )
    }
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
