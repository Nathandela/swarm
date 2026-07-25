package dev.swarm.phone.runtime

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * PB-RUN-5 -- converges after a reboot without waiting for the user to open the app.
 *
 * This is an OPTIMISATION and never the only path. A force-stopped package is in the stopped
 * state and receives no implicit broadcast at all, BOOT_COMPLETED included, until the user
 * launches it by hand -- so [LifecycleConvergence] gives BOOT_COMPLETED and COLD_START the
 * same plan, and LifecycleConvergenceTest asserts they stay equal.
 *
 * Acting on the plan is S16's, once there is something to reconnect.
 */
class BootReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) {
            return
        }
        // Acting on LifecycleConvergence.planFor(BOOT_COMPLETED, ...) is S16's: there is
        // nothing to reconnect until the session exists. What this slice owes is a receiver
        // that is declared, permitted and reachable.
    }
}
