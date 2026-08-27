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
 * [triageInboxView], and no other destination composes one -- so a tab that merely swapped the
 * screen would land the user somewhere with no bar to come back with. A bar that belongs to one
 * destination is a bar the others do not have. Lifting it out is what makes inventory C1.4 a
 * navigation control instead of a picture of one, and it is what gave the activity screen its
 * first production call site: it was built, composed from the kit, covered by its own suite, and
 * reachable from nothing.
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
 * was. Here it serves every destination, which is what lets [activityPanelView] and the settings
 * panel be the wrap-height columns they already are -- neither composes a scroll, and without one
 * they would be cut off at the fold on a long journal or a small handset.
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
     * while the user was on Activity, Settings or inside a session changed nothing on screen. A
     * warning that belongs to one destination is a warning the others do not have, which is the
     * sentence this file already spends on the bar.
     *
     * WHAT IT HOLDS CHANGED AND WHERE IT IS DID NOT. It used to be a stack of up to four sentences
     * drawn for every state that had anything at all to say; it is now [syncStatusView] -- the
     * opaque strip a BROKEN link escalates to, and the detail a tap opens -- while the ordinary
     * report moved into the nav row's own pill. The slot is still above the content and still
     * outside its scroll, for the reasons this file has always given.
     */
    const val STATUS = "scaffold.status"

    /**
     * The conversation's fixed header: what you are reading, and the way back.
     *
     * IT IS THE SCAFFOLD'S AND NOT THE SCREEN'S for [TABS]' own reason, turned around. A header
     * inside the scrolling column would slide away the moment a reader moved, taking the session
     * name and the way out with it -- which is what a drill-down inside [phoneScaffoldView]
     * does today.
     */
    const val HEADER = "scaffold.header"

    /**
     * The conversation's pinned composer.
     *
     * IT IS OUTSIDE THE SCROLL, which is the whole of what "pinned" means and the reason the
     * IME inset had to be read at all: as the last child of a scrolling column it could be
     * scrolled into view, and as a sibling of the scroll it has to be kept above the keyboard.
     */
    const val COMPOSER = "scaffold.composer"

    /**
     * The parts whose ON-SCREEN ORDER is the recorded composition: the status chrome, the content,
     * then the bar. A tab bar that scrolled with the content would be a different screen, which is
     * the assertion `PhoneScaffoldViewTest` inherited from the inbox's suite along with the bar --
     * and a strip UNDER the content is a warning below the fold, which is the same defect at the
     * other end of the column.
     *
     * **IT IS [phoneScaffoldView]'S COMPOSITION AND IT IS NOW SCOPED TO THAT, DELIBERATELY**
     * (chat-surface-plan B.5). It was written as a claim about WHATEVER IS ON SCREEN, at a time
     * when one composition was the only one there was, and the suite around it was written the
     * same way -- `PhoneScaffoldViewTest`'s bar sweep and `PhoneSurfaceNavigationTest`'s two
     * survival tests quantify over every destination the app can reach. Those tests were written
     * to catch exactly the change this file has just made, so narrowing them is a named decision
     * rather than a tidy-up, and the amendment is this: the quantifier is the THREE TAB
     * DESTINATIONS, which is what [Destination] has always enumerated, and the conversation is
     * outside it because a conversation is not a destination -- it is a place you go INTO from
     * one, and it deliberately has no bar ([conversationScaffoldView] argues why). What replaces
     * the coverage the narrowing gives up is a positive assertion in the other direction: that
     * the conversation has NO bar and DOES have the strip, which `ConversationScaffoldViewTest`
     * makes over this set's sibling arrangement and `PhoneSurfaceConversationHostTest` makes over
     * the app the user actually opens. A universal claim quietly weakened and a universal claim
     * replaced by two specific ones look identical in a diff, which is why this paragraph exists.
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
 * THE SECOND COMPOSITION, and the reason PB-DS-9's one-destination-above-one-bar arrangement
 * gains an exception (owner ruling on the conversation surface).
 *
 * WHAT [phoneScaffoldView] DOES THAT A CONVERSATION CANNOT LIVE WITH. It wraps whatever it is
 * handed in ONE ScrollView and puts the tab bar under it, so a session's notices, its
 * conversation and its controls all scrolled as a single document -- which is why the owner's
 * screenshot of one session needed two screenshots, and why the composer, being the last child
 * of that document, could only be reached by scrolling past the entire transcript.
 *
 * SO THE PARTS THAT MUST NOT MOVE ARE SIBLINGS OF THE SCROLL RATHER THAN INSIDE IT: a header
 * that names what you are reading, the list, and a composer that stays under the thumb. This is
 * the arrangement every messaging surface has, and it is not available to a destination that is
 * handed to the scaffold as `content`.
 *
 * THE TAB BAR GOES AND THE STATUS STRIP STAYS, which is one decision each rather than one
 * decision. A conversation is a place you go INTO -- back returns to the inbox, which keeps its
 * bar -- so a bar here is an invitation to leave a screen you just arrived at. The strip is the
 * opposite case and [ScaffoldTag.STATUS] already argues it: a warning that belongs to one
 * destination is a warning the others do not have, and dropping it here would make the one
 * screen where a person is typing the one screen that cannot tell them the link is gone.
 *
 * @param scrollY where in the transcript to put the reader, or null for THE NEWEST MESSAGE. See
 *  [anchorConversation] for both halves of why this parameter exists at all; the short version is
 *  that null is what OPENING a conversation means and a number is what COMING BACK to one means,
 *  and that this scaffold cannot tell the two apart by itself because it is built fresh for both.
 */
