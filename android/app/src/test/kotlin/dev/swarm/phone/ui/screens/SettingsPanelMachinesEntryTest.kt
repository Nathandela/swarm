package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.SettingsScreen
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * The machine-switcher ENTRY as drawn on the Settings panel (bead agents-tracker-0ox9, round-2
 * review observation (c)): the Go gate asserts SOME production file spends ENTRY_LABEL, and
 * nothing drove `settingsPanelView(onOpenMachines = ...)` to check the row composes and fires.
 * This file is that behavioural half.
 *
 * IT PINS EXISTING BEHAVIOUR RATHER THAN DEMANDING NEW -- the entry landed with the round-1
 * composition -- and that is disclosed here (the round-2 RED evidence records it passing on
 * first compile). What it buys is the FINDABLE property as a behavioural fact: the entry is on
 * the screen by its recorded name, it takes a tap, and a null wiring composes no dead control.
 */
@RunWith(RobolectricTestRunner::class)
class SettingsPanelMachinesEntryTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun view(onOpenMachines: (() -> Unit)? = null): View = settingsPanelView(
        context = context,
        panel = SettingsPanelScreen.of(SettingsScreen(alerts = true, mentions = true)),
        rowFor = { View(context) },
        onOpenMachines = onOpenMachines,
    )

    @Test
    fun theEntryComposesByItsRecordedNameAndFires() {
        var opened = 0
        val screen = view(onOpenMachines = { opened++ })

        val label = screen.flatten().filterIsInstance<TextView>()
            .firstOrNull { it.text.toString() == MachinesPanelScreen.ENTRY_LABEL }
        assertNotNull(
            "no view on the Settings panel reads '${MachinesPanelScreen.ENTRY_LABEL}'; a " +
                "control that exists must be findable by its recorded name (defect shape 1, " +
                "agents-tracker-64rf)",
            label,
        )

        var control: View? = label
        while (control != null && !control.hasOnClickListeners()) {
            control = control.parent as? View
        }
        assertNotNull(
            "'${MachinesPanelScreen.ENTRY_LABEL}' is on screen but neither it nor any " +
                "ancestor takes a tap; a label with nothing behind it is decoration",
            control,
        )
        control!!.performClick()
        assertEquals("tapping the entry did not fire onOpenMachines", 1, opened)
    }

    @Test
    fun anUnwiredPanelComposesNoDeadEntry() {
        // `onOpenMachines = null` is the JVM suite's own world (no navigation to wire); the
        // panel must draw no control that goes nowhere -- navHeaderDrill(back = null)'s ruling.
        val screen = view(onOpenMachines = null)
        assertTrue(
            "the entry row composed with nothing behind it; a control that cannot act is a " +
                "dead affordance, worse than absence",
            screen.flatten().filterIsInstance<TextView>()
                .none { it.text.toString() == MachinesPanelScreen.ENTRY_LABEL },
        )
    }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }
}
