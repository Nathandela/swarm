package dev.swarm.phone.runtime

/**
 * Test scaffolding: reads the checked-in policy TSVs that the Go build gate
 * (android/gate/connectivity_test.go) validates, so the Kotlin implementation
 * and the Go-checked table cannot drift apart.
 *
 * The tables are read from the UNIT-TEST CLASSPATH, not by relative path. The
 * module build must therefore register android/connectivity-policy.tsv and
 * android/fcm-priority.tsv as test resources. A relative path would make these
 * tests pass or fail on Gradle's working directory, which is not a property of
 * the app, and would silently start passing over an empty map the first time
 * someone ran them from a different directory.
 */
object PolicyTables {

    /**
     * Returns the table keyed by its first column. Fails loudly rather than
     * returning an empty map: a table that silently reads as empty makes every
     * assertion over it pass.
     */
    fun read(resourceName: String, expectedColumns: Int): Map<String, List<String>> {
        val stream = javaClass.classLoader?.getResourceAsStream(resourceName)
            ?: error(
                "$resourceName is not on the unit-test classpath. The module build must " +
                    "register android/$resourceName as a unit-test resource so the Kotlin " +
                    "policy and the Go-checked table are the same artifact",
            )

        val rows = LinkedHashMap<String, List<String>>()
        stream.bufferedReader().useLines { lines ->
            for ((n, raw) in lines.withIndex()) {
                val line = raw.trimEnd()
                if (line.isBlank() || line.trimStart().startsWith("#")) continue
                val fields = line.split("\t")
                require(fields.size == expectedColumns) {
                    "$resourceName:${n + 1} has ${fields.size} columns, expected " +
                        "$expectedColumns: $line"
                }
                val key = fields[0].trim()
                require(rows.put(key, fields.map { it.trim() }) == null) {
                    "$resourceName declares $key twice"
                }
            }
        }
        require(rows.isNotEmpty()) { "$resourceName contains no rows" }
        return rows
    }
}
