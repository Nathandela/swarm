package dev.swarm.phone.ui.kit

import android.graphics.Color
import android.graphics.Typeface
import android.text.TextPaint
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import dev.swarm.phone.theme.DesignScale
import dev.swarm.phone.theme.DesignTokens
import dev.swarm.phone.theme.TypeScale
import kotlin.math.abs
import kotlin.math.roundToInt

/**
 * Test scaffolding for PB-DS-10: what the DESIGN says a component should look like.
 *
 * EVERY EXPECTED VALUE IN THIS PACKAGE IS COMPUTED HERE, from the staged design artifact, the
 * staged token origin and the two checked-in joins -- never from a constant recorded out of the
 * kit. The requirement says so in as many words, and it says so because that is exactly how
 * `colors.xml` drifted to a third palette with its own test green: the test asserted that the app
 * renders whatever `colors.xml` says, which is what it would do if `colors.xml` were wrong.
 *
 * [DesignScale] and [DesignTokens] are reused rather than reimplemented. They already read the
 * shared structural CSS block, resolve `var(--p-*)` against the token origin and convert hex to
 * ARGB, and a second reader here could disagree with the one PB-DS-1..4 is asserted through.
 *
 * THE TWO BLEND FORMS ARE IMPLEMENTED INDEPENDENTLY OF [ColorMix], and that is the point of them
 * being here at all. The kit's blend is one premultiplied expression covering both CSS forms; the
 * two functions below are the special cases written the obvious way -- an alpha over transparent
 * PRESERVES the base's RGB, and a mix of two opaque colours is the plain weighted average. If the
 * kit ever acquires the un-premultiplied implementation (which darkens a colour on its way to
 * transparent and still reads as "a dimmer version of the token" in a diff), these disagree.
 */
object KitOrigin {

    /** The staged Group -> token join, PB-TOK-8's table. */
    private const val GROUP_TOKENS = "group-tokens.tsv"

    /** `--p-att` -> the ARGB the origin declares for it. */
    fun token(name: String): Int {
        val raw = requireNotNull(DesignTokens.raw(name)) {
            "the token origin declares no colour token $name"
        }
        return DesignTokens.toArgb(raw)
    }

    /**
     * One CSS declaration, resolved through `var()` to an ARGB.
     *
     * `border: 1px solid var(--p-hair)` and `background: var(--p-card)` both work: what is looked
     * for is the first hex the resolved value carries, because the properties that name a colour
     * in this design either are one or end in one.
     */
    fun cssColour(selector: String, property: String): Int {
        val raw = requireNotNull(DesignScale.rule(selector)[property]) {
            "`$selector` declares no $property, so nothing can be expected of the component that " +
                "cites it"
        }
        val resolved = DesignScale.resolve(raw)
        val hex = HEX.find(resolved)?.value
            ?: error("`$selector { $property: $resolved }` resolves to no colour")
        return DesignTokens.toArgb(hex)
    }

    /**
     * One declaration of the OBSIDIAN MAQUETTE, resolved through `var()` to an ARGB.
     *
     * [cssColour]'s reader pointed at ADR-009 D2's normative source rather than at the Substrate
     * artifact. Both are read in this suite and the split is recorded on [DesignScale]: the
     * maquette states the app's own surfaces, the older artifact still states the three frame
     * constants, the type ladder and the tab glyphs it does not draw.
     *
     * IT IS A SEPARATE FUNCTION AND NOT A FLAG ON [cssColour], because the selector names differ.
     * The maquette calls the triage row `.slab` where Substrate calls it `.prow`, so a reader that
     * fell back from one source to the other would answer `.prow` from the artifact that has it
     * and silently stop being about the design that is normative.
     */
    fun maquetteColour(selector: String, property: String): Int {
        val raw = requireNotNull(DesignScale.maquetteRule(selector)[property]) {
            "the maquette's `$selector` declares no $property, so nothing can be expected of the " +
                "component that cites it"
        }
        val resolved = DesignScale.resolve(raw)
        val hex = HEX.find(resolved)?.value
            ?: error("`$selector { $property: $resolved }` resolves to no colour")
        return DesignTokens.toArgb(hex)
    }

