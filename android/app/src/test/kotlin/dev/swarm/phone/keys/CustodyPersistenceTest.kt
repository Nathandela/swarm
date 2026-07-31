package dev.swarm.phone.keys

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the S14 carry-over: the Android key custody has no
 * production persistence, so the sealing key vanishes on process death.
 *
 * THE DEFECT, verified before these assertions were written. [SealedStore] is
 * `LinkedHashMap<String, Entry>`; there is no file I/O anywhere under src/main/; there is no
 * [KekProvider] implementation outside the tests; and nothing under src/main/ constructs
 * `swarmmobile.NewApp` -- the symbol appears only in doc comments.
 *
 * So the KEK and every sealed blob live for exactly one process. The seal succeeds, the app
 * looks correct, and the SECOND launch finds device.key and phone-state.json sealed under a
 * key that no longer exists: permanently unopenable. On Android a process death is routine, so
 * "second launch" means "within minutes". This is standing defect class (ii) -- a
 * plausible-but-wrong value hiding a brick -- and the value doing the hiding is a comment:
 * SealedStore.rawBytes documents its return as "the persisted bytes, exactly as they sit on
 * disk", over a map.
 *
 * It is the same failure shape as the fresh-install defect found earlier in this phase, which
 * lost the whole durable state on the first restart and presented as "it randomly loses my
 * phone".
 *
 * NOTHING HERE TOUCHES HARDWARE. Whether the KEK is really in a TEE, whether a real biometric
 * gates it, whether StrongBox behaves as advertised: PB-E2E-5, deferred, and
 * android/gate/s16_ui_test.go fences that this package's tests do not pretend otherwise. What
 * is modelled is PERSISTENCE (does a blob outlive the object that wrote it) and RECOVERY
 * IDENTITY (which of the two verdicts a genuinely-gone key produces).
 */
class CustodyPersistenceTest {

    private fun blob() = ByteArray(32) { (it + 7).toByte() }

    /**
     * The property, stated as the platform states it: the object goes away and the bytes do
     * not. A store constructed a second time over the same backing must find what the first
     * one sealed.
     *
     * The factory is `openOver` rather than a second `open`: SealedStore already has an
     * instance `open(name)` that unwraps a blob, and a companion of the same name reading
     * "open the store" beside a method reading "open this entry" is a name that will be
     * misread once by everyone.
     *
     * The KEK is deliberately the SAME provider across both, because a real Keystore key
     * survives a process death; a fresh KEK per launch would model a factory reset and the
     * assertion would then be about a store that legitimately cannot open its own blob.
     */
    @Test
    fun `a sealed blob outlives the store that wrote it`() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val backing = PersistentCustodyBacking.inMemoryForTest()

        val first = SealedStore.openOver(kek, backing)
        first.put("probe", KeyTier.WAKE, blob())

        // PROCESS DEATH: the store object is dropped. Everything the next line finds reached
        // the backing store, which is the whole question.
        val second = SealedStore.openOver(kek, backing)

