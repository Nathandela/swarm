package dev.swarm.phone.runtime

import android.Manifest
import android.os.Build

/**
 * PB-RUN-2 -- runtime permissions with denial paths.
 *
 * The resolution is a pure function of four inputs so the interesting logic is not hidden
 * behind Activity and ContextCompat. Two of its rows are the ones real apps get wrong:
 *
 *  - `shouldShowRequestPermissionRationale` is false BEFORE the first ask as well as after a
 *    permanent denial, so reading it alone reports "permanently denied" on a fresh install and
 *    sends the user to a Settings screen where nothing is wrong. Telling the two apart needs a
 *    persisted "have we asked" bit; the platform offers no API for it.
 *  - POST_NOTIFICATIONS does not exist below API 33. There it is neither granted nor denied,
 *    and notifications work. [PermissionState.NOT_APPLICABLE] is a third answer, not a
 *    convenience.
 */

enum class AppPermission(val manifestName: String) {
    /** PB-PAIR-2's QR scanner. */
    CAMERA(Manifest.permission.CAMERA),

    /** PB-PUSH-4's delivery path; without it notifications are silently dropped. */
    POST_NOTIFICATIONS("android.permission.POST_NOTIFICATIONS"),
}

enum class PermissionState {
    GRANTED,
    DENIED,
    PERMANENTLY_DENIED,

    /** The permission does not exist on this API level. */
    NOT_APPLICABLE,
}

/**
 * What stops working while a permission is withheld. PB-RUN-2 exists because the failure is
 * otherwise silent, so every non-granted state names its casualty and the UI has something to
 * render.
 */
enum class DegradedCapability {
    QR_PAIRING,
    PUSH_NOTIFICATIONS,
}

object PermissionStateResolver {

    fun resolve(
        permission: AppPermission,
        sdkInt: Int,
        granted: Boolean,
        hasAskedBefore: Boolean,
        showRationale: Boolean,
    ): PermissionState {
        if (permission == AppPermission.POST_NOTIFICATIONS &&
            sdkInt < Build.VERSION_CODES.TIRAMISU
        ) {
            return PermissionState.NOT_APPLICABLE
        }
        return when {
            granted -> PermissionState.GRANTED
            !hasAskedBefore -> PermissionState.DENIED
            showRationale -> PermissionState.DENIED
            else -> PermissionState.PERMANENTLY_DENIED
        }
    }

    fun degradedCapability(
        permission: AppPermission,
        state: PermissionState,
    ): DegradedCapability? = when (state) {
        PermissionState.GRANTED, PermissionState.NOT_APPLICABLE -> null
        PermissionState.DENIED, PermissionState.PERMANENTLY_DENIED -> when (permission) {
            AppPermission.CAMERA -> DegradedCapability.QR_PAIRING
            AppPermission.POST_NOTIFICATIONS -> DegradedCapability.PUSH_NOTIFICATIONS
        }
    }
}