    /**
     * EVERY colour in one resolved maquette declaration, in the order the design writes them.
     *
     * [maquetteColour] answers the first, which is the whole answer for `background: var(--p-card)`
     * and exactly half of it for a gradient. ADR-009 D4.4 gives the app one vertical gradient and
     * its two stops are two tokens; a reader that could only see the first would let the bottom
     * stop drift to anything at all, including the top one.
     */
    fun maquetteColours(selector: String, property: String): List<Int> {
        val raw = requireNotNull(DesignScale.maquetteRule(selector)[property]) {
            "the maquette's `$selector` declares no $property"
        }
        val found = HEX.findAll(DesignScale.resolve(raw)).map { DesignTokens.toArgb(it.value) }.toList()
        require(found.isNotEmpty()) { "`$selector { $property: $raw }` resolves to no colour" }
        return found
    }

    /**
     * The `rgba(r, g, b, a)` inside one resolved maquette declaration -- an effect token spent by
     * a rule, as `.slab.lit { box-shadow: var(--p-lit-fx) }` spends the promoted key light.
     *
     * READING THE RULE RATHER THAN THE TOKEN IS THE POINT. [rgbaToken] answers what `--p-lit-fx`
     * IS; this answers which surface the design gives it to, which is the half no token can carry
     * and the half a component gets wrong.
     */
    fun maquetteRgba(selector: String, property: String): Int {
        val raw = requireNotNull(DesignScale.maquetteRule(selector)[property]) {
            "the maquette's `$selector` declares no $property"
        }
        val m = requireNotNull(RGBA.find(DesignScale.resolve(raw))) {
            "`$selector { $property: $raw }` resolves to no rgba()"
        }
        val (r, g, b, a) = m.destructured.toList().map { it.toFloat() }
        return Color.argb((a * 255f).roundToInt(), r.toInt(), g.toInt(), b.toInt())
    }

    /** The first px length anywhere in a resolved maquette declaration. */
    fun maquetteFirstPx(selector: String, property: String): Float {
        val raw = requireNotNull(DesignScale.maquetteRule(selector)[property]) {
            "the maquette's `$selector` declares no $property"
        }
        val m = requireNotNull(PX.find(DesignScale.resolve(raw))) {
            "`$selector { $property: $raw }` carries no px length"
        }
        return m.groupValues[1].toFloat()
    }

    /** The nth px field of a CSS declaration, in design px (which is Android dp at 1:1). */
    fun cssDp(selector: String, property: String, index: Int = 0): Float {
        val raw = requireNotNull(DesignScale.rule(selector)[property]) {
            "`$selector` declares no $property"
        }
        val fields = DesignScale.resolve(raw).trim().split(Regex("\\s+"))
        require(index < fields.size) { "`$selector { $property: $raw }` has no field $index" }
        return requireNotNull(DesignScale.px(fields[index])) {
            "`$selector { $property }` field $index is ${fields[index]}, not a px length"
        }
    }

    /** The first px length anywhere in a declaration: `blur(16px)`, `0 0 9px ...`, `1px solid`. */
    fun cssFirstPx(selector: String, property: String): Float {
        val raw = requireNotNull(DesignScale.rule(selector)[property]) {
            "`$selector` declares no $property"
        }
        val m = requireNotNull(PX.find(DesignScale.resolve(raw))) {
            "`$selector { $property: $raw }` carries no px length"
        }
        return m.groupValues[1].toFloat()
    }

    /** The percentage inside a declaration -- a `color-mix` share, always over the whole value. */
    fun cssPercent(selector: String, property: String): Float {
        val raw = requireNotNull(DesignScale.rule(selector)[property]) {
            "`$selector` declares no $property"
        }
        val m = requireNotNull(PERCENT.find(raw)) { "`$selector { $property: $raw }` has no share" }
        return m.groupValues[1].toFloat() / 100f
    }