        assertTrue(
            "the blob written before the restart is not in the store afterwards. The sealing " +
                "key and the sealed bytes live only in the process, so the next launch finds " +
                "device.key and phone-state.json sealed under a key that no longer exists -- " +
                "permanently unopenable, on a platform where process death is routine",
            second.names().contains("probe"),
        )
        assertArrayEquals(blob(), second.open("probe"))
    }

    /**
     * The tier of a restored entry survives too. It decides which KEK opens the blob, so a store
     * that came back with everything on the WAKE tier would be holding one tier under two names
     * -- PB-KEY-2's split silently collapsed by a restart, and with it every property the split
     * still buys after ADR-007 B133: separate aliases are what keep a purge, a discard or a
     * platform invalidation of one tier from taking the other, and what keeps the content
     * material out of reach of the key the FCM path uses.
     */
    @Test
    fun `the tier of a restored entry survives the restart`() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val backing = PersistentCustodyBacking.inMemoryForTest()

        SealedStore.openOver(kek, backing).apply {
            put("wake-thing", KeyTier.WAKE, blob())
            put("content-thing", KeyTier.CONTENT, blob())
        }

        val restored = SealedStore.openOver(kek, backing)
        assertEquals(KeyTier.WAKE, restored.tierOf("wake-thing"))
        assertEquals(
            "a restored entry that lost its tier is PB-KEY-2's split collapsed by a restart: " +
                "the content key would open with no user present",
            KeyTier.CONTENT,
            restored.tierOf("content-thing"),
        )
    }

    /**
     * THE RECOVERY PATH, and it is the half that decides whether the brick is a brick.
     *
     * A KEK that is genuinely gone -- a biometric enrollment change, a cleared credential, a
     * restored image -- must surface as the PERMANENT identity, which the Go core reads as
     * crypto.ErrKeyInvalidated and the transport loop turns into `repair_required`: pair this
     * device again. The failure to avoid is an opaque error, because
     * phonecore.openSealedDeviceKeys refuses a Resume outright for any content-tier error that
     * is NOT one of the two sentinels -- so an unclassifiable refusal turns a recoverable
     * handset into an app that cannot start, with nothing on screen saying why.
     */
    @Test
    fun `a destroyed KEK surfaces as the permanent invalidation and never as a silent failure`() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val backing = PersistentCustodyBacking.inMemoryForTest()
        SealedStore.openOver(kek, backing).put("probe", KeyTier.CONTENT, blob())

        // The enrollment changed while the app was not running. The blob is still on disk and
        // nothing can ever open it again.
        kek.invalidatedTiers = setOf(KeyTier.CONTENT)
        val restarted = SealedStore.openOver(kek, backing)

        try {
            restarted.open("probe")
            fail(
                "opening a blob whose KEK was destroyed returned normally. The bytes cannot be " +
                    "recovered; a store that answers anything at all here hands the Go core " +
                    "material it will treat as a key",
            )
        } catch (e: KeyCustodyException.KeyPermanentlyInvalidated) {
            assertTrue(
                "the exception must carry the PERMANENT verdict token, because gomobile " +
                    "flattens it into a Go error carrying only its message",
                (e.message ?: "").contains(GoCustodyFailure.KEY_INVALIDATED_TOKEN),
            )
        }
    }

    /**
     * And the two verdicts stay apart across the restart, TOKEN AND ALL.
     *
     * WHAT THIS TEST USED TO CLAIM, and why the claim is gone rather than reworded (ADR-007
     * B133). It was `a locked tier after a restart is recoverable and not a re-pair`, and it
     * ended by calling `unlockAll()` and asserting the blob opened -- "the way out works, which
     * is what makes the refusal a prompt rather than a dead end". THERE IS NO WAY OUT ANY MORE.
     * No prompt exists in this app, so an auth-required refusal is NOT recoverable: PhoneRuntime
     * routes it to the same permanent verdict as a destroyed key, because the only population
     * that can raise it is a pre-B133 install whose content KEK a re-pair replaces. A test that
     * kept driving `unlockAll()` would be fencing a recovery the product cannot perform.
     *
     * WHAT SURVIVES IS THE DISTINCTNESS, and it survives for a reason that has nothing to do
     * with screens: `phonecore.openSealedDeviceKeys` reads these tokens to tell a per-operation
     * refusal from a container it cannot parse, and refuses a Resume outright for anything that
     * is neither. Two refusals carrying one token is a handset that will not start.
     */
    @Test
    fun `the two refusal verdicts stay distinct across a restart`() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val backing = PersistentCustodyBacking.inMemoryForTest()
        SealedStore.openOver(kek, backing).put("probe", KeyTier.CONTENT, blob())

        kek.lockedTiers = setOf(KeyTier.CONTENT)
        try {
            SealedStore.openOver(kek, backing).open("probe")
            fail("a content tier whose KEK demands an authentication must refuse")
        } catch (e: KeyCustodyException.UserAuthenticationRequired) {
            assertTrue(
                "the refusal must carry the auth-required token, or the Go core cannot tell it " +
                    "from a corrupt container and refuses the Resume outright",
                (e.message ?: "").contains(GoCustodyFailure.AUTH_REQUIRED_TOKEN),
            )
            assertFalse(
                "the two verdicts carry each other's token, so the discriminator decides nothing",
                (e.message ?: "").contains(GoCustodyFailure.KEY_INVALIDATED_TOKEN),
            )
        }
    }

    /**
     * The KEK is fetched PER OPERATION and never memoized.
     *
     * ITS PREMISE MOVED WITH PB-KEY-7's TRIGGER (ADR-007 B133 decision 3). It used to be the
     * property that made the content tier's GATE real -- "an auth-gated Keystore key re-checks
     * authorisation on every unwrap; a cached answer keeps decrypting content after the screen
     * locks". There is no lock event and no gate. What the property now defends is the PURGE: on
     * revoke or unpair the session drops its record and the Go core zeroizes its copy, and a
     * store holding the unwrapped tier key in a field would keep serving content material to
     * anything that asked, after the device stopped being entitled to it -- while every
     * restart-based test above still passed.
     */
    @Test
    fun `the restored store still asks the KEK on every open`() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val backing = PersistentCustodyBacking.inMemoryForTest()
        SealedStore.openOver(kek, backing).put("probe", KeyTier.CONTENT, blob())

        val restored = SealedStore.openOver(kek, backing)
        restored.open("probe")
        val after = kek.handedOut.size
        restored.open("probe")
        assertTrue(
            "the second open handed out no new key, so the unwrapped KEK is cached. A cached " +
                "answer survives PB-KEY-7's purge -- the material a revoke exists to make " +
                "unreachable stays reachable -- while every restart-based test still passes",
            kek.handedOut.size > after,
        )
    }

    /**
     * A backing that is genuinely durable must exist in PRODUCTION, not only as a test double.
     * This is the assertion that stops the whole file being satisfied by a fixture: the
     * in-memory backing above models the CONTRACT, and the app has to ship something that
     * implements it over real storage.
     */
    @Test
    fun `a production backing exists`() {
        assertNotNull(
            "PersistentCustodyBacking has no production implementation. The contract above can " +
                "be satisfied by a test double forever; what the handset needs is a backing over " +
                "real storage, in dev.swarm.phone.keys beside the policy that governs it. " +
                "android/gate/s16_wiring_test.go checks the same fact from the Go side.",
            PersistentCustodyBacking.production(),
        )
    }
}
