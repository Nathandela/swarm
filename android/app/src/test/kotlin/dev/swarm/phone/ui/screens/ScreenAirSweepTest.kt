package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.HorizontalScrollView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.PairingAttempt
import dev.swarm.phone.ui.PairingStep
import dev.swarm.phone.ui.ScannerState
import dev.swarm.phone.ui.SessionRow
import dev.swarm.phone.ui.TriageInbox
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.PresenceMark
import dev.swarm.phone.ui.kit.composerBar
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.textField
import dev.swarm.phone.ui.kit.toggle
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.10: the owner's field report of
 * 2026-08-09 -- "side padding is right on the Inbox and pairing, absent everywhere else".
 *
 * THE RULING THIS GUARDS, in the bead's own words: *every leaf text/button renders with an
 * EFFECTIVE side inset of at least `swarm_space_12`, applied EXACTLY ONCE.* Both halves are
 * defects. Under the floor is the report itself -- a sentence or a control touching the glass.
 * Twice is agents-tracker-2pnu F2, where `pairOnlyView`'s column and `pairingPanelView`'s own
 * spent row 18's cell one inside the other and the flow rendered at 48 dp sides.
 *
 * WHY A SWEEP AND NOT ONE ASSERTION PER SCREEN. Six screens shipped flush at once, each with its
 * own suite, because no suite in this app ever asked what a person MEASURES: the composition
 * tests read tags and copy, and the kit tests read one component standing alone, where every
 * padding is present and correct. The inset a reader sees is a property of the whole stack from
 * the window's edge down, so it can only be read off a screen that has been laid out -- and a
 * check that walks every destination is the only shape that a seventh screen cannot ship past.
 *
 * ## What counts as a leaf, and where its edge is
 *
 * A leaf is what a person reads or presses: a visible `TextView` with words in it, or anything
 * clickable. Its EDGE is what they see -- the painted box where the view has a background of its
 * own (a card, a well, a CTA), and the TEXT where it does not (a notice, a heading, an empty
 * state), because the ground shows through the second kind and the padding is the only thing
 * holding the glyphs off the glass.
 *
 * NEGATIVE MARGINS ARE ROOM AND NOT POSITION. `ctaButton`'s bloom variant inflates itself by the
 * halo's radius and hands every pixel back with a negative margin ([dev.swarm.phone.ui.kit
 * .CtaSpec]'s `insetPx`), so its VIEW is 18 dp wider on each side than the button anybody aims
 * at. The visible box is the view's box shrunk by whatever a negative margin gave back.
 *
 * A HORIZONTAL SCROLLER IS ITS CONTENT'S EDGE. `monoWell(...).scrolledHorizontally()` measures
 * its child with no width ceiling, so a diff wider than the phone has a right edge off-screen and
 * a naive walk would report it as a negative inset. What a reader sees is the viewport, so the
 * scroller's box is the box for everything inside it.
 *
 * ## What is deliberately out of the sweep
 *
 * THE SCAFFOLD'S OWN CHROME. `tabBar` draws its items at `.ptabs`'s `padding: 14px 8px 24px` and
 * the sync strip is ruled "radius none and no side inset: it is full-bleed chrome across the top
 * of the app" (§4's Sync status pill and strip row). Both are the app's furniture rather than a
 * destination's content, both are ruled at their own numbers, and neither is what the field
 * report is about. The destinations are swept as the scaffold hosts them, which is at the
 * window's own width: `phoneScaffoldView` puts the content in a `ScrollView` that spends no side
 * padding, so a destination's own left edge IS the screen's.
 */