    /** Every `.pdot.*` variant the design draws, selector -> its declarations. */
    fun dotVariants(): Map<String, Map<String, String>> =
        DesignScale.sharedCss().filterKeys { it.startsWith(".pdot.") }

    /**
     * The halo the DESIGN gives the dot whose fill is [token], or null where it declares none.
     *
     * THIS EXISTS SO THE GLOW ASSERTIONS ARE NOT SELF-COMPARISONS. They were: the expected value
     * was `Kit.groupGlow(context, group)` and the resolved value was the drawable's glow, which
     * the same call had just populated -- so the claim certified that the kit agrees with itself
     * and would have accepted any colour at all. Worse, WHICH Groups glow was read the same way,
     * so a kit that glowed on all four would have been checked against its own opinion.
     *
     * The variant is found by its FILL rather than by its selector name, because the selector
     * says nothing about which `status.Group` it belongs to: `.pdot.ok` is green, and after B134
     * green is ReadyForReview rather than Completed.
     *
     * THAT PROPERTY IS A CONSEQUENCE, NOT A VIRTUE, AND THIS KDOC USED TO SELL IT AS ONE. It read
     * "following the fill through group-tokens.tsv is the join that survives the rebinding", which
     * is true and is exactly the wrong thing to be pleased about: ADR-007 B134 decision 1 is marked
     * CONTESTED and awaiting the designer, and a suite built so that no test can object to a
     * contested decision has taken a side by making the other one unfalsifiable. Written that way,
     * the phrase reads as a design goal met.
     *
     * The indifference is kept, because the alternative is worse -- keying on the selector name
     * would hard-code Substrate's *Done* label as this suite's expected answer, which is the
     * disputed half of the decision, asserted by a file that has no business asserting it. What has
     * changed is that the hold is now carried somewhere it has mechanical force instead:
     * `s23ContestedHoldFaults` in `android/gate/s23_kit_test.go` fails the build the moment the ADR
     * stops marking the question open, and `s23ContestedBindings` keeps a revert cheap while it is.
     * This function is silent on the rebinding; that gate is not.
     */
    fun dotGlow(token: String): DotGlow? {
        val fill = token(token)
        val variant = dotVariants().entries.firstOrNull { (selector, _) ->
            cssColour(selector, "background") == fill
        } ?: return null
        val shadow = variant.value["box-shadow"]?.trim()
        // `.pdot.ok` says `box-shadow: none` and `--p-ink3` has no `.pdot` rule at all. Both mean
        // the same thing: the design gives that Group no halo.
        if (shadow == null || shadow == "none") return null
        return DotGlow(
            colour = overTransparent(fill, cssPercent(variant.key, "box-shadow")),
            radiusDp = cssFirstPx(variant.key, "box-shadow"),
        )
    }

    /** True when the design declares that rule inherits its colour rather than stating one. */
    fun inheritsColour(selector: String): Boolean = DesignScale.rule(selector)["color"] == null

    /**
     * `color-mix(in srgb, X p%, transparent)`.
     *
     * CSS interpolates in PREMULTIPLIED space, so transparent contributes nothing on any channel
     * and un-premultiplying returns X's RGB untouched at alpha p. Written out here rather than
     * delegated, because the whole value of this function is that it is not the kit's.
     */
    fun overTransparent(base: Int, share: Float): Int = Color.argb(
        (share * 255f).roundToInt(),
        Color.red(base),
        Color.green(base),
        Color.blue(base),
    )

    /** `color-mix(in srgb, X p%, Y)` where both are opaque: the plain weighted average. */
    fun overOpaque(x: Int, share: Float, y: Int): Int {
        fun channel(cx: Int, cy: Int) = (share * cx + (1 - share) * cy).roundToInt()
        return Color.argb(
            255,
            channel(Color.red(x), Color.red(y)),
            channel(Color.green(x), Color.green(y)),
            channel(Color.blue(x), Color.blue(y)),
        )
    }

