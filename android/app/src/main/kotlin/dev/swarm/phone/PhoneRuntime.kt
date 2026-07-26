package dev.swarm.phone

import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import dev.swarm.phone.keys.AndroidKeyInfoReader
import dev.swarm.phone.keys.AndroidKeystoreAlgorithms
import dev.swarm.phone.keys.AndroidKeystoreProvisioner
import dev.swarm.phone.keys.CapabilityAnomaly
import dev.swarm.phone.keys.CustodyPlan
import dev.swarm.phone.keys.CustodyPlanner
import dev.swarm.phone.keys.CustodyProvisioning
import dev.swarm.phone.keys.DeviceCapabilities
import dev.swarm.phone.keys.FileCustodyBacking
import dev.swarm.phone.keys.GoCustodyFailure
import dev.swarm.phone.keys.KeyCustodyException
import dev.swarm.phone.keys.KeyRole
import dev.swarm.phone.keys.KeyTier
import dev.swarm.phone.keys.KeystoreCustodyBootstrap
import dev.swarm.phone.keys.KeystoreKekProvider
import dev.swarm.phone.keys.KeystoreKeyCustody
import dev.swarm.phone.keys.PersistentCustodyBacking
import dev.swarm.phone.keys.PlatformCapability
import dev.swarm.phone.keys.Recovery
import dev.swarm.phone.keys.SealedStore
import dev.swarm.phone.keys.deviceBiometricAvailability
import dev.swarm.phone.runtime.ContentProvisioning
import dev.swarm.phone.runtime.ContentUnlockPolicy
import dev.swarm.phone.ui.ErrorRouter
import dev.swarm.phone.ui.RoutedError
import dev.swarm.phone.ui.SwarmErrorTokens
import swarmmobile.App
import swarmmobile.Config
import swarmmobile.Swarmmobile
import java.io.File

/**
 * Phase B slice S16 -- the one place the app builds a phone.
 *
 * `swarmmobile.NewApp` is the only constructor there is, and it REQUIRES a
 * `swarmmobile.KeyCustody` (ADR-007 B18(c): an optional parameter is a parameter somebody
 * passes nil to). Until this file existed the symbol appeared under `src/main/` only inside doc
 * comments, so the shipped app had no phone core, no relay connection, no durable state and no
 * screen with anything to render -- while the whole Go conformance suite drove an App the
 * Android app had no code to create.
 *
 * IT IS ITS OWN FILE AND NOT THE APPLICATION SUBCLASS. Construction reaches Keystore, the
 * filesystem and the native library, and every one of those can refuse; an `Application` that
 * did it in `onCreate` would turn a locked handset -- or a re-enrolled fingerprint -- into a
 * process that dies before any screen exists to say why.
 *
 * SO IT IS LAZY AND FAILABLE. [phone] answers a [PhoneStartup], never an exception, and only a
 * SUCCESS is remembered: a refusal is retried on the next call, because the commonest one is
 * "the user has not authenticated yet" and the whole remedy is that they then do.
 */
class PhoneRuntime(private val context: Context) {

    private var ready: PhoneStartup.Ready? = null

    /**
     * The phone, or a legible reason there is none.
     *
     * Synchronized because a push can arrive while a screen is starting, and two concurrent
     * `NewApp` calls would give the same state directory two owners.
     */
    @Synchronized
    fun phone(): PhoneStartup {
        ready?.let { return it }
        val startup = attach()
        if (startup is PhoneStartup.Ready) ready = startup
        return startup
    }

    /**
     * Remember the relay this phone dials. The pairing flow calls it once the destination is
     * confirmed.
     *
     * WHY THE ANDROID SIDE KEEPS IT. `mobile.Config.RelayURL` is the only thing the transport
     * dials, and the phone core's durable state has no field for it -- so a handset that
     * learned the URL from a pairing QR and then restarted would come back with nowhere to dial
     * and sit offline for good, with nothing on screen saying why. It takes effect at the next
     * [phone] construction: the bound facade has no verb to re-target a live App.
     */
    @Synchronized
    fun rememberRelay(url: String) {
        runtimeDir.mkdirs()
        File(runtimeDir, RELAY_FILE).writeText(url)
    }

