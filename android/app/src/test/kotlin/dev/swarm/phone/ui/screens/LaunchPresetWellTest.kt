package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.kit.kitFind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * Phone refit W5.1: a denial that names a command draws it in the kit's mono well under the
 * sentence, and a denial with no command draws no well.
 */
@RunWith(RobolectricTestRunner::class)
class LaunchPresetWellTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun panel(availability: LaunchAvailability) = LaunchPresetPanel(
        availability = availability,
        availabilityNotice = LaunchPresetScreen.noticeFor(availability),
        availabilityCommand = LaunchPresetScreen.commandFor(availability),
        rows = emptyList(),
        deliveryNotice = "",
    )

    private fun view(availability: LaunchAvailability): View =
        launchPresetView(context, panel(availability), fetch = View(context), onSelect = {})

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    @Test
    fun `a denial with a command draws it in a well under the sentence`() {
        val root = view(LaunchAvailability.KILL_SWITCH_OFF)
        assertEquals("swarm remote on", textOf(root.kitFind(LaunchPresetTag.COMMAND)))
        assertEquals(
            LaunchPresetScreen.noticeFor(LaunchAvailability.KILL_SWITCH_OFF),
            textOf(root.kitFind(LaunchPresetTag.AVAILABILITY)),
        )
    }

    @Test
    fun `a denial with no command draws no well`() {
        assertNull(view(LaunchAvailability.OFFLINE).kitFind(LaunchPresetTag.COMMAND))
        assertNull(view(LaunchAvailability.AVAILABLE).kitFind(LaunchPresetTag.COMMAND))
    }
}