    /** An `rgba(r, g, b, a)` token value -- `--p-card-fx`'s highlight, `--p-tabbg`'s fill. */
    fun rgbaToken(name: String): Int {
        val raw = requireNotNull(DesignScale.token(name)) { "the origin declares no $name" }
        val m = requireNotNull(RGBA.find(raw)) { "$name = \"$raw\" carries no rgba()" }
        val (r, g, b, a) = m.destructured.toList().map { it.toFloat() }
        return Color.argb((a * 255f).roundToInt(), r.toInt(), g.toInt(), b.toInt())
    }

    /** A fraction stated as a percentage inside a token value -- `--p-workbar`'s fade stop. */
    fun percentInToken(name: String): Float {
        val raw = requireNotNull(DesignScale.token(name)) { "the origin declares no $name" }
        val m = requireNotNull(PERCENT.find(raw)) { "$name = \"$raw\" carries no percentage" }
        return m.groupValues[1].toFloat() / 100f
    }

    /**
     * The token a derivation row binds to one named part: `--p-hair` for row 24's `fill`.
     *
     * IT IS THE ROW AND NOT A CONSTANT, which is the whole reason this function exists rather than
     * a pair of token names written into a test. `docs/design/substrate-components.md` is the only
     * place a non-Substrate component is specified -- the artifact draws no disabled CTA at all --
     * so a suite that transcribed `--p-hair` here would agree with itself forever and say nothing
     * about the document. Read this way, a row edited to a different token fails the component that
     * paints the old one.
     *
     * EVERY OCCURRENCE MUST AGREE, for `s23DocMetric`'s reason one lane over: a row is prose in a
     * table cell and a token can be restated in it, so taking the first match would make the
     * expected value depend on the order someone wrote a sentence in.
     */
    fun rowToken(component: String, field: String): String =
        tokenIn(derivationRow(component), component, field)

    /**
     * [rowToken] over a row the caller already has, so §4's table can be read by the same rule.
     *
     * The split exists because §3 and §4 are found DIFFERENTLY -- see [adjacentRow] -- and the
     * reading of a found row is identical. One reader for both is what stops the two tables
     * acquiring two ideas of what `field \`--p-token\`` means.
     */
    fun tokenIn(row: String, component: String, field: String): String {
        val matches = Regex("\\b${Regex.escape(field)}\\s+`(--p-[a-z0-9-]+)`").findAll(row)
            .map { it.groupValues[1] }
            .toList()
        require(matches.isNotEmpty()) {
            "the `$component` row states no `$field <token>`, so there is nothing to paint it with"
        }
        require(matches.distinct().size == 1) {
            "the `$component` row states $field twice and disagrees with itself: $matches"
        }
        return matches.first()
    }

    /**
     * One row of §4's adjacent-derivations table, found by its leading component cell.
     *
     * IT IS A SECOND READER RATHER THAN A WIDENING OF [derivationRow], for the reason
     * `s23FindRow` gives in `android/gate/s23_kit_test.go`: §3's rows are numbered and §4's are
     * not, so the component cell sits at a different index in each -- `| 14 | Activity row | ...`
     * against `| Drill-down nav header | ...`. A reader that tried both indexes would answer a
     * name that exists in both tables from whichever section it reached first, which is exactly
     * the ambiguity that had `ActivityRow.kt` reading §3's row while citing §4's.
     *
     * The search is SCOPED to §4 and an ambiguous name is a failure rather than a choice, for the
     * same reason: a value read out of a row nobody chose is a value nobody chose.
     */
    fun adjacentRow(component: String): String {
        val doc = readResource(COMPONENTS_DOC)
        val start = doc.indexOf(SECTION_4)
        require(start >= 0) { "$COMPONENTS_DOC has no `$SECTION_4` heading to search under" }
        val rows = doc.substring(start).substringBefore("\n## ").lineSequence()
            .filter { it.startsWith("|") && it.split("|").getOrNull(1)?.trim() == component }
            .toList()
        require(rows.size == 1) {
            "§4 of $COMPONENTS_DOC declares ${rows.size} rows for `$component`, and this reader " +
                "needs exactly one: no row is a value read out of nothing, and two is a value " +
                "read out of whichever one came first"
        }
        return rows.first()
    }