    /**
     * Throw away the built phone so the URL [rememberRelay] just learned takes effect.
     *
     * WITHOUT THIS A FRESH INSTALL CANNOT USE THE PAIRING IT JUST COMPLETED. `mobile.Config
     * .RelayURL` is read once, at construction, and the phone core has no verb to re-target a
     * live App -- so the App that ran the pairing was built with the empty URL a fresh install
     * has, and `Start` on it can never connect. The user would pair successfully and then sit on
     * an empty roster until something happened to rebuild the process, with nothing on screen
     * saying why. PB-E2E-2 pairs and then observes IN THE SAME SESSION, so this is the whole
     * difference between the demonstration working and appearing to.
     *
     * THE OLD APP IS CLOSED, not dropped. Two Apps over one state directory is the condition
     * [phone]'s synchronization exists to prevent, and the old one owns a goroutine draining the
     * relay. Close waits for the pairing goroutine it may still hold, which by this point has
     * finished -- a pairing that reached PAIRED is what brought us here.
     */
    @Synchronized
    fun rebuildAfterPairing() {
        val previous = ready
        ready = null
        try {
            previous?.app?.close()
        } catch (torn: Exception) {
            // A close that refuses leaves a phone the next `phone()` rebuilds anyway. There is
            // nothing a user could do about it and nothing to report to.
        }
    }

    /**
     * Everything that can refuse, in the order the refusals mean something.
     *
     * `Throwable` and not `Exception`: a missing native library is an `Error`, and a phone that
     * cannot load its own core still has to produce a state rather than take the process down.
     */
    private fun attach(): PhoneStartup = try {
        // The capability question is asked FIRST, before anything touches Keystore or the state
        // directory. A handset that cannot hold the design's keys must be told so by name rather
        // than discovered halfway through provisioning.
        val plan = capabilityPlan()
        PhoneStartup.Ready(construct(), plan.anomalies)
    } catch (failure: Throwable) {
        PhoneStartup.Unavailable(routeStartupFailure(failure), failure.message ?: failure.javaClass.name)
    }

    /**
     * PB-KEY-8's gate, at the one moment it can be asked, and the caller [CustodyPlanner] did
     * not have.
     *
     * `CustodyPlanner.forDevice` was fully tested and invoked by nothing: `construct` went
     * straight to [KeystoreCustodyBootstrap], so [KeyCustodyException.PlatformCapabilityMissing]
     * was declared, routed by [routeStartupFailure], and never thrown. The shipped app therefore
     * refused no handset over any capability -- which also made physical-handset runbook step 2c
     * inert (residuals §2.10(a)).
     *
     * WHAT A USER SEES ON EACH PATH, because "a phone that cannot start" is the worst outcome
     * this file can produce:
     *
     *  - PROVISIONED, no anomalies: nothing. The app starts as before.
     *  - PROVISIONED with anomalies: the app starts, and [PhoneSurface] shows the line
     *    [dev.swarm.phone.ui.CapabilityNotice] writes. Reaching here needs a Keystore that
     *    offers no Curve25519 at the pinned minSdk, which is a Keystore not behaving as its API
     *    level promises -- worth recording, and explicitly not worth refusing over.
     *  - REFUSED: DEVICE_UNSUPPORTED, whose remedy is REPORT_BUG and which offers no pairing
     *    (`PhoneStartupRoutingTest`). It is reachable only when this Keystore cannot produce an
     *    AES key at all, or below API 30 -- and PB-RUN-1 pins minSdk to 33, so the second is
     *    unreachable on any handset the app installs on. The first was already a phone that
     *    could not start; what changes is that it now says which capability was missing instead
     *    of surfacing a platform exception as INTERNAL.
     */
    private fun capabilityPlan(): CustodyPlan.Provisioned {
        val plan = CustodyPlanner.forDevice(
            DeviceCapabilities(
                sdkInt = Build.VERSION.SDK_INT,
                strongBox = strongBoxPreferred,
                algorithms = AndroidKeystoreAlgorithms(),
            ).probe(),
        )
        return when (plan) {
            is CustodyPlan.Provisioned -> plan
            is CustodyPlan.Refused ->
                throw KeyCustodyException.PlatformCapabilityMissing(plan.role, plan.capability)
        }
    }

