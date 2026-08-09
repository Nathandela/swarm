package dev.swarm.phone.theme

/**
 * Test scaffolding for PB-DS-2 and PB-DS-3: the type scale, read from BOTH ends.
 *
 * [styles] reads the checked-in join out of type.xml -- each style's `<!-- origin: ... -->`
 * comment, which records WHICH CSS rule that named style descends from. [designSpec] then reads
 * that rule out of the staged design artifact and resolves it. So the expected value for every
 * typography assertion is computed from the design, and what type.xml supplies is only the
 * mapping: a decision a reviewer can see and disagree with, rather than a number nobody checked.
 *
 * type.xml is read here as TEXT and resolved elsewhere as a STYLE, and the pair is the point.
 * The text says what the file declares; the resolution says what Android actually hands a view
 * after merging appcompat's, camera-view's and firebase's resource tables over it. A value can be
 * right in the first and wrong in the second, and only the comparison of both catches it.
 */
object TypeScale {

    private const val TYPE_RESOURCE = "type.xml"

    /**
     * ADR-012 phase 2's rung table: where a style's SIZE has come from since owner ruling R1
     * (2026-08-09).
     *
     * Everything else about a style is still read out of the CSS rule it cites. The size is not,
     * and cannot be: R1 consolidated twelve sizes onto five rungs, and a rung is a decision about
     * this app's own hierarchy rather than a fact any design rule states. So the record that
     * decided it is read, by both halves of the join -- `android/gate/s22b_type_test.go` parses
     * the same table out of the same file, which is what stops the two from checking two
     * different ladders.
     */
    private const val RUNG_RESOURCE = "ADR-012-type-ladder-consolidation-phase-1.md"

    /** The first cell of the rung table's header row, required so a moved table fails loudly. */
    private const val RUNG_TABLE_HEADER = "Ladder style"

    /** What `--p-font` substitutes to: the platform sans, still, still with no bundled asset. */
    const val SANS_FAMILY = "sans-serif"

    /**
     * What `--p-mono` substitutes to (ADR-009 D7): the bundled family, as an XML resource
     * reference rather than a family name, because that is the only spelling Android has for a
     * face that ships inside the APK.
     *
     * IT IS A CONSTANT AND NOT A LITERAL AT EACH CALL SITE for the reason the whole of this file
     * exists: nine styles and six suites ask "is this the mono family?", and nine copies of a
     * string are nine places a skin change has to find.
     */
    const val MONO_FAMILY = "@font/jetbrains_mono"

    /**
     * The CSS line-height that states no leading at all. See [Spec.lineHeightPx] for why the
     * design's `/1` and Android's `android:lineHeight` are not the same statement.
     */
    const val NO_EXTRA_LEADING = 1f

    /**
     * The resource NAME behind a `@font/...` substitution, or null when the family is a platform
     * name the resource table knows nothing about.
     *
     * A style whose family is a bundled resource resolves to a RESOURCE ID, not to a string, so a
     * test comparing the two has to know which of the two it is holding. This is that question,
     * asked once.
     */
    fun bundledFontName(family: String): String? =
        family.removePrefix("@font/").takeIf { it != family }

    /**
     * ADR-007 B134 decision 2: the CSS stacks name SF Pro and SF Mono, neither licensable off
     * Apple, so every text style in this app has always rendered a substitute chosen by nobody.
     * The decision was the platform families with zero bundled assets.
     *
     * android/gate/s22b_type_test.go is the authority for this table -- it joins each row back to
     * the ADR that decides it, so the decision cannot be changed here without the record
     * disagreeing. This copy exists because the CSS says `var(--p-mono)` and Android says
     * something else, and something has to translate.
     *
     * AUTHORIZED REWRITE, ADR-009 D8.3 / D7 (phase O5). What this table said before:
     *
     *     private val ANDROID_FAMILY = mapOf(
     *         "--p-font" to "sans-serif",
     *         "--p-mono" to "monospace",
     *     )
     *
     * The sans row does not move. The mono row does: `monospace` is Droid Sans Mono, which does
     * not cover U+2500-257F, and MonoBoxDrawingTest measured what that costs -- the terminal
     * peek's frame renders through fallback 18% wider per character than the text it frames.
     * ADR-009 D7 bundles JetBrains Mono for the mono roles; the token value in tokens.json is
     * unchanged, because the maquette is the normative source and the substitution is the layer
     * that says what Android renders for it.
     */
    private val ANDROID_FAMILY = mapOf(
        "--p-font" to SANS_FAMILY,
        "--p-mono" to MONO_FAMILY,
    )

    private val STYLE = Regex(
        """<!--\s*origin:\s*(.*?)\s*-->\s*<style\s+name="([^"]+)"""",
        RegexOption.DOT_MATCHES_ALL,
    )