    /** §4's heading, literal so that renaming the section fails loudly rather than silently. */
    private const val SECTION_4 = "## 4. Adjacent derivations the same screens need"

    /**
     * The token a derivation row's named COLUMN specifies -- `--p-ink` for row 23's `Surface`.
     *
     * IT READS THE HEADER RATHER THAN COUNTING PIPES, because a column added to the table would
     * otherwise shift every expectation in this suite by one silently. And it takes the cell's
     * LEADING token, which is the table's own reading rule: "A token | `--p-elev` | Spend it as-is",
     * with everything after it -- the resolved ARGB, the contrast ratios, the surfaces a value is
     * measured against -- being the reviewer's arithmetic rather than a second specification.
     */
    fun cellToken(component: String, column: String): String {
        val cell = rowCell(component, column)
        return requireNotNull(Regex("`(--p-[a-z0-9-]+)`").find(cell)?.groupValues?.get(1)) {
            "the `$component` row's $column cell names no token: $cell"
        }
    }

    /** One named cell of a derivation row, by the column heading the table declares. */
    fun rowCell(component: String, column: String): String {
        val header = readResource(COMPONENTS_DOC).lineSequence()
            .firstOrNull { it.startsWith("| # | Component |") }
            ?: error("$COMPONENTS_DOC's derivation table has no header row to name columns by")
        val index = header.split("|").map { it.trim() }.indexOf(column)
        require(index > 0) { "the derivation table declares no `$column` column" }
        val cells = derivationRow(component).split("|").map { it.trim() }
        require(index < cells.size) { "the `$component` row has no $column cell" }
        return cells[index]
    }

    /**
     * One row of the derivation table, found by its leading component cell.
     *
     * Fails loudly rather than returning null or the empty string: a value read out of a row that
     * is not there is a value read out of nothing, and every assertion over it would pass.
     */
    fun derivationRow(component: String): String =
        readResource(COMPONENTS_DOC).lineSequence()
            .firstOrNull { it.startsWith("|") && it.split("|").getOrNull(2)?.trim() == component }
            ?: error("$COMPONENTS_DOC's derivation table declares no row for `$component`")

    private fun readResource(name: String): String =
        javaClass.classLoader?.getResourceAsStream(name)?.bufferedReader()?.use { it.readText() }
            ?: error(
                "$name is not on the unit-test classpath. app/build.gradle.kts must stage it as a " +
                    "unit-test resource so these assertions read the design itself rather than a " +
                    "value copied out of the implementation they are checking",
            )

    /** PB-DS-7's derivation table: the design as DECIDED, for the components Substrate never drew. */
    private const val COMPONENTS_DOC = "substrate-components.md"

    /** PB-TOK-8's join: which design token a `status.Group` IS. */
    fun groupToken(group: String): String = groupTokens()[group]
        ?: error("$GROUP_TOKENS binds no token to the status.Group $group")

    /** Every Group the checked-in table binds, in file order. */
    fun groupTokens(): Map<String, String> {
        val text = javaClass.classLoader?.getResourceAsStream(GROUP_TOKENS)
            ?.bufferedReader()?.use { it.readText() }
            ?: error(
                "$GROUP_TOKENS is not on the unit-test classpath. app/build.gradle.kts must stage " +
                    "it, so the expected colour for each Group is followed out of the checked-in " +
                    "join rather than transcribed into this suite as a second copy of it",
            )
        val out = LinkedHashMap<String, String>()
        text.lineSequence().forEach { line ->
            if (line.isBlank() || line.trimStart().startsWith("#")) return@forEach
            val fields = line.split("\t")
            require(fields.size >= 3) { "$GROUP_TOKENS row has fewer than three columns: $line" }
            out[fields[1].trim()] = fields[2].trim()
        }
        require(out.isNotEmpty()) { "$GROUP_TOKENS bound no Groups" }
        return out
    }

