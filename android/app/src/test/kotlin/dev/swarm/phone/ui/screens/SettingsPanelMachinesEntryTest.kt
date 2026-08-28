package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.ClockBanner
import dev.swarm.phone.ui.MachineFreshness
import dev.swarm.phone.ui.SettingsScreen
import dev.swarm.phone.ui.StreamView
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

    /**
     * phone-refit-playbook W7.5: the separate Computers row folds into the computer card, so the
     * panel under test carries a connection section for the chevron to sit on.
     */
    private fun view(onOpenMachines: (() -> Unit)? = null): View = settingsPanelView(
        context = context,
        panel = SettingsPanelScreen.of(
            SettingsScreen(alerts = true, mentions = true),
            machine = "nathans-mbp",
            connection = SettingsPanelScreen.connectionOf(
                machineId = "ep-1a2b3c4d",
                machineName = "nathans-mbp",
                presence = "online",
                freshness = MachineFreshness(silent = false, lastHeardUnixMs = 1_000L),
                streams = listOf("journal", "terminal", "reply", "grant").map { name ->
                    StreamView(stream = name, stale = false, resyncPending = false)
                },
                clock = ClockBanner.of(""),
                killSwitchEngaged = false,
                nowUnixMs = 1_754_000_000_000L,
            ),
        ),
        rowFor = { View(context) },
        onOpenMachines = onOpenMachines,
    )

    @Test
    fun theEntryComposesByItsRecordedNameAndFires() {
        var opened = 0
        val screen = view(onOpenMachines = { opened++ })

        // W7.5: the entry is the computer card's chevron. It is findable by its recorded name
        // through what a screen reader hears (defect shape 1, agents-tracker-64rf), and it sits
        // ON the card rather than in a row of its own.
        val chevron = screen.flatten().firstOrNull { it.tag == SettingsTag.MACHINES_ENTRY }
        assertNotNull(
            "no view on the Settings panel is the Computers chevron (SettingsTag.MACHINES_ENTRY); " +
                "the separate Computers row folded into the computer card and the card got no " +
                "way in (phone-refit-playbook W7.5)",
            chevron,
        )
        assertEquals(
            "the chevron does not announce '${MachinesPanelScreen.ENTRY_LABEL}'; a control that " +
                "exists must be findable by its recorded name",
            MachinesPanelScreen.ENTRY_LABEL,
            chevron!!.contentDescription?.toString(),
        )
        val card = screen.flatten().firstOrNull { it.tag == SettingsTag.CONNECTION_ROW }
        assertNotNull("the panel drew no computer card for the chevron to sit on", card)
        assertTrue(
            "the chevron is not composed after the card it belongs to",
            screen.flatten().indexOf(chevron) > screen.flatten().indexOf(card),
        )

        var control: View? = chevron
        while (control != null && !control.hasOnClickListeners()) {
            control = control.parent as? View
        }
        assertNotNull(
            "the chevron is on screen but neither it nor any ancestor takes a tap; a control " +
                "with nothing behind it is decoration",
            control,
        )
        control!!.performClick()
        assertEquals("tapping the chevron did not fire onOpenMachines", 1, opened)
    }

    @Test
    fun anUnwiredPanelComposesNoDeadEntry() {
        // `onOpenMachines = null` is the JVM suite's own world (no navigation to wire); the
        // panel must draw no control that goes nowhere -- navHeaderDrill(back = null)'s ruling.
        val screen = view(onOpenMachines = null)
        assertTrue(
            "the chevron composed with nothing behind it; a control that cannot act is a " +
                "dead affordance, worse than absence",
            screen.flatten().none { it.tag == SettingsTag.MACHINES_ENTRY },
        )
        assertTrue(
            "a Computers entry composed by name with nothing behind it",
            screen.flatten().filterIsInstance<TextView>()
                .none { it.text.toString() == MachinesPanelScreen.ENTRY_LABEL },
        )
    }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }
}
