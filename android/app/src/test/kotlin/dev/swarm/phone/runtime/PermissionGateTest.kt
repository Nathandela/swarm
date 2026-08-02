package dev.swarm.phone.runtime

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * PB-RUN-2 -- "Runtime permissions are handled with denial paths: CAMERA
 * (PB-PAIR-2) and POST_NOTIFICATIONS (API 33+, without which PB-PUSH-4's
 * notifications are silently dropped). Tests for granted/denied/
 * permanently-denied per permission."
 *
 * The resolution is a PURE function of four inputs, tested here as a truth
 * table on the JVM. That is deliberate: routing it through Activity and
 * ContextCompat would put the interesting logic behind shadow behaviour and
 * make the table above unreadable, and §10's tier order prefers a plain JVM
 * test to a Robolectric one wherever the Android runtime is not the subject.
 * The Android-runtime half -- that the manifest actually declares both
 * permissions -- is PermissionManifestTest.
 *
 * Two of these rows are the ones real apps get wrong.
 *
 * 1. PERMANENTLY DENIED IS NOT `!shouldShowRequestPermissionRationale`.
 *    Before the user has ever been asked, shouldShowRequestPermissionRationale
 *    is ALSO false. An implementation that reads it alone reports
 *    "permanently denied" on first launch, sends the user to a Settings screen
 *    where nothing is wrong, and never asks for the camera at all -- so pairing,
 *    the first thing the app does, is dead on a fresh install. Distinguishing
 *    the two states requires a PERSISTED "have we asked" bit; there is no
 *    platform API for it.
 *
 * 2. POST_NOTIFICATIONS DOES NOT EXIST BELOW API 33. On a lower device the
 *    permission is neither granted nor denied -- it is absent, and notifications
 *    work. Resolving it to DENIED there produces a permanent "notifications are
 *    broken" banner on a device where they are fine, and resolving it to GRANTED
 *    hides the real thing. NOT_APPLICABLE is a third answer, not a convenience.
 */
class PermissionGateTest {

    // --- CAMERA (PB-PAIR-2: the QR scanner) ----------------------------------

    @Test
    fun camera_granted_resolves_to_granted() {
        assertEquals(
            PermissionState.GRANTED,
            PermissionStateResolver.resolve(
                permission = AppPermission.CAMERA,
                sdkInt = 35,
                granted = true,
                hasAskedBefore = true,
                showRationale = false,
            ),
        )
    }

    @Test
    fun camera_denied_but_never_asked_is_denied_not_permanently_denied() {
        assertEquals(
            "before the first ask, shouldShowRequestPermissionRationale is false for a " +
                "reason that has nothing to do with a permanent denial",
            PermissionState.DENIED,
            PermissionStateResolver.resolve(
                permission = AppPermission.CAMERA,
                sdkInt = 35,
                granted = false,
                hasAskedBefore = false,
                showRationale = false,
            ),
        )
    }

    @Test
    fun camera_denied_once_with_rationale_is_denied() {
        assertEquals(
            PermissionState.DENIED,
            PermissionStateResolver.resolve(
                permission = AppPermission.CAMERA,
                sdkInt = 35,
                granted = false,
                hasAskedBefore = true,
                showRationale = true,
            ),
        )
    }

    @Test
    fun camera_denied_after_asking_with_no_rationale_is_permanently_denied() {
        assertEquals(
            PermissionState.PERMANENTLY_DENIED,
            PermissionStateResolver.resolve(
                permission = AppPermission.CAMERA,
                sdkInt = 35,
                granted = false,
                hasAskedBefore = true,
                showRationale = false,
            ),
        )
    }

    // --- POST_NOTIFICATIONS (PB-PUSH-4's delivery path) ----------------------

