package dev.swarm.phone.runtime

import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * PB-RUN-5 -- "Lifecycle events do not corrupt state: force-stop, reboot, app
 * upgrade, and network handoff (Wi-Fi <-> cellular) all converge. Tests per
 * event, composed with PB-STATE-2."
 *
 * What is and is not provable here, stated plainly because §10's honesty clause
 * forbids letting an emulator or a shadow stand in for hardware:
 *
 *   - A real force-stop cannot be issued from a test. What IS testable, and is
 *     the part that actually breaks, is its CONSEQUENCE: after a force-stop
 *     Android puts the package in the stopped state, and no implicit broadcast
 *     -- including BOOT_COMPLETED -- reaches it until the user launches it by
 *     hand. So convergence must not depend on a broadcast arriving. That is
 *     asserted below by showing the cold-start path reaches the same end state
 *     with no broadcast at all.
 *   - A real reboot likewise cannot be issued; delivering BOOT_COMPLETED to the
 *     declared receiver exercises the receiver, not the reboot.
 *   - A real Wi-Fi <-> cellular handoff is PB-E2E-5's, on hardware. The shadow
 *     ConnectivityManager exercises the callback wiring, which is where the
 *     "one re-establish, not one per stream" bug lives.
 *
 * The durable-state half of every one of these is PB-STATE-1/-2 and belongs to
 * the Go core; this asserts the Android lifecycle plumbing that decides whether
 * the core is ever given the chance to converge.
 */
@RunWith(RobolectricTestRunner::class)
class LifecycleConvergenceTest {

    private val context get() = ApplicationProvider.getApplicationContext<android.content.Context>()

    // --- force-stop ---------------------------------------------------------

    /**
     * The force-stop case. A cold start with persisted state present must resume
     * from it and re-establish the connection, with no broadcast involved.
     */
    @Test
    fun cold_start_after_force_stop_resumes_from_persisted_state() {
        val plan = LifecycleConvergence.planFor(
            LifecycleEvent.COLD_START,
            hasPersistedState = true,
        )
        assertTrue(
            "a cold start after force-stop must resume the persisted session rather " +
                "than start clean; starting clean is how a paired device silently " +
                "un-pairs itself",
            plan.resumesFromPersistedState,
        )
        assertTrue(plan.reestablishConnection)
    }

    /**
     * The assertion that makes the force-stop case real: the end state reached
     * WITHOUT any broadcast must equal the one reached after BOOT_COMPLETED. If
     * they differ, the app has a correctness dependency on a broadcast that a
     * force-stopped package never receives.
     */
    @Test
    fun convergence_does_not_depend_on_a_boot_broadcast_arriving() {
        val viaBroadcast = LifecycleConvergence.planFor(
            LifecycleEvent.BOOT_COMPLETED,
            hasPersistedState = true,
        )
        val viaColdStart = LifecycleConvergence.planFor(
            LifecycleEvent.COLD_START,
            hasPersistedState = true,
        )
        assertEquals(
            "a force-stopped package receives no implicit broadcast until the user " +
                "launches it, so BOOT_COMPLETED must be an optimisation and never the " +
                "only path to a converged state",
            viaBroadcast.copy(triggeredByBroadcast = false),
            viaColdStart,
        )
    }

    // --- reboot -------------------------------------------------------------

    @Test
    fun boot_completed_reaches_a_declared_receiver() {
        val intent = Intent(Intent.ACTION_BOOT_COMPLETED)
        val receivers = context.packageManager.queryBroadcastReceivers(intent, 0)
        assertTrue(
            "no manifest receiver handles ACTION_BOOT_COMPLETED, so after a reboot the " +
                "app is invisible to the machine until the user opens it",
            receivers.isNotEmpty(),
        )
    }

    @Test
    fun boot_receiver_holds_the_permission_that_makes_it_fire() {
        val info = context.packageManager.getPackageInfo(
            context.packageName,
            android.content.pm.PackageManager.GET_PERMISSIONS,
        )
        val declared = info.requestedPermissions?.toSet() ?: emptySet()
        assertTrue(
            "RECEIVE_BOOT_COMPLETED is not requested; the receiver is declared and will " +
                "never be invoked, which is silent rather than broken",
            "android.permission.RECEIVE_BOOT_COMPLETED" in declared,
        )
    }

    // --- app upgrade --------------------------------------------------------

    @Test
    fun package_replaced_keeps_persisted_state() {
        val plan = LifecycleConvergence.planFor(
            LifecycleEvent.PACKAGE_REPLACED,
            hasPersistedState = true,
        )
        assertTrue(
            "an upgrade must not discard persisted state; PB-STATE-5 requires counters " +
                "to survive it",
            plan.resumesFromPersistedState,
        )
        assertFalse(
            "an upgrade must not clear state as a side effect of re-establishing",
            plan.discardsPersistedState,
        )
    }

    // --- network handoff ----------------------------------------------------

    /**
     * A wait outstanding on a network that has gone away does not fail; it hangs
     * until some timeout unrelated to the handoff. On a Wi-Fi to cellular
     * transition that is the difference between a keystroke landing and a
     * session that looks alive and is not.
     */
    @Test
    fun losing_the_network_cancels_the_outstanding_wait() {
        val plan = LifecycleConvergence.planFor(
            LifecycleEvent.NETWORK_LOST,
            hasPersistedState = true,
        )
        assertTrue(
            "an outstanding server-side wait bound to a dead network must be cancelled, " +
                "not left to time out",
            plan.cancelOutstandingWait,
        )
    }

    /**
     * And the other half: exactly one re-establish for the handoff. Per-stream
     * reconnection is the natural implementation and it multiplies the metered
     * reconnect by the number of streams, against §6.0's drain budget.
     */
    @Test
    fun regaining_the_network_reestablishes_exactly_once() {
        val plan = LifecycleConvergence.planFor(
            LifecycleEvent.NETWORK_AVAILABLE,
            hasPersistedState = true,
        )
        assertTrue(plan.reestablishConnection)
        assertEquals(
            "a handoff must produce one re-establish, not one per stream; each is a " +
                "metered op against the relay's tumbling one-minute window",
            1,
            plan.reestablishCount,
        )
    }

    @Test
    fun planner_is_total_over_every_lifecycle_event() {
        for (event in LifecycleEvent.entries) {
            for (persisted in booleanArrayOf(true, false)) {
                assertTrue(
                    "no plan for $event (hasPersistedState=$persisted)",
                    LifecycleConvergence.planFor(event, persisted).reestablishCount >= 0,
                )
            }
        }
    }
}
