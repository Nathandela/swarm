package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.PermissionStateResolver
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.PairingFlow
import dev.swarm.phone.ui.PairingAttempt
import dev.swarm.phone.ui.PairingStep
import dev.swarm.phone.ui.SasStep
import dev.swarm.phone.ui.ScannerState
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-9 over the pairing screen AS DRAWN.
 *
 * THE ASSERTION THAT MATTERS MOST IS THE ORDER OF THE TWO SAS ANSWERS. [PairingPanel.controls] is
 * a Set, and a set has no order: a composition that walked it directly would put "They match" and
 * "They do not match" on screen in whatever order the implementation happened to insert them, and
 * that order could change between two draws of the same step. After ADR-007 B133 that comparison
 * is the only human-in-the-loop security check left in the product, so a mis-tap there is not a
 * usability defect.
 *
 * WHAT IS NOT ASSERTED: appearance, for the reason `SettingsPanelViewTest` gives -- and here for a
 * second reason worth stating. The pairing screen's five specified components (derivation rows 7,
 * 9, 10, 18 and the mono well) have no kit factory, so the views this composition arranges are
 * the surface's own. Their look is not this file's subject and is not yet anybody's.
 */
@RunWith(RobolectricTestRunner::class)
class PairingPanelViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun panel(
        step: PairingStep,
        holding: Boolean,
        scanner: ScannerState = ScannerState.SCANNING,
        sas: SasStep? = null,
        origin: String = "",
        interrupted: Boolean = false,
        machine: String = "",
        // A phone that has paired before; the relay ask is `PairingGuidanceViewTest`'s subject.
        relayKnown: Boolean = true,
    ) = PairingPanelScreen.of(
        attempt = PairingAttempt(
            step = step,
            originShown = origin,
            originIsLocalNetwork = false,
            explainsInterruptedAttempt = interrupted,
        ),
        scanner = scanner,
        sas = sas,
        holding = holding,
        machine = machine,
        relayKnown = relayKnown,
    )

    /**
     * One view per slot and one per control, all distinct, so an assertion can tell which of them
     * reached the screen.
     */
    private class Stubs(context: Context) {
        val body = View(context)
        val notice = View(context)
        val destination = View(context)
        val sas = View(context)
        val sasInstruction = View(context)
        val scanner = View(context)
        val scanProgress = View(context)
        val controls: Map<PairingControl, View> =
            PairingControl.entries.associateWith { View(context) }

        val slots = PairingSlots(
            body = body,
            notice = notice,
            destination = destination,
            sas = sas,
            sasInstruction = sasInstruction,
            scanner = scanner,
            scanProgress = scanProgress,
            controls = controls,
        )
    }

    private fun View.tags(): List<String> {
        val found = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { found += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    // ---- the composition ---------------------------------------------------

    @Test
    fun `the step title is drawn by the kit's nav header`() {
        val stubs = Stubs(context)
        val root = pairingPanelView(context, panel(PairingStep.SCAN, holding = false), stubs.slots)

        assertEquals(
            "Pair a computer",
            (root.kitRequire(PairingTag.NAV).kitRequire(KitTag.TITLE) as android.widget.TextView)
                .text.toString(),
        )
    }

    @Test
    fun `a step the artifact never drew renders no heading at all`() {
        val stubs = Stubs(context)
        val root = pairingPanelView(
            context,
            panel(PairingStep.HANDSHAKING, holding = true),
            stubs.slots,
        )

        assertNull(
            "a heading was drawn for a step the design records none for, which puts a second, " +
                "shorter statement of the step above the sentence PB-PAIR-5 argued",
            root.kitFind(PairingTag.NAV),
        )
        assertNotNull("the step's own sentence is not on screen", root.kitFind(PairingTag.BODY))
    }

    @Test
    fun `the destination is on screen only in the step that shows one`() {
        val stubs = Stubs(context)
        val confirming = pairingPanelView(
            context,
            panel(PairingStep.CONFIRM_DESTINATION, holding = true, origin = "wss://relay.example"),
            stubs.slots,
        )
        assertNotNull(confirming.kitFind(PairingTag.DESTINATION))

        val handshaking = pairingPanelView(
            context,
            panel(PairingStep.HANDSHAKING, holding = true, origin = "wss://relay.example"),
            Stubs(context).slots,
        )
        assertNull(
            "PB-PAIR-6's destination survived into a step the confirm control is no longer about",
            handshaking.kitFind(PairingTag.DESTINATION),
        )
    }

    @Test
    fun `the symbols and their instruction arrive together or not at all`() {
        val stubs = Stubs(context)
        val comparing = pairingPanelView(
            context,
            panel(
                PairingStep.COMPARING_CODES,
                holding = true,
                sas = SasStep("owl microscope galaxy anchor lock key"),
            ),
            stubs.slots,
        )

        assertNotNull(comparing.kitFind(PairingTag.SAS))
        assertNotNull(
            "the symbols are on screen with no sentence telling the user what to do with them",
            comparing.kitFind(PairingTag.SAS_INSTRUCTION),
        )

        val notYet = pairingPanelView(
            context,
            panel(PairingStep.COMPARING_CODES, holding = true, sas = null),
            Stubs(context).slots,
        )
        assertNull(notYet.kitFind(PairingTag.SAS))
        assertNull(notYet.kitFind(PairingTag.SAS_INSTRUCTION))
    }

    @Test
    fun `a notice is on screen only when the step has one to make`() {
        assertNotNull(
            pairingPanelView(
                context,
                panel(PairingStep.SCAN, holding = false, interrupted = true),
                Stubs(context).slots,
            ).kitFind(PairingTag.NOTICE),
        )
        assertNull(
            "an empty notice line was drawn, which reads as a warning nobody wrote",
            pairingPanelView(
                context,
                panel(PairingStep.SCAN, holding = false),
                Stubs(context).slots,
            ).kitFind(PairingTag.NOTICE),
        )
    }

    // ---- the controls ------------------------------------------------------

    @Test
    fun `exactly the controls the step offers are on screen`() {
        val stubs = Stubs(context)
        val page = panel(
            PairingStep.SCAN,
            holding = false,
            scanner = ScannerState.PERMISSION_PERMANENTLY_DENIED,
        )
        val root = pairingPanelView(context, page, stubs.slots)
        val onScreen = PairingControl.entries
            .filter { root.kitFind(PairingTag.control(it)) != null }
            .toSet()

        assertEquals(page.controls, onScreen)
        assertTrue(
            "a permanently denied camera still offers the scanner",
            PairingControl.SCAN !in onScreen,
        )
    }

    @Test
    fun `a fresh install has the scan button ON SCREEN`() {
        // THE FIELD REPORT WAS ABOUT WHAT WAS DRAWN (agents-tracker-qx9m): "i don't see any open
        // camera for qr code scanning button whatsoever. only enter code". The model test says the
        // step offers the control; this says the control reaches the tree, which is the claim the
        // owner could check on a handset and the suite could not.
        //
        // THE CAMERA STATE IS RESOLVED, NEVER NAMED. Passing `ScannerState` by hand is what every
        // test in this file does and is exactly how this shipped: nothing asked what a phone that
        // has never been asked for the permission actually gets.
        val freshInstall = PairingFlow.scannerState(
            PermissionStateResolver.resolve(
                permission = AppPermission.CAMERA,
                sdkInt = 35,
                granted = false,
                hasAskedBefore = false,
                showRationale = false,
            ),
        )
        val root = pairingPanelView(
            context,
            panel(PairingStep.SCAN, holding = false, scanner = freshInstall),
            Stubs(context).slots,
        )

        assertNotNull(
            "the scan button is not on screen for a phone that has never been asked for the " +
                "camera, so the only control that can request the permission is undrawable and " +
                "QR pairing is unreachable for the life of the install",
            root.kitFind(PairingTag.control(PairingControl.SCAN)),
        )
        // REACHABLE ON SCREEN, WHICH IS WHAT PB-PAIR-2 ASKS FOR. The guided screen draws the paste
        // path as a quieter "Enter code instead" disclosure rather than an expanded field, so the
        // assertion is that SOMETHING reaching the typed path is in the tree -- not that the field
        // itself is. A collapsed fallback one labelled tap away is not the dead end; a fallback
        // with no affordance at all would be.
        assertTrue(
            "the paste fallback went with it: neither the field nor the control that reveals it " +
                "is on screen, so a phone whose camera cannot decode has no way to pair at all",
            root.kitFind(PairingTag.control(PairingControl.REVEAL_TYPED_PAYLOAD)) != null ||
                root.kitFind(PairingTag.control(PairingControl.TYPED_PAYLOAD)) != null,
        )
    }

    @Test
    fun `the two SAS answers are always in the same order`() {
        // A Set has no order. Composing it directly would let "They match" and "They do not
        // match" swap places between two draws of the same step, and after ADR-007 B133 that
        // comparison is the only human-in-the-loop security check left in the product.
        val root = pairingPanelView(
            context,
            panel(
                PairingStep.COMPARING_CODES,
                holding = true,
                sas = SasStep("owl microscope galaxy anchor lock key"),
            ),
            Stubs(context).slots,
        )
        val order = root.tags().filter { it.startsWith("pairing.control.") }

        assertEquals(
            listOf(
                PairingTag.control(PairingControl.CODES_MATCH),
                PairingTag.control(PairingControl.CODES_DO_NOT_MATCH),
                PairingTag.control(PairingControl.STOP),
            ),
            order,
        )
    }

    @Test
    fun `a control the step offers and the surface cannot supply fails loudly`() {
        // Silently drawing a shorter screen is the alternative, and on this screen the control
        // that would go missing is "They do not match" -- the only signal this protocol has for
        // a man-in-the-middle.
        val stubs = Stubs(context)
        val crippled = PairingSlots(
            body = stubs.body,
            notice = stubs.notice,
            destination = stubs.destination,
            sas = stubs.sas,
            sasInstruction = stubs.sasInstruction,
            scanner = stubs.scanner,
            scanProgress = stubs.scanProgress,
            controls = stubs.controls.filterKeys { it != PairingControl.CODES_DO_NOT_MATCH },
        )
        val page = panel(
            PairingStep.COMPARING_CODES,
            holding = true,
            sas = SasStep("owl microscope galaxy anchor lock key"),
        )

        val failure = try {
            pairingPanelView(context, page, crippled)
            null
        } catch (expected: IllegalArgumentException) {
            expected
        }
        assertNotNull("the panel drew a comparison with one answer missing", failure)
    }

    // ---- the remainder ------------------------------------------------------

    @Test
    fun `the scanner stays in the tree, because whether it shows is the camera's business`() {
        val stubs = Stubs(context)
        val root = pairingPanelView(
            context,
            panel(PairingStep.HANDSHAKING, holding = true),
            stubs.slots,
        )

        assertNotNull(root.kitFind(PairingTag.SCANNER))
    }

    @Test
    fun `what this slice has not recomposed is hosted under the panel`() {
        val trailing = View(context)
        val root = pairingPanelView(
            context,
            panel(PairingStep.SCAN, holding = false),
            Stubs(context).slots,
            below = trailing,
        ) as ViewGroup

        assertSame(trailing, root.getChildAt(root.childCount - 1))
    }
}
