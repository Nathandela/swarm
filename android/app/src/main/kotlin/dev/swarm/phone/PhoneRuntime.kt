package dev.swarm.phone

import android.content.Context
import android.content.pm.PackageManager
import android.net.http.X509TrustManagerExtensions
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
import dev.swarm.phone.keys.KeyTier
import dev.swarm.phone.keys.KeystoreCustodyBootstrap
import dev.swarm.phone.keys.KeystoreKekProvider
import dev.swarm.phone.keys.KeystoreKeyCustody
import dev.swarm.phone.keys.PersistentCustodyBacking
import dev.swarm.phone.keys.SealedStore
import dev.swarm.phone.relay.RelayTrustImpl
import dev.swarm.phone.ui.ErrorRouter
import dev.swarm.phone.ui.RoutedError
import dev.swarm.phone.ui.SwarmErrorTokens
import swarmmobile.App
import swarmmobile.Config
import swarmmobile.Swarmmobile
import java.io.File
import java.security.KeyStore
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager

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
 * did it in `onCreate` would turn a handset whose Keystore refuses into a process that dies
 * before any screen exists to say why.
 *
 * SO IT IS LAZY AND FAILABLE. [phone] answers a [PhoneStartup], never an exception, and only a
 * SUCCESS is remembered: a refusal is retried on the next call rather than latched, so a
 * transient platform fault does not brick the process it happened in.
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
     * The relay this phone already knows, or empty. The short-code pairing spelling (ADR-007
     * B140) needs it BEFORE any pairing exists: ten typed characters cannot carry a URL, so
     * the remembered slot stands in for the QR's relay field, and the Go facade refuses a
     * code arriving with an empty one -- with words, on the pairing screen.
     */
    @Synchronized
    fun knownRelay(): String = rememberedRelay()

    /**
     * The operation id of the revoke this install issued, or empty (agents-tracker-4zue).
     *
     * WHY IT IS HERE AND NOT ON THE SURFACE THAT ISSUED IT. The revoke DESTROYS that surface: a
     * revoked phone is an unpaired phone, and `PhoneSurface.drawPairOnly` replaces the whole
     * scaffold the settings panel is drawn inside -- so the panel is gone long before the machine
     * can answer, and the answer belongs to the screen the phone was sent to. This runtime is the
     * one object both surfaces already hold, and it is where the relay coordinate the facade has
     * no field for is kept for the same reason.
     *
     * IT IS MEMORY AND NOT A FILE, unlike the relay URL, and the asymmetry is the honest one. A
     * process that died between the revoke and its answer has lost the id, and what the pair-only
     * screen says then is that the removal is unconfirmed -- the true statement about what is
     * knowable on this handset, and the same degradation `PairOnlyScreen.reasonFor` already
     * accepts for a transport state that is process memory too. Writing it down would instead
     * leave an install remembering one command from a pairing that no longer exists.
     */
    private var revokeOp: String = ""

    /**
     * Remember the revoke whose answer the pair-only screen claims -- or, with an empty id, forget
     * it.
     *
     * FORGETTING IS THE SAME VERB because the clearing has one reason and one call site:
     * `PhoneSurface.renderReady` spends the divergence the moment the presentation gate says this
     * handset is usably paired again, which is the fact that ends the warning.
     */
    @Synchronized
    fun latchRevoke(operationId: String) {
        revokeOp = operationId
    }

    /** The revoke this install issued, for the screen it sent the phone to. */
    @Synchronized
    fun revokeOperation(): String = revokeOp

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
     *    AES key at all, which was already a phone that could not start; what changes is that it
     *    now says which capability was missing instead of surfacing a platform exception as
     *    INTERNAL.
     *
     * THE CONSUMED SET IS ONE ROW AFTER ADR-007 B133, and the API-level row that used to sit
     * beside it (USER_AUTH_PER_USE) is gone with the authenticator it described. That row was
     * never what refused the A26 -- B132's refusal was an ENROLMENT check further down this
     * file, which is also gone.
     */
    private fun capabilityPlan(): CustodyPlan.Provisioned {
        val plan = CustodyPlanner.forDevice(
            DeviceCapabilities(
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
     * ADR-007 B132 AND B133, AND THE ONE THING THAT WAS DELETED HERE. This used to call
     * `refuseAHandsetThatCannotHoldTheContentKek` first, which asked whether the handset had an
     * enrolled Class-3 biometric and threw if it did not -- because the platform refuses to
     * GENERATE an `AUTH_BIOMETRIC_STRONG` key with nothing enrolled. On the A26 (a PIN, no
     * fingerprint) that made every screen in the app unreachable: `PhoneSurface` and
     * `PairingSurface` are both downstream of [phone], so the user was shown six inoperable
     * controls and NO route to pairing at all. The content KEK no longer asks for an
     * authenticator, so there is nothing left to refuse over and the check is gone rather than
     * relaxed.
     */
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
            // ADR-015 R3's push gateway, from the build's operator settings (see
            // app/build.gradle.kts). It rides Config for relayURL's exact reason: the phone
            // core has no durable field for either, so the endpoint must be supplied at
            // construction. Empty is a real state and not an error -- a build with no
            // gateway configured is honestly foreground-only, and the facade says so by
            // name rather than reporting a push path that does not exist.
            pushGatewayURL = configuredPushGateway()
            // "" adopts whatever the durable blob describes (PB-STATE-1/-2). Naming a machine
            // here would override the coordinate the phone resumed, which is the one thing a
            // resume exists to preserve.
            machineID = ""
        }
        val app = Swarmmobile.newApp(config, KeystoreKeyCustody(store))
        installRelayTrust(app)
        return app
    }

    /**
     * ADR-016 W2's production wiring: install a real [RelayTrustImpl] over the DEFAULT
     * [TrustManagerFactory] -- the platform's own verifier, reading the Conscrypt APEX store
     * Go itself cannot see (`security.go`'s own reasoning for `TrustRootsPinned`).
     *
     * BEST-EFFORT, NOT A STARTUP GATE. A failure here (no default trust manager on this
     * device) is exactly `RelayTrust`'s own "registration failed" case its doc comment
     * names: the delegate policy stays selected with none installed, so every dial without a
     * pin refuses with `ErrPinRequired` -- Android's PRE-ADR-016 floor, unchanged, not a
     * regression this construction step could introduce. A handset that cannot build a
     * `TrustManagerFactory` has no screen this app could show either way; failing app
     * construction over it would brick a phone that a `pinned_spki` machine's pairing QR
     * could still reach.
     */
    private fun installRelayTrust(app: App) {
        try {
            val tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm())
            tmf.init(null as KeyStore?)
            val tm = tmf.trustManagers.first { it is X509TrustManager } as X509TrustManager
            app.setRelayTrust(RelayTrustImpl(X509TrustManagerExtensions(tm)))
        } catch (e: Exception) {
            // Swallowed deliberately -- see the doc comment above.
        }
    }

    /**
     * PB-KEY-7's purge, whose TRIGGER moved (ADR-007 B133 decision 3).
     *
     * It used to be a screen lock, observed process-wide by `ContentLock`. There is no lock event
     * any more -- nothing on this handset is gated on a user authentication -- so the purge is
     * now reached from REVOKE and UNPAIR, which is where the phone stops being entitled to the
     * epoch keys at all. It is not recoverable without pairing again, and that is the point.
     *
     * IT NEVER CONSTRUCTS A PHONE. `ready` is only set once one was built, so a revoke on a
     * handset whose app has not been opened does not reach Keystore, the filesystem or the native
     * library on the way -- and there is nothing there to purge in the first place.
     *
     * THE ERROR IS RETURNED, AND IT USED TO BE SWALLOWED (agents-tracker-r3os). This comment said
     * that was "the honest handling rather than a shortcut", on the argument that the memory half
     * has happened regardless and the screen has already reported the verb's own outcome. The
     * first half is true and the second is about a DIFFERENT fact: the verb's outcome is whether
     * the machine removed the device, and this one is whether the sealed containers left on this
     * handset are gone. `App.PurgeKeys` says so in its own words -- "an error means the material
     * AT REST survived (a full disk, a read-only data directory)" -- and both Go layers keep that
     * promise deliberately, at the cost of a fail-open path they argue for at length. A caller
     * that is handed nothing draws an unpaired phone over key material still on disk.
     *
     * IT IS A VALUE AND NOT A THROW, which is the caller's shape rather than a style preference:
     * `SettingsSurface.onReplace` runs this in a `finally` so that both tiers go whether or not
     * the revoke reached the machine, and an exception raised in a `finally` REPLACES the one the
     * block was carrying -- the user would be told about the disk instead of about their machine.
     *
     * @return null when the purge finished, at rest and in memory, or the routed reason the
     *  at-rest half did not. It is [unlockContent]'s shape for [routeStartupFailure]'s reason:
     *  the refusal is routed by the layer that knows the custody taxonomy and SAID by the layer
     *  that has a screen.
     */
    @Synchronized
    fun purgeKeys(): RoutedError? {
        // CLEARED FIRST, SO THE LATCH ALWAYS DESCRIBES THIS CALL. The early return below is the
        // reason: a runtime that never built a phone purged nothing, and leaving a previous
        // purge's failure standing would report key material as surviving a purge that never ran.
        // Reaching that needs a re-pair between two revokes and `ready` gone at exactly the wrong
        // moment, which no caller can produce today -- but the readers are on another surface and
        // this keeps them from having to know that. No reader can see the gap: both this and
        // [purgeFailure] hold the same monitor.
        purgeFailed = ""
        val live = ready ?: return null
        val failure = try {
            live.app.purgeKeys()
            null
        } catch (refused: Exception) {
            routeStartupFailure(refused)
        }
        purgeFailed = failure?.message.orEmpty()
        return failure
    }

    /**
     * The routed reason the last purge could not finish at rest, or empty (agents-tracker-jx23).
     *
     * WHY IT IS REMEMBERED AND NOT ONLY RETURNED. The return is for the caller holding a screen at
     * the moment of the press; this is for the screen the phone LANDS on, which redraws long
     * afterwards and recomposes its notice from scratch every time the machine's answer changes
     * ([PhoneSurface] does the recomposing). Without the fact kept somewhere both can reach, the
     * sentence would be on screen until the machine replied and gone from the moment it did.
     *
     * IT NEEDS NO CLEARING VERB, which is the whole reason it is written HERE. [purgeKeys] is the
     * only thing that sets it and it sets it on EVERY path, including the early return, so this
     * always describes the last purge attempted; and the only reader is the pair-only screen,
     * which is not drawn at all once the phone is paired again. A stale value therefore has
     * neither a way to arise nor a draw to appear on.
     */
    @Synchronized
    fun purgeFailure(): String = purgeFailed

    private var purgeFailed: String = ""

    /**
     * PB-KEY-7's "require a fresh unwrap before restoring content".
     *
     * WHAT IT ASKS IS KEY AVAILABILITY (ADR-007 B133 decision 2). It is a REQUEST, not a promise:
     * the content KEK carries `setUserAuthenticationRequired(false)`, so it no longer refuses over
     * a user who has not authenticated -- but it still refuses over a destroyed key, a missing
     * Keystore entry or a platform fault, and each of those is something the user has to be told.
     * The refusal is routed like every other facade failure, by the caller that has a screen to
     * put it on ([PhoneSurface]).
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

    /**
     * The push gateway this build was configured with, or empty.
     *
     * It is a GENERATED STRING RESOURCE (`swarm_push_gateway_url`, written by
     * `app/build.gradle.kts` from the `SWARM_PUSH_GATEWAY_URL` operator setting) and not a
     * remembered file like the relay URL, because the two coordinates are learned differently:
     * the relay is per-pairing and arrives in a scanned QR, while the gateway is one
     * deployment-wide endpoint owned by the same operator who supplies `google-services.json`.
     * Keeping it in a runtime file would invent a setting nothing writes.
     */
    private fun configuredPushGateway(): String =
        context.getString(R.string.swarm_push_gateway_url).trim()

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
 * IT IS ONE EXHAUSTIVE `when` OVER THE SIX VERDICTS, and it carries no `else` -- a verdict added
 * later fails to compile here rather than falling into a bucket. It used to route four of them
 * indirectly, through a `GoCustodyFailure.recoveryFor` whose middle answer was REAUTHENTICATE;
 * ADR-007 B133 removed the state that answer named, so the indirection had one arm left and is
 * gone with it.
 */
