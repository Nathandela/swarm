package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

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

    /**
     * A scale step in whole pixels, ROUNDED -- which is what `getDimensionPixelSize` does and
     * therefore what a component that spends the step correctly resolves to. [KitDensityTest] is
     * the suite that runs at a density where rounding and truncation differ.
     */
    private fun dimenPx(name: String): Int = dimen(name).roundToInt()

    // ---- filter chip ------------------------------------------------------

    /** `.chip` and `.chip.on`: two fills, two inks, one set of metrics. */
    @Test
    fun `the filter chip resolves both of its states`() {
        val off = filterChip(context, "All machines", selected = false, present = null)
        val on = filterChip(context, "All machines", selected = true, present = null)

        val claims = mutableListOf(
            Claim("`.chip` padding-y (top)", dimenPx("swarm_space_8"), off.paddingTop),
            Claim("`.chip` padding-y (bottom)", dimenPx("swarm_space_8"), off.paddingBottom),
            Claim("`.chip` padding-x (start)", dimenPx("swarm_space_10"), off.paddingStart),
            Claim("`.chip` padding-x (end)", dimenPx("swarm_space_10"), off.paddingEnd),
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

        val size = px(KitOrigin.cssDp(".chip .pd", "width")).roundToInt()
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
                        dimenPx("swarm_space_4"),
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
                    Claim("`.chips` padding-x (start)", dimenPx("swarm_space_18"), row.paddingStart),
                    Claim("`.chips` padding-x (end)", dimenPx("swarm_space_18"), row.paddingEnd),
                    Claim("`.chips` padding-bottom", dimenPx("swarm_space_12"), row.paddingBottom),
                    Claim(
                        "no gap before the first chip",
                        0,
                        (row.getChildAt(0).layoutParams as ViewGroup.MarginLayoutParams).marginStart,
                    ),
                    Claim(
                        "`.chips` gap before the second chip",
                        dimenPx("swarm_space_8"),
                        (row.getChildAt(1).layoutParams as ViewGroup.MarginLayoutParams).marginStart,
                    ),
                ),
            ),
        )
    }

    // ---- section label ----------------------------------------------------

    /**
     * `.plabel`: tracked wide, uppercase, and in the tertiary ink whose contrast failure PB-DS-12
     * records rather than fixes.
     *
     * SANS, NOT MONO, SINCE ADR-012 PHASE 2 P9 (owner ruling R2, 2026-08-09). `.plabel` is
     * `--p-mono` in the design source and this style rendered it verbatim until R2 restated the
     * sans/mono boundary as "mono is for data the machine produced -- agent names, code, ids,
     * timestamps -- and nothing else": a section header is the app speaking, not machine data, so
     * it moves to sans-serif at the ruling's own specimen tracking (0.11em) rather than `.plabel`'s
     * own 0.09em. [KitOrigin.textClaims] reads both the family and the tracking through
     * `TypeScale.renderedSpec`, which carries the one R2 exception.
     *
     * `text-transform: uppercase` is the COMPONENT's, not the copy's -- `isAllCaps` rather than an
     * uppercased string, so the screen keeps passing "Needs you" and the accessibility tree keeps
     * reading it as a word rather than as four letters.
     */
    @Test
    fun `the section label is the design's sans-serif uppercase heading`() {
        val label = sectionLabel(context, "Needs you")
        val claims = mutableListOf(
            Claim("`.plabel` padding-top", dimenPx("swarm_space_12"), label.paddingTop),
            Claim("`.plabel` padding-x (start)", dimenPx("swarm_space_18"), label.paddingStart),
            Claim("`.plabel` padding-x (end)", dimenPx("swarm_space_18"), label.paddingEnd),
            Claim("`.plabel` padding-bottom", dimenPx("swarm_space_8"), label.paddingBottom),
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
            Claim("`.pnav` padding-top", dimenPx("swarm_space_4"), header.paddingTop),
            Claim("`.pnav` padding-x (start)", dimenPx("swarm_space_18"), header.paddingStart),
            Claim("`.pnav` padding-x (end)", dimenPx("swarm_space_18"), header.paddingEnd),
            Claim("`.pnav` padding-bottom", dimenPx("swarm_space_10"), header.paddingBottom),
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
                dimenPx("swarm_space_10"),
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
        // AUTHORIZED REWRITE, ADR-009 D3/D6. What this assertion said before:
        //
        //     assertNotEquals(
        //         "the live counter renders in the badge's colour. §1.4 ships both counters ONLY " +
        //             "because they are distinct instruments -- hero means live, attention means " +
        //             "this needs you -- and one colour for both is the contradiction that " +
        //             "argument avoids.",
        //         KitOrigin.token("--p-att"),
        //         counter.currentTextColor,
        //     )
        //
        // ADR-009 D3 makes --p-att VALUE-ALIAS --p-hero deliberately: "in Obsidian the accent IS
        // the needs-you signal", and D6 records the champagne as unifying five meanings Substrate
        // had split across green and amber. So the two counters now render in the same colour ON
        // PURPOSE, and an assertion that they differ is an assertion against the decision.
        //
        // WHAT SURVIVES IS THE PART THAT WAS ALWAYS THE POINT: each counter must read its own
        // token. D3 keeps --p-att on its own row and its own resource exactly as --p-cta-bg
        // already was, so a future skin can break either alias in one line -- and it can only do
        // that if nothing downstream has quietly collapsed the two. Asserting the SOURCE rather
        // than the VALUE is what makes that checkable while the alias holds.
        assertEquals(
            "the live counter must resolve --p-hero, not be handed the badge's --p-att. The two " +
                "hold the same bytes today (ADR-009 D3) and are separate tokens on separate " +
                "rows; a component that read the wrong one would look identical now and break " +
                "silently the day a skin separates them.",
            KitOrigin.token("--p-hero"),
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
     * THE EXPECTATION IS READ OUT OF THE TOKEN'S OWN `rgba()`, ALL FOUR CHANNELS OF IT. The bar
     * used to compose its fill as `--p-bg` at `--p-tabbg`'s alpha, because the token was typed
     * `effect` and had no `<color>` to spend -- which joined the ALPHA to the origin and assumed
     * the RGB. `--p-tabbg` and `--p-bg` share a value today, so the difference was invisible: the
     * same value-alias hazard the `--p-cta-bg` / `--p-hero` assertion exists to catch. The token
     * converts now and `R.color.swarm_tabbar_background` is what the bar spends; this claim reads
     * the origin's rgba() and so fails on the opaque ground AND on a drifted RGB.
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
                    Claim("`.ptabs` height", dimenPx("swarm_tabbar_height"), bar.layoutParams.height),
                    // THE BAR SPENDS NO BOTTOM PADDING, and the zero is the assertion rather than
                    // the absence of one. It used to spend `space_14` -- the mock's iPhone home
                    // indicator, reserved inside the bar's own 74 px box -- while
                    // `PhoneActivity.insetTheSystemBars` applied the real navigation inset under
                    // the whole surface, so the bar's bottom air was the design's constant PLUS a
                    // platform measurement of the same thing. Derivation row 20 puts the inset
                    // under a bar that is `tabbar_height` tall; row 19 has already ruled that an
                    // iPhone frame constant yields to the platform's own measurement.
                    Claim("`.ptabs` bottom air is the window's, not the mock's", 0, bar.paddingBottom),
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
                dimenPx("swarm_space_4"),
                (selected.layoutParams as ViewGroup.MarginLayoutParams).topMargin,
            ),
            Claim(
                "`.ptabs svg` size",
                px(KitOrigin.cssDp(".ptabs svg", "width")).roundToInt(),
                bar.getChildAt(0).kitRequire(KitTag.TAB_ICON).layoutParams.width,
            ),
        )
        assertEquals(emptyList<String>(), mismatches(claims))
    }

    /**
     * `.ptabs svg`: the four glyphs, which this bar did not draw at all.
     *
     * `TabItem.icon` was null at every call site and the icon frame held an `ImageView` with no
     * drawable in it, so the bar rendered four bare labels under four empty 22 dp boxes. The
     * glyphs were never missing from the design -- they are in the artifact, inside each tab's own
     * element -- they were missing from the app, and the screen that called the kit recorded it as
     * "drawables nobody has drawn".
     *
     * WHAT THE GO GATE CANNOT SEE IS ASSERTED HERE. `android/gate/tabbar_test.go` proves the four
     * drawables are the artifact's paths and that the kit names each one beside its label; what it
     * cannot see is whether a glyph reaches the view. Those are different failures: a drawable
     * that is perfect and unreferenced ships an empty box, which is exactly the state this test
     * was written against.
     *
     * THE INK IS THE ITEM'S, WHICH IS WHAT `stroke: currentColor` MEANS. The drawable carries the
     * platform's white so the tint has something to replace; if the tint were dropped the glyph
     * would render white on every tab and the selected one would stop being distinguishable.
     */
    @Test
    fun `every tab draws the glyph the artifact pairs with its label`() {
        val bar = tabs()
        val labels = listOf("Inbox", "Machines", "Activity", "Settings")
        labels.forEachIndexed { index, label ->
            val image = tabGlyphView(bar, index)
            assertNotNull(
                "the $label tab draws no glyph. The artifact puts one inside that tab's own " +
                    "element, so a null drawable here is a 22dp empty box on the bar.",
                image.drawable,
            )
        }

        val claims = labels.indices.map { index ->
            Claim(
                "`.ptabs div${if (index == 0) ".on" else ""}` glyph ink",
                KitOrigin.cssColour(if (index == 0) ".ptabs div.on" else ".ptabs div", "color"),
                tabGlyphView(bar, index).imageTintList?.defaultColor,
            )
        }
        assertEquals(emptyList<String>(), mismatches(claims))

        // THE CONTROL, through the same lookup the four assertions above go through. A bar that
        // drew SOMETHING for every label -- a placeholder, a shared fallback -- would satisfy all
        // four and would satisfy them for a tab the design has no glyph for.
        val unknown = tabBar(context, listOf(TabItem("Terminal")))
        assertNull(
            "a tab the artifact draws no glyph for still gets one, so the assertions above are " +
                "about a drawable being present rather than about the design's own pairing",
            tabGlyphView(unknown, 0).drawable,
        )
    }

    private fun tabGlyphView(bar: LinearLayout, index: Int): ImageView =
        (bar.getChildAt(index).kitRequire(KitTag.TAB_ICON) as ViewGroup).getChildAt(0) as ImageView

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
            // WHICH STEP THESE ARE IS ROW 3'S, AND IT IS CHECKED WHERE THE ROW CAN BE READ.
            // Substrate draws no badge, so there is no `.badge` rule for PB-DS-1's ledger to
            // absorb and no CSS these two can be computed from -- the authority is the sentence
            // "padding `space_2` x `space_6`" in the derivation table, which is a file this suite
            // has no access to (it is not staged on the unit-test classpath). So the join lives in
            // android/gate/s23_kit_test.go, TestPBDS7_EveryDerivedSpacingIsTheRowsStep: it reads
            // the steps out of row 3 and requires Badge.kt to spend them. What is left here is the
            // other half, which only a running resource table can answer -- that the step resolves
            // to the pixels the component actually spent. Read alone, this claim has
            // R.dimen.swarm_space_2 on both sides of it and certifies nothing about the design.
            Claim("badge padding-y (top)", dimenPx("swarm_space_2"), badge.paddingTop),
            Claim("badge padding-x (start)", dimenPx("swarm_space_6"), badge.paddingStart),
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

    /**
     * agents-tracker-ksvb.3: the pill has a floor, and the floor is its own height.
     *
     * THE BADGE IS ANCHORED, WHICH IS WHY ITS WIDTH IS NOT ITS OWN BUSINESS. It hangs off the
     * Inbox tab's icon at `Gravity.END or Gravity.TOP` with a negative `marginEnd`, so it grows
     * LEFTWARD across the glyph it is pinned to. A box sized to its text alone is a different
     * width for every count -- a one-digit badge is a narrow lozenge and the ninth session
     * arriving turns it into a wide one, moving the mark against the icon underneath. A minimum
     * equal to the 16 dp height makes one digit sit in a square, which is the box row 3 draws.
     *
     * IT IS `minimumWidth` RATHER THAN A FIXED WIDTH, because two digits and `99+` still have to
     * fit: a fixed box would clip the count this component exists to show.
     */
    @Test
    fun `the badge's pill is at least as wide as it is tall`() {
        val badge = badge(context, 9, "9 sessions need you")

        assertEquals(
            "the badge has no width floor, so a one-digit count draws a narrower pill than a " +
                "two-digit one and the mark shifts against the icon it is anchored to when the " +
                "ninth session arrives",
            badge.layoutParams.height,
            badge.minimumWidth,
        )
    }

    /**
     * THE BADGE IS NEVER SILENCED, which is what `?: ""` did.
     *
     * The tab bar filled a missing description with the empty string. A non-null content
     * description is what a screen reader reads INSTEAD of a view's own text, so an empty one is a
     * request to say nothing at all -- on the one signal this product promises means something,
     * and on a badge whose text is the count itself. `null` is the value that means "no words of
     * my own, read what is on me", which leaves the count announceable.
     *
     * Both spellings are invisible in a diff and only one of them can be heard.
     */
    @Test
    fun `the tab badge is announceable whether or not the screen gives it words`() {
        val described = tabs(badgeCount = 3).getChildAt(0).kitRequire(KitTag.BADGE) as TextView
        assertEquals("needs you", described.contentDescription)

        val bare = tabBar(context, listOf(TabItem("Inbox", selected = true, badgeCount = 3)))
            .getChildAt(0).kitRequire(KitTag.BADGE) as TextView
        assertNull(
            "a tab that named no description gives its badge the EMPTY one, which asks a screen " +
                "reader to skip the view rather than to read the count on it. Passing the absent " +
                "description through is what leaves \"3\" announceable.",
            bare.contentDescription,
        )
        assertNull(announcementFault(bare.contentDescription, bare.text))
        assertNull(announcementFault(described.contentDescription, described.text))

        // The control, through the same function: the value the kit used to construct.
        assertNotNull(
            "an empty content description passes the announcement check, so the shipped defect " +
                "would pass it too",
            announcementFault("", bare.text),
        )
        assertNotNull(
            "a view with neither words nor text passes the announcement check",
            announcementFault(null, ""),
        )
    }

    /**
     * The presence dot is a compound drawable, and a drawable cannot carry a description.
     *
     * `.chip .pd` is a 5dp mark whose colour is the machine's entire online/offline state, and it
     * is drawn INTO the chip rather than beside it -- so the only view that can speak for it is
     * the chip. The kit does not compose that sentence: copy is the screen's (PB-DS-9), and
     * "nathans-mbp, online" is copy. What it does is take one, and take `null` rather than an
     * empty string when there is none, so the chip's own label is still read.
     */
    @Test
    fun `the chip can speak for the presence dot drawn inside it`() {
        val bare = filterChip(context, "nathans-mbp", selected = false, present = true)
        assertNull(
            "a chip with no supplied description carries an empty one, which silences the label " +
                "as well as the dot",
            bare.contentDescription,
        )
        assertNull(announcementFault(bare.contentDescription, bare.text))

        val spoken = filterChip(
            context, "nathans-mbp", selected = false, present = true,
            contentDescription = "nathans-mbp, online",
        )
        assertEquals("nathans-mbp, online", spoken.contentDescription)
        assertEquals(
            "the chip's visible label changed with its description; the description is what a " +
                "screen reader hears, not what the scope bar shows",
            "nathans-mbp",
            spoken.text.toString(),
        )
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
                listOf(Claim("padding", dimenPx("swarm_space_8"), dimenPx("swarm_space_10"))),
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

        // AUTHORIZED REWRITE, ADR-009 D3/D6. What this control said before:
        //
        //     // The readers must tell the two counters' colours apart, or §1.4's whole argument
        //     // is unasserted.
        //     assertNotEquals(
        //         "the origin reader returns the same colour for --p-hero and --p-att",
        //         KitOrigin.cssColour(".pnav .live", "color"),
        //         KitOrigin.token("--p-att"),
        //     )
        //
        // It was a discrimination control that happened to be satisfiable by the skin rather than
        // by the reader: --p-hero and --p-att differed under Substrate, so the reader looked
        // capable of telling colours apart without anything proving it could. ADR-009 D3 aliases
        // them, which retires that accident and exposes what the control was really leaning on.
        //
        // The property it was defending survives, checked against a pair the ADR keeps distinct
        // by decision: D6 holds the FOUR GROUP indicators pairwise distinct, so --p-att against
        // --p-work is a difference the reader must see and one that no future skin may collapse.
        assertNotEquals(
            "the origin reader returns the same colour for --p-att and --p-work. PB-TOK-8's four " +
                "Groups are pairwise distinct by decision (ADR-009 D6 keeps that intact while " +
                "aliasing --p-att to --p-hero), so a reader that cannot separate these two " +
                "cannot separate anything.",
            KitOrigin.token("--p-att"),
            KitOrigin.token("--p-work"),
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