    /**
     * THE PLATFORM QUANTISES TEXT SIZE TO WHOLE PIXELS, and five of the eighteen styles are
     * fractional.
     *
     * `TextView` reads `android:textSize` with `TypedArray.getDimensionPixelSize`, which is
     * `(int)(f + 0.5f)`. So at density 1.0 the 9.5 sp tab label renders at 10 px, the 10.5 sp
     * section label at 11 px, and `Display.SAS`'s siblings at 12/13/14/16 -- up to 0.5 px above
     * their stated size. The error is absolute in pixels, so it shrinks as density rises: on a
     * 2.75x handset 9.5 sp is 26.125 px, quantised to 26, which is 9.45 sp.
     *
     * This is a fact about Android rather than a defect in the kit, and asserting the unrounded
     * product would fail every fractional style forever. It is recomputed here with the
     * platform's own rule rather than hidden behind a tolerance, so what the assertion says is
     * "the size is the design's, as the platform is able to express it".
     */
    fun quantisedTextSize(px: Float): Float = (px + 0.5f).toInt().toFloat()

    /**
     * Is this paint fixed-pitch?
     *
     * The resolved FAMILY of a TextView cannot be read back -- `Typeface` exposes no name and two
     * `Typeface.create` calls for the same family are not equal -- so the property that is
     * actually asked is the one that distinguishes the two families this app has: a monospace
     * face advances `i` and `W` identically and a proportional one does not. It is the same
     * measurement MonoBoxDrawingTest uses, for the same reason.
     *
     * MEASURED AT 100 px ON A COPY, not at the view's own size. At 9.5 px the two advances are a
     * couple of pixels apart and any tolerance wide enough to be safe is wide enough to swallow
     * the answer.
     *
     * IT REQUIRES `@GraphicsMode(NATIVE)` ON THE TEST CLASS. Robolectric's default LEGACY graphics
     * stubs the text stack and returns exactly one pixel per character, which makes EVERY font
     * measure fixed-pitch -- so this would report `true` for the sans styles and quietly certify
     * the opposite of the truth. [measurementIsReal] is the guard that says so out loud.
     */
    fun isFixedPitch(paint: TextPaint): Boolean {
        val probe = TextPaint(paint).apply { textSize = MEASURE_SIZE }
        return abs(probe.measureText("i") - probe.measureText("W")) < 0.01f
    }

    /** False when text measurement is stubbed, which makes every pitch answer meaningless. */
    fun measurementIsReal(paint: TextPaint): Boolean =
        TextPaint(paint).apply { textSize = MEASURE_SIZE }.measureText("M") > 2f

    /** [isFixedPitch] over a typeface directly -- no view, no style, no resource table. */
    fun isFixedPitch(face: Typeface): Boolean =
        isFixedPitch(TextPaint().apply { typeface = face })

    /**
     * VALIDATES THE PROBE ITSELF, against two typefaces whose pitch is not in question.
     *
     * Every `is monospace` claim in this package is an inference from an advance-width comparison,
     * and that inference has two failure modes that produce IDENTICAL symptoms in the suite: the
     * probe cannot tell any two faces apart (so everything reads fixed-pitch), or `setTextAppearance`
     * is not delivering `android:fontFamily` (so everything really IS one face). Both show up as
     * "sans selector reports monospace", and they have opposite fixes.
     *
     * Constructing the two platform faces directly separates them. If this reports a fault, the
     * probe is the defect and no component is implicated; if it is clean and a component still
     * reports the wrong pitch, the component or its style is. Asserted in all three suites, because
     * a guard that is only defined is not a guard.
     *
     * @return one line per fault. Empty means the probe can answer the question it is asked.
     */
    fun typefaceProbeFaults(): List<String> {
        val faults = mutableListOf<String>()
        val plain = TextPaint()
        if (!measurementIsReal(plain)) {
            faults += "text measurement is stubbed: `M` at ${MEASURE_SIZE}px measures " +
                "${TextPaint(plain).apply { textSize = MEASURE_SIZE }.measureText("M")}. " +
                "@GraphicsMode(NATIVE) is missing, Paint.measureText returns one pixel per " +
                "character, and every pitch claim in this package is about nothing."
        }
        if (!isFixedPitch(Typeface.MONOSPACE)) {
            faults += "the probe says Typeface.MONOSPACE is NOT fixed-pitch, so it cannot " +
                "recognise a monospace face at all"
        }
        if (isFixedPitch(Typeface.SANS_SERIF)) {
            faults += "the probe says Typeface.SANS_SERIF IS fixed-pitch, so it answers " +
                "\"monospace\" for everything and every mono claim in this package passes vacuously"
        }
        return faults
    }