private fun routeCustodyVerdict(failure: KeyCustodyException): RoutedError = when (failure) {
    // A bug, not a state the user can act on. Telling them to re-pair would say a perfectly
    // good key was destroyed.
    is KeyCustodyException.Unexpected -> ErrorRouter.route(SwarmErrorTokens.INTERNAL)

    // PB-KEY-8's two refusals: the handset is not capable of what the design requires. Routed
    // to re-pair, the user would get re-pair -> re-provision the same key on the same platform
    // -> the same refusal, which is the failure LOOP PB-APP-10 forbids reached through the
    // remedy. DEVICE_UNSUPPORTED's remedy is not a user action at all.
    is KeyCustodyException.PlatformCapabilityMissing,
    is KeyCustodyException.KeystoreDowngrade,
    -> ErrorRouter.route(SwarmErrorTokens.DEVICE_UNSUPPORTED)

    // THE KEY IS GONE, which IS something the user can act on.
    //
    // UserAuthenticationRequired shares this arm and NOT an authenticate one, because after
    // ADR-007 B133 there is no authentication left for the user to perform. The one population
    // that can still raise it is an install that was provisioned BEFORE this change and still
    // holds an AUTH_BIOMETRIC_STRONG content KEK -- `KeystoreCustodyBootstrap.ensure` returns
    // early when the alias exists, so the spec is not re-requested on upgrade. For that handset
    // the key really is unusable and pairing again really is the fix: a re-pair discards the
    // alias and the next provision writes one that asks for no authenticator.
    is KeyCustodyException.UserAuthenticationRequired,
    is KeyCustodyException.KeyPermanentlyInvalidated,
    is KeyCustodyException.KeystoreKeyMissing,
    -> ErrorRouter.route(GoCustodyFailure.KEY_INVALIDATED_TOKEN)
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