    /**
     * PB-SEC-2's precondition, asked before anything tries to CREATE the content KEK.
     *
     * `DeviceCapabilities.probe` answers USER_AUTH_PER_USE from the API LEVEL, which is a fact
     * about the platform and not about this handset -- so a user with a PIN and no fingerprint
     * passed the capability plan and then hit `KeyGenerator.init` throwing
     * `InvalidAlgorithmParameterException`, because the platform will not generate an
     * `AUTH_BIOMETRIC_STRONG` key with nothing enrolled. That is not a [KeyCustodyException], so
     * [routeStartupFailure] fell through to `SwarmErrorTokens.UNKNOWN`: "something failed in a way
     * the app does not recognise", remedy NONE. An app that will not start, for a reason the user
     * could have fixed in thirty seconds, with nothing telling them so.
     *
     * IT IS ADR-007 B57'S BILL. Refusing `AUTH_DEVICE_CREDENTIAL` is what makes an enrolled
     * Class-3 biometric mandatory, so naming this refusal is part of that decision rather than a
     * separate concern -- see [ContentUnlockPolicy.provisioningFor] for why each answer routes
     * where it does, and why a transient answer proceeds instead of refusing.
     */
    private fun refuseAHandsetThatCannotHoldTheContentKek() {
        when (ContentUnlockPolicy.provisioningFor(deviceBiometricAvailability(context))) {
            ContentProvisioning.PROCEED -> Unit
            // REAUTHENTICATE, whose remedy is AUTHENTICATE -- the same remedy the unlock control
            // keys on, so the user gets the control, it finds it cannot prompt, and it says to
            // enrol. One mechanism reached by two roads rather than a second error class.
            ContentProvisioning.NEEDS_ENROLMENT ->
                throw KeyCustodyException.UserAuthenticationRequired(KeyTier.CONTENT)

            // Nothing the user does to this handset helps, which is exactly what
            // DEVICE_UNSUPPORTED means and what PlatformCapabilityMissing routes to.
            ContentProvisioning.UNSUPPORTED -> throw KeyCustodyException.PlatformCapabilityMissing(
                KeyRole.COMMAND_SIGN,
                PlatformCapability.USER_AUTH_PER_USE,
            )
        }
    }

    private fun construct(): App {
        val backing = FileCustodyBacking(custodyDir)
        val bootstrap = bootstrapOver(backing)

        refuseAHandsetThatCannotHoldTheContentKek()
        refuseAnOrphanedStateDirectory(backing)
        for (tier in KeyTier.entries) bootstrap.ensure(tier)

        val store = SealedStore.openOver(KeystoreKekProvider(), backing)
        for (tier in KeyTier.entries) bootstrap.ensureStateKey(store, tier)

        coreDir.mkdirs()
        val config = Config().apply {
            stateDir = coreDir.path
            relayURL = rememberedRelay()
            // "" adopts whatever the durable blob describes (PB-STATE-1/-2). Naming a machine
            // here would override the coordinate the phone resumed, which is the one thing a
            // resume exists to preserve.
            machineID = ""
        }
        return Swarmmobile.newApp(config, KeystoreKeyCustody(store))
    }