    /** style name -> the CSS selector it declares as its origin. */
    fun styles(): Map<String, String> {
        val text = readResource(TYPE_RESOURCE)
        val out = LinkedHashMap<String, String>()
        STYLE.findAll(text).forEach { m ->
            val origin = m.groupValues[1].trim().split(Regex("\\s+")).joinToString(" ")
            val name = m.groupValues[2]
            require(out.put(name, origin) == null) { "$TYPE_RESOURCE declares $name twice" }
        }
        require(out.isNotEmpty()) {
            "no `<!-- origin: ... -->` + <style> pairs parsed from $TYPE_RESOURCE; every " +
                "assertion built on this map would iterate zero times and pass"
        }
        return out
    }

    /** One row of the rung table: which rung a style stands on, and at what size. */
    data class Rung(val style: String, val origin: String, val name: String, val sp: Float)

    private val RUNG_ROW = Regex(
        """^\|\s*`([A-Za-z]+\.[A-Za-z]+)`\s*\|\s*`([^`]+)`\s*\|\s*([0-9.]+)\s*\|""" +
            """\s*([a-z]+)\s*\|\s*([0-9.]+)\s*\|""",
    )

    /** The rung table, keyed by the CSS selector the row's style cites. */
    fun rungs(): Map<String, Rung> = RUNGS

    private val RUNGS: Map<String, Rung> by lazy { parseRungs() }

    private fun parseRungs(): Map<String, Rung> {
        val text = readResource(RUNG_RESOURCE)
        require(text.lineSequence().any { it.trimStart().startsWith("| $RUNG_TABLE_HEADER ") }) {
            "$RUNG_RESOURCE has no table whose first column is `$RUNG_TABLE_HEADER`. That table " +
                "is where a style's size is decided since ruling R1; a reader that cannot find " +
                "it reports that no style has a rung, and every size assertion built on it would " +
                "then be about nothing"
        }
        val out = LinkedHashMap<String, Rung>()
        text.lineSequence().forEach { line ->
            val m = RUNG_ROW.find(line.trim()) ?: return@forEach
            val row = Rung(
                style = m.groupValues[1],
                origin = m.groupValues[2],
                name = m.groupValues[4],
                sp = m.groupValues[5].toFloat(),
            )
            require(out.put(row.origin, row) == null) {
                "$RUNG_RESOURCE puts two styles on a rung for `${row.origin}`"
            }
        }
        require(out.isNotEmpty()) {
            "no rung rows parsed from $RUNG_RESOURCE's `$RUNG_TABLE_HEADER` table"
        }
        return out
    }

    /**
     * The size a style renders at: its rung's, for the sixteen styles on the ladder.
     *
     * `Display.SAS` is not on it -- 34 sp for the pairing screen's verification emoji, a specimen
     * to be matched rather than text read in a hierarchy -- and neither is any rule the app does
     * not transcribe, so an unruled selector falls back to what the design draws. The fallback is
     * NOT silent about being one: it is the only path on which the design px is still the app's
     * size, and ADR-012 phase 2 names the one style that takes it.
     */
    fun renderedSizeSp(selector: String): Float =
        rungs()[selector]?.sp ?: designSpec(selector).sizePx

    /**
     * One CSS rule's typography as this app renders it: the design's rule, with the ruled rung's
     * size in place of the rule's own px.
     *
     * THE TWO ARE DIFFERENT CLAIMS AND BOTH ARE ASSERTED. [designSpec] is what the design draws
     * and is what the Go gate holds the rung table's `Design px` column to; this is what a view
     * has to resolve to. A suite comparing a rendered view against `designSpec` would have been
     * asserting the ladder ADR-012 phase 2 retired.
     */
    fun renderedSpec(selector: String): Spec =
        designSpec(selector).let { it.copy(sizePx = renderedSizeSp(selector)) }

    /** One CSS rule's resolved typography, in the units Android expresses them in. */
    data class Spec(
        val selector: String,
        val sizePx: Float,
        val weight: Int,
        val trackingEm: Float,
        val androidFamily: String,
        val lineHeightMultiplier: Float?,
    ) {
        /**
         * The leading to transcribe, or null where there is none TO transcribe.
         *
         * CSS's unitless multiplier has no Android form -- `android:lineHeight` is an absolute
         * dimension -- so the product is the value, computed rather than transcribed.
         *
         * **A MULTIPLIER OF 1 TRANSCRIBES AS NULL, AND IT USED TO TRANSCRIBE AS `1 x size`**
         * (ADR-009 D7, amended 2026-08-08). The two are not the same statement on this platform.
         * `line-height: 1` on a single-line label means NO EXTRA LEADING; `android:lineHeight`
         * sets the line box's absolute height, and a font's natural line box is taller than its
         * em -- so the same number there SHRINKS the box, which the platform spends as a negative
         * `lineSpacingExtra`. The visible result is `Label.Button` sitting low inside its own CTA
         * and `Label.Chip`'s descenders clipping. The honest Android form of `/1` is silence.
         */
        val lineHeightPx: Float?
            get() = lineHeightMultiplier?.takeIf { it != NO_EXTRA_LEADING }?.let { it * sizePx }

        /**
         * Does this design fact render in the mono family?
         *
         * ASKED THROUGH THE TOKEN'S SUBSTITUTION rather than by comparing the family string to a
         * literal, because the literal moved once already (ADR-009 D7) and every call site that
         * had spelled it out had to be found by hand.
         */
        val isMono: Boolean get() = androidFamily == MONO_FAMILY
    }

