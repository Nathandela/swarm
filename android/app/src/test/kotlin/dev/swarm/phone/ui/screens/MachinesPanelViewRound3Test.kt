package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for wave R4's D3 round-3 fix pack (bead agents-tracker-0ox9):
 * the switcher AS DRAWN over round 3's findings the JVM can see.
 *
 * COMPILE-RED ON PURPOSE: `MachinesPanelScreen.of` takes no `selected` yet and
 * `MachinesPanelScreen.ADD_LIMITS` does not exist.
 *
 * WHY THE MARK IS ASSERTED AS A RENDERED ROW AND NOT AS A MODEL STRING. MachinesPanelRound3Test
 * freezes what the sentence SAYS; this file asks whether the row a user looks at carries it,
 * which is the half BLOCKING 1 was actually about -- the model could have said anything and the
 * screen still said nothing.
 */
@RunWith(RobolectricTestRunner::class)
class MachinesPanelViewRound3Test {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun row(id: String, name: String) = MachineRowModel(
        machineId = id,
        displayName = name,
        connected = true,
        lastSyncUnixMs = 1_000L,
        needsInput = 0,
    )

    private fun view(
        machines: List<MachineRowModel>,
        selected: String = "",
        nowUnixMs: Long = 10_000L,
        addForm: View? = null,
    ): View = machinesPanelView(
        context = context,
        panel = MachinesPanelScreen.of(machines, cap = 3, selected = selected),
        onAddComputer = {},
        onSwitchComputer = {},
        onForgetComputer = {},
        onOpenGlobalInbox = {},
        addForm = addForm,
        nowUnixMs = nowUnixMs,
    )

    // -------------------------------------------------------------------
    // BLOCKING 1: the switch is visible on the row it switched to.
    // -------------------------------------------------------------------

    @Test
    fun theSelectedRowIsMarkedOnScreenAndTheOthersAreNot() {
        val now = 10_000L
        val rows = listOf(row("m-a", "laptop"), row("m-b", "desk"))
        val screen = view(rows, selected = "m-b", nowUnixMs = now)
        val texts = screen.texts()

        assertTrue(
            "the selected machine's row does not render the model's selected line, so a " +
                "successful switch changes nothing a user can see -- the whole of BLOCKING 1",
            texts.contains(MachinesPanelScreen.statusLine(rows[1], now, selected = true)),
        )
        assertEquals(
            "exactly one row may carry the mark: a mark on every row says nothing, and a mark " +
                "on the wrong row is worse than none",
            1,
            texts.count { it.startsWith(MachinesPanelScreen.SELECTED_MARK) },
        )
        assertTrue(
            "the machine that was NOT selected renders as if it had been",
            texts.contains(MachinesPanelScreen.statusLine(rows[0], now, selected = false)),
        )
    }

    @Test
    fun withNothingSelectedNoRowClaimsToBe() {
        val rows = listOf(row("m-a", "laptop"), row("m-b", "desk"))
        val texts = view(rows, selected = "").texts()
        assertEquals(
            "a panel with no recorded selection marked a row anyway; claiming a machine is " +
                "selected when none was chosen is the dishonest rendering in the other direction",
            0,
            texts.count { it.startsWith(MachinesPanelScreen.SELECTED_MARK) },
        )
    }

    // -------------------------------------------------------------------
    // BLOCKING 2: the add form's own limits are ON SCREEN, under the form.
    // -------------------------------------------------------------------

    @Test
    fun theAddFormCarriesItsLimitsUnderIt() {
        val form = TextView(context).apply { text = "form-probe" }
        val screen = view(listOf(row("m-a", "laptop")), addForm = form)
        // W7.6: the form block is composed by the header's Add action.
        screen.findViewWithTag<View>(MachinesTag.ADD_TOGGLE)!!.performClick()
        val order = screen.flatten()
        val texts = screen.texts()

        assertTrue(
            "nothing on screen says where a machine id comes from, that the added computer " +
                "still needs its own pairing ceremony (bead agents-tracker-ak2s), or that " +
                "switching does not move the live session. A form that cannot be completed and " +
                "does not say so is the overclaim the honesty amendment exists to prevent",
            texts.contains(MachinesPanelScreen.ADD_LIMITS),
        )

        val formAt = order.indexOf(form)
        val limitsAt = order.indexOfFirst {
            it is TextView && it.text.toString() == MachinesPanelScreen.ADD_LIMITS
        }
        assertTrue(
            "the limits sentence is composed ABOVE the add form (form@$formAt limits@$limitsAt); " +
                "the disclosure belongs where the user is typing, under the form it is about -- " +
                "ADR-018's cap sentence is placed by the same rule",
            formAt >= 0 && limitsAt > formAt,
        )
    }

    // -------------------------------------------------------------------
    // Reading the screen: MachinesPanelViewTest's own helpers.
    // -------------------------------------------------------------------

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }

    private fun View.texts(): List<String> =
        flatten().filterIsInstance<TextView>().map { it.text.toString() }
}
