package dev.swarm.phone.theme

/**
 * Test scaffolding for PB-DS-1..4: the NON-COLOUR half of the design origin.
 *
 * [DesignTokens] deliberately reads only colours, and says so at length -- tokens.json also
 * carries font stacks, radii, weights and a gradient, and a parser that handed those to an ARGB
 * converter would either invent a conversion or fail on a perfectly valid value. This object is
 * the other half: the radii, the weight and the two tracking values, plus the spacing and
 * typography the token set never captured at all.
 *
 * BOTH SOURCES ARE READ FROM THE UNIT-TEST CLASSPATH, never by relative path, for the reason
 * [DesignTokens] and [dev.swarm.phone.runtime.PolicyTables] both give: a relative path makes the
 * test depend on Gradle's working directory, which is not a property of the app, and it silently
 * starts passing over an empty map the first time someone runs it from somewhere else.
 * app/build.gradle.kts stages both.
 *
 * WHY THE HTML ARTIFACT IS STAGED AT ALL, when the Go gate already joins it to the XML. The two
 * checks answer different questions and neither subsumes the other, which is exactly the split
 * PB-TOK-1 arrived at between android/gate/s16_tokens_test.go and [ThemeTokenOriginTest]. The Go
 * gate compares two FILES; it cannot say what the app RESOLVES, because Android picks a style
 * from the merged resource table at runtime and a value that is right in type.xml can still be
 * overridden by a library resource of the same name or by a qualifier nobody noticed. Robolectric
 * resolves it -- and it must resolve it against the DESIGN, not against a number copied out of
 * the implementation it is checking, or the test certifies that the app renders whatever type.xml
 * says. That is the precise mistake that let colors.xml drift to a third palette with its own
 * test green.
 *
 * ONLY THE SHARED STRUCTURAL BLOCK IS PARSED. The artifact draws four candidate skins and `.d2`,
 * `.d3` and `.d4` override the same selectors with different values (`.d2 .pnav .big` is 30px,
 * `.d4 .pnav .big` is 21px). PB-TOK-2 chose Substrate; a parser that swept the whole file would
 * resolve a selector to whichever skin it met last.
 */
object DesignScale {

    private const val TOKENS_RESOURCE = "tokens.json"
    private const val DESIGN_RESOURCE = "remote-control-design-directions.html"

    private const val SHARED_START = "/* ---------- shared phone structure ---------- */"
    private const val SHARED_END = "/* ============ D1 SUBSTRATE ============ */"

    /** `"--p-card-r": "9px"`, and every other token regardless of what kind of value it holds. */
    private val ANY_TOKEN = Regex("\"(--[A-Za-z0-9-]+)\"\\s*:\\s*\"((?:[^\"\\\\]|\\\\.)*)\"")

    private val COMMENT = Regex("/\\*.*?\\*/", RegexOption.DOT_MATCHES_ALL)
    private val RULE = Regex("([^{}]+)\\{([^{}]*)\\}", RegexOption.DOT_MATCHES_ALL)
    private val VAR = Regex("var\\(\\s*(--[A-Za-z0-9-]+)\\s*\\)")

    /** Every token the origin declares, colour or not. */
    fun tokens(): Map<String, String> {
        val text = readResource(TOKENS_RESOURCE)
        val out = LinkedHashMap<String, String>()
        ANY_TOKEN.findAll(text).forEach { m ->
            // The "kinds" object beside "tokens" maps the same keys to a kind name; a token whose
            // value is a kind ("color", "dimen") would silently overwrite the real one, so the
            // FIRST occurrence wins -- "tokens" comes first in the document.
            out.putIfAbsent(m.groupValues[1], m.groupValues[2].replace("\\\"", "\""))
        }
        require(out.isNotEmpty()) {
            "$TOKENS_RESOURCE declares no tokens; every assertion over the origin would be vacuous"
        }
        return out
    }

    /** One token's literal value, or null when the origin does not declare it. */
    fun token(name: String): String? = tokens()[name]

    /**
     * A token declared in px, as a float. The design's px is Android dp at 1:1 -- the mock is a
     * 386x812 frame at device scale, so there is no conversion factor to apply or to forget.
     */
    fun tokenPx(name: String): Float {
        val raw = requireNotNull(token(name)) { "the token origin declares no $name" }
        return requireNotNull(px(raw)) { "$name = \"$raw\" is not a px length" }
    }

    /** The shared structural CSS: selector -> declarations, in declaration order. */
    fun sharedCss(): Map<String, Map<String, String>> {
        val text = readResource(DESIGN_RESOURCE)
        val start = text.indexOf(SHARED_START)
        val end = text.indexOf(SHARED_END)
        require(start >= 0 && end > start) {
            "$DESIGN_RESOURCE no longer delimits the shared structural block with \"$SHARED_START\"" +
                " and \"$SHARED_END\". Every expected value in these tests is computed from that " +
                "block; without it they would compare against an empty map and pass vacuously."
        }
        val block = COMMENT.replace(text.substring(start, end), "\n")

        val out = LinkedHashMap<String, LinkedHashMap<String, String>>()
        RULE.findAll(block).forEach { m ->
            m.groupValues[1].split(",").forEach { rawSelector ->
                val selector = rawSelector.trim().split(Regex("\\s+")).joinToString(" ")
                if (selector.isEmpty() || selector.startsWith("@")) return@forEach
                val decls = out.getOrPut(selector) { LinkedHashMap() }
                m.groupValues[2].split(";").forEach { decl ->
                    val colon = decl.indexOf(':')
                    if (colon <= 0) return@forEach
                    val prop = decl.substring(0, colon).trim()
                    val value = decl.substring(colon + 1).trim()
                    if (prop.isNotEmpty() && value.isNotEmpty()) decls[prop] = value
                }
            }
        }
        require(out.isNotEmpty()) { "no CSS rules parsed from the shared block of $DESIGN_RESOURCE" }
        return out
    }

    /** One rule's declarations. Fails loudly rather than returning an empty map. */
    fun rule(selector: String): Map<String, String> =
        requireNotNull(sharedCss()[selector]) {
            "the design source declares no `$selector`; an assertion over it would say nothing"
        }

    /**
     * Substitutes every `var(--p-*)` with the token origin's literal.
     *
     * An unresolvable var throws rather than passing through: the artifact carries one already
     * (`.panelframe .cap` names `var(--mono)`, which no skin declares), and a resolver that left
     * it alone would compare a font family against the string "var(--mono)".
     */
    fun resolve(value: String): String = VAR.replace(value) { m ->
        requireNotNull(tokens()[m.groupValues[1]]) {
            "\"$value\" references ${m.groupValues[1]}, which the token origin does not declare"
        }
    }

    /** `12px` -> 12f, `0` -> 0f, anything else -> null. */
    fun px(value: String): Float? {
        val v = value.trim()
        if (v == "0") return 0f
        if (!v.endsWith("px")) return null
        return v.removeSuffix("px").toFloatOrNull()
    }

    private fun readResource(name: String): String =
        javaClass.classLoader?.getResourceAsStream(name)?.bufferedReader()?.use { it.readText() }
            ?: error(
                "$name is not on the unit-test classpath. app/build.gradle.kts must stage it as a " +
                    "unit-test resource so these assertions read the design itself rather than a " +
                    "number copied out of the implementation they are checking",
            )
}
