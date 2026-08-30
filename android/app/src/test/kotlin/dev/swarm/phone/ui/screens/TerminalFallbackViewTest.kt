package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * WAVE R8 -- the capability-routed terminal fallback AS DRAWN (ADR-017 T1/T4/T6).
 *
 * WHAT THIS ASKS THAT `android/gate/r8_fallback_ui_test.go` CANNOT. That gate is a set of
 * DELETION obligations -- no other file may watch, no other body may print the grid, this body may
 * never name a markdown path -- and a deletion cannot be observed from a rendered tree. What a
 * rendered tree can say, and a source scan cannot, is that the screen actually PUTS the sanitized
 * rows, the honest header, the staleness indicator, the interleaving warning and the persistent
 * control banner on the glass. Both halves are needed: a gate that only bans is a gate that passes
 * over an empty screen.
 */
@RunWith(RobolectricTestRunner::class)
class TerminalFallbackViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun model(
        provider: String = "opencode",
        providerVersion: String = "0.9.3",
        missingCapability: String = "structured_chat",
        controlOffered: Boolean = true,
    ) = TerminalFallbackModel(
        provider = provider,
        providerVersion = providerVersion,
        missingCapability = missingCapability,
        sessionInstance = "inst-a",
        controlOffered = controlOffered,
    )

    private fun view(
        model: TerminalFallbackModel = model(),
        rows: List<String> = listOf("$ go test ./...", "ok  swarm/vt  1.4s"),
        snapshotAge: Long = 0L,
        streamStale: Boolean = false,
        snapshotNotice: String = "",
        snapshotUnavailable: Boolean = false,
        controlRemaining: Long? = null,
        onRelease: (() -> Unit)? = null,
    ): View = terminalFallbackView(
        context = context,
        model = model,
        rows = rows,
        gridRows = 24,
        snapshotAge = snapshotAge,
        streamStale = streamStale,
        snapshotNotice = snapshotNotice,
        snapshotUnavailable = snapshotUnavailable,
        controlRemaining = controlRemaining,
        onBack = {},
        onRelease = onRelease,
    )

    private fun textOf(root: View, tag: String): String =
        (root.kitRequire(tag) as TextView).text.toString()

    // ---- T1/T4: the machine's own screen, and only the machine's ----------------

    @Test
    fun `the sanitized rows are on the glass, one grid row per line`() {
        val rows = listOf("first", "second", "third")
        val grid = textOf(view(rows = rows), TerminalFallbackTag.GRID)
        assertEquals(
            "the grid is `vt.SnapText`'s output verbatim: one string per grid row, joined here " +
                "and nowhere else. A row that arrived carrying its own break would have made the " +
                "grid taller than the machine drew it.",
            rows.joinToString("\n"),
            grid,
        )
    }

    @Test
    fun `every grid row is laid out left-to-right whatever it contains`() {
        // A strongly-RTL row. No control character is present, so no machine-side strip can
        // reach it: implicit bidi reorders the line in the text stack itself (ADR-017 T4-c).
        val well = view(rows = listOf("אבג /etc/passwd")).kitRequire(TerminalFallbackTag.GRID)
        assertEquals(
            "ADR-017 T4-c: the row's paragraph direction is not forced, so a line containing " +
                "strongly-RTL characters is reordered by the text stack and the ADR's stated A7 " +
                "property (\"no Unicode bidi rune can visually spoof what is displayed\") is false. " +
                "This is the half the machine cannot supply.",
            View.TEXT_DIRECTION_LTR,
            well.textDirection,
        )
        assertEquals(View.LAYOUT_DIRECTION_LTR, well.layoutDirection)
    }

    @Test
    fun `an awaiting snapshot is visible as phone chrome and never terminal output`() {
        val root = view(
            rows = emptyList(),
            snapshotNotice = TerminalGrid.AWAITING_SNAPSHOT.snapshotNotice,
        )

        assertEquals(
            TerminalGrid.AWAITING_SNAPSHOT_NOTICE,
            textOf(root, TerminalFallbackTag.SNAPSHOT_STATE),
        )
        assertEquals(
            "phone-authored loading copy must never enter the machine-authored terminal well",
            "",
            textOf(root, TerminalFallbackTag.GRID),
        )
    }

    @Test
    fun `a routed snapshot refusal is visible outside the terminal well`() {
        val routed = "No link to your computer right now."
        val root = view(
            rows = emptyList(),
            snapshotNotice = routed,
            snapshotUnavailable = true,
        )

        assertEquals(routed, textOf(root, TerminalFallbackTag.SNAPSHOT_STATE))
        assertEquals("", textOf(root, TerminalFallbackTag.GRID))
    }

    // ---- playbook:280: the honest header ----------------------------------------

    @Test
    fun `the header names the provider, its detected version, and says chat is off`() {
        val root = view()
        assertTrue(
            "the header must say chat is off for this session, in the reader's words (phone " +
                "refit W5.4); the provider and its version are the checkable half beside it",
            textOf(root, TerminalFallbackTag.HEADER).contains("Chat is off for this session."),
        )
        assertEquals("opencode 0.9.3", model().headline)
    }

    @Test
    fun `an undetected version is stated as absent rather than invented`() {
        assertEquals(
            "an empty detected version must render as the provider alone. A guessed version is " +
                "the one thing that would make a checkable header worse than silence.",
            "opencode",
            model(providerVersion = "").headline,
        )
    }

    // ---- T4-b: staleness comes from the snapshot's own age ----------------------

    @Test
    fun `a fresh snapshot draws no staleness indicator at all`() {
        assertNull(
            "an always-on staleness line is a line nobody reads; the empty string means FRESH",
            view(snapshotAge = 0L).kitFind(TerminalFallbackTag.STALENESS),
        )
    }

    @Test
    fun `a quiet machine is never drawn as an idle terminal`() {
        val root = view(snapshotAge = TerminalFallbackModel.STALE_AFTER_MS * 3)
        assertNotNull(
            "ADR-017 T4-b: past the horizon the screen must SAY the snapshot is old. Without it a " +
                "machine that went quiet is drawn identically to a terminal that is idle, which is " +
                "the state a user is most likely to type into.",
            root.kitFind(TerminalFallbackTag.STALENESS),
        )
        assertTrue(textOf(root, TerminalFallbackTag.STALENESS).contains("ago"))
    }

    @Test
    fun `a machine the phone is not hearing from is never drawn as fresh`() {
        // ROUND-2 MODERATE 9. `App.Peek` has always returned this verdict and the adapter threw
        // it away while hardcoding an age of zero -- and zero renders as FRESH. So a screen
        // showing a grid the machine stopped sending hours ago said nothing at all, which is
        // exactly T4-b's named lie one signal over.
        val root = view(snapshotAge = 0L, streamStale = true)
        assertNotNull(
            "ADR-017 T4-b: the snapshot's own age and the machine's stream are INDEPENDENT " +
                "signals, and a fresh-looking age must not silence a dead stream.",
            root.kitFind(TerminalFallbackTag.STALENESS),
        )
        assertEquals(TerminalFallbackModel.STREAM_STALE, textOf(root, TerminalFallbackTag.STALENESS))
    }

    @Test
    fun `a dead stream outranks an old snapshot`() {
        assertEquals(
            "an old snapshot on a LIVE stream is an idle terminal; the same snapshot on a DEAD " +
                "stream is an unknown one, and reporting the weaker of the two is the softer " +
                "form of the same lie",
            TerminalFallbackModel.STREAM_STALE,
            TerminalFallbackModel.stalenessLine(TerminalFallbackModel.STALE_AFTER_MS * 3, streamStale = true),
        )
        assertEquals(
            "",
            TerminalFallbackModel.stalenessLine(0L, streamStale = false),
        )
    }

    // ---- T8 / playbook:286-287: the interleaving warning ------------------------

    @Test
    fun `the interleaving warning is always on the screen`() {
        assertTrue(
            "Decision G keeps the owner typing throughout and ADR-013's co-presence finding " +
                "proves both streams stay live, so the UX must warn that simultaneous typing can " +
                "interleave -- and must NOT \"fix\" it by evicting the terminal user",
            textOf(view(), TerminalFallbackTag.INTERLEAVING).contains("typing"),
        )
    }

    // ---- T6: the control banner is persistent and in view -----------------------

    @Test
    fun `a live generation draws a persistent banner with its remaining horizon`() {
        val root = view(controlRemaining = 9 * 60_000L, onRelease = {})
        val banner = textOf(root, TerminalFallbackTag.BANNER)
        assertTrue(
            "ADR-017 T6: for the whole life of a generation the screen must continuously display " +
                "that control is live AND its remaining horizon. A sheet that grants control and " +
                "then disappears is explicit and not persistent, and it leaves a user typing into " +
                "a live generation they have to REMEMBER they opened.",
            banner.contains("typing into this terminal") && banner.contains("9m"),
        )
        assertNotNull(
            "the Release must be IN VIEW -- not a drawer entry and not a second navigation step",
            root.kitFind(TerminalFallbackTag.RELEASE),
        )
    }

    @Test
    fun `an expired horizon says control ended rather than quietly vanishing`() {
        assertTrue(
            "a banner that disappears is indistinguishable from a screen that never had control",
            TerminalFallbackModel.controlBanner(0L).contains("ended"),
        )
    }

    @Test
    fun `a session the machine granted no control over says read only`() {
        val root = view(model = model(controlOffered = false))
        assertNotNull(
            "ADR-017 T6-b: read-only must be STATED. A session degraded into the fallback by a " +
                "proven structured gap may watch and may not control, and the reason a keyboard is " +
                "missing must never be left for the user to infer.",
            root.kitFind(TerminalFallbackTag.READ_ONLY),
        )
        assertNull("no control banner without a generation", root.kitFind(TerminalFallbackTag.BANNER))
        assertNull("and no release either", root.kitFind(TerminalFallbackTag.RELEASE))
    }

    // ---- T1: the routing rule, at the one seam that builds this screen ----------

    @Test
    fun `the model refuses to be built for a session the machine did not route here`() {
        // The PURE half of the routing rule. `swarmmobile.Session` cannot be constructed here --
        // its static initialiser loads libgojni, which a JVM unit test does not have -- so the
        // rule is stated over the flat facts and `from` delegates to it.
        assertNull(
            "ADR-017 T2 rule 4: there is NO route to the fallback from a healthy structured " +
                "session -- no power-user escape hatch, no long-press, no debug toggle in a " +
                "release build. The model answers null, so the view cannot be composed for one " +
                "even by a caller who wants to.",
            TerminalFallbackModel.of(
                destination = "chat",
                provider = "claude",
                providerVersion = "1.0.0",
                missingCapability = "",
                sessionInstance = "inst-a",
                controlOffered = false,
            ),
        )
        assertNull(
            "the status card is the fail-closed destination for an absent record, an inconsistent " +
                "one, a record binding no instance and a machine with no TerminalView version -- " +
                "and none of them may reach this screen either",
            TerminalFallbackModel.of(
                destination = "status_card",
                provider = "somecli",
                providerVersion = "",
                missingCapability = "",
                sessionInstance = "",
                controlOffered = false,
            ),
        )

        val built = TerminalFallbackModel.of(
            destination = TerminalFallbackModel.DESTINATION,
            provider = "agy",
            providerVersion = "2.1",
            missingCapability = "structured_chat",
            sessionInstance = "inst-b",
            controlOffered = true,
        )
        assertNotNull("a routed session must build", built)
        assertEquals("inst-b", built!!.sessionInstance)
        assertTrue(built.controlOffered)
    }
}
