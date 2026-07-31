package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over the inbox's chrome: the scope chips, the
 * section labels, the nav header with its live counter, the tab bar and the badge.
 *
 * The two counters are the interesting pair. §1.4 of the derivation table ships BOTH, on the
 * argument that they are different instruments -- the header counter says how much is in flight,
 * the tab badge says what needs you and is the only one that survives leaving the screen -- and
 * that only works if they stay visually distinct. So the badge is asserted to be `--p-att` and
 * the counter `--p-hero`, from two different rules, which is the arrangement that would break
 * silently if someone "unified" them.
 */
@RunWith(RobolectricTestRunner::class)
// NATIVE and not the default. Robolectric's LEGACY graphics stubs the text stack -- measureText
// returns one pixel per character -- which makes every font measure fixed-pitch and every
// typeface assertion in this file certify the opposite of the truth. The same annotation, for the
// same reason, as MonoBoxDrawingTest.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class InboxChromeTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val density: Float get() = context.resources.displayMetrics.density

    private fun px(dp: Float) = dp * density

    private val spScale: Float
        get() = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
        )

    private fun dimen(name: String): Float {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id)
    }

    // ---- filter chip ------------------------------------------------------

    /** `.chip` and `.chip.on`: two fills, two inks, one set of metrics. */
    @Test
    fun `the filter chip resolves both of its states`() {
        val off = filterChip(context, "All machines", selected = false, present = null)
        val on = filterChip(context, "All machines", selected = true, present = null)

        val claims = mutableListOf(
            Claim("`.chip` padding-y (top)", dimen("swarm_space_8").toInt(), off.paddingTop),
            Claim("`.chip` padding-y (bottom)", dimen("swarm_space_8").toInt(), off.paddingBottom),
            Claim("`.chip` padding-x (start)", dimen("swarm_space_10").toInt(), off.paddingStart),
            Claim("`.chip` padding-x (end)", dimen("swarm_space_10").toInt(), off.paddingEnd),
            Claim("`.chip` fill", KitOrigin.cssColour(".chip", "background"), (off.background as SubstrateSurface).spec.fill),
            Claim("`.chip.on` fill", KitOrigin.cssColour(".chip.on", "background"), (on.background as SubstrateSurface).spec.fill),
        )
        claims += KitOrigin.textClaims(off, ".chip", KitOrigin.cssColour(".chip", "color"), spScale)
        claims += KitOrigin.textClaims(on, ".chip", KitOrigin.cssColour(".chip.on", "color"), spScale)
        assertEquals(emptyList<String>(), mismatches(claims))

        assertNotEquals(
            "the selected chip renders in the same ink as an unselected one, so selection is " +
                "carried by the fill alone -- and `--p-hero-ink` on `--p-card` would be unreadable",
            off.currentTextColor,
            on.currentTextColor,
        )
    }

    /**
     * `.chip .pd`: the machine-presence dot, 5dp, and the one component whose OFF state the design
     * source does not draw.
     *
     * Substrate declares only the online colour. Offline is the derivation table's, row 11: the
     * same `--p-ink3` the offline machine dot and the Completed Group take, because all three mean
     * "not active" -- and, unlike the status dot, flat in both states. A machine that is merely
     * reachable is not a running agent.
     */
    @Test
    fun `the presence dot is the design's 5dp mark, present only when a machine is named`() {
        val online = filterChip(context, "nathans-mbp", selected = false, present = true)
        val offline = filterChip(context, "mac-studio", selected = false, present = false)
        val none = filterChip(context, "All machines", selected = false, present = null)

        val onlineDot = online.compoundDrawablesRelative[0]
        val offlineDot = offline.compoundDrawablesRelative[0]
        assertTrue("the online chip carries no presence dot", onlineDot != null)
        assertTrue("the offline chip carries no presence dot", offlineDot != null)
        assertNull(
            "a chip that names no machine carries a presence dot, which says a scope filter is " +
                "online",
            none.compoundDrawablesRelative[0],
        )

        val size = px(KitOrigin.cssDp(".chip .pd", "width")).toInt()
        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("online dot colour", KitOrigin.cssColour(".chip .pd", "background"), (onlineDot as StatusDotDrawable).fill),
                    Claim("offline dot colour", KitOrigin.token("--p-ink3"), (offlineDot as StatusDotDrawable).fill),
                    Claim("presence dot never glows (online)", null, onlineDot.glow),
                    Claim("presence dot never glows (offline)", null, offlineDot.glow),
                    Claim("`.chip .pd` width", size, onlineDot.bounds.width()),
                    Claim("`.chip .pd` height", size, onlineDot.bounds.height()),
                    Claim(
                        "`.chip .pd` margin-right",
                        dimen("swarm_space_4").toInt(),
                        online.compoundDrawablePadding,
                    ),
                ),
            ),
        )
    }

    /** `.chips`: the container's own padding, and the 7px gap between chips. */
    @Test
    fun `the chip row carries the gap and the side padding`() {
        val row = chipRow(context)
        row.addView(filterChip(context, "All", selected = true, present = null))
        row.addView(filterChip(context, "nathans-mbp", selected = false, present = true))

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.chips` padding-x (start)", dimen("swarm_space_18").toInt(), row.paddingStart),
                    Claim("`.chips` padding-x (end)", dimen("swarm_space_18").toInt(), row.paddingEnd),
                    Claim("`.chips` padding-bottom", dimen("swarm_space_12").toInt(), row.paddingBottom),
                    Claim(
                        "no gap before the first chip",
                        0,
                        (row.getChildAt(0).layoutParams as ViewGroup.MarginLayoutParams).marginStart,
                    ),
                    Claim(
                        "`.chips` gap before the second chip",
                        dimen("swarm_space_8").toInt(),
                        (row.getChildAt(1).layoutParams as ViewGroup.MarginLayoutParams).marginStart,
                    ),
                ),
            ),
        )
    }

    // ---- section label ----------------------------------------------------

    /**
     * `.plabel`: mono, tracked wide, uppercase, and in the tertiary ink whose contrast failure
     * PB-DS-12 records rather than fixes.
     *
     * `text-transform: uppercase` is the COMPONENT's, not the copy's -- `isAllCaps` rather than an
     * uppercased string, so the screen keeps passing "Needs you" and the accessibility tree keeps
     * reading it as a word rather than as four letters.
     */
    @Test
    fun `the section label is the design's mono uppercase heading`() {
        val label = sectionLabel(context, "Needs you")
        val claims = mutableListOf(
            Claim("`.plabel` padding-top", dimen("swarm_space_12").toInt(), label.paddingTop),
            Claim("`.plabel` padding-x (start)", dimen("swarm_space_18").toInt(), label.paddingStart),
            Claim("`.plabel` padding-x (end)", dimen("swarm_space_18").toInt(), label.paddingEnd),
            Claim("`.plabel` padding-bottom", dimen("swarm_space_8").toInt(), label.paddingBottom),
            Claim("`.plabel` text-transform", true, label.isAllCaps),
            Claim("the copy is not uppercased in place", "Needs you", label.text.toString()),
        )
        claims += KitOrigin.textClaims(label, ".plabel", KitOrigin.cssColour(".plabel", "color"), spScale)
        assertEquals(emptyList<String>(), mismatches(claims))
    }

    // ---- nav header and live counter --------------------------------------

    /** `.pnav`: the big title, the counter pushed to the right, and a baseline they share. */
    @Test
    fun `the nav header resolves the display title and the live counter`() {
        val header = navHeader(context, "Inbox", "3 LIVE")
        val title = header.kitRequire(KitTag.TITLE) as TextView
        val live = header.kitRequire(KitTag.LIVE) as TextView

        val claims = mutableListOf(
            Claim("`.pnav` padding-top", dimen("swarm_space_4").toInt(), header.paddingTop),
            Claim("`.pnav` padding-x (start)", dimen("swarm_space_18").toInt(), header.paddingStart),
            Claim("`.pnav` padding-x (end)", dimen("swarm_space_18").toInt(), header.paddingEnd),
            Claim("`.pnav` padding-bottom", dimen("swarm_space_10").toInt(), header.paddingBottom),
            // `align-items: baseline`. LinearLayout aligns baselines only when asked, and the
            // 27sp title beside a 10sp counter is exactly the pair that looks wrong without it.
            Claim("`.pnav` baseline alignment", true, header.isBaselineAligned),
            Claim(
                "`.live { margin-left: auto }` -- the title takes the slack",
                1f,
                (title.layoutParams as LinearLayout.LayoutParams).weight,
            ),
            Claim(
                "`.pnav` gap",
                dimen("swarm_space_10").toInt(),
                (live.layoutParams as ViewGroup.MarginLayoutParams).marginStart,
            ),
        )
        // `.pnav .big` declares no colour and inherits `.pscreen`'s.
        claims += KitOrigin.textClaims(title, ".pnav .big", KitOrigin.cssColour(".pscreen", "color"), spScale)
        claims += KitOrigin.textClaims(live, ".pnav .live", KitOrigin.cssColour(".pnav .live", "color"), spScale)
        assertEquals(emptyList<String>(), mismatches(claims))

        assertNull(
            "a header built with no live count still carries the counter, so a screen with " +
                "nothing running claims something is",
            navHeader(context, "Machines", null).kitFind(KitTag.LIVE),
        )
    }

    /** The counter on its own: phosphor green, mono, tracked. */
    @Test
    fun `the live counter is the phosphor readout the design specifies`() {
        val counter = liveCounter(context, "3 LIVE")
        assertEquals(
            emptyList<String>(),
            mismatches(
                KitOrigin.textClaims(
                    counter, ".pnav .live", KitOrigin.cssColour(".pnav .live", "color"), spScale,
                ),
            ),
        )
        assertNotEquals(
            "the live counter renders in the badge's colour. §1.4 ships both counters ONLY " +
                "because they are distinct instruments -- hero means live, attention means this " +
                "needs you -- and one colour for both is the contradiction that argument avoids.",
            KitOrigin.token("--p-att"),
            counter.currentTextColor,
        )
    }

    // ---- tab bar ----------------------------------------------------------

    private fun tabs(badgeCount: Int = 0): LinearLayout = tabBar(
        context,
        listOf(
            TabItem("Inbox", selected = true, badgeCount = badgeCount, badgeDescription = "needs you"),
            TabItem("Machines"),
            TabItem("Activity"),
            TabItem("Settings"),
        ),
    )

    /**
     * `.ptabs`: the frame constant, the translucent fill and the 1dp top rule.
     *
     * The fill is `--p-tabbg`, which is declared `effect` in the token origin and therefore has NO
     * colour resource -- PB-TOK-6's converters produce none for that kind. So the kit computes it
     * from `--p-bg` at the token's own alpha, and the expectation here is read straight out of the
     * token's `rgba()`, which is the only way to catch a component that used the opaque ground.
     */
    @Test
    fun `the tab bar is the design's translucent bar with a hairline rule`() {
        val bar = tabs()
        val surface = bar.background as? TopRule
        assertTrue("the tab bar's background is not a kit TopRule", surface != null)

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.ptabs` height", dimen("swarm_tabbar_height").toInt(), bar.layoutParams.height),
                    Claim("`.ptabs` padding-bottom", dimen("swarm_space_14").toInt(), bar.paddingBottom),
                    Claim("--p-tabbg fill", KitOrigin.rgbaToken("--p-tabbg"), surface!!.fill),
                    Claim("`.ptabs` border-top colour", KitOrigin.cssColour(".ptabs", "border-top"), surface.rule),
                    Claim("`.ptabs` border-top width", px(KitOrigin.cssFirstPx(".ptabs", "border-top")), surface.rulePx),
                    Claim("four tabs", 4, bar.childCount),
                ),
            ),
        )
        assertNotEquals(
            "the tab bar is opaque. --p-tabbg is 88% for a reason the token was pinned for: a " +
                "hero chip or a line of ink scrolling under the bar shows through as a tint.",
            KitOrigin.token("--p-bg"),
            surface.fill,
        )
    }

    /** `.ptabs div` and `.ptabs div.on`: the inactive ink and the one that says where you are. */
    @Test
    fun `the tab items resolve the inactive and active inks`() {
        val bar = tabs()
        val selected = bar.getChildAt(0).kitRequire(KitTag.TAB_LABEL) as TextView
        val inactive = bar.getChildAt(1).kitRequire(KitTag.TAB_LABEL) as TextView

        val claims = mutableListOf<Claim>()
        claims += KitOrigin.textClaims(selected, ".ptabs div", KitOrigin.cssColour(".ptabs div.on", "color"), spScale)
        claims += KitOrigin.textClaims(inactive, ".ptabs div", KitOrigin.cssColour(".ptabs div", "color"), spScale)
        claims += listOf(
            Claim(
                "`.ptabs div` gap between icon and label",
                dimen("swarm_space_4").toInt(),
                (selected.layoutParams as ViewGroup.MarginLayoutParams).topMargin,
            ),
            Claim(
                "`.ptabs svg` size",
                px(KitOrigin.cssDp(".ptabs svg", "width")).toInt(),
                bar.getChildAt(0).kitRequire(KitTag.TAB_ICON).layoutParams.width,
            ),
        )
        assertEquals(emptyList<String>(), mismatches(claims))
    }

    /** The badge is anchored to a tab and shows only when something needs the user. */
    @Test
    fun `the tab badge appears only for a non-zero count`() {
        assertNull(
            "a tab with nothing waiting carries a badge, which is the one signal the product " +
                "promises means something",
            tabs(badgeCount = 0).getChildAt(0).kitFind(KitTag.BADGE),
        )
        assertTrue(
            "a tab with sessions waiting carries no badge",
            tabs(badgeCount = 1).getChildAt(0).kitFind(KitTag.BADGE) != null,
        )
    }

    // ---- badge ------------------------------------------------------------

    /**
     * The badge, derivation-table row 3: `--p-att` and NOT the mock's `#ff453a`.
     *
     * Red is retired here on semantics rather than on taste -- `--p-err` means denial, failure and
     * destruction in this product, and a session waiting for a human is none of those -- so the
     * assertion that matters most is the one that says the fill is the attention colour, the same
     * one the row dot and the row rail already carry.
     */
    @Test
    fun `the badge is the attention pill the derivation table specifies`() {
        val badge = badge(context, 3, "3 sessions need you")
        val surface = badge.background as? SubstrateSurface
        assertTrue("the badge's background is not a kit surface", surface != null)

        val claims = mutableListOf(
            Claim("badge fill", KitOrigin.token("--p-att"), surface!!.spec.fill),
            Claim("badge padding-y (top)", dimen("swarm_space_2").toInt(), badge.paddingTop),
            Claim("badge padding-x (start)", dimen("swarm_space_6").toInt(), badge.paddingStart),
            Claim("badge height", dimen("swarm_radius_chip") * 2f, badge.layoutParams.height.toFloat()),
            Claim("badge radius", dimen("swarm_radius_chip"), surface.spec.radiusPx),
            Claim("badge has no border", 0, android.graphics.Color.alpha(surface.spec.stroke)),
            Claim("badge count", "3", badge.text.toString()),
            Claim("badge content description", "3 sessions need you", badge.contentDescription),
        )
        // Row 3 gives the badge `Mono.Agent`, whose CSS rule is `.prow .ag`, in `--p-hero-ink` --
        // Substrate defines exactly one ink-for-saturated-fills token and this is the fourth site
        // of the state it fills with.
        claims += KitOrigin.textClaims(badge, ".prow .ag", KitOrigin.token("--p-hero-ink"), spScale)
        assertEquals(emptyList<String>(), mismatches(claims))

        assertNotEquals(
            "the badge is painted --p-err. The mock's red was retired on semantics: this state " +
                "is the same NeedsInput the row dot, the rail and the warmed border already carry.",
            KitOrigin.token("--p-err"),
            surface.spec.fill,
        )
        assertEquals(
            "a count of 100 or more must render as 99+; the badge is 16dp tall and three digits " +
                "either overflow it or shrink the type below the 10sp floor PB-DS-12 already flags",
            "99+",
            badge(context, 128, "128 sessions need you").text.toString(),
        )
        assertEquals("99", badge(context, 99, "99 sessions need you").text.toString())
    }

    /** The negative control PB-DS-10 requires for this suite. */
    @Test
    fun `the chrome assertions can actually fail`() {
        // The typeface probe is validated against two faces whose pitch is not in question,
        // BEFORE any component is blamed for reporting the wrong one. A probe that cannot tell
        // Typeface.MONOSPACE from Typeface.SANS_SERIF produces the same symptom as a component
        // that lost its family, and the two have opposite fixes.
        assertEquals(emptyList<String>(), KitOrigin.typefaceProbeFaults())

        assertTrue(
            "a chip padded with the wrong scale step passes the comparison",
            mismatches(
                listOf(Claim("padding", dimen("swarm_space_8").toInt(), dimen("swarm_space_10").toInt())),
            ).isNotEmpty(),
        )
        assertTrue(
            "an opaque tab bar passes the comparison against the 88% token",
            mismatches(
                listOf(Claim("fill", KitOrigin.rgbaToken("--p-tabbg"), KitOrigin.token("--p-bg"))),
            ).isNotEmpty(),
        )
        assertTrue(
            "a red badge passes the comparison against the attention one",
            mismatches(
                listOf(Claim("fill", KitOrigin.token("--p-att"), KitOrigin.token("--p-err"))),
            ).isNotEmpty(),
        )
        assertTrue(
            "a label that is not all-caps passes the comparison",
            mismatches(listOf(Claim("allCaps", true, false))).isNotEmpty(),
        )

        // The readers must tell the two counters' colours apart, or §1.4's whole argument is
        // unasserted.
        assertNotEquals(
            "the origin reader returns the same colour for --p-hero and --p-att",
            KitOrigin.cssColour(".pnav .live", "color"),
            KitOrigin.token("--p-att"),
        )
        assertNotEquals(
            "the origin reader returns the same ink for an active tab and an inactive one",
            KitOrigin.cssColour(".ptabs div", "color"),
            KitOrigin.cssColour(".ptabs div.on", "color"),
        )
        assertNotEquals(
            "--p-tabbg resolves to the same value as --p-bg, so the translucency assertion above " +
                "cannot fail",
            KitOrigin.rgbaToken("--p-tabbg"),
            KitOrigin.token("--p-bg"),
        )
    }
}
