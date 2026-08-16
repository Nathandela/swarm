package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for wave R4's global inbox destination (inbox.global, bead
 * agents-tracker-0ox9).
 *
 * COMPILE-RED ON PURPOSE: `GlobalInboxScreen` and `GlobalInboxRowModel` do not exist. The facade
 * half landed in round 1 (`App.GlobalInbox`, rows keyed by the TUPLE (machine_id, session_id)),
 * is ledgered unbound in android/unbound-verbs.tsv, and no Kotlin composes it. This file freezes
 * the phone-side keying contract BEFORE the destination is composed, because the tuple is the R4
 * exit criterion the aggregate surface exists for: two machines may serve the same session id and
 * the same title without colliding (ADR-018 MM4, playbook 4.2 "Session identity is always
 * (machine_id, session_id); a display title is never an authority").
 */
class GlobalInboxScreenTest {

    private fun item(
        machineId: String,
        sessionId: String,
        machineName: String = "laptop",
        title: String = "session",
        needsInput: Boolean = false,
    ) = GlobalInboxRowModel(
        machineId = machineId,
        machineName = machineName,
        sessionId = sessionId,
        title = title,
        needsInput = needsInput,
    )

    @Test
    fun theSameSessionIdOnTwoMachinesIsTwoRows() {
        val rows = GlobalInboxScreen.rows(
            listOf(item("m-a", "s-1"), item("m-b", "s-1")),
        )
        assertEquals(
            "session id s-1 served by two machines collapsed into one row; identity is the " +
                "TUPLE (machine_id, session_id), and folding on session id alone is the " +
                "collision the aggregate inbox exists to refuse (R4 exit, MM4)",
            2,
            rows.size,
        )
        assertTrue(
            "the two rows do not carry distinct (machine, session) keys",
            rows.map { it.machineId to it.sessionId }.toSet().size == 2,
        )
    }

    @Test
    fun aDuplicateTupleIsFoldedToItsFirstRow() {
        val rows = GlobalInboxScreen.rows(
            listOf(item("m-a", "s-1", title = "first"), item("m-a", "s-1", title = "second")),
        )
        assertEquals(
            "one (machine, session) tuple drew two rows; two rows for one identity is the " +
                "duplicate the machine-switcher row set already refuses for machine ids",
            1,
            rows.size,
        )
        assertEquals("first", rows.single().title)
    }

    @Test
    fun aSharedTitleIsNeverAnAuthority() {
        val rows = GlobalInboxScreen.rows(
            listOf(
                item("m-a", "s-1", title = "blog"),
                item("m-b", "s-2", title = "blog"),
            ),
        )
        assertEquals(
            "two sessions sharing a display title folded; a title is never an authority " +
                "(playbook 4.2)",
            2,
            rows.size,
        )
    }
}