    @Test
    fun postNotifications_below_api33_is_not_applicable() {
        for (sdk in intArrayOf(30, 31, 32)) {
            assertEquals(
                "API $sdk has no POST_NOTIFICATIONS permission; notifications work without it",
                PermissionState.NOT_APPLICABLE,
                PermissionStateResolver.resolve(
                    permission = AppPermission.POST_NOTIFICATIONS,
                    sdkInt = sdk,
                    granted = false,
                    hasAskedBefore = false,
                    showRationale = false,
                ),
            )
        }
    }

    @Test
    fun postNotifications_at_api33_and_above_resolves_normally() {
        assertEquals(
            PermissionState.GRANTED,
            PermissionStateResolver.resolve(
                permission = AppPermission.POST_NOTIFICATIONS,
                sdkInt = 33,
                granted = true,
                hasAskedBefore = true,
                showRationale = false,
            ),
        )
        assertEquals(
            PermissionState.DENIED,
            PermissionStateResolver.resolve(
                permission = AppPermission.POST_NOTIFICATIONS,
                sdkInt = 33,
                granted = false,
                hasAskedBefore = false,
                showRationale = false,
            ),
        )
        assertEquals(
            PermissionState.PERMANENTLY_DENIED,
            PermissionStateResolver.resolve(
                permission = AppPermission.POST_NOTIFICATIONS,
                sdkInt = 35,
                granted = false,
                hasAskedBefore = true,
                showRationale = false,
            ),
        )
    }

    // --- The denial must be VISIBLE, which is the whole point ----------------

    /**
     * PB-RUN-2 exists because a denied POST_NOTIFICATIONS makes PB-PUSH-4's
     * notifications "silently dropped". Silence is the defect. Every non-granted
     * state must therefore produce a named degraded capability the UI can render;
     * a resolver that returns a state and nothing else lets the app carry on as
     * if push worked.
     */
    @Test
    fun every_non_granted_state_names_a_degraded_capability() {
        for (permission in AppPermission.entries) {
            for (state in listOf(PermissionState.DENIED, PermissionState.PERMANENTLY_DENIED)) {
                assertNotNull(
                    "$permission in state $state must name what stops working",
                    PermissionStateResolver.degradedCapability(permission, state),
                )
            }
            assertNull(
                "$permission granted degrades nothing",
                PermissionStateResolver.degradedCapability(permission, PermissionState.GRANTED),
            )
            assertNull(
                "$permission not applicable degrades nothing",
                PermissionStateResolver.degradedCapability(
                    permission,
                    PermissionState.NOT_APPLICABLE,
                ),
            )
        }
    }

    /**
     * A permanent denial is recoverable only through Settings, and the two
     * permissions differ in what the user must be told. An implementation that
     * returns one generic message for both has not handled the denial path; it
     * has handled the denial.
     */
    @Test
    fun permanently_denied_capabilities_are_distinct_per_permission() {
        val camera = PermissionStateResolver.degradedCapability(
            AppPermission.CAMERA,
            PermissionState.PERMANENTLY_DENIED,
        )
        val notifications = PermissionStateResolver.degradedCapability(
            AppPermission.POST_NOTIFICATIONS,
            PermissionState.PERMANENTLY_DENIED,
        )
        org.junit.Assert.assertNotEquals(camera, notifications)
    }

    /**
     * The resolver must be total. A `when` that falls through to a default is
     * how a fifth permission added by a later slice silently resolves to
     * "granted" and its denial path stops existing.
     */
    @Test
    fun resolver_is_total_over_every_declared_permission() {
        for (permission in AppPermission.entries) {
            for (sdk in intArrayOf(30, 33, 35)) {
                for (granted in booleanArrayOf(true, false)) {
                    for (asked in booleanArrayOf(true, false)) {
                        for (rationale in booleanArrayOf(true, false)) {
                            assertNotNull(
                                PermissionStateResolver.resolve(
                                    permission, sdk, granted, asked, rationale,
                                ),
                            )
                        }
                    }
                }
            }
        }
    }
}