    /**
     * PB-KEY-7's lock purge, reached from [dev.swarm.phone.runtime.ContentLock].
     *
     * IT NEVER CONSTRUCTS A PHONE. `ready` is only set once one was built, so a screen turning off
     * on a handset whose app has not been opened does not reach Keystore, the filesystem or the
     * native library on the way -- and there is nothing there to purge in the first place.
     *
     * THE ERROR IS SWALLOWED, and that is the honest handling rather than a shortcut. The memory
     * half of the purge is unconditional and has already happened when this returns; a failure
     * means only that the decrypted caches AT REST survived it, on a full disk or a data
     * directory gone read-only. There is no user present at a screen lock and no screen left to
     * report to, and the next successful lock finishes the job.
     */
    @Synchronized
    fun lockContent() {
        val live = ready ?: return
        try {
            live.app.purgeKeys()
        } catch (refused: Exception) {
            // See above: the live key and the decrypted caches are gone regardless.
        }
    }

    /**
     * PB-KEY-7's "require a fresh unwrap before restoring content", and PB-SEC-2's gate.
     *
     * It is a REQUEST, not a promise: the Keystore-backed content KEK answers only while the
     * device has authenticated inside the window it was provisioned with, so this legitimately
     * refuses on a locked handset. The refusal is routed like every other facade failure, by the
     * caller that has a screen to put it on ([PhoneSurface]).
     *
     * @return null when content custody is live, or the routed reason it is not.
     */
    @Synchronized
    fun unlockContent(): RoutedError? {
        val live = ready ?: return null
        return try {
            live.app.unlockContent()
            null
        } catch (refused: Exception) {
            routeStartupFailure(refused)
        }
    }

    /**
     * The remedy for the permanent verdict, and the only implementation it can have.
     *
     * A destroyed KEK means the material it sealed exists nowhere else -- the three content-tier
     * device scalars among them, including the COMMAND_SIGN seed the daemon registry pins this
     * device's id to. So the blobs and the state directory are not recoverable data being thrown
     * away; they are bytes nothing can ever open again, and leaving them in place is what makes
     * the verdict permanent in the wrong sense: [refuseAnOrphanedStateDirectory] would then
     * refuse every launch for the life of the install.
     *
     * IT IS NEVER AUTOMATIC. An automatic discard is exactly the "treat it as fresh" behaviour
     * that silently throws away a working pairing; this runs when the user acts on the RE_PAIR
     * remedy PB-APP-9 put on screen, and not before.
     */
    @Synchronized
    fun discardInvalidatedCustody() {
        ready = null
        val backing = FileCustodyBacking(custodyDir)
        val bootstrap = bootstrapOver(backing)
        for (tier in KeyTier.entries) bootstrap.discard(tier)
        coreDir.deleteRecursively()
    }

    /**
     * PB-SEC-2's per-use gate keys, over the same provisioning path the tier KEKs take.
     *
     * IT IS ASKED FOR HERE AND NOT BUILT AT THE SCREEN, for the reason [strongBoxPreferred]
     * exists: provisioning is one platform conversation, and a second construction of
     * [CustodyProvisioning] beside a screen is a second place the read-back could be omitted.
     * The gate entries themselves are provisioned lazily on first use -- they seal nothing, so
     * there is no first-launch ordering to get right and nothing to lose by not having them.
     */
    fun perUseCiphers(): dev.swarm.phone.keys.PerUseCipherSource =
        dev.swarm.phone.keys.KeystorePerUseCiphers(
            CustodyProvisioning(AndroidKeystoreProvisioner(), AndroidKeyInfoReader()),
        )

    private fun bootstrapOver(backing: PersistentCustodyBacking) = KeystoreCustodyBootstrap(
        backing = backing,
        provisioning = CustodyProvisioning(AndroidKeystoreProvisioner(), AndroidKeyInfoReader()),
        strongBoxPreferred = strongBoxPreferred,
    )

