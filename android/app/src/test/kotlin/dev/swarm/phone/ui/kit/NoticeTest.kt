package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.widget.LinearLayout
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over `§4 Notice line` -- the notice component
 * (agents-tracker-ksvb.4).
 *
 * **THE DEFECT THIS IS THE CONTROL FOR IS THAT "NO APPEARANCE" IS AN APPEARANCE.** Sixteen sites
 * built a bare `TextView` and eight KDocs called that the absence of a decision. It is not: a
 * `TextView` with no `TextAppearance` renders at the platform's ~14 sp, and the largest body style
 * in this app's ladder is `Body.Message` at 12.5 sp -- so every stale mark and every routed refusal
 * was set LARGER than the block it qualified. The first assertion below is therefore as much about
 * the SIZE being on the ladder at all as about it being the right rung.
 *
 * WHAT RESOLVES FROM THE ORIGIN. §4 states one type role and two tokens. [TypeScale] follows
 * `type.xml`'s own `origin:` comment for `Body.Secondary` to `.prow .ln` and reads the size, the
 * tracking and the family out of the design source; [KitOrigin.token] reads the ARGB out of the
 * token origin. Nothing here transcribes a number.
 *
 * WHAT IS DELIBERATELY NOT HERE: any spacing claim. §4's cell says this component carries no
 * margin, no padding and no gravity, because the same sentence appears in eight different stacks
 * and the air is the composing column's. The assertions for that are the NEGATIVE ones below --
 * a notice that grew a margin would be a component deciding a screen's layout.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class NoticeTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val spScale: Float
        get() = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
        )

    private val copy = "The machine has not sent a frame for this session since 14:02."

    private fun line(kind: NoticeKind = NoticeKind.INFO) = notice(context, copy, kind)

    @Test
    fun `the notice is Body Secondary in the secondary ink`() {
        val claims = KitOrigin.textClaims(
            // `.prow .ln` is what type.xml records as Body.Secondary's origin.
            view = line(),
            selector = ".prow .ln",
            ink = KitOrigin.token("--p-ink2"),
            spScale = spScale,
        )

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the notice is `--p-ink3`, which is 3.17 to 3.50:1 on every surface in this product. " +
                "It is the ink every de-emphasised label in this kit takes, and a notice is the " +
                "one kind of prose that is always worth reading",
            KitOrigin.token("--p-ink3"),
            line().currentTextColor,
        )
    }

    /*
     * DELETED, ADR-020 D3 (2026-08-28, wave W4, lead's ruling): `the notice is smaller than the
     * platform default it used to render at`. What it asserted:
     *
     *     val ladderPx = line().textSize
     *     val platformDefaultPx = android.widget.TextView(context).textSize
     *     assertTrue(..., ladderPx < platformDefaultPx)
     *
     * It existed because the sixteen sites this component replaced rendered at the platform
     * default, which was LARGER than every style in the ladder, so a notice whose appearance was
     * dropped in a later edit would not go missing, it would get bigger. Two facts retire it.
     * First, ruling R9 put the body rung at 14 sp, the platform default's own size, and on that
     * rung every rendered attribute of `Body.Secondary` -- family, weight, tracking, size -- equals
     * the platform default, with the ink set outside the appearance by `notice()` itself. Second,
     * `android:lineHeight` is a `TextView` attribute and not a `TextAppearance` one, so it does not
     * travel through `setTextAppearance` on a framework `TextView` (measured in the W4 evidence:
     * 16 px on the notice and on a bare view alike). No attribute is left that could tell a
     * dropped appearance from an applied one, and a test asserting 14 <= 14 would be a claim
     * nobody could fail. The notice's rung and ink stay pinned by the first test in this file.
     */

    /**
     * The error variant moves the ink and NOTHING else.
     *
     * §4: "what changes is who is speaking, not how loudly". A second size or a second weight would
     * make the machine's answer shout over the screen's own sentences, and it is the plausible wrong
     * answer -- an error is the one thing a designer reaches for bold on.
     */
    @Test
    fun `the error variant is the same type in the error ink`() {
        val info = line()
        val error = line(NoticeKind.ERROR)
        val claims = listOf(
            Claim("error ink", KitOrigin.token("--p-err"), error.currentTextColor),
            Claim("size unchanged", info.textSize, error.textSize),
            Claim("tracking unchanged", info.letterSpacing, error.letterSpacing),
            Claim("typeface unchanged", info.typeface, error.typeface),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the error variant carries the same ink as the informational one, so the two say the " +
                "same thing and the parameter decides nothing",
            info.currentTextColor,
            error.currentTextColor,
        )
    }

    /**
     * §4's three `none` cells, and the absence of a box of its own.
     *
     * The plausible wrong answer is the tinted panel an error line gets in most products. This one
     * is a sentence in a column: a filled block would make every stale mark a card, and two of the
     * sixteen sites sit directly above a mono well whose own fill would then be fighting it.
     */
    @Test
    fun `the notice paints no surface and claims no air`() {
        val subject = line()
        assertNull(
            "the notice carries a background. §4's surface, border and radius cells all say " +
                "`none` -- the ground shows through, as it does for rows 8 and 22",
            subject.background,
        )
        val params = subject.layoutParams as LinearLayout.LayoutParams
        val claims = listOf(
            Claim("no top margin", 0, params.topMargin),
            Claim("no start margin", 0, params.marginStart),
            Claim("no end margin", 0, params.marginEnd),
            Claim("no bottom margin", 0, params.bottomMargin),
            Claim("no start padding", 0, subject.paddingStart),
            Claim("no top padding", 0, subject.paddingTop),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    // ---- the detail: the machine's own register (agents-tracker-ksvb.10) ----

    private val machineWords = "kill_switch: remote control is disabled (kill switch off)"

    private fun detail() = noticeDetail(context, machineWords)

    /**
     * FAILING-FIRST (TDD RED, GG-5). `.sheet2 .ctx` is a rule Substrate DRAWS, so this component
     * carries an `origin:` rather than a §4 derivation and every number below is read out of it.
     *
     * WHAT THE MACHINE SAYS IS NOT WHAT THE SCREEN SAYS, and the two had one appearance between
     * them: five screens pasted a daemon Go error into the middle of their own sentence, so
     * `remote control is disabled (kill switch off)` rendered as prose the product had written.
     * Mono is what says a string came off the wire, and tertiary is what says it is not the line
     * to act on.
     */
    @Test
    fun `the detail is the sheet2 ctx cell -- Mono Meta in the tertiary ink`() {
        val claims = KitOrigin.textClaims(
            // `.sheet2 .ctx` is what type.xml records as Mono.Meta's origin.
            view = detail(),
            selector = ".sheet2 .ctx",
            ink = KitOrigin.token("--p-ink3"),
            spScale = spScale,
        )

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * The detail RECEDES BEHIND the sentence it explains, which is the whole demotion.
     *
     * A detail set in the notice's own ink and size would be the defect this component was written
     * for wearing a second view: the machine's raw words would still be reading as the screen's
     * primary copy, just on the next line.
     */
    @Test
    fun `the detail is smaller and quieter than the notice it sits under`() {
        val head = line()
        val subject = detail()

        assertTrue(
            "the detail is set at ${subject.textSize / spScale} sp against the notice's " +
                "${head.textSize / spScale} sp, so the machine's raw reason is drawn at least as " +
                "loudly as the sentence the user is meant to act on",
            subject.textSize < head.textSize,
        )
        assertNotEquals(
            "the detail carries the notice's own ink, so nothing about it says the words came " +
                "off the wire rather than out of this product's copy",
            head.currentTextColor,
            subject.currentTextColor,
        )
        assertEquals(
            "the detail is not the tertiary ink. `--p-ink3` is what de-emphasised means everywhere " +
                "in this kit, and it is legitimate HERE and not on the notice for one reason: the " +
                "head sentence carries the user's next act, so this line is never the sole carrier " +
                "of required information",
            KitOrigin.token("--p-ink3"),
            subject.currentTextColor,
        )
    }

    /**
     * The same three `none` cells the notice takes. The air belongs to the column that composes
     * the pair, exactly as it does for the sentence above it.
     */
    @Test
    fun `the detail paints no surface and claims no air`() {
        val subject = detail()
        assertNull(
            "the detail carries a background, which would make every refusal a tinted panel",
            subject.background,
        )
        val params = subject.layoutParams as LinearLayout.LayoutParams
        val claims = listOf(
            Claim("no top margin", 0, params.topMargin),
            Claim("no start margin", 0, params.marginStart),
            Claim("no end margin", 0, params.marginEnd),
            Claim("no bottom margin", 0, params.bottomMargin),
            Claim("no start padding", 0, subject.paddingStart),
            Claim("no top padding", 0, subject.paddingTop),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * PB-DS-10's control, fed to the SAME function every assertion above calls.
     */
    @Test
    fun `the comparison fails when a value diverges`() {
        val subject = line()
        assertTrue(
            "a perturbed ink produced no mismatch, so the ink claim above would hold against a " +
                "notice painted in any colour at all",
            mismatches(
                listOf(Claim("ink", KitOrigin.token("--p-ink2") + 1, subject.currentTextColor)),
            ).isNotEmpty(),
        )
        assertTrue(
            "a perturbed size produced no mismatch, so the type claim is about nothing",
            mismatches(listOf(Claim("size", subject.textSize + 1f, subject.textSize))).isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything",
            mismatches(listOf(Claim("size", subject.textSize, subject.textSize))).isEmpty(),
        )
    }
}