    /**
     * Resolve one selector against the design source.
     *
     * FAMILY INHERITANCE IS MODELLED, because CSS has it and four styles depend on it.
     * `.pnav .big`, `.prow .pj`, `.sheet2 h4` and `.m2` declare no font-family; they inherit it
     * from `.pscreen` and `.panelframe`, which both declare `font-family: var(--p-font)`.
     * Treating "declares none" as "has none" would leave those four unasserted and would quietly
     * accept a monospace display title.
     *
     * WEIGHT DEFAULTS TO 400 and tracking to 0, which is what a browser does with `normal` and
     * what Android does with an unset textFontWeight and letterSpacing.
     */
    fun designSpec(selector: String): Spec {
        val rule = DesignScale.rule(selector)
        var sizePx: Float? = null
        var weight = 400
        var tracking = 0f
        var family = "--p-font"
        var lineHeight: Float? = null

        // Declaration order matters: `font:` is a shorthand and resets what a longhand before it
        // set, exactly as in a browser.
        rule.forEach { (prop, raw) ->
            when (prop) {
                "font-size" -> sizePx = requirePx(selector, prop, DesignScale.resolve(raw))
                "font-weight" -> weight = requireInt(selector, prop, DesignScale.resolve(raw))
                "letter-spacing" -> tracking = requireEm(selector, prop, DesignScale.resolve(raw))
                "line-height" -> lineHeight = requireFloat(selector, prop, DesignScale.resolve(raw))
                "font-family" -> family = familyToken(selector, raw)
                "font" -> raw.trim().split(Regex("\\s+")).forEach { field ->
                    when {
                        field.startsWith("var(") -> family = familyToken(selector, field)
                        field.contains("px") -> {
                            val size = field.substringBefore('/')
                            sizePx = requirePx(selector, prop, size)
                            if (field.contains('/')) {
                                lineHeight = requireFloat(selector, prop, field.substringAfter('/'))
                            }
                        }
                        else -> field.toIntOrNull()?.let { weight = it }
                    }
                }
            }
        }

        return Spec(
            selector = selector,
            sizePx = requireNotNull(sizePx) {
                "`$selector` declares no font size, so it is not a text style and nothing should " +
                    "descend from it"
            },
            weight = weight,
            trackingEm = tracking,
            androidFamily = requireNotNull(ANDROID_FAMILY[family]) {
                "`$selector` renders in $family, for which ADR-007 B134 records no substitution"
            },
            lineHeightMultiplier = lineHeight,
        )
    }

    private fun familyToken(selector: String, value: String): String {
        val name = Regex("var\\(\\s*(--[A-Za-z0-9-]+)\\s*\\)").find(value)?.groupValues?.get(1)
            ?: DesignScale.tokens().entries.firstOrNull { it.value.trim() == value.trim() }?.key
            ?: error("`$selector` font-family \"$value\" matches no token in the origin")
        require(DesignScale.token(name) != null) {
            "`$selector` names $name, which the token origin does not declare"
        }
        return name
    }

    private fun requirePx(selector: String, prop: String, value: String): Float =
        requireNotNull(DesignScale.px(value)) { "`$selector { $prop: $value }` is not a px length" }

    private fun requireEm(selector: String, prop: String, value: String): Float =
        requireNotNull(value.trim().removeSuffix("em").toFloatOrNull().takeIf { value.endsWith("em") }) {
            "`$selector { $prop: $value }` is not an em length"
        }

    private fun requireInt(selector: String, prop: String, value: String): Int =
        requireNotNull(value.trim().toIntOrNull()) { "`$selector { $prop: $value }` is not a number" }

    private fun requireFloat(selector: String, prop: String, value: String): Float =
        requireNotNull(value.trim().toFloatOrNull()) {
            "`$selector { $prop: $value }` is not a unitless multiplier"
        }

    private fun readResource(name: String): String =
        javaClass.classLoader?.getResourceAsStream(name)?.bufferedReader()?.use { it.readText() }
            ?: error(
                "$name is not on the unit-test classpath. app/build.gradle.kts must stage it so " +
                    "the style-to-selector join is read from the same artifact aapt compiles, " +
                    "rather than from a second copy of it",
            )
}
