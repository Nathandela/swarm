package dev.swarm.phone.runtime

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-0dij -- the one bit the platform will not keep,
 * for BOTH permissions rather than for the camera alone.
 *
 * WHY THE BIT EXISTS AT ALL is [PermissionStateResolver]'s own header:
 * `shouldShowRequestPermissionRationale` is false BEFORE the first ask as well as after a permanent
 * denial, so reading it alone reports PERMANENTLY_DENIED on a fresh install and sends a user with
 * nothing wrong to a Settings screen. `PairingSurface` persisted it for CAMERA and
 * `SettingsSurface` hard-coded `hasAskedBefore = true` for POST_NOTIFICATIONS, which is that exact
 * wrong report: on API 33+ every ungranted phone resolved PERMANENTLY_DENIED five seconds after
 * install, both switches drew disabled, and the notice sent the owner to system settings.
 *
 * WHY IT IS ONE HELPER AND NOT TWO COPIES. The store, the file it lives in and the backup exclusion
 * that covers it (PB-SEC-10) are one decision; a second surface writing its own `getSharedPreferences`
 * would be a second decision that agrees today. What is NOT shared is the KEY -- the whole point of
 * the bit is that it is per permission, and the assertion below is that asking for one does not
 * answer for the other.
 */
@RunWith(RobolectricTestRunner::class)
class PermissionAsksTest {

    private val context: Context
        get() = ApplicationProvider.getApplicationContext()

    @Test
    fun `a permission nobody has asked for carries no bit`() {
        for (permission in AppPermission.values()) {
            assertFalse(
                "$permission reads as already asked on a phone where nothing has asked. That is " +
                    "the state agents-tracker-qx9m and agents-tracker-0dij both shipped: with " +
                    "`showRationale` false before the first ask, a true bit resolves a fresh " +
                    "install to PERMANENTLY_DENIED and every control that could ask goes dead",
                PermissionAsks.hasAsked(context, permission),
            )
        }
    }

    @Test
    fun `the bit is per permission, so one screen's ask does not answer for another`() {
        PermissionAsks.remember(context, AppPermission.POST_NOTIFICATIONS)

        assertTrue(
            "the notification ask was not remembered, so the next resolve reports DENIED forever " +
                "and the permanent refusal can never be told from the first run",
            PermissionAsks.hasAsked(context, AppPermission.POST_NOTIFICATIONS),
        )
        assertFalse(
            "asking for POST_NOTIFICATIONS marked CAMERA as asked. One bit for two permissions " +
                "resolves the un-asked one to PERMANENTLY_DENIED the moment the other is asked, " +
                "which is the pairing screen losing its scan control because somebody opened " +
                "settings",
            PermissionAsks.hasAsked(context, AppPermission.CAMERA),
        )
    }

    /**
     * The bit can come back off, and it is per permission on the way out too
     * (agents-tracker-qyb3).
     *
     * IT WAS WRITE-ONCE, AND THAT MADE IT A ONE-WAY DOOR. `shouldShowRequestPermissionRationale`
     * is false after a GRANT as well as after a permanent refusal, so once the bit was set the
     * resolver could only ever say PERMANENTLY_DENIED about an ungranted permission -- and a user
     * who granted the permission and later revoked it in system settings got two dead switches
     * and a redirect, on a phone the platform would still have prompted for. The grant is the
     * moment the truth is in hand, so it is the moment the record of the ask stops being needed.
     */
    @Test
    fun `the bit can be cleared, and clearing one does not clear the other`() {
        PermissionAsks.remember(context, AppPermission.POST_NOTIFICATIONS)
        PermissionAsks.remember(context, AppPermission.CAMERA)

        PermissionAsks.forget(context, AppPermission.POST_NOTIFICATIONS)

        assertFalse(
            "agents-tracker-qyb3: the ask bit cannot be cleared, so a permission that was " +
                "granted and later revoked in system settings resolves to PERMANENTLY_DENIED for " +
                "the life of the install",
            PermissionAsks.hasAsked(context, AppPermission.POST_NOTIFICATIONS),
        )
        assertTrue(
            "clearing the notification bit cleared the camera's. The two permissions are asked " +
                "by different screens and answered at different times; one bit for both is the " +
                "defect `keyFor` exists to prevent, in the other direction",
            PermissionAsks.hasAsked(context, AppPermission.CAMERA),
        )
    }

    @Test
    fun `the camera bit keeps the store and the key the shipped build wrote`() {
        // THE LITERALS ARE THE POINT. `PairingSurface` shipped this bit under
        // `swarm-permission-asks` / `asked-camera`, and installs in the field carry it. A helper
        // that renamed either would answer false on every one of those handsets, which resolves a
        // permission the user already refused permanently back to DENIED -- and the scan control
        // starts asking again with the platform silently ignoring it.
        PermissionAsks.remember(context, AppPermission.CAMERA)

        val shipped = context.getSharedPreferences("swarm-permission-asks", Context.MODE_PRIVATE)
        assertTrue(
            "the camera ask is no longer written to `swarm-permission-asks`/`asked-camera`, so " +
                "every install that already answered the camera prompt reads as never asked",
            shipped.getBoolean("asked-camera", false),
        )
    }
}