    /** Large enough that an advance difference is bigger than any rounding. */
    private const val MEASURE_SIZE = 100f

    /**
     * The four things PB-DS-10 names, for one text view against the CSS rule it renders.
     *
     * `ink` is passed rather than read from the selector because colour is NOT welded to a metric
     * style in this design -- Substrate binds one `Label.Button` to three inks across the three
     * CTA variants, `.chip.on` re-inks `.chip` without restating its font, and four rules inherit
     * `--p-ink` from `.pscreen` rather than declaring it.
     *
     * FAMILY IS ASSERTED AS PITCH, for the reason [isFixedPitch] gives: a TextView's resolved
     * typeface has no readable name.
     */
    fun textClaims(view: TextView, selector: String, ink: Int, spScale: Float): List<Claim> {
        val spec = TypeScale.designSpec(selector)
        return listOf(
            Claim("`$selector` size", quantisedTextSize(spec.sizePx * spScale), view.textSize),
            Claim("`$selector` tracking", spec.trackingEm, view.letterSpacing),
            Claim("`$selector` ink", ink, view.currentTextColor),
            Claim("`$selector` is monospace", spec.androidFamily == "monospace", isFixedPitch(view.paint)),
        )
    }

    private val HEX = Regex("#[0-9A-Fa-f]{8}|#[0-9A-Fa-f]{6}")
    private val PX = Regex("([0-9]*\\.?[0-9]+)px")
    private val PERCENT = Regex("([0-9]*\\.?[0-9]+)%")
    private val RGBA = Regex(
        "rgba\\(\\s*([0-9.]+)\\s*,\\s*([0-9.]+)\\s*,\\s*([0-9.]+)\\s*,\\s*([0-9.]+)\\s*\\)",
    )
}

/** The halo one `.pdot` variant declares: the blended colour, and the blur radius in design px. */
data class DotGlow(val colour: Int, val radiusDp: Float)

/**
 * The reason a screen reader would announce nothing useful for a view, or null when it has
 * something to say.
 *
 * `contentDescription = ""` IS NOT "NO DESCRIPTION". It is the platform's idiom for a decorative
 * view: a non-null description is what a screen reader reads INSTEAD of the view's own text, and
 * an empty one is therefore a request to say nothing. `null` is the value that means "I have no
 * words of my own, read what is written on me" -- so on a badge whose entire purpose is to be
 * announced, the empty string is strictly worse than the absent one. `item.badgeDescription ?: ""`
 * shipped exactly that.
 *
 * It takes the two values rather than the view so the negative control can feed it the shipped
 * one directly, through the same function the real assertion calls.
 */
fun announcementFault(description: CharSequence?, text: CharSequence?): String? = when {
    description != null && description.isBlank() ->
        "the content description is the empty string, which asks a screen reader to skip the " +
            "view rather than to read what is on it. `null` is the value that means \"no words " +
            "of my own\"."
    description == null && text.isNullOrBlank() ->
        "the view carries neither a content description nor any text, so a screen reader has " +
            "nothing at all to announce for it"
    else -> null
}

/**
 * One appearance claim: a named property, what the design says it is, and what the component
 * resolved to.
 *
 * THE CLAIM LIST AND [mismatches] EXIST SO THE NEGATIVE CONTROL CAN BE THE SAME COMPARISON.
 * PB-DS-10 requires each appearance test to carry a control proving it fails when the value
 * diverges, and a control that rebuilds the comparison inline proves something about the copy and
 * nothing about the assertion: rewrite a real assertion as `assertEquals(got, got)` -- the
 * self-comparison this project has already shipped once -- and a control with its own loop stays
 * green while certifying a component nobody checked. Every control in this package feeds a
 * perturbed claim to THIS function.
 */