fun conversationScaffoldView(
    context: Context,
    header: View,
    content: View,
    composer: View?,
    status: View? = null,
    scrollY: Int? = null,
): View {
    val scroll = ScrollView(context).apply {
        tag = ScaffoldTag.CONTENT
        isFillViewport = true
        // THE VIEWPORT CLIPS (phone refit W1.2): a row scrolled out of it stops painting, so
        // nothing draws over the pinned header or under the pinned composer. See the inbox
        // viewport below for why nothing needs it open.
        clipChildren = true
        clipToPadding = true
        isVerticalScrollBarEnabled = false
        // WEIGHT 1 AND HEIGHT 0: the list takes what is left after the two fixed parts, which is
        // what makes it the only thing that scrolls.
        layoutParams = LinearLayout.LayoutParams(MATCH, 0, 1f)
        addView(content)
    }
    scroll.anchorConversation(content, scrollY)
    // The grain rides the SCROLLED CHILD, for ADR-009 D4.3's amended reason: one overlay per
    // moving part, so the tile and the glyph travel together.
    content.foreground = grainOverlay(context)

    return LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
        status?.let { addView(it.apply { foreground = grainOverlay(context) }) }
        addView(
            header.apply {
                tag = ScaffoldTag.HEADER
                foreground = grainOverlay(context)
            },
        )
        addView(scroll)
        // NULL DRAWS NOTHING, WHICH IS A STATE AND NOT AN ABSENCE. A session with no message
        // sink has no composer at all rather than a disabled one (ADR-017), and the sentence
        // saying why is drawn by the screen inside the scroll, where the reader is looking.
        composer?.let {
            addView(
                it.apply {
                    tag = ScaffoldTag.COMPOSER
                    foreground = grainOverlay(context)
                },
            )
        }
    }
}

/**
 * Put the reader at [scrollY], or at THE NEWEST MESSAGE when there is nothing to restore.
 *
 * **THE DEFECT THIS EXISTS AGAINST IS THE OWNER'S FIRST COMPLAINT, UNANSWERED BY THE WHOLE
 * SURFACE THAT WAS BUILT AROUND IT** (agents-tracker-tu7z, P0). The transcript is oldest-at-top
 * and this scaffold hands it a brand-new `ScrollView` at `scrollY = 0`, so opening a session put
 * a reader on its FIRST messages -- and it never recovered, because `SessionDetailView`'s
 * stick-to-bottom is gated on `listIsScrolledToBottom`, which is false for a reader parked at the
 * top. Every subsequent line the agent wrote landed below the fold with no scroll. "Messages that
 * are scattered, an app page that is way too long" is the literal reading of a screen that opens
 * at the beginning of a session and then accumulates the rest of it out of sight.
 *
 * **AND IT IS ALSO agents-tracker-jz0z, WHICH IS THE SAME MECHANISM ONE REBUILD LATER** (P1).
 * `PhoneSurface.drawScaffold` calls this function on every `ScaffoldKey` change, and the key
 * includes `literal` and `composer` -- so opening an R8 output screen or an R9 diff and coming
 * back, or a session losing its message sink, discards the `ScrollView` the offset lived on. A
 * scaffold rebuilt fresh cannot know which of the two it is; only the surface knows, so the
 * surface says, and [scrollY] is how. Null means OPEN and a number means COME BACK.
 *
 * **IT IS A LAYOUT LISTENER AND NOT A `post`, AND THAT IS THE ONE DECISION IN HERE.** A
 * `ScrollView` cannot scroll before it has been laid out: `ScrollView.scrollTo` clamps against its
 * own viewport height and its child's height, and both are zero until a measure and layout pass
 * has run, so a scroll issued at construction time is silently discarded. `post { fullScroll() }`
 * is the common idiom and it is a RACE dressed as a fix -- it lands after whichever traversal the
 * message queue happens to run first, which on a warm view hierarchy is usually the right one and
 * on a cold start, a slow first frame or a Robolectric looper is not. Acting ON the layout pass
 * that produces the height is the same instruction with the race removed: the listener is invoked
 * from the end of `View.layout`, which is the first moment the answer exists.
 *
 * The listener is hung on the CONTENT and not on the scroll, because the two lay out on different
 * occasions: a `ScrollView` whose bounds never change does not re-layout when the transcript grows
 * inside it, and it is the transcript's height that decides where the bottom is.
 *
 * **IT IS SPENT ONCE, AND THE ALTERNATIVE IS A WORSE DEFECT THAN THE ONE IT FIXES.** An anchor
 * re-applied on every layout would drag a reader who had scrolled up to re-read something down to
 * the newest message the instant their agent wrote a line. Following the agent AFTER the opening
 * is `TranscriptIncremental.stickToBottom`'s decision and it declines for exactly that reader;
 * this is only where they START.
 *
 * **WHAT "ONCE" MEANS IS THE FIRST LAYOUT WITH SOMETHING TO SCROLL, AND THE OBVIOUS READING OF
 * THAT IS WRONG.** "The first layout with a height at all" is what this was written as, and
 * `isFillViewport` -- three lines up, and load-bearing for the tab bar -- makes it a bug: a
 * content host that is EMPTY or shorter than the screen is re-measured to exactly the viewport,
 * so it never reports zero and the anchor would be spent on a transcript that had not arrived
 * yet. `drawScaffold` does run on draws where the host is empty. So the condition is that the
 * content is TALLER THAN THE VIEWPORT: below that there is no bottom that differs from the top,
 * scrolling would be a no-op, and a reader cannot have moved away from a position they cannot
 * leave -- which is what makes staying armed safe rather than merely convenient.
 *
 * @param content the scrolled child, whose height is what "the bottom" is measured against.
 * @param scrollY the offset to restore, or null for the newest message. Handing the content's own
 *  height to `scrollTo` IS the bottom, because the clamp turns any y past the end into the end --
 *  which is also what makes a restored offset safe when the conversation shrank underneath it, as
 *  a page prepended past the retention bound or an item the machine replaced can do.
 */
