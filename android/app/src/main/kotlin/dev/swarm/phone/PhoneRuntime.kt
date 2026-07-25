package dev.swarm.phone

import android.content.Context
import android.content.pm.PackageManager
import dev.swarm.phone.keys.AndroidKeyInfoReader
import dev.swarm.phone.keys.AndroidKeystoreProvisioner
import dev.swarm.phone.keys.CustodyProvisioning
import dev.swarm.phone.keys.FileCustodyBacking
import dev.swarm.phone.keys.GoCustodyFailure
import dev.swarm.phone.keys.KeyCustodyException
import dev.swarm.phone.keys.KeyTier
import dev.swarm.phone.keys.KeystoreCustodyBootstrap
import dev.swarm.phone.keys.KeystoreKekProvider
import dev.swarm.phone.keys.KeystoreKeyCustody
import dev.swarm.phone.keys.PersistentCustodyBacking
import dev.swarm.phone.keys.Recovery
import dev.swarm.phone.keys.SealedStore
import dev.swarm.phone.ui.ErrorRouter
import dev.swarm.phone.ui.RoutedError
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
     * Everything that can refuse, in the order the refusals mean something.
     *
     * `Throwable` and not `Exception`: a missing native library is an `Error`, and a phone that
     * cannot load its own core still has to produce a state rather than take the process down.
     */
    private fun attach(): PhoneStartup = try {
        PhoneStartup.Ready(construct())
    } catch (failure: Throwable) {
        PhoneStartup.Unavailable(routed(failure), failure.message ?: failure.javaClass.name)
    }

    private fun construct(): App {
        val backing = FileCustodyBacking(custodyDir)
        val bootstrap = bootstrapOver(backing)

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

    private fun bootstrapOver(backing: PersistentCustodyBacking) = KeystoreCustodyBootstrap(
        backing = backing,
        provisioning = CustodyProvisioning(AndroidKeystoreProvisioner(), AndroidKeyInfoReader()),
        // The handset's own answer. StrongBox absence is a fallback and not a refusal --
        // refusing without it would refuse most handsets -- and what must not happen is a key
        // claiming hardware it does not have, which CustodyProvisioning's read-back settles from
        // the KeyInfo rather than from this preference.
        strongBoxPreferred =
        context.packageManager.hasSystemFeature(PackageManager.FEATURE_STRONGBOX_KEYSTORE),
    )

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

    /**
     * The failure as something a screen can render (PB-APP-9).
     *
     * A custody failure is routed by its RECOVERY rather than by its message, because only two
     * of the six carry a verdict token; the rest would fall through to the unknown row, which
     * for a permanent invalidation is a user told to try again forever. Everything else IS a
     * message -- gomobile leaves nothing of a Go error but its text, with the class stamped on
     * the front -- so [ErrorRouter] reads it exactly as every other facade failure is read.
     */
    private fun routed(failure: Throwable): RoutedError = when {
        failure !is KeyCustodyException -> ErrorRouter.route(failure.message.orEmpty())
        GoCustodyFailure.recoveryFor(failure) == Recovery.REAUTHENTICATE ->
            ErrorRouter.route(GoCustodyFailure.AUTH_REQUIRED_TOKEN)

        else -> ErrorRouter.route(GoCustodyFailure.KEY_INVALIDATED_TOKEN)
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
 * The outcome of trying to build the phone. Two cases and no third, because "null App" is how a
 * caller ends up rendering an empty roster for a handset whose key was destroyed.
 */
sealed class PhoneStartup {

    data class Ready(val app: App) : PhoneStartup()

    /**
     * @param error what to show and what the user can do about it -- PB-APP-9's routed state,
     *  so the startup path and every later failure reach the screen through one table.
     * @param detail the platform's own words, for the log and for a bug report. Never the thing
     *  shown to the user: a Keystore alias is not a remedy.
     */
    data class Unavailable(val error: RoutedError, val detail: String) : PhoneStartup()
}