data class Claim(val what: String, val want: Any?, val got: Any?)

/** @return one line per claim whose resolved value is not the design's. Empty means it matches. */
fun mismatches(claims: List<Claim>): List<String> = claims.mapNotNull { claim ->
    val agrees = when {
        claim.want is Float && claim.got is Float -> abs(claim.want - claim.got) < 0.01f
        else -> claim.want == claim.got
    }
    if (agrees) {
        null
    } else {
        val format = { v: Any? ->
            when (v) {
                is Int -> "0x%08X".format(v)
                else -> v.toString()
            }
        }
        "${claim.what}: design says ${format(claim.want)}, component resolved ${format(claim.got)}"
    }
}

/**
 * Every reason a view's touch target is under PB-DS-12's floor, measured as a screen lays it out.
 *
 * IT MEASURES RATHER THAN READING `minimumHeight`, and the difference is the failure this check
 * exists for. A minimum is a request; what a finger gets is the box the view ends up with, and a
 * fixed `layoutParams` height, a parent's EXACTLY spec or a child that swallowed the space all
 * produce a view whose minimum says 48 and whose box does not.
 *
 * IT SUBTRACTS ROOM THE VIEW HANDS BACK. `ctaButton` inflates itself to house the CTA bloom and
 * returns every pixel of it as negative margin, so its measured box is up to 36 dp wider than the
 * button anyone can see -- and a floor met by transparent halo is met by nothing a user can aim at.
 * The visible box is the subject; a component with no negative margins is unaffected.
 *
 * @param availableWidthPx what the parent offers. A hugging control ignores it; a MATCH_PARENT one
 *  is the width of its row, and measuring it against zero would report every row in the app as too
 *  narrow to touch.
 */
fun touchTargetFaults(view: View, floorPx: Int, availableWidthPx: Int): List<String> {
    view.measure(
        View.MeasureSpec.makeMeasureSpec(availableWidthPx, View.MeasureSpec.AT_MOST),
        View.MeasureSpec.makeMeasureSpec(0, View.MeasureSpec.UNSPECIFIED),
    )
    view.layout(0, 0, view.measuredWidth, view.measuredHeight)
    val margins = view.layoutParams as? ViewGroup.MarginLayoutParams
    fun handedBack(a: Int, b: Int) = -minOf(0, a) - minOf(0, b)
    val visible = mapOf(
        "width" to view.measuredWidth -
            handedBack(margins?.marginStart ?: 0, margins?.marginEnd ?: 0),
        "height" to view.measuredHeight -
            handedBack(margins?.topMargin ?: 0, margins?.bottomMargin ?: 0),
    )
    return visible.filterValues { it < floorPx }.map { (edge, got) ->
        "the visible $edge is $got px against PB-DS-12's ${floorPx}px floor: a control this size " +
            "refuses taps that were aimed at it"
    }
}

/**
 * Depth-first search for the part of a component the kit tagged with the design selector it
 * renders.
 *
 * THE TAGS ARE A PRODUCTION MECHANISM WITH A TEST USE, and the alternative is worse. Android has
 * no stable child identity without an `@id`, `res/values/ids.xml` is S22's file and closed, and
 * `View.generateViewId` is not stable across instances -- so a suite without tags would find its
 * subjects by walking indices, and an assertion that reads `row.getChildAt(0).getChildAt(1)`
 * silently starts checking a different view the day a component gains a child. Naming each tag
 * after the CSS rule it renders is what keeps them documentation rather than test hooks.
 */
fun View.kitFind(tag: String): View? {
    if (this.tag == tag) return this
    if (this !is ViewGroup) return null
    for (i in 0 until childCount) {
        getChildAt(i).kitFind(tag)?.let { return it }
    }
    return null
}

/** [kitFind], failing loudly. A null subject makes every assertion over it vacuous. */
fun View.kitRequire(tag: String): View = requireNotNull(kitFind(tag)) {
    "the component carries no view tagged `$tag`; every assertion about it would compare nulls"
}
