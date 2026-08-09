package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import dev.swarm.phone.ui.kit.TabItem
import dev.swarm.phone.ui.kit.grainOverlay
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
     * agents-tracker-e6mi, as replaced by agents-tracker-nx44.2: the app's sync chrome, above the
     * destination.
     *
     * IT IS THE SCAFFOLD'S FOR THE TAB BAR'S OWN REASON. PB-APP-8's offline/reconnecting/stale
     * states and PB-APP-11's freshness verdict were written to a line inside the inbox's column,
     * which the surface detaches on the way to every other destination -- so a link that dropped
     * while the user was on Machines, Activity, Settings or inside a session changed nothing on
     * screen. A warning that belongs to one of four destinations is a warning the other three do
     * not have, which is the sentence this file already spends on the bar.
     *
     * WHAT IT HOLDS CHANGED AND WHERE IT IS DID NOT. It used to be a stack of up to four sentences
     * drawn for every state that had anything at all to say; it is now [syncStatusView] -- the
     * opaque strip a BROKEN link escalates to, and the detail a tap opens -- while the ordinary
     * report moved into the nav row's own pill. The slot is still above the content and still
     * outside its scroll, for the reasons this file has always given.
     */
    const val STATUS = "scaffold.status"

    /**
     * The parts whose ON-SCREEN ORDER is the recorded composition: the status chrome, the content,
     * then the bar. A tab bar that scrolled with the content would be a different screen, which is
     * the assertion `PhoneScaffoldViewTest` inherited from the inbox's suite along with the bar --
     * and a strip UNDER the content is a warning below the fold, which is the same defect at the
     * other end of the column.
     */
    val COMPOSITION: Set<String> = setOf(STATUS, CONTENT, TABS)
}

/**
 * Where a tab goes: `Inbox` (on) - `Activity` - `Settings`.
 *
 * THREE, AND INVENTORY C1.4 DRAWS FOUR (agents-tracker-nx44.3). `Machines` is deleted rather than
 * unwired: everything on that destination was either the four per-channel gap cards or a sentence
 * saying this phone could not read its machine's details, and field test 3 (2026-08-09) is the
 * record of an owner reading it and asking what the page was for. What it was actually for -- which
 * computer am I attached to, and is what I am looking at current -- is the settings screen's
 * CONNECTION section now. The artifact is owner-signed and still draws four tabs, so `TabBar`'s
 * glyph table still binds the fourth (android/gate/tabbar_test.go joins every glyph the artifact
 * draws to a drawable and to that binding); what is gone is the destination behind it.
 *
 * THE LABEL IS THE JOIN AND IT IS CHECKED RATHER THAN ASSUMED. `TriageInboxScreen` owns the tab
 * labels as recorded copy, and this enum is the places they lead; the two are separate because one
 * is copy and the other is navigation, and they are joined by [forLabel], which REFUSES a label it
 * cannot place. A weak key is right here for `TabBar`'s own recorded reason -- the alternative is
 * an identity on every tab that each call site sets, which is the arrangement that shipped four
 * tabs with nothing behind them -- and the refusal is what stops the two copies drifting silently:
 * a tab drawn without a destination fails loudly at the first composition instead of rendering a
 * control that quietly goes nowhere. It is what would fire if the deleted label came back to the
 * bar without a destination behind it.
 */
enum class Destination(val label: String) {
    INBOX("Inbox"),
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
                "it. This enum is the destinations this app has; a tab outside it renders as a " +
                "control that goes nowhere."
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
 * @param status the sync chrome ([syncStatusView]), drawn above [content] and OUTSIDE its scroll,
 *  or null for a caller with nothing to say. It is a view for [content]'s reason -- what it says
 *  changes on the surface's clock and the scaffold is rebuilt on the bar's, so a scaffold that
 *  built one would re-parent the destination under whoever is using it. Being outside the scroll
 *  is the whole of the placement: a strip inside it scrolls away under a long journal, which is
 *  the same disappearance this slot exists to end, in a slower form.
 */
fun phoneScaffoldView(
    context: Context,
    content: View,
    tabs: List<InboxTab>,
    destination: Destination,
    onSelectDestination: (Destination) -> Unit,
    status: View? = null,
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
    // ADR-009 D4.3 as amended 2026-08-08: the grain goes on the SCROLLED CHILD and not on the
    // ScrollView, which is the whole of what "content-anchored" means. A ScrollView does not
    // move; its child does, so an overlay on the viewport is the window-anchored field again
    // under a different parent.
    content.foreground = grainOverlay(context)

    return LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
        // ADR-009 D4.3 / derivation row 21: the grain, full-bleed over the whole destination.
        //
        // A FOREGROUND AND NOT A BACKGROUND, WHICH IS THE ENTIRE PLACEMENT. A background is drawn
        // UNDER the children, so a grain there would be covered by every card, chip and bar in the
        // app and would texture only the gaps between them -- correct in every value a test could
        // read and absent from every surface it exists for. A foreground is drawn after the
        // children, over all of them, and cannot take a touch (row 21: non-interactive).
        //
        // **IT IS ONE OVERLAY PER MOVING PART AND IT USED TO BE ONE ON THIS ROOT** (ADR-009 D4.3's
        // 2026-08-08 amendment, agents-tracker-ksvb.3). This column does not scroll and the
        // destination inside it does, so a single overlay here was a noise field pinned to the
        // WINDOW with the type sliding under it: at 9.5-11 sp the antialiasing ramp is most of a
        // stroke, so every glyph was re-modulated on every scroll frame. Invisible in a
        // screenshot, and the literal reading of "the fonts are dancing". Anchored to each part
        // that moves -- the scrolled child above, and the two pieces of chrome below, which move
        // with nothing and so keep an overlay of their own -- the tile and the glyph travel
        // together and the modulation stops changing.
        //
        // THIS SCREEN IS STILL THE HOST BECAUSE OF WHAT IT DOES NOT COVER. Row 21 exempts the QR
        // tile -- 4% soft-light noise on a 29-module symbol is a scan risk -- and the scaffold
        // hosts the four paired destinations while `pairOnlyView`, which draws the code, replaces
        // it outright (`PhoneSurface.drawPairOnly` empties the app host first). So the exemption is
        // structural rather than a condition someone has to remember, and moving the overlay down
        // one level does not weaken it: every site below is still inside the paired scaffold.
        //
        // IT IS A COMPOSITION AND NOT A CHOICE, which is what keeps it inside PB-DS-9's rule for
        // this package: the value, the tile and the blend are all the kit's, and these lines say
        // only WHERE the overlay goes. `s24_screens_test.go` fences a `background =` here for the
        // stronger reason -- a screen that painted its own surface would be choosing one.
        //
        // FIRST, AND ABOVE THE SCROLL. Both halves are the requirement: above, because a warning
        // under the destination is a warning under the fold; outside the scroll, because one
        // inside it leaves the screen as soon as the user reads past it.
        status?.let { addView(it.apply { foreground = grainOverlay(context) }) }
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
            ).apply {
                tag = ScaffoldTag.TABS
                foreground = grainOverlay(context)
            },
        )
    }
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT

private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
