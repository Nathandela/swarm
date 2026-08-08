package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import dev.swarm.phone.ui.StatusBanner
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.TabItem
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.grainOverlay
import dev.swarm.phone.ui.kit.readOnlyNote
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
     * agents-tracker-e6mi: what the app says about its link, above the destination.
     *
     * IT IS THE SCAFFOLD'S FOR THE TAB BAR'S OWN REASON. PB-APP-8's offline/reconnecting/stale
     * states and PB-APP-11's freshness verdict were written to a line inside the inbox's column,
     * which the surface detaches on the way to every other destination -- so a link that dropped
     * while the user was on Machines, Activity, Settings or inside a session changed nothing on
     * screen. A warning that belongs to one of four destinations is a warning the other three do
     * not have, which is the sentence this file already spends on the bar.
     */
    const val BANNER = "scaffold.banner"

    /** One fact on the banner. Three of them are three lines, never one sentence. */
    const val BANNER_LINE = "scaffold.banner.line"

    /**
     * The banner's one CONTROL, which is a different thing from a line (agents-tracker-agre).
     *
     * IT HAS ITS OWN TAG BECAUSE IT IS NOT A FACT. [BANNER_LINE] names the sentences a reader
     * reads; this names the thing a finger presses, and a test that found either under one tag
     * could assert the remedy was "on screen" while it was drawn as a fourth paragraph -- which is
     * precisely the defect the control exists to end.
     */
    const val BANNER_ACTION = "scaffold.banner.action"

    /**
     * The parts whose ON-SCREEN ORDER is the recorded composition: the banner, the content, then
     * the bar. A tab bar that scrolled with the content would be a different screen, which is the
     * assertion `PhoneScaffoldViewTest` inherited from the inbox's suite along with the bar -- and
     * a banner UNDER the content is a warning below the fold, which is the same defect at the
     * other end of the column.
     */
    val COMPOSITION: Set<String> = setOf(BANNER, CONTENT, TABS)
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
 * @param banner what the app says about its link, drawn above [content] and OUTSIDE its scroll, or
 *  null for a caller with nothing to say. It is a view for [content]'s reason -- what it says
 *  changes on the surface's clock and the scaffold is rebuilt on the bar's, so a scaffold that
 *  built one would re-parent the destination under whoever is using it. Being outside the scroll
 *  is the whole of the placement: a banner inside it scrolls away under a long journal, which is
 *  the same disappearance this slot exists to end, in a slower form.
 */
fun phoneScaffoldView(
    context: Context,
    content: View,
    tabs: List<InboxTab>,
    destination: Destination,
    onSelectDestination: (Destination) -> Unit,
    banner: View? = null,
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
        banner?.let { addView(it.apply { foreground = grainOverlay(context) }) }
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

/**
 * The banner as a view: one line per fact the phone has to report, and nothing when it has none.
 *
 * THE LINES ARE THE MODEL'S AND THE ORDER IS TOO. [StatusBanner.lines] drops the facts with
 * nothing to say and puts the rest outward from the phone -- link, machine, list -- so this
 * composes what it is handed and decides neither. A view that assembled its own would be free to
 * re-run them together, which is the defect this whole change is about.
 *
 * THE COMPONENT IS `readOnlyNote` AND IT IS A REUSE RATHER THAN A NEAR MISS. The design's own type
 * register (substrate-components.md §7) puts the read-only note and the STALE NOTE in one cell --
 * "12 / 11.5 (ro-note, stale note, banner meta, cmdline, settings sublabel) takes `Body.Secondary`"
 * -- so the two are the same type role by the design's own assignment, not by a resemblance
 * noticed here. Building a second factory painting the same style would be the copy §2's reuse
 * rule exists to prevent, and it would need a derivation row that does not exist: the table's row 2
 * is the PUSH banner, an overlay with a motion budget and a tap target, which this is not.
 *
 * IT IS NOT ROW 2's BANNER AND MUST NOT ACQUIRE ITS SURFACE. That row is one of the two motions
 * ADR-007 B134 keeps -- it translates in, auto-dismisses and opens the approval sheet. This is
 * persistent chrome that says what is true right now and goes away when it stops being true; a
 * fill, a border and an entry animation would make a standing condition look like an event that
 * has just arrived.
 *
 * THE ONE CONTROL IS DRAWN LAST AND ONLY WHEN THE MODEL OFFERS ONE (agents-tracker-agre). PB-APP-10
 * asks for "an explicit re-pair PROMPT, not a failure loop", and a prompt is something a person can
 * press: a handset the transport has permanently refused reads "Pair this phone again" and, until
 * this control, had nothing on screen to do it with. [StatusBanner.pairAgain] decides whether it is
 * owed and what it reads; where it leads is the surface's, for [content]'s reason -- navigation is
 * not something a composition can know.
 *
 * @param onPairAgain what [StatusBanner.pairAgain]'s control does. It is defaulted so the suites
 *  that build a banner from three facts alone are unaffected; those banners offer no control, so
 *  the default is never installed on anything.
 */
fun statusBannerView(
    context: Context,
    banner: StatusBanner,
    onPairAgain: () -> Unit = {},
): View =
    LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        tag = ScaffoldTag.BANNER
        // `capped = true` IS THE SLOT'S OWN FACT AND NOT THE COMPONENT'S (agents-tracker-ksvb.3).
        // This banner is the one `readOnlyNote` in the app drawn OUTSIDE a scroll, so a sentence
        // that wraps does not cost its own height -- it pushes the destination, on all four tabs,
        // under a user who is reading something else. Three facts wrapping to four lines each is
        // a third of a handset. Two lines and the platform's mark bound that; the same component
        // under a terminal well still wraps, because there it is prose in a column that scrolls.
        banner.lines.forEach { line ->
            addView(
                readOnlyNote(context, line, capped = true).apply { tag = ScaffoldTag.BANNER_LINE },
            )
        }
        // `CtaKind.MORE` IS THE NEUTRAL RULE AND THAT IS THE RIGHT ONE HERE. The press approves
        // nothing and destroys nothing -- it opens the screen this banner's sentence sends the user
        // to -- and `.a2-ok` on a warning would read as the app recommending the act.
        if (banner.pairAgain.isNotEmpty()) {
            addView(
                ctaButton(context, banner.pairAgain, CtaKind.MORE).apply {
                    tag = ScaffoldTag.BANNER_ACTION
                    setOnClickListener { onPairAgain() }
                    // A `TextView` ANNOUNCES ITSELF AS TEXT, which on this banner is the whole
                    // distinction being drawn: three of these views ARE text, and a screen reader
                    // that heard a fourth sentence would meet the same defect the sighted user
                    // just stopped meeting. `pairOnlyView` sets the role at the click for the same
                    // reason -- the kit has no click to hang it on.
                    setAccessibilityDelegate(
                        object : View.AccessibilityDelegate() {
                            override fun onInitializeAccessibilityNodeInfo(
                                host: View,
                                info: AccessibilityNodeInfo,
                            ) {
                                super.onInitializeAccessibilityNodeInfo(host, info)
                                info.className = Button::class.java.name
                            }
                        },
                    )
                },
            )
        }
    }

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT

private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
