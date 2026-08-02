package dev.swarm.phone.runtime

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Test

/**
 * PB-RUN-4 -- "FCM message priority is chosen deliberately (normal-priority is
 * deferred in Doze; high-priority wakes the device but is quota'd). Decision
 * recorded; behavior tested."
 *
 * "Deliberately" is the operative word and it is not directly testable, so what
 * is tested is the shape a deliberate choice leaves behind: a priority per
 * message class with no default, a stated behaviour when the high-priority quota
 * is exhausted, and consistency with PB-RUN-3.
 *
 * The consistency test is the one that matters. Each table is defensible alone
 * and the pair can still be wrong: a connectivity policy that closes the socket
 * in Doze plus a normal-priority wake message means the machine cannot reach the
 * phone in Doze at all -- the messages queue until the maintenance window, which
 * is exactly the state the wake exists to break out of.
 */
class FcmPriorityPolicyTest {

    private fun table(): Map<String, List<String>> =
        PolicyTables.read("fcm-priority.tsv", expectedColumns = 3)

    @Test
    fun implementation_declares_a_priority_for_every_message_class() {
        for (cls in PushMessageClass.entries) {
            assertNotNull(
                "no priority declared for $cls",
                FcmPriorityPolicy.priorityFor(cls),
            )
            assertNotNull(
                "no quota-exhaustion behaviour declared for $cls",
                FcmPriorityPolicy.onQuotaExhausted(cls),
            )
        }
    }

    @Test
    fun implementation_matches_the_checked_in_priority_table() {
        val rows = table()
        assertEquals(
            "the priority table and PushMessageClass declare different class sets",
            PushMessageClass.entries.map { it.tableKey }.toSet(),
            rows.keys,
        )
        for (cls in PushMessageClass.entries) {
            val row = rows.getValue(cls.tableKey)
            assertEquals("$cls priority", row[1], FcmPriorityPolicy.priorityFor(cls).tableKey)
            assertEquals(
                "$cls on_quota_exhausted",
                row[2],
                FcmPriorityPolicy.onQuotaExhausted(cls).tableKey,
            )
        }
    }

    /**
     * PB-RUN-4 states the quota in the requirement itself. A high-priority class
     * that does not say what happens when the quota bites has recorded half a
     * decision, and the half it left out is the one that shows up in production.
     */
    @Test
    fun a_high_priority_class_states_what_happens_when_the_quota_is_exhausted() {
        for (cls in PushMessageClass.entries) {
            if (FcmPriorityPolicy.priorityFor(cls) != FcmPriority.HIGH) continue
            assertNotEquals(
                "$cls is high-priority but declares no quota-exhaustion behaviour",
                QuotaExhaustedBehaviour.NOT_APPLICABLE,
                FcmPriorityPolicy.onQuotaExhausted(cls),
            )
        }
    }

    /**
     * The join with PB-RUN-3. Normal-priority FCM messages are DEFERRED in Doze;
     * they are not merely slower. So if the Doze rule's only path back to the
     * phone is push, the wake class must be high-priority -- and conversely,
     * paying the high-priority quota for a wake nothing needs is a cost with no
     * purchaser.
     */
    @Test
    fun the_doze_wake_path_and_the_wake_priority_agree() {
        val doze = ConnectivityPolicy.ruleFor(RuntimeState.DOZE)
        val wake = FcmPriorityPolicy.priorityFor(PushMessageClass.WAKE)

        if (doze.wakePath == WakePath.PUSH) {
            assertEquals(
                "the Doze state's only wake path is push, but the wake class is $wake. " +
                    "Normal-priority messages are deferred until Doze ends, so the one " +
                    "state that needs the wake is the one this priority cannot reach",
                FcmPriority.HIGH,
                wake,
            )
        } else {
            assertNotEquals(
                "the wake class is high-priority (quota'd, wakes the device) but the Doze " +
                    "state's wake path is ${doze.wakePath}, so nothing needs it",
                FcmPriority.HIGH,
                wake,
            )
        }
    }
}
