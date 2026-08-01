package dev.swarm.phone.theme

/**
 * Test scaffolding for PB-TOK-1: reads the ONE machine-readable token source and the checked-in
 * join, so the Kotlin theme assertions and the Go gate (android/gate/s16_tokens_test.go) are
 * built on the same two artifacts rather than on two copies of them.
 *
 * BOTH ARE READ FROM THE UNIT-TEST CLASSPATH, never by relative path. That is the arrangement
 * [dev.swarm.phone.runtime.PolicyTables] spells out for the policy TSVs and it holds for the
 * same reason: a relative path makes the test depend on Gradle's working directory, which is
 * not a property of the app, and it silently starts passing over an empty map the first time
 * someone runs it from somewhere else. app/build.gradle.kts stages both files.
 *
 * ONLY COLOUR TOKENS ARE READ, and that is stated rather than implied. tokens.json also carries
 * font stacks, radii, weights and a gradient; none of those has an ARGB form, so a parser that
 * accepted them would either invent a conversion or fail on a value that is perfectly valid.
 * The colour reader is therefore exact about what a colour looks like, and a mapped token whose
 * value is not one comes back as null -- which the test reports as a bad row rather than
 * skipping.
 */
object DesignTokens {

    private const val TOKENS_RESOURCE = "tokens.json"
    private const val MAP_RESOURCE = "design-tokens.tsv"

    /** `"--p-bg": "#08090a"` and nothing else: a value that is not a hex colour is not matched. */
    private val COLOUR_TOKEN =
        Regex(
            "\"(--[A-Za-z0-9-]+)\"\\s*:\\s*\"(#[0-9A-Fa-f]{6}|#[0-9A-Fa-f]{8}|" +
                "rgba\\([^\")]*\\))\"",
        )

    private val RGBA_VALUE =
        Regex("rgba\\(\\s*(\\d{1,3})\\s*,\\s*(\\d{1,3})\\s*,\\s*(\\d{1,3})\\s*,\\s*(0|1|0?\\.\\d+)\\s*\\)")

    /** Every COLOUR token the origin declares, token name -> its literal value. */
    fun colourTokens(): Map<String, String> {
        val text = readResource(TOKENS_RESOURCE)
        val out = LinkedHashMap<String, String>()
        COLOUR_TOKEN.findAll(text).forEach { m -> out[m.groupValues[1]] = m.groupValues[2] }
        require(out.isNotEmpty()) {
            "$TOKENS_RESOURCE declares no colour tokens; every assertion over the origin would be vacuous"
        }
        return out
    }

    /** The literal value of one token, or null when the origin does not declare it as a colour. */
    fun raw(token: String): String? = colourTokens()[token]

    /** The checked-in join: token -> the `<color name=...>` it is. */
    fun mapping(): Map<String, String> {
        val out = LinkedHashMap<String, String>()
        readResource(MAP_RESOURCE).lineSequence().forEachIndexed { n, raw ->
            val line = raw.trimEnd()
            if (line.isBlank() || line.trimStart().startsWith("#")) return@forEachIndexed
            val fields = line.split("\t")
            require(fields.size >= 2) {
                "$MAP_RESOURCE:${n + 1} needs at least a token and an android_color: $line"
            }
            val token = fields[0].trim()
            require(out.put(token, fields[1].trim()) == null) { "$MAP_RESOURCE maps $token twice" }
        }
        require(out.isNotEmpty()) { "$MAP_RESOURCE contains no rows" }
        return out
    }

    /**
     * The mapped palette as Android colours: `<color name=...>` -> ARGB.
     *
     * Row order is preserved, so a caller comparing this against a recorded IntArray is comparing
     * the origin's own ordering rather than one the test invented.
     */
    fun androidColors(): Map<String, Int> {
        val tokens = colourTokens()
        val out = LinkedHashMap<String, Int>()
        mapping().forEach { (token, resource) ->
            val value = tokens[token]
                ?: error("$MAP_RESOURCE maps $token, which $TOKENS_RESOURCE declares no colour for")
            out[resource] = toArgb(value)
        }
        return out
    }

    /**
     * `#rrggbb` or `#aarrggbb` -> the opaque ARGB int an Android colour resource resolves to.
     *
     * It is deliberately strict. A converter that accepted a three-digit shorthand, folded case
     * loosely or dropped alpha would make every value assertion pass over colours that differ,
     * which is precisely the shape PB-TOK-1's criterion ("the assertion must fail when they
     * diverge") calls out.
     */
    fun toArgb(value: String): Int {
        val v = value.trim()
        // CSS rgba(), which the skin uses for --p-tabbg. This reader was hex-only, which is one
        // of the two parsers that forced that token to be typed `effect` -- and typing it
        // `effect` is what made "every colour token reaches the app" true by leaving out the
        // colour nobody could read. Widened 2026-08-01 with the Go side, deliberately together:
        // a join whose two ends disagree about what a colour IS cannot check anything.
        RGBA_VALUE.matchEntire(v)?.let { m ->
            val ch = (1..3).map { i ->
                m.groupValues[i].toInt().also {
                    require(it in 0..255) { "$value: channel ${m.groupValues[i]} is not 0-255" }
                }
            }
            val alpha = Math.round(m.groupValues[4].toDouble() * 255.0).toInt()
            require(alpha in 0..255) { "$value: alpha is not a fraction" }
            return (alpha shl 24) or (ch[0] shl 16) or (ch[1] shl 8) or ch[2]
        }
        require(v.startsWith("#")) { "$value is not a hex or rgba() colour" }
        val hex = when (v.length) {
            7 -> "FF" + v.substring(1)
            9 -> v.substring(1)
            else -> error("$value is neither #rrggbb nor #aarrggbb")
        }
        return hex.toLong(16).toInt()
    }

    private fun readResource(name: String): String =
        javaClass.classLoader?.getResourceAsStream(name)?.bufferedReader()?.use { it.readText() }
            ?: error(
                "$name is not on the unit-test classpath. The module build must stage it as a " +
                    "unit-test resource so the Kotlin theme assertions and the Go gate read the " +
                    "same artifact rather than two copies of it",
            )
}