@RunWith(RobolectricTestRunner::class)
class ScreenAirSweepTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /** The ruled floor: `swarm_space_12`, the step the Inbox's own row container already spends. */
    private fun air(): Int = context.resources.getDimensionPixelSize(R.dimen.swarm_space_12)

    /**
     * The two steps a COLUMN is allowed to spend as the screen's own side air: the destinations'
     * `swarm_space_12` and the pairing scaffold's ruled `swarm_space_24` (derivation row 18).
     * Either one, once -- a path that crosses two of them is the F2 doubling.
     */
    private fun airSteps(): Set<Int> = setOf(
        context.resources.getDimensionPixelSize(R.dimen.swarm_space_12),
        context.resources.getDimensionPixelSize(R.dimen.swarm_space_24),
    )

    /** A handset's width, so the sweep reads the layout a person holds rather than an unbounded one. */
    private fun screenWidthPx(): Int = (360 * context.resources.displayMetrics.density).toInt()

    // ---- the sweep --------------------------------------------------------

    /** One leaf, as measured: what it says, how far in each edge sits, and how often the air was spent. */
    private data class Leaf(
        val what: String,
        val start: Int,
        val end: Int,
        val airSpends: Int,
    )

    /**
     * Every visible leaf on [root], laid out at a handset's width.
     *
     * The walk carries three things down: the absolute left of the current view, the box a
     * horizontal scroller has clamped it to (null outside one), and how many times a container
     * above it has spent the screen's own side air.
     */
    private fun sweep(root: View): List<Leaf> {
        val width = screenWidthPx()
        root.measure(
            View.MeasureSpec.makeMeasureSpec(width, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(0, View.MeasureSpec.UNSPECIFIED),
        )
        root.layout(0, 0, root.measuredWidth, root.measuredHeight)

        val leaves = mutableListOf<Leaf>()
        fun walk(view: View, left: Int, clamp: IntRange?, spentAbove: Int) {
            if (view.visibility != View.VISIBLE) return
            val margins = view.layoutParams as? ViewGroup.MarginLayoutParams
            val spent = spentAbove + if (margins?.marginStart in airSteps()) 1 else 0
            val scroller = clamp ?: if (view is HorizontalScrollView) left..(left + view.width) else null

            if (view is ViewGroup && !isLeaf(view)) {
                val inner = spent + if (view.background == null && view.paddingStart in airSteps()) 1 else 0
                for (i in 0 until view.childCount) {
                    walk(view.getChildAt(i), left + view.getChildAt(i).left, scroller, inner)
                }
                return
            }
            if (!isLeaf(view)) return

            // The view's own box, then what a negative margin handed back, then the padding on a
            // view that paints nothing -- the glyphs are its edge when the ground shows through.
            val room = maxOf(0, -(margins?.marginStart ?: 0)) to maxOf(0, -(margins?.marginEnd ?: 0))
            val pad = if (view.background == null) view.paddingStart to view.paddingEnd else 0 to 0
            val box = clamp ?: left..(left + view.width)
            leaves += Leaf(
                what = describe(view),
                start = box.first + room.first + pad.first,
                end = width - box.last + room.second + pad.second,
                airSpends = spent,
            )
        }
        walk(root, 0, null, 0)
        return leaves
    }

    /** What a person reads or presses: words on screen, or anything that takes a tap. */
    private fun isLeaf(view: View): Boolean =
        (view is TextView && view.text.isNotBlank()) || view.isClickable

    private fun describe(view: View): String {
        val words = (view as? TextView)?.text?.toString()?.take(40).orEmpty()
        val tag = view.tag?.toString()?.takeIf { it.isNotEmpty() }
        return "${view.javaClass.simpleName}${tag?.let { "[$it]" } ?: ""}" +
            if (words.isEmpty()) "" else " \"$words\""
    }

    /** Every screen this app can put in front of a person, built the way production builds it. */
    private fun destinations(): Map<String, View> = mapOf(
        "Inbox" to inbox(),
        "Activity" to activity(),
        "Settings" to settings(),
        "Session detail" to sessionDetail(),
        "Launch form" to launchForm(),
        "Approval sheet" to approvalSheet(),
        "Pair-only offer" to pairOnlyOffer(),
        "Pairing (started)" to pairingStarted(),
    )

    // ---- the claims -------------------------------------------------------

    @Test
    fun `every leaf on every destination clears the ruled side inset`() {
        val floor = air()
        val faults = destinations().flatMap { (screen, root) ->
            sweep(root).filter { minOf(it.start, it.end) < floor }.map { leaf ->
                "$screen: ${leaf.what} sits ${leaf.start}px from the left edge and ${leaf.end}px " +
                    "from the right, against the ruled ${floor}px floor"
            }
        }

        assertEquals(
            "agents-tracker-nx44.10: ${faults.size} leaves render inside the ruled " +
                "`swarm_space_12` side inset -- text and buttons touching the glass:\n" +
                faults.joinToString("\n"),
            emptyList<String>(),
            faults,
        )
    }

    @Test
    fun `no leaf is given the screen's air twice`() {
        val faults = destinations().flatMap { (screen, root) ->
            sweep(root).filter { it.airSpends > 1 }.map { leaf ->
                "$screen: ${leaf.what} has the screen's own side air spent ${leaf.airSpends} " +
                    "times above it, so it renders ${leaf.start}px in"
            }
        }

        assertEquals(
            "agents-tracker-nx44.10: ${faults.size} leaves are double-padded. This is " +
                "agents-tracker-2pnu F2's defect: a column inset stacked on a child that " +
                "already carried its own.\n" + faults.joinToString("\n"),
            emptyList<String>(),
            faults,
        )
    }

    /**
     * The negative controls, in memory. A reader that always answered "far enough" would certify
     * a screen built entirely out of flush text, and one that never counted the air twice would
     * certify F2 itself.
     */
    @Test
    fun `the sweep can see a flush leaf and a doubled one`() {
        val flush = LinearLayout(context).apply {
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            addView(TextView(context).apply { text = "flush against the glass" })
        }
        assertTrue(
            "the sweep certified a bare TextView with no inset at all",
            sweep(flush).any { it.start < air() },
        )

        val doubled = sessionList(context).apply {
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            addView(sessionList(context).apply { addView(notice(context, "twice over")) })
        }
        assertTrue(
            "the sweep counted one air spend where two containers spent it",
            sweep(doubled).any { it.airSpends > 1 },
        )
    }

    // ---- the screens, as production builds them ---------------------------

    private fun inbox(): View = triageInboxView(
        context = context,
        screen = TriageInboxScreen.of(
            inbox = TriageInbox.from(
                listOf(
                    SessionRow(
                        id = "mbp/api",
                        title = "api",
                        group = "needs_input",
                        need = "waiting on you",
                        present = true,
                        agent = "claude",
                    ),
                ),
                journalStale = true,
            ),
        ),
        onSelectSession = {},
        onSelectScope = {},
    )

    private fun activity(): View = activityPanelView(
        context = context,
        panel = ActivityPanel(
            title = "Activity",
            sections = listOf(
                ActivitySection(
                    heading = "TODAY",
                    rows = listOf(ActivityEntry(cursor = 1, body = "api launched", emphasis = "api")),
                    emptyCopy = "Nothing has happened yet.",
                ),
                ActivitySection(heading = "EARLIER", rows = emptyList(), emptyCopy = "Nothing here."),
            ),
            staleNotice = "Some records did not reach this phone.",
        ),
    )

    private fun settings(): View = settingsPanelView(
        context = context,
        panel = SettingsPanel(
            title = "Settings",
            sections = listOf(
                SettingsSection(
                    heading = "NOTIFICATIONS",
                    rows = listOf(
                        SettingsRow(
                            toggle = dev.swarm.phone.ui.PushToggle.FIRST,
                            label = "Alerts",
                            sublabel = "When a session needs you",
                            checked = true,
                            enabled = true,
                            description = "Alerts, on",
                        ),
                    ),
                ),
            ),
            notices = listOf("Notifications are blocked for this app."),
            disclosure = "Battery saver can delay a push.",
            machineSection = MachineSection(
                heading = "PAIRING",
                row = PairedMachineRow(
                    label = "mbp",
                    sublabel = "Replacing revokes this device",
                    replaceLabel = "Replace",
                    replaceConfirmation = "Replace mbp?",
                ),
            ),
            connection = ConnectionSection(
                heading = "CONNECTION",
                machine = MachineRow(
                    name = "mbp",
                    endpoint = "relay",
                    presenceLine = "Online",
                    presenceDescription = null,
                    mark = PresenceMark.ONLINE,
                ),
                health = "The journal has a gap in it.",
                clockNotice = "This phone's clock is ahead.",
                remoteAccess = RemoteAccessRow(
                    title = "Remote access",
                    body = "The machine refuses commands. Turn it back on with swarm remote on.",
                    command = "swarm remote on",
                ),
            ),
            permissionRedirectLabel = "Open notification settings",
            deliveryRedirectLabel = "Open the wake channel",
        ),
        rowFor = { row -> toggle(context, checked = row.checked, description = row.description) },
    )

    private fun sessionDetail(): View = sessionDetailView(
        context = context,
        panel = detailPanel(),
        takeControl = ctaButton(context, "Take control", CtaKind.MORE),
        release = ctaButton(context, "Release control", CtaKind.MORE),
        stop = ctaButton(context, "Stop", CtaKind.DENY),
        kill = ctaButton(context, "Kill session", CtaKind.DENY),
        resync = ctaButton(context, "Fetch what is missing", CtaKind.MORE),
        acknowledge = ctaButton(context, "Clear", CtaKind.MORE),
        composer = composerBar(
            context,
            textField(context, "Type a line"),
            ctaButton(context, "Send line", CtaKind.APPROVE),
        ),
        outcome = "The machine refused: remote control is disabled.",
        onBack = {},
    )

    /** Every conditional block on the detail screen drawn at once, so nothing is swept vacuously. */
    private fun detailPanel(): SessionDetailPanel = SessionDetailScreen.of(
        dev.swarm.phone.ui.SessionDetail(
            sessionId = "mbp/api",
            leaseHeld = false,
            online = true,
            journalStale = true,
            stopNotSent = true,
        ),
        TranscriptScreen.of(emptyList()),
        dev.swarm.phone.ui.SessionLease(sessionId = "mbp/api", leaseHeld = false, online = true),
    ).copy(
        transcript = TranscriptPanel(
            heading = "CONVERSATION",
            blocks = listOf(
                TranscriptBlock(itemId = "i1", kind = "message", line = "Running the suite"),
                TranscriptBlock(
                    itemId = "i2",
                    kind = "tool_result",
                    line = "go test ./...",
                    well = "ok  dev.swarm/internal/design  0.4s\nok  dev.swarm/internal/wire  1.1s",
                ),
            ),
            emptyCopy = "Nothing has been said yet.",
        ),
        leaseNotice = "You are watching. Take control to type.",
        leaseDetail = "kill_switch: remote control is disabled",
        undeliveredNotice = "4 bytes never reached the machine.",
        undeliveredDetail = "the link dropped",
        offersAcknowledge = true,
        offersResync = true,
    )

    private fun launchForm(): View = launchPanelView(
        context = context,
        panel = LaunchPanel(
            heading = "START A SESSION",
            fields = listOf(
                LaunchField(id = LaunchFieldId.AGENT, hint = "agent", required = true),
                LaunchField(id = LaunchFieldId.CWD, hint = "working directory", required = true),
            ),
            submit = "Launch",
            notice = "The machine refused the launch.",
            noticeDetail = "no such directory",
        ),
        fieldFor = { field -> textField(context, field.name) },
        submit = ctaButton(context, "Launch", CtaKind.APPROVE),
    )

    private fun approvalSheet(): View = approvalSheetView(
        context = context,
        panel = ApprovalSheetPanel(
            contextLine = "MBP / API",
            question = "Run the test suite?",
            command = "go test ./...",
            actions = listOf(ApprovalDecision(id = "accept", label = "Allow")),
            sessionId = "mbp/api",
            itemId = "i-approve-1",
        ),
    )

    private fun pairOnlyOffer(): View = pairOnlyView(
        context = context,
        pairing = View(context),
        started = false,
        onStartPairing = {},
        revokedNotice = "Your machine kept this device registered.",
        revokedDetail = "revoke refused: unknown device",
    )

    private fun pairingStarted(): View = pairOnlyView(
        context = context,
        pairing = pairingPanelView(
            context,
            PairingPanelScreen.of(
                attempt = PairingAttempt(
                    step = PairingStep.SCAN,
                    originShown = "",
                    originIsLocalNetwork = false,
                    explainsInterruptedAttempt = false,
                ),
                scanner = ScannerState.SCANNING,
                sas = null,
                holding = false,
                machine = "",
                relayKnown = true,
            ),
            PairingSlots(
                body = notice(context, "Point the camera at the code on your machine."),
                notice = notice(context, ""),
                destination = notice(context, ""),
                sas = notice(context, ""),
                sasInstruction = notice(context, ""),
                scanner = View(context),
                scanProgress = notice(context, ""),
                controls = PairingControl.entries.associateWith { control ->
                    ctaButton(context, control.name, CtaKind.MORE)
                },
            ),
        ),
        started = true,
        onStartPairing = {},
    )
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
