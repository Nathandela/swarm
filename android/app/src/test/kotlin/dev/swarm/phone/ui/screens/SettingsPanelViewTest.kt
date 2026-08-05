package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.runtime.PermissionState
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.SettingsScreen
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-9 over the settings screen AS DRAWN.
 *
 * WHAT THIS ASKS THAT `SettingsPanelScreenTest` CANNOT. That suite asks what the screen SAYS;
 * this asks whether it is on screen -- which component renders it, in what order, and whether the
 * control the surface supplied went to the row it belongs to. "The model is beautiful and nothing
 * renders it" is the defect PB-DS-6 was recorded NOT MET over, and a suite that only asserted the
 * model harder would reproduce it exactly.
 *
 * WHAT IS DELIBERATELY NOT ASSERTED: appearance. The nav header's metrics and the section label's
 * tracking are PB-DS-10's and are asserted in `ui/kit`; repeating them here would be a second
 * opinion that can disagree with the first. The settings row's own metrics are asserted there too,
 * now that it exists. What still has no appearance to assert is the TOGGLE -- derivation row 4 has
 * no component, so the trailing control here is a bare `View` stub, and this suite records that
 * gap by having nothing to say about it rather than by asserting a theme default as if it were a
 * decision.
 */
@RunWith(RobolectricTestRunner::class)
class SettingsPanelViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun panel(
        alerts: Boolean = true,
        mentions: Boolean = true,
        permission: PermissionState = PermissionState.GRANTED,
        pending: Boolean = false,
    ): SettingsPanel {
        val base = SettingsScreen(alerts = alerts, mentions = mentions)
            .withNotificationPermission(permission)
        return SettingsPanelScreen.of(if (pending) base.setAlerts(alerts) else base)
    }

    /** A stand-in for the toggle derivation row 4 specifies and the kit still does not ship. */
    private fun stubControl(context: Context): View = View(context)

    private fun view(
        panel: SettingsPanel,
        controls: MutableMap<SettingsRow, View> = mutableMapOf(),
        below: View? = null,
    ): View = settingsPanelView(
        context = context,
        panel = panel,
        rowFor = { row -> controls.getOrPut(row) { stubControl(context) } },
        below = below,
    )

    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    // ---- the composition --------------------------------------------------

    @Test
    fun `the settings screen is composed of the kit components C6 names`() {
        val root = view(panel())

        assertNotNull("C6.1 -- the screen has no title", root.kitFind(SettingsTag.NAV))
        assertNotNull("C6.2 -- the section has no label", root.kitFind(SettingsTag.SECTION_LABEL))
        assertNotNull(
            "C6.2 -- the rows are not the kit's `settingsRow`, so the screen hand-built them",
            root.kitFind(SettingsTag.ROW)?.kitFind(KitTag.SETTINGS_LABEL),
        )
    }

    @Test
    fun `the title is drawn by the nav header and carries no live counter`() {
        val nav = view(panel()).kitRequire(SettingsTag.NAV)

        assertEquals("Settings", textOf(nav.kitRequire(KitTag.TITLE)))
        assertNull(
            "a live counter was drawn on the settings screen. It is the inbox's in-context " +
                "liveness readout (derivation §1.4) and settings has nothing in flight to report",
            nav.kitFind(KitTag.LIVE),
        )
    }

    @Test
    fun `the heading comes before the rows it heads`() {
        val root = view(panel())
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in SettingsTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)

        assertEquals(
            listOf(SettingsTag.NAV, SettingsTag.SECTION_LABEL, SettingsTag.ROW, SettingsTag.ROW),
            order,
        )
    }

    @Test
    fun `every row in the model is on screen, with its two lines`() {
        val page = panel()
        val root = view(page)
        val rows = root.allTagged(SettingsTag.ROW)

        assertEquals(page.sections.single().rows.size, rows.size)
        assertEquals(
            listOf("Needs your decision", "Task done"),
            rows.map { textOf(it.kitRequire(KitTag.SETTINGS_LABEL)) },
        )
        assertEquals(
            listOf("Approvals and blocked prompts", "Completions and failures"),
            rows.map { textOf(it.kitRequire(KitTag.SETTINGS_SUBLABEL)) },
        )
    }

    // ---- the control goes to the row it belongs to -------------------------

    @Test
    fun `each row hosts the control the caller supplied for that row and no other`() {
        // The bug this catches is the plausible one: a view that resolves the control once and
        // hands the same one to every row, which leaves one preference unreachable and looks
        // exactly like a working screen.
        val page = panel()
        val supplied = page.sections.single().rows.associateWith { View(context) }
        val root = settingsPanelView(
            context = context,
            panel = page,
            rowFor = { row -> requireNotNull(supplied[row]) },
        )
        val rows = root.allTagged(SettingsTag.ROW)

        page.sections.single().rows.forEachIndexed { index, row ->
            val hosted = (rows[index] as ViewGroup).let { group ->
                (0 until group.childCount).map { group.getChildAt(it) }
            }
            assertTrue(
                "row ${row.label} does not host the control supplied for it",
                hosted.any { it === supplied[row] },
            )
        }
    }

    // ---- the row is one accessibility node ---------------------------------

    @Test
    fun `the row announces its own copy and its two lines announce nothing`() {
        // Derivation row 15: the whole row is one >=48 dp target when it carries a toggle. Left
        // alone a screen reader reads the label, the sublabel and the control as three unrelated
        // things; with the description on the row and the lines excluded it reads the row once.
        val page = panel()
        val root = view(page)
        val row = root.allTagged(SettingsTag.ROW).first()

        assertEquals(page.sections.single().rows.first().description, row.contentDescription)
        listOf(KitTag.SETTINGS_LABEL, KitTag.SETTINGS_SUBLABEL).forEach { tag ->
            assertEquals(
                "`$tag` is still announced, so the row's words are read twice",
                View.IMPORTANT_FOR_ACCESSIBILITY_NO,
                row.kitRequire(tag).importantForAccessibility,
            )
        }
    }

    // ---- the notices --------------------------------------------------------

    @Test
    fun `a notice is drawn only when the panel has one to make`() {
        assertEquals(
            "a notice line was drawn over a screen with nothing to report",
            0,
            view(panel()).allTagged(SettingsTag.NOTICE).size,
        )

        val blocked = panel(permission = PermissionState.PERMANENTLY_DENIED)
        assertEquals(
            blocked.notices,
            view(blocked).allTagged(SettingsTag.NOTICE).map { textOf(it) },
        )
    }

    // ---- agents-tracker-0dij: the way out of a blocked permission ------------

    /**
     * The redirect is the caller's control, placed and tagged -- never one this file builds.
     *
     * IT LEAVES THE APP, so it is `SettingsSurface`'s for the three reasons the toggle and the
     * replace chip are: PB-SEC-12 clause 1's touch filter is applied at CONSTRUCTION and must reach
     * the instance actually on screen, the surface is what starts the Activity, and the control has
     * to survive a redraw. What this file owes is that the panel's own decision -- the label is
     * present -- puts that control on screen, under the sentence that names it.
     */
    @Test
    fun `the settings redirect is the caller's control, placed under the notice that names it`() {
        val blocked = panel(permission = PermissionState.PERMANENTLY_DENIED)
        val supplied = View(context)
        val root = settingsPanelView(
            context = context,
            panel = blocked,
            rowFor = { stubControl(context) },
            redirectFor = { supplied },
        )

        val placed = root.allTagged(SettingsTag.PERMISSION_REDIRECT)
        assertEquals("the panel named a redirect and drew none, or drew more than one", 1, placed.size)
        assertSame(
            "the redirect on screen is not the control the surface supplied, so the touch filter " +
                "and the Intent behind it belong to a view nobody wired",
            supplied,
            placed.single(),
        )

        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it == SettingsTag.NOTICE || it == SettingsTag.PERMISSION_REDIRECT) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)
        assertEquals(
            "the control comes before the sentence that explains why it is there",
            listOf(SettingsTag.NOTICE, SettingsTag.PERMISSION_REDIRECT),
            order,
        )
    }

    @Test
    fun `a panel with no redirect to offer draws no control`() {
        for (permission in listOf(PermissionState.GRANTED, PermissionState.DENIED)) {
            assertEquals(
                "a settings redirect was drawn under $permission. On DENIED the platform still " +
                    "prompts, so the control would walk the user past the prompt that fixes it",
                0,
                view(panel(permission = permission)).allTagged(SettingsTag.PERMISSION_REDIRECT).size,
            )
        }
    }

    // ---- agents-tracker-2yfn: the way out of a blocked channel --------------
    //
    // FAILING-FIRST (TDD RED, GG-5). It is a SECOND control beside the permission redirect and not
    // the same one wearing different words: the two Intents go to different system screens, and the
    // Intent is fixed inside a listener installed at construction, so one view cannot be both.

    private fun channelBlocked(): SettingsPanel = SettingsPanelScreen.of(
        SettingsScreen(alerts = true, mentions = true)
            .withNotificationPermission(PermissionState.GRANTED)
            .withNotificationDelivery(dev.swarm.phone.runtime.NotificationDelivery.CHANNEL_BLOCKED),
    )

    @Test
    fun `the channel redirect is the caller's control, placed under the notice that names it`() {
        val supplied = View(context)
        val root = settingsPanelView(
            context = context,
            panel = channelBlocked(),
            rowFor = { stubControl(context) },
            deliveryRedirectFor = { supplied },
        )

        val placed = root.allTagged(SettingsTag.DELIVERY_REDIRECT)
        assertEquals(
            "agents-tracker-2yfn: the panel named a channel redirect and drew none, or drew more " +
                "than one -- so the one state the permission check cannot see has no way out of it",
            1,
            placed.size,
        )
        assertSame(
            "the redirect on screen is not the control the surface supplied, so the touch filter " +
                "and the Intent behind it belong to a view nobody wired",
            supplied,
            placed.single(),
        )

        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let {
                if (it == SettingsTag.NOTICE || it == SettingsTag.DELIVERY_REDIRECT) order += it
            }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)
        assertEquals(
            "the control comes before the sentence that explains why it is there",
            listOf(SettingsTag.NOTICE, SettingsTag.DELIVERY_REDIRECT),
            order,
        )
    }

    @Test
    fun `a panel with no channel to un-block draws no channel redirect`() {
        assertEquals(
            "a channel redirect was drawn over a phone whose channel delivers, sending its owner " +
                "to a system page to fix nothing",
            0,
            view(panel()).allTagged(SettingsTag.DELIVERY_REDIRECT).size,
        )
    }

    @Test
    fun `what this slice has not recomposed is hosted under the panel, not instead of it`() {
        val trailing = View(context)
        val root = view(panel(), below = trailing) as ViewGroup

        assertSame(trailing, root.getChildAt(root.childCount - 1))
        assertNotNull("hosting the remainder dropped the screen", root.kitFind(SettingsTag.NAV))
    }
}
