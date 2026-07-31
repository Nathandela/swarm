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
     * ADR-007 B134 decision 2: the CSS stacks name SF Pro and SF Mono, neither licensable off
     * Apple, so every text style in this app has always rendered a substitute chosen by nobody.
     * The decision is the platform families with zero bundled assets.
     *
     * android/gate/s22b_type_test.go is the authority for this table -- it joins the same pair
     * back to the ADR's own words, so the decision cannot be changed here without the record
     * disagreeing. This copy exists because the CSS says `var(--p-mono)` and Android says
     * `monospace`, and something has to translate.
     */
    private val ANDROID_FAMILY = mapOf(
        "--p-font" to "sans-serif",
        "--p-mono" to "monospace",
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
         * CSS's unitless multiplier has no Android form -- `android:lineHeight` is an absolute
         * dimension -- so the product is the value, computed rather than transcribed.
         */
        val lineHeightPx: Float? get() = lineHeightMultiplier?.let { it * sizePx }
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