private fun ScrollView.anchorConversation(content: View, scrollY: Int?) {
    val scroll = this
    content.addOnLayoutChangeListener(
        object : View.OnLayoutChangeListener {
            override fun onLayoutChange(
                view: View,
                left: Int,
                top: Int,
                right: Int,
                bottom: Int,
                oldLeft: Int,
                oldTop: Int,
                oldRight: Int,
                oldBottom: Int,
            ) {
                // AN ANCHOR THAT WAS NEVER SPENT OUTLIVES ITS SCAFFOLD, and the content it is
                // hung on outlives every scaffold there will ever be: `PhoneSurface.contentHost`
                // is built once and re-parented for the life of the app. So a conversation that
                // fit the screen leaves an armed listener behind, and the host it is on goes on
                // to hold the Activity journal and the next session's transcript.
                //
                // IT IS NOT THAT A STALE ONE WOULD SCROLL THE WRONG SCREEN. `ScrollView.scrollTo`
                // does nothing at all on a scroll whose child has been taken away, and
                // `drawScaffold` takes the content host out of every scaffold it discards -- so
                // today the leftovers are inert. Depending on that is the accident; this says the
                // thing that is actually meant, which is that the anchor is a fact about ONE
                // composition, and drops it on the way out so they cannot pile up either.
                if (view.parent !== scroll) {
                    view.removeOnLayoutChangeListener(this)
                    return
                }
                val height = bottom - top
                // The scroll's own frame is set before its children lay out, so this reads the
                // viewport of the pass that is happening rather than the previous one's.
                if (height <= scroll.height) return
                // Removing from inside the callback is safe and is the idiom: `View.layout`
                // iterates a CLONE of its listener list for this exact reason.
                view.removeOnLayoutChangeListener(this)
                scroll.scrollTo(0, scrollY ?: height)
            }
        },
    )
}

/**
 * The scaffold as a view.
 *
 * @param content the destination on screen. It is a view rather than a `when` over [destination]
 *  because what each destination shows is the SURFACE's business -- the inbox needs its callbacks,
 *  the activity screen needs a journal page, and the settings panel is a live object that redraws
 *  itself -- and a scaffold that built them would need the facade.
 * @param tabs the tabs as `InboxScreen` records them: the labels, and the NeedsInput badge the
 *  inbox counts (derivation table 1.4). Their [InboxTab.selected] is not read here --
 *  see [destination].
 * @param destination which of them is on screen. IT IS THE PARAMETER AND `InboxTab.selected`
 *  IS NOT, because selection is a fact about navigation and that model was written when the inbox
 *  was the only screen: it answers `label == "Inbox"` for every tab list it builds, which on the
 *  other destinations would tell a user standing on Activity that they are in the Inbox.
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
        // THE VIEWPORT CLIPS (phone refit W1.2). It used to be told not to, so that a glowing dot
        // and the tab badge could overhang -- and so every row scrolled out of it kept painting,
        // over the header and under the bar. Nothing needs the viewport open: the rows open their
        // own bounds (SessionRow.kt), the bloom and the dot draw inside their own layer
        // (CtaButton.kt, StatusDot.kt), and the badge lives in the bar, a sibling of this scroll
        // under the root -- which still does not clip, for the badge's sake.
        clipChildren = true
        clipToPadding = true
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
        // WINDOW with the type sliding under it: at 10-11.5 sp the antialiasing ramp is most of a
        // stroke, so every glyph was re-modulated on every scroll frame. (The range read "9.5-11
        // sp" until owner ruling R1 of 2026-08-09 consolidated the ladder, ADR-012 phase 2 P1: the
        // app's two smallest rungs are 10 micro and 11.5 code now. The argument is the ramp at the
        // bottom of the scale, and the bottom of the scale moved.) Invisible in a
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
