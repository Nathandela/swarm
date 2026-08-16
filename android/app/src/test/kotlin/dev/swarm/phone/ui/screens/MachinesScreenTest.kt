package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 3 (bead agents-tracker-hggx.5): the
 * machine switcher's SCREEN MODEL, in the module's established pure-function shape
 * (PairOnlyScreen, TriageInboxScreen -- PB-DS-9: the kit takes data, not views or copy).
 *
 * COMPILE-RED ON PURPOSE: `MachinesScreen`, `MachineRowModel` and `MachinesDestination` do not
 * exist. This suite is the frozen contract the R4 implementation must supply, the same shape the
 * repo's Go RED suites take (docs/verification/r3-red convention: "undefined symbols are the
 * frozen contract").
 *
 * WHAT THE CONTRACT IS:
 *
 *  - `MachinesScreen.destinationFor(machineCount)`: the FIRST-RUN RESOLVER. Zero machines
 *    resolves to the pair-only destination (the screen an unpaired phone is shown and nothing
 *    else, PhoneSurface's drawPairOnly world); one or more resolves to the machines destination.
 *    Every test in this file starts from that resolver, because every R4 screen is reached
 *    through it.
 *  - `MachinesScreen.affordances`: the nameable controls -- ADD_COMPUTER, SWITCH_COMPUTER,
 *    FORGET_COMPUTER, GLOBAL_INBOX. An affordance the model cannot name cannot be asserted on.
 *  - `MachineRowModel`: one switcher row -- machine id, display name, reachability, last-sync
 *    instant, needs-input count, stale (playbook 4.2:198). Identity is the MACHINE ID; the
 *    display name is never an authority (ADR-018 MM4), so two rows may share a name without
 *    colliding.
 *  - `MachinesScreen.rows(...)`: parked-beyond-the-cap rows render stale with their last-sync
 *    age; connected rows do not (playbook 4.2:200-202).
 */
class MachinesScreenTest {

    // ------------------------------------------------------------------ first-run resolver

    @Test
    fun firstRunWithZeroMachinesResolvesToPairOnly() {
        assertEquals(
            "an install with no pairings shows the pair-only screen and nothing else",
            MachinesDestination.PAIR_ONLY,
            MachinesScreen.destinationFor(machineCount = 0),
        )
    }

    @Test
    fun onePairedMachineResolvesToTheMachinesDestination() {
        assertEquals(MachinesDestination.MACHINES, MachinesScreen.destinationFor(machineCount = 1))
        assertEquals(MachinesDestination.MACHINES, MachinesScreen.destinationFor(machineCount = 3))
    }

    // ------------------------------------------------------------------ nameable affordances

    @Test
    fun everyR4AffordanceIsNameableByTheModel() {
        val names = MachinesScreen.affordances.map { it.name }
        for (wanted in listOf("ADD_COMPUTER", "SWITCH_COMPUTER", "FORGET_COMPUTER", "GLOBAL_INBOX")) {
            assertTrue("affordance $wanted is not nameable by MachinesScreen", wanted in names)
        }
    }

    // ------------------------------------------------------------------ rows

    private fun row(
        id: String,
        name: String = "laptop",
        connected: Boolean = true,
        lastSyncUnixMs: Long = 1_755_300_000_000,
        needsInput: Int = 0,
    ) = MachineRowModel(
        machineId = id,
        displayName = name,
        connected = connected,
        lastSyncUnixMs = lastSyncUnixMs,
        needsInput = needsInput,
    )

    @Test
    fun duplicateDisplayNamesDoNotCollideBecauseIdentityIsTheMachineId() {
        val rows = MachinesScreen.rows(listOf(row("m-a", name = "laptop"), row("m-b", name = "laptop")))
        assertEquals("two machines named alike are two rows", 2, rows.size)
        assertEquals(setOf("m-a", "m-b"), rows.map { it.machineId }.toSet())
    }

    @Test
    fun aParkedRowRendersStaleWithItsLastSyncAge() {
        val parkedSync = 1_755_299_910_000 // 90s before the connected row's instant.
        val rows = MachinesScreen.rows(
            listOf(row("m-a", connected = false, lastSyncUnixMs = parkedSync), row("m-b", connected = true)),
        )
        val parked = rows.single { it.machineId == "m-a" }
        assertTrue("a row beyond the connection cap must visibly show its last-sync age", parked.stale)
        assertEquals(parkedSync, parked.lastSyncUnixMs)
        val connected = rows.single { it.machineId == "m-b" }
        assertFalse("a live row must not be marked stale", connected.stale)
    }

    @Test
    fun aRowCarriesItsNeedsInputCount() {
        val rows = MachinesScreen.rows(listOf(row("m-a", needsInput = 2)))
        assertEquals(2, rows.single().needsInput)
    }
}