    /**
     * The handset's own answer, asked ONCE and read by both the capability probe and the
     * bootstrap. Two copies of a platform question are two things to get wrong.
     *
     * StrongBox absence is a fallback and not a refusal -- refusing without it would refuse most
     * handsets -- and what must not happen is a key claiming hardware it does not have, which
     * CustodyProvisioning's read-back settles from the KeyInfo rather than from this preference.
     */
    private val strongBoxPreferred: Boolean
        get() = context.packageManager.hasSystemFeature(PackageManager.FEATURE_STRONGBOX_KEYSTORE)

    /**
     * The third way the two situations can be told apart, and the one [KeystoreCustodyBootstrap]
     * cannot see: a state directory with contents and a custody store with none.
     *
     * `device.key` and `phone-state.json` are sealed under the state KEKs held in that store, so
     * without them the directory is already unopenable. Carrying on would generate FRESH state
     * KEKs, hand them to a core that then fails to open the old material, and report whatever
     * `Resume` happened to say -- an opaque refusal, which `phonecore.openSealedDeviceKeys`
     * turns into an app that cannot start with nothing on screen explaining it. The permanent
     * verdict is both true and actionable: pair this device again.
     */
    private fun refuseAnOrphanedStateDirectory(backing: PersistentCustodyBacking) {
        val stateExists = coreDir.list()?.isNotEmpty() == true
        if (stateExists && backing.load().isEmpty()) {
            throw KeyCustodyException.KeyPermanentlyInvalidated(KeyTier.CONTENT)
        }
    }

    private fun rememberedRelay(): String {
        val file = File(runtimeDir, RELAY_FILE)
        return if (file.isFile) file.readText().trim() else ""
    }

    /**
     * `noBackupFilesDir`, and it is chosen rather than defaulted to.
     *
     * PB-SEC-10's `res/xml/data_extraction_rules.xml` excludes `domain="root"` from both cloud
     * backup and device-to-device transfer, and this directory is under the app's data root, so
     * it is covered by that exclusion. It is ALSO the one directory the platform excludes on its
     * own, which is the point: these bytes are sealed under Keystore KEKs that do not travel, so
     * a copy restored onto another handset is not a leak but a brick -- material that looks like
     * state and can never be opened. The rules file and the platform default therefore agree,
     * and neither is load-bearing alone.
     */
    private val runtimeDir: File get() = context.noBackupFilesDir

    /** The sealed custody blobs -- the two state KEKs among them. */
    private val custodyDir: File get() = File(runtimeDir, "custody")

    /** `mobile.Config.StateDir`: the Go core's `device.key` and `phone-state.json`. */
    private val coreDir: File get() = File(runtimeDir, "phone-core")

    private companion object {
        const val RELAY_FILE = "relay-url"
    }
}

/**
 * A construction failure as something a screen can render (PB-APP-9).
 *
 * A custody failure is routed by TYPE rather than by its message, because only two of the six
 * carry a verdict token; the rest would fall through to the unknown row, which for a permanent
 * invalidation is a user told to try again forever. Everything else IS a message -- gomobile
 * leaves nothing of a Go error but its text, with the class stamped on the front -- so
 * [ErrorRouter] reads it exactly as every other facade failure is read.
 *
 * IT IS A TOP-LEVEL `internal` FUNCTION AND NOT A PRIVATE METHOD. Constructing a [PhoneRuntime]
 * needs a `Context`, and its failures come from Keystore, so the routing table was the one part
 * of this file no test could reach -- and it shipped S16 as a two-arm `when` that folded FOUR
 * verdicts onto re-pair. Separating it is what makes the table assertable on a plain JVM.
 *
 * THE TWO ARMS THAT WERE WRONG, and they were wrong in the way this whole taxonomy exists to
 * prevent -- a remedy the user can perform that cannot help:
 *
 *  - [KeyCustodyException.KeystoreDowngrade] and [KeyCustodyException.PlatformCapabilityMissing]
 *    are PB-KEY-8's refusals: the handset is not capable of what the design requires. Routed to
 *    re-pair, the user gets re-pair -> re-provision the same key on the same platform -> the same
 *    refusal -> the same screen, which is the failure LOOP PB-APP-10 forbids, reached through
 *    the remedy. They render as DEVICE_UNSUPPORTED, whose remedy is not a user action at all.
 *  - [KeyCustodyException.Unexpected] is "anything else the platform threw" -- a `renameTo`
 *    failing on a full disk, say. Routed to re-pair it tells a user with a perfectly good key
 *    that it was destroyed and no authentication brings it back. It is a bug, so it renders as
 *    INTERNAL, the class that already means exactly that.
 *
 * What remains on re-pair is what re-pairing actually fixes: a key the platform invalidated, a
 * Keystore entry that is gone, and the state directory orphaned from its custody store (which
 * [refuseAnOrphanedStateDirectory] raises as the invalidation it is).
 */
