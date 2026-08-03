package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.PairingAttempt
import dev.swarm.phone.ui.PairingStep
import dev.swarm.phone.ui.ScannerState
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for the guided pairing screen AS DRAWN.
 *
 * `PairingGuidanceTest` says the panel MODEL carries the steps, the command and a collapsed
 * fallback. This says they reach the view tree, in the owner's order -- which is the claim the
 * owner could check on a handset and the model suite could not. The distinction is not academic
 * here: the defect being fixed (agents-tracker-qx9m) was a screen whose model was right and whose
 * rendering offered the user a bare paste field.
 */
@RunWith(RobolectricTestRunner::class)
class PairingGuidanceViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun panel(
        step: PairingStep = PairingStep.SCAN,
        holding: Boolean = false,
        scanner: ScannerState = ScannerState.PERMISSION_DENIED,
        revealed: Boolean = false,
    ) = PairingPanelScreen.of(
        attempt = PairingAttempt(
            step = step,
            originShown = "",
            originIsLocalNetwork = false,
            explainsInterruptedAttempt = false,
        ),
        scanner = scanner,
        sas = null,
        holding = holding,
        machine = "",
        manualEntryRevealed = revealed,
    )

    /** One view per slot and one per control, all distinct, so an assertion can tell them apart. */
    private class Stubs(context: Context) {
        val controls: Map<PairingControl, View> =
            PairingControl.entries.associateWith { View(context) }

        val slots = PairingSlots(
            body = View(context),
            notice = View(context),
            destination = View(context),
            sas = View(context),
            sasInstruction = View(context),
            scanner = View(context),
            controls = controls,
        )
    }

    private fun draw(
        step: PairingStep = PairingStep.SCAN,
        holding: Boolean = false,
        scanner: ScannerState = ScannerState.PERMISSION_DENIED,
        revealed: Boolean = false,
    ): View = pairingPanelView(
        context,
        panel(step, holding, scanner, revealed),
        Stubs(context).slots,
    )

    /** Every tagged view in the tree, in the order the composition added them. */
    private fun View.tags(): List<String> {
        val found = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { found += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun texts(root: View, tag: String): List<String> {
        val found = mutableListOf<String>()
        fun walk(v: View) {
            if (v.tag == tag && v is TextView) found += v.text.toString()
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)
        return found
    }

    // ---- the steps reach the screen ----------------------------------------

    @Test
    fun `both steps are on screen, in the order they are numbered`() {
        val root = draw()

        assertNotNull("the guided steps are not in the tree at all", root.kitFind(PairingTag.STEPS))
        assertEquals(listOf("1", "2"), texts(root, KitTag.STEP_ORDINAL))
        assertEquals(
            PairingPanelScreen.GUIDANCE.map { it.line },
            texts(root, KitTag.STEP_LINE),
        )
    }

    @Test
    fun `the command is on screen in a mono well`() {
        // Row 18's own instruction: "Command line reuses the `.cmd` mono well verbatim ... so every
        // mono block in the app is one component". A command a person has to retype into a shell is
        // exactly the string a proportional face makes hard to read character by character.
        val root = draw()

        assertEquals(
            listOf(PairingPanelScreen.COMMAND),
            texts(root, KitTag.MONO_WELL),
        )
    }

    @Test
    fun `the steps come before the control that acts on them`() {
        // The order is the owner's: read what to do, then do it. A CTA above its own instructions
        // is a button a person presses before they have been told what it needs.
        val order = draw().tags()
        val steps = order.indexOf(PairingTag.STEPS)
        val scan = order.indexOf(PairingTag.control(PairingControl.SCAN))

        assertTrue("the steps are not on screen", steps >= 0)
        assertTrue("the scan control is not on screen", scan >= 0)
        assertTrue("the scan CTA is drawn above the steps that explain it", steps < scan)
    }

    @Test
    fun `the steps sit under the heading and the step's own sentence`() {
        val order = draw().tags()

        assertTrue(
            "the guided steps displaced the heading or the sentence PB-PAIR-5 argued",
            order.indexOf(PairingTag.NAV) < order.indexOf(PairingTag.BODY) &&
                order.indexOf(PairingTag.BODY) < order.indexOf(PairingTag.STEPS),
        )
    }

    @Test
    fun `a step with no guidance draws no empty steps block`() {
        // An empty container is the version of this that renders as a gap nobody can explain.
        assertNull(
            "the steps block was drawn over a live handshake, which has no steps",
            draw(PairingStep.HANDSHAKING, holding = true).kitFind(PairingTag.STEPS),
        )
    }

    // ---- the primary control, and the quiet one ----------------------------

    @Test
    fun `the scan control leads and the reveal control follows it`() {
        // "Scan QR code" is the hero CTA and "Enter code instead" is the quieter thing beneath it.
        // The composition walks `PairingControl.entries`, so this is an assertion about the ENUM's
        // order, which is the only thing that decides it -- `PairingPanel.controls` is a Set.
        val order = draw().tags().filter { it.startsWith("pairing.control.") }

        assertEquals(
            listOf(
                PairingTag.control(PairingControl.SCAN),
                PairingTag.control(PairingControl.REVEAL_TYPED_PAYLOAD),
            ),
            order,
        )
    }

    @Test
    fun `the paste field is not in the tree until it is revealed`() {
        val collapsed = draw()

        assertNull(
            "the paste field is on screen before anyone asked for it",
            collapsed.kitFind(PairingTag.control(PairingControl.TYPED_PAYLOAD)),
        )
        assertNull(
            collapsed.kitFind(PairingTag.control(PairingControl.USE_TYPED_PAYLOAD)),
        )

        val revealed = draw(revealed = true)
        assertNotNull(
            "the reveal control leads nowhere",
            revealed.kitFind(PairingTag.control(PairingControl.TYPED_PAYLOAD)),
        )
        assertNotNull(
            revealed.kitFind(PairingTag.control(PairingControl.USE_TYPED_PAYLOAD)),
        )
    }

    @Test
    fun `the field and its button keep the scanner above them once revealed`() {
        val order = draw(revealed = true).tags().filter { it.startsWith("pairing.control.") }

        assertEquals(
            listOf(
                PairingTag.control(PairingControl.SCAN),
                PairingTag.control(PairingControl.TYPED_PAYLOAD),
                PairingTag.control(PairingControl.USE_TYPED_PAYLOAD),
            ),
            order,
        )
    }

    // ---- the permanently denied camera --------------------------------------

    @Test
    fun `a permanently denied camera draws the settings route where the scanner was`() {
        val root = draw(scanner = ScannerState.PERMISSION_PERMANENTLY_DENIED)
        val order = root.tags().filter { it.startsWith("pairing.control.") }

        assertEquals(
            "the settings route is not where the withdrawn scan control was, so the screen reads " +
                "as one that lost a button rather than one that swapped it",
            listOf(
                PairingTag.control(PairingControl.OPEN_SYSTEM_SETTINGS),
                PairingTag.control(PairingControl.TYPED_PAYLOAD),
                PairingTag.control(PairingControl.USE_TYPED_PAYLOAD),
            ),
            order,
        )
    }

    @Test
    fun `the reason the scanner is gone is on screen above the route that fixes it`() {
        val root = draw(scanner = ScannerState.PERMISSION_PERMANENTLY_DENIED)
        val order = root.tags()

        assertNotNull(
            "a permanently denied camera swapped the button and said nothing about why",
            root.kitFind(PairingTag.CAMERA_NOTICE),
        )
        assertTrue(
            "the explanation is drawn below the button it explains",
            order.indexOf(PairingTag.CAMERA_NOTICE) <
                order.indexOf(PairingTag.control(PairingControl.OPEN_SYSTEM_SETTINGS)),
        )
    }

    @Test
    fun `no explanation is drawn for a camera nothing is wrong with`() {
        listOf(ScannerState.SCANNING, ScannerState.PERMISSION_DENIED).forEach { state ->
            assertNull(
                "$state drew a sentence about a blocked camera, which is a warning nobody wrote",
                draw(scanner = state).kitFind(PairingTag.CAMERA_NOTICE),
            )
        }
    }

    /** The negative control: the readers these assertions depend on can actually miss. */
    @Test
    fun `the guided view assertions can actually fail`() {
        val root = draw()

        assertEquals(
            "the ordinal reader answers for a tag no view carries, so every list above could be " +
                "reading whatever it happened to reach",
            emptyList<String>(),
            texts(root, "no view carries this"),
        )
        assertNull(root.kitFind("no view carries this"))
    }
}
