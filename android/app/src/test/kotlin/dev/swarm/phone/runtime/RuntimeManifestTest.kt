package dev.swarm.phone.runtime

import android.content.pm.PackageManager
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * The Android-runtime half of PB-RUN-2 and PB-RUN-3: what the merged manifest
 * actually declares.
 *
 * This is separate from the pure-JVM policy tests on purpose. A permission
 * resolver with a perfect truth table over a permission the manifest never
 * declares always resolves to "denied", and every one of its tests still passes.
 * The manifest is the only place that gap is visible.
 */
@RunWith(RobolectricTestRunner::class)
class RuntimeManifestTest {

    private val context get() = ApplicationProvider.getApplicationContext<android.content.Context>()

    private fun requestedPermissions(): Set<String> {
        val info = context.packageManager.getPackageInfo(
            context.packageName,
            PackageManager.GET_PERMISSIONS,
        )
        return info.requestedPermissions?.toSet() ?: emptySet()
    }

    // --- PB-RUN-2 -----------------------------------------------------------

    @Test
    fun manifest_declares_every_permission_the_gate_resolves() {
        val declared = requestedPermissions()
        for (permission in AppPermission.entries) {
            assertTrue(
                "${permission.manifestName} is resolved by PermissionStateResolver but is " +
                    "not declared in the manifest, so it can only ever resolve to DENIED " +
                    "and every denial path is dead code",
                permission.manifestName in declared,
            )
        }
    }

    /**
     * CAMERA is for pairing, and pairing is the first thing the app does. A
     * <uses-feature android:name="android.hardware.camera" android:required=
     * "true"> alongside it removes the app from Play for every device without a
     * camera -- and, more to the point here, states a hard requirement where the
     * design has a manual-entry fallback (PB-PAIR-2).
     */
    @Test
    fun camera_is_not_declared_as_a_required_hardware_feature() {
        val info = context.packageManager.getPackageInfo(
            context.packageName,
            PackageManager.GET_CONFIGURATIONS,
        )
        val required = info.reqFeatures
            ?.filter {
                it.name == "android.hardware.camera" &&
                    it.flags and android.content.pm.FeatureInfo.FLAG_REQUIRED != 0
            }
            ?: emptyList()
        assertTrue(
            "android.hardware.camera is declared required, which contradicts the " +
                "manual-entry fallback the CAMERA denial path exists for",
            required.isEmpty(),
        )
    }

    // --- PB-RUN-3 -----------------------------------------------------------

    /**
     * The manifest and the connectivity policy must agree in BOTH directions.
     * Flipping the policy's foreground_service column without touching the
     * manifest gives a policy that says a service holds the connection and an
     * app that cannot start one; the reverse gives a foreground service, and its
     * permanent notification, that no policy asked for.
     */
    @Test
    fun foreground_service_declaration_agrees_with_the_connectivity_policy() {
        val policyUsesService = RuntimeState.entries.any {
            ConnectivityPolicy.ruleFor(it).foregroundService
        }
        val services = context.packageManager.getPackageInfo(
            context.packageName,
            PackageManager.GET_SERVICES,
        ).services ?: emptyArray()
        val foregroundServices = services.filter { it.foregroundServiceType != 0 }

        if (policyUsesService) {
            assertTrue(
                "the connectivity policy holds a foreground service but the manifest " +
                    "declares no service with a foregroundServiceType; from API 34 such a " +
                    "service does not start",
                foregroundServices.isNotEmpty(),
            )
            val declared = requestedPermissions()
            assertTrue(
                "android.permission.FOREGROUND_SERVICE is not requested",
                "android.permission.FOREGROUND_SERVICE" in declared,
            )
            assertTrue(
                "no FOREGROUND_SERVICE_<TYPE> permission is requested; each " +
                    "foregroundServiceType has required its own permission since API 34",
                declared.any { it.startsWith("android.permission.FOREGROUND_SERVICE_") },
            )
        } else {
            assertEquals(
                "the manifest declares a foreground service that no connectivity policy " +
                    "state uses. A foreground service is a permanent notification and a " +
                    "Play review question; it needs a reason recorded in the policy",
                emptyList<Any>(),
                foregroundServices,
            )
        }
    }

    /**
     * PB-RUN-3 names battery saver and App Standby. An app that requests
     * REQUEST_IGNORE_BATTERY_OPTIMIZATIONS has opted out of the constraints the
     * policy exists to describe -- and Play restricts the permission to a short
     * list of use cases this app is not on. If the policy is ever satisfied by
     * that exemption, it is not a policy.
     */
    @Test
    fun the_app_does_not_request_a_battery_optimisation_exemption() {
        assertTrue(
            "REQUEST_IGNORE_BATTERY_OPTIMIZATIONS is requested; the Doze and App Standby " +
                "rows of the connectivity policy would then describe behaviour the app has " +
                "opted out of, and Play restricts the permission besides",
            "android.permission.REQUEST_IGNORE_BATTERY_OPTIMIZATIONS" !in requestedPermissions(),
        )
    }

    @Test
    fun the_application_class_is_declared() {
        val info = context.packageManager.getApplicationInfo(context.packageName, 0)
        assertNotNull(info.className)
    }
}
