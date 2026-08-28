package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.kit.KitTag
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for wave R4's D3 round-2 fix pack (bead agents-tracker-0ox9):
 * the switcher AS DRAWN, over the review findings the JVM can see.
 *
 * COMPILE-RED ON PURPOSE: `machinesPanelView` takes no `nowUnixMs` yet, so the whole file fails
 * to compile until the clock seam exists -- the same seam PhoneSurface already spends into
 * `SyncStatus.of` one screen over, taken as a parameter so this suite can freeze the words
 * without a clock (MachineFreshness's own arrangement).
 *
 * TWO OF THESE TESTS PIN EXISTING BEHAVIOUR RATHER THAN DEMAND NEW -- the `addForm` slot and
 * `onBack` (review observation (d): neither had a behavioural test). They are expected to pass
 * the moment the file compiles, and that is disclosed here rather than discovered later: what
 * they buy is that the round-2 surgery on the same file cannot regress either seam unnoticed.
 */
@RunWith(RobolectricTestRunner::class)
class MachinesPanelViewRound2Test {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun row(
        id: String,
        connected: Boolean = true,
        lastSyncUnixMs: Long = 1_000L,
    ) = MachineRowModel(
        machineId = id,
        displayName = "laptop",
        connected = connected,
        lastSyncUnixMs = lastSyncUnixMs,
        needsInput = 0,
    )

    private fun view(
        machines: List<MachineRowModel>,
        nowUnixMs: Long,
        onBack: (() -> Unit)? = null,
        addForm: View? = null,
    ): View = machinesPanelView(
        context = context,
        panel = MachinesPanelScreen.of(machines, cap = 3),
        onAddComputer = {},
        onSwitchComputer = {},
        onForgetComputer = {},
        onOpenGlobalInbox = {},
        onBack = onBack,
        addForm = addForm,
        nowUnixMs = nowUnixMs,
    )

    // -------------------------------------------------------------------
    // The fourth row fact reaches the screen (review finding 5).
    // -------------------------------------------------------------------

    @Test
    fun aParkedRowRendersItsLastSyncAge() {
        val now = 10_000_000_000L
        val threeDays = 3 * 24 * 60 * 60_000L
        val screen = view(
            listOf(row("m-a", connected = false, lastSyncUnixMs = now - threeDays)),
            nowUnixMs = now,
        )
        assertTrue(
            "the parked row's last-sync age is nowhere on screen; ADR-018 MM3 and playbook " +
                "4.2:200-202 require a parked row to VISIBLY show it, and MachineRowModel has " +
                "carried lastSyncUnixMs for exactly this since the model landed",
            screen.texts().any { it.contains("synced 3d ago") },
        )
    }

    // -------------------------------------------------------------------
    // Review observation (d), pinned: the named addForm slot composes, and the
    // chevron fires onBack.
    // -------------------------------------------------------------------

    @Test
    fun theAddFormSlotHostsTheFormItWasHanded() {
        val form = TextView(context).apply { text = "form-probe" }
        val screen = view(listOf(row("m-a")), nowUnixMs = 10_000L, addForm = form)
        // W7.6: the form block is composed by the header's Add action.
        screen.findViewWithTag<View>(MachinesTag.ADD_TOGGLE)!!.performClick()
        assertTrue(
            "the addForm handed to the NAMED slot is not in the composed tree; the slot " +
                "exists so the surface-owned fields survive a redraw, and a slot that drops " +
                "its view is the anonymous-burial defect with extra steps",
            screen.flatten().contains(form),
        )
    }

    @Test
    fun theChevronFiresOnBack() {
        var backed = 0
        val screen = view(listOf(row("m-a")), nowUnixMs = 10_000L, onBack = { backed++ })
        val chevron = screen.findViewWithTag<View>(KitTag.DRILL_BACK)
        assertNotNull(
            "no drill-back chevron on the switcher although onBack was wired; a drill-down " +
                "with no way back but the system gesture strands every user who navigates by " +
                "touch",
            chevron,
        )
        chevron!!.performClick()
        assertEquals("tapping the chevron did not fire onBack", 1, backed)
    }

    // -------------------------------------------------------------------
    // Reading the screen: MachinesPanelViewTest's own helpers, kept private there
    // and here alike.
    // -------------------------------------------------------------------

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }

    private fun View.texts(): List<String> =
        flatten().filterIsInstance<TextView>().map { it.text.toString() }
}