internal fun routeStartupFailure(failure: Throwable): RoutedError = when (failure) {
    is KeyCustodyException -> routeCustodyVerdict(failure)
    else -> ErrorRouter.route(failure.message.orEmpty())
}

/**
 * The custody half, split out only so the branch above needs no smart cast.
 *
 * TWO VERDICTS ARE ROUTED BY TYPE AND NOT BY RECOVERY, and that is the point rather than an
 * exception to it: [Recovery.REPAIR_DEVICE] is the answer for THREE different causes and only
 * one of them is the user's to fix. The rest go through [GoCustodyFailure.recoveryFor], which
 * is the shared, separately-tested policy, and the inner `when` carries no `else` -- a
 * [Recovery] value added later fails to compile here rather than falling into a bucket.
 */
private fun routeCustodyVerdict(failure: KeyCustodyException): RoutedError = when (failure) {
    is KeyCustodyException.Unexpected -> ErrorRouter.route(SwarmErrorTokens.INTERNAL)
    is KeyCustodyException.PlatformCapabilityMissing ->
        ErrorRouter.route(SwarmErrorTokens.DEVICE_UNSUPPORTED)

    else -> when (GoCustodyFailure.recoveryFor(failure)) {
        Recovery.REAUTHENTICATE -> ErrorRouter.route(GoCustodyFailure.AUTH_REQUIRED_TOKEN)
        // KeystoreDowngrade, and the only verdict that reaches it.
        Recovery.REPROVISION_KEK -> ErrorRouter.route(SwarmErrorTokens.DEVICE_UNSUPPORTED)
        // The key itself is gone, which IS something the user can act on.
        Recovery.REPAIR_DEVICE -> ErrorRouter.route(GoCustodyFailure.KEY_INVALIDATED_TOKEN)
    }
}

/**
 * The outcome of trying to build the phone. Two cases and no third, because "null App" is how a
 * caller ends up rendering an empty roster for a handset whose key was destroyed.
 */
sealed class PhoneStartup {

    /**
     * @param anomalies capabilities the handset did not confirm that NO matrix row consumes, so
     *  none of them stopped the app starting. They ride the READY state on purpose: they are a
     *  property of a phone that works, and the only thing left to do with them is show them
     *  ([dev.swarm.phone.ui.CapabilityNotice]). A record with no reader is the same as no record.
     */
    data class Ready(
        val app: App,
        val anomalies: List<CapabilityAnomaly>,
    ) : PhoneStartup()

    /**
     * @param error what to show and what the user can do about it -- PB-APP-9's routed state,
     *  so the startup path and every later failure reach the screen through one table.
     * @param detail the platform's own words, for the log and for a bug report. Never the thing
     *  shown to the user: a Keystore alias is not a remedy.
     */
    data class Unavailable(val error: RoutedError, val detail: String) : PhoneStartup()
}
