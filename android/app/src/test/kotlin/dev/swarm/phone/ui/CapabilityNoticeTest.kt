package dev.swarm.phone.ui

import dev.swarm.phone.keys.CapabilityAnomaly
import dev.swarm.phone.keys.CapabilityState
import dev.swarm.phone.keys.PlatformCapability
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Where a [CapabilityAnomaly] goes.
 *
 * WHY IT NEEDS A DESTINATION AT ALL. `CustodyPlan.Provisioned.anomalies` records a capability
 * the handset did not confirm that no matrix row consumes -- at the pinned minSdk the platform
 * is meant to offer it, so a non-PRESENT answer means a Keystore not behaving as its API level
 * promises. That is worth knowing and is deliberately NOT worth an app that will not start
 * (residuals §2.8). A record with no reader is the same as no record: it would be computed on
 * every launch and observed by nobody, which is the shape of the defect class this whole slice
 * is closing.
 *
 * THE WORDING IS A PURE FUNCTION, and that is the point of the file. The path that produces an
 * anomaly runs inside `PhoneRuntime.construct()`, which needs a Context and a real Keystore and
 * is therefore unreachable from any test on this machine. What CAN be held to account is what
 * the user is told, so the text lives here where a JVM test reads it.
 */
class CapabilityNoticeTest {

    private val absentX25519 =
        CapabilityAnomaly(PlatformCapability.KEYSTORE_X25519, CapabilityState.ABSENT)
    private val unknownEd25519 =
        CapabilityAnomaly(PlatformCapability.KEYSTORE_ED25519, CapabilityState.UNKNOWN)

    /**
     * A healthy handset gets NOTHING, not a reassuring line.
     *
     * A label that is always present is a label nobody reads, and the one occasion this text
     * matters is the one where it must be noticed.
     */
    @Test
    fun a_handset_that_confirmed_everything_is_told_nothing() {
        assertEquals("", CapabilityNotice.of(emptyList()))
    }

    /** The capability and the STATE both, because ABSENT and UNKNOWN are different statements. */
    @Test
    fun the_notice_names_the_capability_and_the_answer_the_platform_gave() {
        val notice = CapabilityNotice.of(listOf(absentX25519))
        assertTrue(
            "the notice does not name the capability: $notice",
            notice.contains("KEYSTORE_X25519"),
        )
        assertTrue(
            "\"absent\" and \"could not be probed\" are different facts about a Keystore, and a " +
                "notice that collapses them is a bug report with the interesting half removed: " +
                "$notice",
            notice.contains("ABSENT"),
        )
    }

    @Test
    fun every_anomaly_is_named_and_none_is_summarised_away() {
        val notice = CapabilityNotice.of(listOf(absentX25519, unknownEd25519))
        for (fragment in listOf("KEYSTORE_X25519", "ABSENT", "KEYSTORE_ED25519", "UNKNOWN")) {
            assertTrue("the notice drops $fragment: $notice", notice.contains(fragment))
        }
    }

    /**
     * IT MUST NOT READ AS A FAILURE. Nothing the app needs is missing -- that is the whole
     * reason these two capabilities were moved off the fatal floor -- so a notice implying the
     * device is unsupported would send a user chasing a remedy for a working phone, which is
     * PB-APP-10's failure loop reached through wording instead of through a button.
     */
    @Test
    fun the_notice_does_not_tell_a_working_phone_it_is_broken() {
        val notice = CapabilityNotice.of(listOf(absentX25519)).lowercase()
        for (alarm in listOf("unsupported", "cannot", "failed", "error", "re-pair")) {
            assertTrue(
                "the notice says \"$alarm\" about a handset that provisioned normally: $notice",
                !notice.contains(alarm),
            )
        }
    }

    /**
     * agents-tracker-ksvb.6: "include this line if you ever report a problem" was bug-report
     * instrumentation wearing user copy -- instructions for a report the reader may never file.
     */
    @Test
    fun the_notice_does_not_ask_the_reader_to_remember_it_for_a_bug_report() {
        val notice = CapabilityNotice.of(listOf(absentX25519))
        assertFalse(
            "the notice still tells the reader to include this line in a report: $notice",
            notice.contains("report"),
        )
    }
}
