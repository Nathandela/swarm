package dev.swarm.phone.ui.screens

/**
 * The machine switcher's SCREEN MODEL (wave R4, ADR-018 MM3/MM4; bead agents-tracker-hggx.5), in
 * the module's established pure-function shape (PairOnlyScreen, TriageInboxScreen -- PB-DS-9: the
 * kit takes data, views take data and copy from it, and logic lives here where the JVM suite can
 * drive it without an Activity).
 *
 * Which screen a launch resolves to. Zero machines is the pair-only world -- an install with no
 * pairings shows the one offer to pair and nothing else (PairOnlyReason.FIRST_RUN) -- and one or
 * more is the machines destination the switcher, the rows and the global inbox live behind.
 */
enum class MachinesDestination {
    /** No pairings: the pair-only screen and nothing else. */
    PAIR_ONLY,

    /** At least one pairing: the machine switcher and its destinations. */
    MACHINES,
}

/**
 * The nameable controls of the R4 screens (playbook 4.1/4.2/4.9). An affordance the model cannot
 * name cannot be asserted on, deep-linked to, or read by a screen reader, so each is an enum row
 * rather than copy composed in a view.
 */
enum class MachinesAffordance {
    /** Add computer: adds a pairing BESIDE the existing ones, never replacing (playbook 4.1). */
    ADD_COMPUTER,

    /** Switch computer: the selection that feeds the least-recently-viewed policy (playbook 4.2). */
    SWITCH_COMPUTER,

    /** Forget this computer: phone-side, distinct from machine-side revoke (playbook 4.9). */
    FORGET_COMPUTER,

    /** The aggregate inbox destination across every pairing (playbook 4.2). */
    GLOBAL_INBOX,
}

/**
 * One switcher row: the four facts of playbook 4.2:198 -- name, reachability, last successful
 * sync, needs-input count -- keyed by machine id. IDENTITY IS THE MACHINE ID; the display name is
 * never an authority (ADR-018 MM4), so two rows may share a name without colliding.
 */
data class MachineRowModel(
    val machineId: String,
    val displayName: String,
    val connected: Boolean,
    val lastSyncUnixMs: Long,
    val needsInput: Int,
    /**
     * MM8's per-machine recovery fact: this pairing's durable state failed to resume, so the row
     * is registered and has no live client. Its affordances are forget-or-re-pair, and tapping it
     * must surface [brokenReason] rather than spend a switch on a refusal the screen can state
     * itself (machines.recovery).
     */
    val broken: Boolean = false,
    /** The broken pairing's OWN fault, verbatim from the facade; empty on a healthy row. */
    val brokenReason: String = "",
) {
    /**
     * A row beyond the connection cap is parked, and a parked row must visibly show its last-sync
     * age (playbook 4.2:200-202): stale with [lastSyncUnixMs] as the instant the age is computed
     * from. A connected row is never marked stale -- rendering a deliberately-unconnected row as
     * live is the dishonest rendering ADR-018 MM3 forbids, in either direction.
     */
    val stale: Boolean get() = !connected

    /** The row's reachability word, computed here so no view invents its own vocabulary. */
    val reachability: String get() = if (connected) "connected" else "stale"
}

/** The machine switcher's pure logic: the first-run resolver and the row set. */
object MachinesScreen {

    /** Every control the R4 screens offer, nameable by the model. */
    val affordances: List<MachinesAffordance> = MachinesAffordance.entries

    /**
     * The FIRST-RUN RESOLVER: with zero machines the destination is the pair-only screen
     * (PairOnlyReason.FIRST_RUN's world); with one or more it is the machines destination. A
     * function rather than a branch in PhoneSurface's redraw, so the JVM suite drives it from the
     * empty state.
     */
    fun destinationFor(machineCount: Int): MachinesDestination =
        if (machineCount <= 0) MachinesDestination.PAIR_ONLY else MachinesDestination.MACHINES

    /**
     * The row set as drawn: one row per MACHINE ID, in the order given. Duplicate display names
     * are two rows -- identity is the id (MM4) -- and a duplicate id is folded to its first row,
     * because two rows for one pairing would be two identities for one send sequencer.
     */
    fun rows(machines: List<MachineRowModel>): List<MachineRowModel> =
        machines.distinctBy { it.machineId }
}
