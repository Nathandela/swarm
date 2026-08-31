package dev.swarm.phone

import android.os.Looper
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.CompoundButton
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.kit.ComposerActionGlyph
import dev.swarm.phone.ui.kit.CtaSurface
import dev.swarm.phone.ui.kit.TopRule
import dev.swarm.phone.ui.screens.conversationScaffoldView
import dev.swarm.phone.ui.screens.PairingPanelScreen
import dev.swarm.phone.ui.screens.SessionCapabilityFacts
import dev.swarm.phone.ui.screens.SessionDetailPanel
import dev.swarm.phone.ui.screens.SessionDetailScreen
import dev.swarm.phone.ui.screens.TranscriptScreen
import dev.swarm.phone.ui.screens.TranscriptTag
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.shadows.ShadowDialog

/**
 * Phase B slice S19 -- PB-E2E-2's in-app actions have SUBJECTS, and every one of them is
 * overlay-protected.
 *
 * WHY THIS TEST EXISTS AT ALL. android/gate/s19_pbe2e2_test.go asserts the SOURCE fact -- that
 * production Kotlin calls each facade verb the smoke needs -- and a source scan cannot say
 * whether the call sits behind a control a person can press. S18's Activity passed every
 * source-level fence in this module while shipping three buttons and no scanner, no destination
 * confirmation, no SAS display, no confirm control and no keyboard; four of the requirement's
 * five actions had no subject and nothing anywhere was red. This is the runtime half.
 *
 * IT ASSERTS THE CONTROLS BY THE LABEL A USER READS, which is also what the instrumented smoke
 * presses (app/src/androidTest/.../PhoneScreenDriver.kt). A label changed on one side and not the
 * other is a smoke that cannot find the button, and this is where that surfaces -- on a JVM, in
 * two seconds, rather than on an emulator ten minutes into a run.
 *
 * PB-E2E-5 STAYS DEFERRED. Nothing here drives a camera, a biometric or an FCM delivery: the
 * phone core cannot even be built on the unit-test JVM, so what is asserted is that the controls
 * exist and carry the overlay filter -- not that pressing one succeeds.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneSurfaceControlsTest {

    /**
     * PB-E2E-2's five in-app actions, as the labels that perform them.
     *
     * ## Why one entry is a constant and six are literals
     *
     * The scan control's words are [PairingPanelScreen.SCAN_CTA] -- SOURCED, not transcribed -- and
     * the rest are literals. The split is not inconsistency; it is where each label's second copy
     * lives.
     *
     * THE LITERALS HAVE A COPY THIS FILE CANNOT SEE. `Use this code`, `Join this destination` and
     * `They match` are pressed BY LITERAL in the instrumented smoke
     * (`PbE2E2PairAndTypeTest`, `PbE2E2ResumeTest`), which runs on a device and cannot be typechecked
     * against the app. For those, this ledger's independent transcription is the cheap half of a
     * two-sided pin: a label changed in `PairingSurface` and not in the smoke fails HERE, on a JVM in
     * two seconds, rather than on an emulator ten minutes into a run -- which is what the class
     * comment above promises.
     *
     * THE SCAN CONTROL HAS NO SUCH COPY. The smoke never presses it; it takes the typed path
     * (`PbE2E2PairAndTypeTest` presses `Use this code`). So a transcription here would be pinning
     * the words against nothing but themselves, and the words are the SCREEN MODEL'S -- PB-DS-9 puts
     * copy there and this codebase argues everywhere that it exists once. Sourcing the constant
     * keeps the assertion that matters (a touch-filtered control that starts a scan exists and is
     * reachable) and drops only the ability to notice a deliberate re-wording, which is not a defect.
     *
     * `Scan QR code` WAS `Scan the code on your machine` (agents-tracker-qx9m). The old label
     * restated, word for word, the sentence `PairingFlow.messageFor(SCAN)` already puts two lines
     * above the button, so a reader had nothing to tell them apart.
     */
    private val requiredControls = mapOf(
        PairingPanelScreen.SCAN_CTA to
            "\"pairs against a local relay + daemon\": there is no way to start a scan",
        "Use this code" to
            "PB-PAIR-2's manual-entry fallback, which is also how the smoke hands the QR over",
        "Join this destination" to
            "PB-PAIR-6's destination confirmation, which BeginPairing leaves the app owing",
        "They match" to
            "\"SAS matches\": there is nothing to answer the comparison with",
        "They do not match" to
            "PB-SAS-3's mismatch answer, which is NOT cancel -- it is the only signal this " +
                "protocol has for a man-in-the-middle",
        // "Take control" WAS HERE, for PB-E2E-2's "takes control" clause. Owner ruling R1
        // deleted the control, and the clause with it: the smoke's own sequence -- observe,
        // then type -- no longer has a step between the two, because composer_send takes no
        // lease at any layer and the composer is live on any session with a link and a sink.
        // PB-E2E-2's scenario is amended in docs/specifications/remote-phaseB-requirements.md
        // beside PB-INPUT-2, which is where the lease's disappearance is argued.
        // "Send line" WAS HERE, for PB-E2E-2's "types" clause. Phone refit W3 made the composer's
        // control a glyph that SPEAKS rather than a label that reads -- "Send", or "Stop" over a
        // working agent and an empty field -- so the smoke presses it by content description
        // (`PhoneScreenDriver.pressDescribed`) and the pin is the surface tests below, which read
        // `SessionDetailScreen.COMPOSER_SEND` off the very control.
    )

    @Test
    fun every_action_pb_e2e_2_names_has_a_control_that_performs_it() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val labels = activity.touchFilteredViews()
                    .filterIsInstance<TextView>()
                    .map { it.text.toString() }
                for ((label, clause) in requiredControls) {
                    assertTrue(
                        "PB-E2E-2: no declared control labelled \"$label\", so the smoke cannot " +
                            "perform $clause.\nthe declared controls were:\n" +
                            labels.joinToString("\n"),
                        labels.contains(label),
                    )
                }
            }
        }
    }

    /**
     * PB-SEC-12 clause 1 for the controls S19 added, asserted as a PROPERTY OF THE HIERARCHY
     * rather than of a list the surface hands out.
     *
     * `PhoneActivityWindowTest` already walks `touchFilteredViews()`, and that list is exactly
     * what a new panel can forget to contribute to. This looks at every Button and Switch
     * actually on screen instead, so a control added without the filter fails here even if
     * nobody remembered to add it to the list.
     *
     * IT IS THE STRONGER OF THE TWO AFTER ADR-007 B133. The overlay filter is now the only
     * defence standing on revoke and take-control, so the fence that cannot be satisfied by
     * remembering to update a list is the one that has to hold.
     */
    @Test
    fun every_button_and_switch_on_screen_filters_obscured_touches() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                // `it.background is CtaSurface` IS THE THIRD CLAUSE AND IT IS DELIBERATELY NARROW.
                // Derivation §3's `.acts2 button` is a TextView with a layered surface, so the two
                // controls composed into the recomposed screens -- the peek's `[Take control]`
                // (row 22) and the launch form's submit -- are no longer `Button`s and would have
                // dropped out of this walk entirely. Widening to every clickable TextView instead
                // would sweep in the scope-bar chips, which are neither destructive nor
                // authorising; what this fence is for is the set an overlay attack is worth
                // mounting against, and reading the KIT'S OWN CTA SURFACE names exactly that set.
                val pressable = activity
                    .findViewById<ViewGroup>(android.R.id.content)
                    .flatten()
                    .filter { it is Button || it is CompoundButton || it.background is CtaSurface }
                assertTrue(
                    "PB-SEC-12: no pressable control on screen, so this assertion has no subject",
                    pressable.isNotEmpty(),
                )
                for (control in pressable) {
                    assertTrue(
                        "PB-SEC-12: \"${(control as TextView).text}\" does not filter obscured " +
                            "touches. Tapjacking is the attack where an overlay covers a control " +
                            "so the user's tap lands on something they cannot see, and the ones " +
                            "here revoke a device, take control of a shell, join a relay and " +
                            "answer a man-in-the-middle check",
                        control.filterTouchesWhenObscured,
                    )
                }
            }
        }
    }

    /**
     * PB-SAS-3, at runtime. android/gate/s16_ui_test.go fences the SHAPE of a SAS field in the
     * sources; this fences that no field on the pairing screen could collect one, whatever it is
     * named. The six symbols are compared by the person holding both screens -- a field would
     * move the comparison to the phone, which sees one string and whatever an attacker relayed.
     */
    @Test
    fun no_field_on_screen_collects_a_short_authentication_string() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val hints = activity
                    .findViewById<ViewGroup>(android.R.id.content)
                    .flatten()
                    .filterIsInstance<EditText>()
                    .map { it.hint?.toString().orEmpty().lowercase() }
                for (hint in hints) {
                    assertTrue(
                        "PB-SAS-3: a text field on the pairing screen invites a code to be " +
                            "typed (\"$hint\"). The SAS is compared on two displays and never " +
                            "entered",
                        !hint.contains("sas") && !hint.contains("symbol") &&
                            !hint.contains("six") && !hint.contains("emoji"),
                    )
                }
            }
        }
    }

    // -----------------------------------------------------------------------
    // Phone refit W3 (agents-tracker-d45a.3): one button. The conversation pins ONE region under
    // the thumb, and it holds the composer bar and the notice under it -- the full-width Stop
    // that stood above the bar is gone, and stopping is what the bar's own control does while the
    // agent works and the field is empty.
    //
    // THESE DRIVE A SURFACE OF THEIR OWN. `PhoneRuntime.phone()` answers Unavailable on every JVM
    // run, so the Activity's surface can never open a drill-down; a surface built here
    // (`SettingsSurfaceReadTest`'s arrangement) still owns the same long-lived controls, and its
    // draw path is the production one.
    // -----------------------------------------------------------------------

    /** W3.1: the region is the bar and its notice; nothing stands above the bar any more. */
    @Test
    fun `the composer region is the bar and its notice`() {
        withSurface { _, surface ->
            val region = surface.composerRegion()
            assertEquals(
                "the pinned region under the conversation holds more than the composer bar and " +
                    "the notice under it, so something is still standing between the transcript " +
                    "and the field -- the full-width Stop the one-button ruling removes. Its " +
                    "children were:\n" +
                    (0 until region.childCount).joinToString("\n") {
                        region.getChildAt(it).javaClass.simpleName
                    },
                2,
                region.childCount,
            )
        }
    }

    /**
     * W3.2: the glyph and the spoken word follow ONE predicate -- the session works
     * ([SessionDetailPanel.composerWorking], the same fact the header and the placeholder read)
     * and the field is empty -- on every draw.
     */
    @Test
    fun `the one composer control speaks Send when idle and Stop when the session works and the field is empty`() {
        withSurface { _, surface ->
            val action = surface.composerAction()
            assertEquals(
                "a surface with no conversation drawn does not offer Send, so the control has " +
                    "no words before the first panel lands",
                SessionDetailScreen.COMPOSER_SEND,
                action.contentDescription?.toString(),
            )
            assertEquals(ComposerActionGlyph.SEND.ordinal, action.drawable.level)

            surface.drawDetail(panelWith(openTurn))
            assertEquals(
                "the session works and the field is empty, and the control still says Send: " +
                    "the one thing a reader can do to a working agent from here is stop it",
                SessionDetailScreen.COMPOSER_STOP,
                action.contentDescription?.toString(),
            )
            assertEquals(ComposerActionGlyph.STOP.ordinal, action.drawable.level)

            surface.drawDetail(panelWith(closedTurn))
            assertEquals(
                "the turn closed and the control still says Stop, so it offers to interrupt an " +
                    "agent that is not doing anything",
                SessionDetailScreen.COMPOSER_SEND,
                action.contentDescription?.toString(),
            )
            assertEquals(ComposerActionGlyph.SEND.ordinal, action.drawable.level)
        }
    }

    /**
     * W3.2's "Done when": a fast typist's tap never aborts the agent. The field is read LIVE, on
     * every text change, so the moment there is a draft the control is Send again -- and the
     * moment the draft is cleared it is Stop again.
     */
    @Test
    fun `typing into the field turns Stop back into Send`() {
        withSurface { _, surface ->
            val action = surface.composerAction()
            surface.drawDetail(panelWith(openTurn))
            assertEquals(SessionDetailScreen.COMPOSER_STOP, action.contentDescription?.toString())

            surface.composerField().setText("keep going, but skip the tests")
            assertEquals(
                "there is a draft in the field and the control still says Stop, so a typist " +
                    "who taps after their first word interrupts the agent instead of sending",
                SessionDetailScreen.COMPOSER_SEND,
                action.contentDescription?.toString(),
            )
            assertEquals(ComposerActionGlyph.SEND.ordinal, action.drawable.level)

            surface.composerField().setText("")
            assertEquals(SessionDetailScreen.COMPOSER_STOP, action.contentDescription?.toString())
            assertEquals(ComposerActionGlyph.STOP.ordinal, action.drawable.level)
        }
    }

    @Test
    fun `every unavailable chat state keeps the inline composer shell but disables both controls`() {
        withSurface { _, surface ->
            data class Case(
                val name: String,
                val panel: SessionDetailPanel,
                val placeholder: String,
                val detail: String,
            )

            val gap = item("g1", "structured_gap", turn = "", status = "completed")
            val cases = listOf(
                Case(
                    "offline",
                    panelWith(closedTurn, online = false),
                    "Not connected.",
                    "Reconnect to send.",
                ),
                Case(
                    "chat off",
                    panelWith(closedTurn, structuredChat = false),
                    "Chat is off for this session.",
                    "Reply on your computer.",
                ),
                Case(
                    "ended",
                    panelWith(closedTurn, ended = true),
                    "This session has ended",
                    "",
                ),
            )

            for (case in cases) {
                surface.drawDetail(case.panel)
                assertFalse("${case.name} leaves the composer field enabled", surface.composerField().isEnabled)
                assertFalse("${case.name} leaves the composer action enabled", surface.composerAction().isEnabled)
                assertEquals(case.placeholder, surface.composerField().hint?.toString())
                assertTrue(
                    "${case.name} has no inline composer explanation",
                    surface.composerRegion().flatten()
                        .filterIsInstance<TextView>()
                        .any { it.text.toString() == case.detail },
                )
            }

            surface.drawDetail(panelWith(closedTurn + gap, structuredChat = true))
            assertTrue("a retained-history gap disabled the live composer field", surface.composerField().isEnabled)
            assertTrue("a retained-history gap disabled the live composer action", surface.composerAction().isEnabled)

            surface.drawDetail(panelWith(closedTurn))
            assertTrue("AVAILABLE did not re-enable the composer field", surface.composerField().isEnabled)
            assertTrue("AVAILABLE did not re-enable the composer action", surface.composerAction().isEnabled)
        }
    }

    @Test
    fun `only a visible composer tail reserves the named bottom air`() {
        withSurface { activity, surface ->
            val region = surface.composerRegion()
            val expected = activity.resources.getDimensionPixelSize(R.dimen.swarm_space_8)

            surface.drawDetail(panelWith(closedTurn, structuredChat = false))
            assertEquals(
                "the no-chat helper ends on the system-navigation edge with no breathing room",
                expected,
                region.paddingBottom,
            )

            surface.drawDetail(panelWith(openTurn))
            surface.drawStopped(session)
            assertEquals(
                "the dynamically appended Stopped notice inherited the same clipped bottom edge",
                expected,
                region.paddingBottom,
            )

            surface.drawDetail(panelWith(closedTurn))
            assertEquals("an available empty composer grew an unrequested bottom gutter", 0, region.paddingBottom)

            surface.drawDetail(panelWith(closedTurn, ended = true))
            assertEquals("an ended composer with no helper grew an empty bottom gutter", 0, region.paddingBottom)
        }
    }

    @Test
    fun `the no-chat helper clears both navigation and IME floors by the composer step`() {
        withSurface { activity, surface ->
            surface.drawDetail(panelWith(closedTurn, structuredChat = false))
            val region = surface.composerRegion()
            val helper = region.flatten()
                .filterIsInstance<TextView>()
                .single { it.text.toString() == "Reply on your computer." }
            val air = activity.resources.getDimensionPixelSize(R.dimen.swarm_space_8)

            for ((name, bars, ime) in listOf(Triple("navigation", 135, 0), Triple("IME", 135, 900))) {
                (region.parent as? ViewGroup)?.removeView(region)
                val scaffold = conversationScaffoldView(
                    context = activity,
                    header = View(activity),
                    content = View(activity),
                    composer = region,
                )
                // PhoneActivity has already removed this floor from the root's usable height.
                // Measure the production conversation scaffold in that exact remaining frame;
                // doing inset arithmetic a second time here would test a double-inset layout the
                // app deliberately does not build.
                val usableHeight = 2340 - bottomInsetPx(barsBottomPx = bars, imeBottomPx = ime)
                scaffold.measure(exactly(1080), exactly(usableHeight))
                scaffold.layout(0, 0, 1080, usableHeight)

                assertEquals(
                    "$name inset leaves the helper flush with the usable bottom instead of " +
                        "the composer's named 8dp tail air",
                    air,
                    region.height - helper.bottom,
                )
            }
        }
    }

    @Test
    fun `a live capability redraw recovers the composer in place and keeps the visible gap`() {
        withSurface { _, surface ->
            val gap = item("g-live", "structured_gap", turn = "", status = "completed")
            val disabled = panelWith(closedTurn + gap, structuredChat = false)
            val recovered = panelWith(closedTurn + gap, structuredChat = true)

            surface.drawDetail(disabled)
            val sameField = surface.composerField()
            try {
                assertFalse(sameField.isEnabled)
                assertTrue(
                    "the history gap was not visible before capability recovery",
                    surface.drawnDetailContent().flatten().any { it.tag == TranscriptTag.GAP },
                )

                // A stale-instance transition or a terminal-grant-shaped transition is rejected
                // in phonecore, so the screen receives the same disabled capability facts on its
                // event-driven redraw. The ordinary composer shell and gap must remain unchanged.
                PhoneEvents.observe { surface.drawDetail(disabled) }
                PhoneEvents.onEvent(null)
                shadowOf(Looper.getMainLooper()).idle()
                assertFalse("a rejected capability transition enabled the composer", sameField.isEnabled)
                assertTrue(surface.drawnDetailContent().flatten().any { it.tag == TranscriptTag.GAP })

                // Once the same-instance chat-plane recovery is accepted, the exact same event path
                // re-reads the panel and enables the existing composer without dropping the tear.
                PhoneEvents.observe { surface.drawDetail(recovered) }
                PhoneEvents.onEvent(null)
                shadowOf(Looper.getMainLooper()).idle()
                assertTrue("accepted recovery did not enable the composer in place", sameField.isEnabled)
                assertTrue("recovery rebuilt or erased the visible history gap", surface.drawnDetailContent().flatten().any {
                    it.tag == TranscriptTag.GAP
                })

                // Capability recovery never outruns lifecycle state.
                PhoneEvents.observe {
                    surface.drawDetail(panelWith(closedTurn + gap, structuredChat = true, online = false))
                }
                PhoneEvents.onEvent(null)
                shadowOf(Looper.getMainLooper()).idle()
                assertFalse("recovered capability overrode OFFLINE", sameField.isEnabled)
            } finally {
                PhoneEvents.stopObserving()
            }
        }
    }

    /**
     * W3.3: no confirmation before Stop. The press goes straight to the plan; `confirmThenPress`
     * opens its dialog only for a control that passes an `ask`, and the square passes none.
     *
     * WHAT THIS CANNOT SEE, SAID PLAINLY: the verb. `PhoneRuntime.phone()` answers Unavailable
     * here, so the press stops at the runtime gate before any plan runs; that `app.interrupt(`
     * is what the plan reaches is `android/gate/w34_onebutton_test.go`'s and r6's to read.
     */
    @Test
    fun `pressing the square while the session works interrupts without asking`() {
        withSurface { _, surface ->
            val action = surface.composerAction()
            surface.drawDetail(panelWith(openTurn))
            assertEquals(SessionDetailScreen.COMPOSER_STOP, action.contentDescription?.toString())

            action.performClick()

            assertNull(
                "a question stood between the press and the interrupt. Owner ruling (W3.3): the " +
                    "square resolves the model's CONFIRM arm directly, and a dialog over a " +
                    "working agent is a second tap the ruling removed",
                ShadowDialog.getLatestDialog(),
            )
        }
    }

    /**
     * Review round (W3, 2026-08-28): "STOPPED" OUTLIVES THE SETTLE. The settle that adds the
     * notice runs inside the dispatch that then calls `render()`, and a working agent appends an
     * item at output rate, so a notice the NEXT region draw took off was never on screen for a
     * frame (and when the outcome line was non-empty at press time, the full draw path took it
     * off in the same dispatch). The word is said over a TURN -- the one the panel showed open
     * when the interrupt was sealed -- and it stays while that turn is the open one.
     *
     * THE SETTLE'S HALF IS CALLED DIRECTLY ([PhoneSurface.drawStopped]): `press` stops at the
     * runtime gate on every JVM (`PhoneRuntime.phone()` answers Unavailable) and `Op` is the
     * AAR's, so no press here can reach `rememberInterrupt`. That the settle calls it, and calls
     * nothing that toasts, is `android/gate/w34_onebutton_test.go`'s to read.
     */
    @Test
    fun `Stopped stays under the composer while the turn it was said over is open`() {
        withSurface { _, surface ->
            surface.drawDetail(panelWith(openTurn))
            surface.drawStopped(session)
            assertTrue("the sealing said nothing under the composer", surface.composerRegion().saysStopped())

            surface.drawDetail(
                panelWith(openTurn + item("t2", "tool_run", turn = "turn-a", status = "in_progress")),
            )
            assertTrue(
                "the agent wrote one more item inside the same turn and \"Stopped\" was taken " +
                    "off, so over a working agent the word never survives to a frame",
                surface.composerRegion().saysStopped(),
            )
        }
    }

    /** The other half of "said once": the turn it was said over closing is what takes it off. */
    @Test
    fun `Stopped comes off when the turn it was said over closes`() {
        withSurface { _, surface ->
            surface.drawDetail(panelWith(openTurn))
            surface.drawStopped(session)
            assertTrue(surface.composerRegion().saysStopped())

            surface.drawDetail(panelWith(closedTurn))
            assertFalse(
                "the turn closed and \"Stopped\" is still under the composer: said once means " +
                    "cleared when the conversation it reported on has moved",
                surface.composerRegion().saysStopped(),
            )
        }
    }

    /**
     * Residual (review re-check, 2026-08-28): THE WORD CARRIES ITS SESSION. The settle can land
     * after the drill-down closed -- the command lane waits on `awaitConn` for up to 5 s on a
     * flapping link, and `VerbDispatch.press` still runs the settle, since `attached` clears only
     * on pause -- or while ANOTHER conversation is drawn. Before this the settle read whatever
     * `detailDrawn` said at that moment: over the inbox it recorded turn "" and the next idle
     * drill-down kept the word ("" == ""); over another working session it put the word under
     * that one. The press knows its session (`interruptPlan`'s `target`); the settle passes it,
     * and the notice is said only under that session and kept only under it.
     */
    @Test
    fun `a Stop that settles after the conversation closed says nothing`() {
        withSurface { _, surface ->
            surface.drawDetail(panelWith(openTurn))
            surface.closeSessionDetail()
            surface.drawStopped(session)

            surface.drawDetail(panelWith(closedTurn))
            assertFalse(
                "the interrupt settled over the inbox and \"Stopped\" greets the reader under a " +
                    "composer that was never stopped: a notice recorded over no turn matches " +
                    "every idle session",
                surface.composerRegion().saysStopped(),
            )
        }
    }

    @Test
    fun `a Stop that settles over another session does not say Stopped there`() {
        withSurface { _, surface ->
            surface.drawDetail(panelWith(openTurnOf(otherSession), of = otherSession))
            surface.drawStopped(session)
            assertFalse(
                "a Stop pressed on one session settled while another was drawn, and the word " +
                    "went under the other one, over a turn nobody stopped",
                surface.composerRegion().saysStopped(),
            )
        }
    }

    @Test
    fun `Stopped comes off when another session is drawn`() {
        withSurface { _, surface ->
            surface.drawDetail(panelWith(openTurn))
            surface.drawStopped(session)
            assertTrue(surface.composerRegion().saysStopped())

            // The other session's open turn carries the same id string: the turn alone cannot
            // tell the two conversations apart.
            surface.drawDetail(panelWith(openTurnOf(otherSession), of = otherSession))
            assertFalse(
                "another conversation was drawn and \"Stopped\" stayed under its composer " +
                    "because its open turn happens to carry the same id",
                surface.composerRegion().saysStopped(),
            )
        }
    }

    /** Whether the pinned region carries the sealing's word, read the way a reader would. */
    private fun ViewGroup.saysStopped(): Boolean = (0 until childCount).any {
        (getChildAt(it) as? TextView)?.text?.toString() == SessionDetail.INTERRUPT_SENT
    }

    // -----------------------------------------------------------------------
    // Building a conversation for the surface to draw: the real screen model over real items,
    // never a hand-made panel.
    // -----------------------------------------------------------------------

    private val session = "mbp/swarm"

    /** A second conversation on the same phone, for the notice's session fence. */
    private val otherSession = "mbp/api"

    private fun item(
        id: String, kind: String, turn: String, status: String = "", of: String = session,
    ) = InteractionItem(
        sessionId = of, itemId = id, cursor = 1, kind = kind,
        status = status, turnId = turn, text = "words",
    )

    /** A turn the agent is still inside: the header says working, and the square owes Stop. */
    private fun openTurnOf(of: String) = listOf(
        item("u1", "user_message", turn = "turn-a", of = of),
        item("t1", "tool_run", turn = "turn-a", status = "in_progress", of = of),
    )

    private val openTurn = openTurnOf(session)

    /** The same turn, closed by its terminal agent_message: idle, and the square owes Send. */
    private val closedTurn = listOf(
        item("u1", "user_message", turn = "turn-a"),
        item("a1", "agent_message", turn = "turn-a", status = "completed"),
    )

    private fun panelWith(
        items: List<InteractionItem>,
        of: String = session,
        online: Boolean = true,
        structuredChat: Boolean = true,
        ended: Boolean = false,
    ): SessionDetailPanel = SessionDetailScreen.of(
        SessionDetail(
            sessionId = of, online = online, journalStale = false, ended = ended,
            title = "claude-swarm", group = "working", machineLabel = "mbp",
        ),
        TranscriptScreen.of(items),
        SessionLease(sessionId = of, online = online),
        capabilities = SessionCapabilityFacts(structuredChat = structuredChat),
    )

    private fun PhoneSurface.composerAction(): ImageView = composerBar().getChildAt(1) as ImageView

    private fun PhoneSurface.composerField(): EditText = composerBar().getChildAt(0) as EditText

    private fun withSurface(assertions: (PhoneActivity, PhoneSurface) -> Unit) {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertions(activity, PhoneSurface(activity, PhoneRuntime(activity), VerbDispatch.direct()))
            }
        }
    }

    /**
     * The composer bar, found by the one thing only it is: the kit's `TopRule` surface around a
     * touch-filtered control. Its parent is the region the conversation pins.
     */
    private fun PhoneSurface.composerBar(): LinearLayout = touchFilteredActions
        .map { it.parent }
        .filterIsInstance<LinearLayout>()
        .first { it.background is TopRule }

    private fun PhoneSurface.composerRegion(): ViewGroup = composerBar().parent as ViewGroup

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }

    private fun exactly(px: Int) = View.MeasureSpec.makeMeasureSpec(px, View.MeasureSpec.EXACTLY)
}
