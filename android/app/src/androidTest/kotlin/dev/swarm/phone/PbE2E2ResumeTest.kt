package dev.swarm.phone

import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import dev.swarm.phone.PhoneScreenDriver.awaitPressable
import dev.swarm.phone.PhoneScreenDriver.awaitScreen
import dev.swarm.phone.PhoneScreenDriver.press
import dev.swarm.phone.PhoneScreenDriver.textOnScreen
import dev.swarm.phone.PhoneScreenDriver.type
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * PB-E2E-2's upgraded clause, on the far side of it: what the app is after a real
 * `adb shell am force-stop`.
 *
 * WHY force-stop AND NOT A PROCESS KILL. force-stop also puts the package in the STOPPED state,
 * so no implicit broadcast -- BOOT_COMPLETED included -- reaches the app until a person launches
 * it by hand. A runbook that killed the process instead would satisfy every other word of the
 * requirement and skip the clause it was upgraded for. The kill itself is
 * scripts/pbe2e2-emulator-smoke.sh's, because only adb can issue it; this is the assertion
 * afterwards.
 *
 * IT IS ASSERTED THROUGH THE PRODUCT, not through the facade. Nothing off the device can see the
 * phone's durable blob, and nothing inside the facade can show that the SCREEN came back holding
 * it. Three product-level facts stand in for the coordinates:
 *
 *   - the pairing panel says the phone is paired, which is `State.MachineStatic` surviving --
 *     the attempt record was cleared on success, so this can only come from the durable blob;
 *   - Take control becomes pressable, which is the relay cursor and the epoch key surviving far
 *     enough to drain the mailbox and redraw the roster;
 *   - a typed line is accepted, which is the SEND-SEQ CEILING surviving. A phone that came back
 *     from zero would re-use sequence numbers the machine has already seen, and the machine
 *     refuses a replayed frame -- so an accepted keystroke after a force-stop is the assertion
 *     that the ceiling did not rewind.
 *
 * PB-E2E-5 stays deferred. This is an emulator, and an emulator is not a handset.
 */
@RunWith(AndroidJUnit4::class)
class PbE2E2ResumeTest {

    @Test
    fun the_force_stopped_app_comes_back_paired_connected_and_able_to_type() {
        ActivityScenario.launch(PhoneActivity::class.java).use { app ->
            app.awaitScreen(
                "This phone is paired with your machine.",
                "the relaunched app does not believe it is paired. PB-STATE-2 requires the " +
                    "pairing to survive a force-stop; a phone that came back offering a scanner " +
                    "has lost the durable blob, or cannot open it",
            )

            app.awaitPressable(
                "Take control",
                "the relaunched app never redrew a session. Its durable coordinates -- the epoch " +
                    "key, the relay cursor -- are what let it resume the mailbox rather than " +
                    "start from nothing",
            )
            app.press("Take control")

            app.type("Type into the session you hold", "echo swarm-pb-e2e-2-after-force-stop")
            app.press("Send line")

            val after = app.textOnScreen()
            for (refusal in ROUTED_REFUSALS) {
                assertTrue(
                    "PB-STATE-2: the machine refused a line typed after the force-stop. The " +
                        "send-seq ceiling is the coordinate at stake -- a phone that resumed " +
                        "from zero re-uses numbers the machine has already seen. The screen " +
                        "said:\n$after",
                    !after.contains(refusal),
                )
            }
        }
    }

    private companion object {
        val ROUTED_REFUSALS = listOf(
            "has not given this phone control",
            "No link to your machine right now",
            "This phone is not paired with a machine yet",
        )
    }
}
