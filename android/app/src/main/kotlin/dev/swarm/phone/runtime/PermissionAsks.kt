package dev.swarm.phone.runtime

import android.content.Context

/**
 * The one bit the platform will not keep: has this app ever asked for this permission
 * (agents-tracker-0dij).
 *
 * WHY IT EXISTS is [PermissionStateResolver]'s own header, and it is the row real apps get wrong:
 * `shouldShowRequestPermissionRationale` is false BEFORE the first ask as well as after a permanent
 * denial, so reading it alone reports PERMANENTLY_DENIED on a fresh install and sends a user with
 * nothing wrong to a Settings screen. Telling the two apart needs a persisted bit and the platform
 * offers no API for it.
 *
 * WHY IT IS A HELPER AND NOT TWO COPIES. `PairingSurface` owned this for CAMERA -- the store, the
 * key, and the backup exclusion that covers it -- and `SettingsSurface` owned nothing for
 * POST_NOTIFICATIONS: it passed `hasAskedBefore = true` as a literal, so every ungranted phone on
 * API 33+ resolved PERMANENTLY_DENIED five seconds after install, both switches drew disabled, and
 * the app's only way to ask (the switch's own tap) was on a control that receives no taps. A second
 * surface writing its own `getSharedPreferences` would be a second version of a decision that has
 * to hold for both, so the decision moved here and both surfaces spend it.
 *
 * THE KEY IS PER PERMISSION AND THE STORE IS NOT. One bit for two permissions would resolve the
 * un-asked one to PERMANENTLY_DENIED the moment the other was asked -- the pairing screen losing
 * its scan control because somebody opened settings.
 *
 * WHERE IT LIVES IS A SECURITY DECISION ALREADY MADE. The app's own preferences sit under the data
 * root `res/xml/data_extraction_rules.xml` excludes from both cloud backup and device-to-device
 * transfer (PB-SEC-10). What is stored is a UX coordinate and carries nothing else -- no payload,
 * no origin, no key material.
 */
object PermissionAsks {

    /** True once [remember] has recorded an ask for [permission] on this install. */
    fun hasAsked(context: Context, permission: AppPermission): Boolean =
        store(context).getBoolean(keyFor(permission), false)

    /**
     * Record that the platform prompt for [permission] has been raised.
     *
     * IT IS WRITTEN BEFORE THE ASK AND NOT AFTER THE ANSWER, which is what `PairingSurface` already
     * does and is the only order that works: nothing in this app overrides
     * `onRequestPermissionsResult`, so there is no "after" to write in -- the answer arrives as a
     * resume, and by then the resolve that needs this bit has already run.
     */
    fun remember(context: Context, permission: AppPermission) {
        store(context).edit().putBoolean(keyFor(permission), true).apply()
    }

    /**
     * Forget that the prompt was ever raised, because the answer is now in hand
     * (agents-tracker-qyb3).
     *
     * THE BIT WAS WRITE-ONCE AND THAT MADE IT A ONE-WAY DOOR. It exists to tell a first run from a
     * permanent refusal, and both of those are `showRationale == false` -- so once it was set, an
     * ungranted permission could only ever resolve to PERMANENTLY_DENIED. A user who GRANTED the
     * permission and later revoked it in system settings then met two dead switches and a
     * "turn them on in system settings" notice, on a phone where the platform would still have
     * raised its own prompt. The grant is the one moment this app KNOWS the answer, so it is the
     * moment the record of having asked stops being evidence of anything.
     *
     * IT IS CALLED ON THE GRANT AND NOT ON THE DENIAL, and the asymmetry is the whole point: a
     * denial is exactly the state the bit was written for. `SettingsSurface.notificationPermissionNow`
     * is the caller.
     *
     * WHAT IT DOES NOT FIX, said here rather than left to be discovered: a first dialog the user
     * DISMISSES rather than answers also leaves the bit set over a platform that would still
     * prompt. Nothing in this app can see that, because nothing overrides
     * `onRequestPermissionsResult` -- the answer arrives as a resume -- so it stays on
     * agents-tracker-qyb3 rather than being half-handled here.
     */
    fun forget(context: Context, permission: AppPermission) {
        store(context).edit().putBoolean(keyFor(permission), false).apply()
    }

    /**
     * The shipped names, and they are literals for a reason: installs in the field carry
     * `swarm-permission-asks`/`asked-camera` already, and a helper that renamed either would answer
     * false on every one of those handsets -- resolving a permission the user permanently refused
     * back to DENIED, where the control asks again and the platform silently drops the request.
     */
    private const val STORE = "swarm-permission-asks"

    private fun keyFor(permission: AppPermission): String = when (permission) {
        AppPermission.CAMERA -> "asked-camera"
        AppPermission.POST_NOTIFICATIONS -> "asked-post-notifications"
    }

    private fun store(context: Context) = context.getSharedPreferences(STORE, Context.MODE_PRIVATE)
}
