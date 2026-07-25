package dev.swarm.phone.runtime

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-RUN-3 -- the Kotlin half. The Go gate (android/gate/connectivity_test.go)
 * checks that android/connectivity-policy.tsv is total and internally
 * consistent. This checks that the SHIPPING implementation is that table.
 *
 * The two halves are both necessary. A policy table nothing reads is a document;
 * an implementation with no table is a decision made in code review. The failure
 * this pair prevents is drift -- the table saying "the socket closes in Doze"
 * while ConnectivityPolicy keeps it open, with each artifact defensible alone.
 *
 * The table is read from the unit-test classpath, so the module build must
 * register android/connectivity-policy.tsv as a test resource. That is a
 * deliberate constraint: a test that reads it by relative path passes or fails
 * depending on Gradle's working directory, which is not a property of the app.
 */
class ConnectivityPolicyTest {

    private fun table(): Map<String, List<String>> =
        PolicyTables.read("connectivity-policy.tsv", expectedColumns = 7)

    @Test
    fun implementation_declares_a_rule_for_every_runtime_state() {
        for (state in RuntimeState.entries) {
            assertNotNull(
                "ConnectivityPolicy has no rule for $state. PB-RUN-3 requires a stated " +
                    "answer for backgrounding, Doze, App Standby and battery saver -- an " +
                    "absent rule is the unstated policy the requirement exists to prevent",
                ConnectivityPolicy.ruleFor(state),
            )
        }
    }

    @Test
    fun implementation_matches_the_checked_in_policy_table() {
        val rows = table()
        assertEquals(
            "the policy table and RuntimeState declare different state sets",
            RuntimeState.entries.map { it.tableKey }.toSet(),
            rows.keys,
        )

        for (state in RuntimeState.entries) {
            val row = rows.getValue(state.tableKey)
            val rule = ConnectivityPolicy.ruleFor(state)

            assertEquals("$state socket", row[1], rule.socket.tableKey)
            assertEquals("$state max_wait_s", row[2].toInt(), rule.maxWaitSeconds)
            assertEquals("$state foreground_service", row[3] == "yes", rule.foregroundService)
            assertEquals(
                "$state fgs_type",
                row[4],
                rule.foregroundServiceType ?: "-",
            )
            assertEquals("$state wake_path", row[5], rule.wakePath.tableKey)
            assertEquals("$state wait_on_entry", row[6], rule.waitOnEntry.tableKey)
        }
    }

    /**
     * ADR-007 B7 and §6.0 bind the server-side wait to 25 s. The implementation
     * must not be able to ask for longer than the relay will hold, and this is
     * asserted against the CODE rather than the table so a constant edited in
     * Kotlin cannot slip past the Go gate.
     */
    @Test
    fun no_state_asks_for_a_longer_wait_than_the_relay_holds() {
        for (state in RuntimeState.entries) {
            val seconds = ConnectivityPolicy.ruleFor(state).maxWaitSeconds
            assertTrue(
                "$state asks for a ${seconds}s wait; the relay ceiling is " +
                    "${ConnectivityPolicy.RELAY_MAX_WAIT_SECONDS}s (ADR-007 B7)",
                seconds <= ConnectivityPolicy.RELAY_MAX_WAIT_SECONDS,
            )
        }
    }

    /**
     * The interaction that makes PB-RUN-3 more than documentation: a socket
     * parked for 25 s is exactly what Doze, App Standby and battery saver exist
     * to terminate. Either the process is held up -- foreground, or a foreground
     * service -- or the state does not issue the wait. An implementation that
     * leaves a wait outstanding in the background with nothing holding it does
     * not fail loudly; it fails as a session that stops updating and a keystroke
     * that never lands, which is the exit criterion.
     */
    @Test
    fun a_wait_is_only_issued_where_something_holds_the_process_up() {
        for (state in RuntimeState.entries) {
            val rule = ConnectivityPolicy.ruleFor(state)
            if (rule.maxWaitSeconds == 0) continue
            assertEquals(
                "$state issues a wait on a ${rule.socket} socket",
                SocketDisposition.CONNECTED,
                rule.socket,
            )
            assertTrue(
                "$state parks a wait for up to ${rule.maxWaitSeconds}s with neither the " +
                    "app in the foreground nor a foreground service holding the process",
                state == RuntimeState.FOREGROUND || rule.foregroundService,
            )
        }
    }

    /**
     * If the socket does not survive a state, push is the only remaining path
     * from the machine to the phone. A state with no socket and no wake path is
     * a state the app never comes back from without the user opening it.
     */
    @Test
    fun a_state_without_a_socket_names_push_as_its_wake_path() {
        for (state in RuntimeState.entries) {
            val rule = ConnectivityPolicy.ruleFor(state)
            if (rule.socket == SocketDisposition.CONNECTED) continue
            assertEquals(
                "$state leaves the socket ${rule.socket} but wakes via ${rule.wakePath}",
                WakePath.PUSH,
                rule.wakePath,
            )
        }
    }

    /**
     * wait_on_entry is what happens to an ALREADY-OUTSTANDING wait the moment the
     * state is entered -- the user pressing Home mid-session. Keeping one in a
     * state that may not issue one is the shape of the bug where a backgrounded
     * app holds a socket the OS has already stopped scheduling.
     */
    @Test
    fun an_outstanding_wait_is_only_kept_where_a_wait_is_permitted() {
        for (state in RuntimeState.entries) {
            val rule = ConnectivityPolicy.ruleFor(state)
            if (rule.waitOnEntry != WaitOnEntry.KEEP) continue
            assertTrue(
                "$state keeps an outstanding wait but permits none " +
                    "(max_wait_s=${rule.maxWaitSeconds}, socket=${rule.socket})",
                rule.maxWaitSeconds > 0 && rule.socket == SocketDisposition.CONNECTED,
            )
        }
    }

    /**
     * A foreground service without a foregroundServiceType does not start from
     * API 34, and the type is a decision Google reviews at submission. Declared
     * in both directions so flipping the boolean without touching the type is
     * caught.
     */
    @Test
    fun foreground_service_type_is_declared_exactly_when_a_service_is_used() {
        for (state in RuntimeState.entries) {
            val rule = ConnectivityPolicy.ruleFor(state)
            if (rule.foregroundService) {
                assertNotNull(
                    "$state holds a foreground service with no foregroundServiceType; " +
                        "mandatory from API 34",
                    rule.foregroundServiceType,
                )
            } else {
                assertEquals(
                    "$state declares no foreground service but names a type",
                    null,
                    rule.foregroundServiceType,
                )
            }
        }
    }
}
