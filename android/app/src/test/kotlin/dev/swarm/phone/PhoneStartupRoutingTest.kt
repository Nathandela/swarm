package dev.swarm.phone

import dev.swarm.phone.keys.KeyCustodyException
import dev.swarm.phone.keys.KeyRole
import dev.swarm.phone.keys.KeyTier
import dev.swarm.phone.keys.PlatformCapability
import dev.swarm.phone.ui.ErrorState
import dev.swarm.phone.ui.Remedy
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Phase B slice S18b -- the fence [routeStartupFailure] shipped without.
 *
 * S16 built the app's one construction path and routed its failures with a two-arm `when` that
 * folded FOUR custody verdicts onto re-pair, and nothing anywhere could see it: constructing a
 * [PhoneRuntime] needs a `Context` and its failures come from Keystore, so there was no
 * PhoneRuntime test at all. The custody suite could not see it either -- `FailableCustodyTest`
 * accepts REPROVISION_KEK and REPAIR_DEVICE as a pair, which is correct at ITS layer and is
 * exactly what hides them being merged one layer up.
 *
 * WHAT THIS FILE ASSERTS, and it is one property in three shapes: a startup failure may never be
 * given a remedy the user can perform that cannot help. PB-APP-10 forbids the resulting loop in
 * as many words, and both defects below reach it THROUGH the remedy rather than around it.
 *
 * Plain JVM. Nothing here touches Keystore, a Context, or hardware; it is the routing TABLE
 * under test, which is where both defects lived.
 */
class PhoneStartupRoutingTest {

    /**
     * PB-KEY-8's read-back refusal: the platform generated the key but delivered something
     * weaker than requested. Its own doc says "nothing the user does fixes it -- the handset is
     * not capable of what the design requires", and it was routed to re-pair, which gives the
     * user: re-pair -> re-provision the same key on the same platform -> the same refusal ->
     * this screen. That is the failure loop, and the remedy is the thing that closes it.
     */
    @Test
    fun a_handset_that_downgrades_its_keys_is_never_told_to_pair_again() {
        val routed = routeStartupFailure(
            KeyCustodyException.KeystoreDowngrade(
                alias = "swarm.content.kek",
                detail = "user authentication required is false, requested true",
            ),
        )
        assertEquals(ErrorState.DEVICE_UNSUPPORTED, routed.state)
        assertEquals(Remedy.REPORT_BUG, routed.remedy)
        assertNotEquals(
            "re-pairing re-provisions the same key on the same handset and lands here again",
            Remedy.RE_PAIR,
            routed.remedy,
        )
        assertFalse(
            "a screen must not offer the pairing flow for a failure pairing cannot fix",
            routed.offersPairing,
        )
    }

    /** The same refusal from the capability probe rather than the read-back. */
    @Test
    fun a_handset_missing_a_required_capability_is_never_told_to_pair_again() {
        val routed = routeStartupFailure(
            KeyCustodyException.PlatformCapabilityMissing(
                role = KeyRole.COMMAND_SIGN,
                // Was USER_AUTH_PER_USE, which left the enum with PB-SEC-2 (ADR-007 B133).
                // KEYSTORE_AES_GCM is the one capability the design still consumes, so it is
                // also the only one this refusal can now be about.
                capability = PlatformCapability.KEYSTORE_AES_GCM,
            ),
        )
        assertEquals(ErrorState.DEVICE_UNSUPPORTED, routed.state)
        assertFalse(routed.offersPairing)
    }

    /**
     * The second defect, and the more alarming one to be told. [KeyCustodyException.Unexpected]
     * is "anything else the platform threw" -- a `renameTo` failing on a full disk at
     * construction, say. Routed to re-pair, a user whose key is perfectly intact is told it was
     * destroyed and that no authentication brings it back.
     */
    @Test
    fun an_unexpected_platform_failure_is_not_reported_as_a_destroyed_key() {
        val routed = routeStartupFailure(
            KeyCustodyException.Unexpected(
                tier = KeyTier.CONTENT,
                detail = "could not rename the custody file: no space left on device",
            ),
        )
        assertEquals(ErrorState.INTERNAL, routed.state)
        assertEquals(Remedy.REPORT_BUG, routed.remedy)
        assertNotEquals(
            "the key was never destroyed; telling the user it was is false and unactionable",
            ErrorState.REPAIR_REQUIRED,
            routed.state,
        )
    }

    /**
     * The three for which re-pair is TRUE. Asserted so the fix above cannot be "route
     * everything away from re-pair", which would break the one verdict PB-KEY-6 exists for.
     */
    @Test
    fun a_destroyed_or_missing_key_still_routes_to_the_re_pair_it_needs() {
        val invalidated = routeStartupFailure(KeyCustodyException.KeyPermanentlyInvalidated(KeyTier.CONTENT))
        assertEquals(ErrorState.REPAIR_REQUIRED, invalidated.state)
        assertEquals(Remedy.RE_PAIR, invalidated.remedy)
        assertTrue(invalidated.offersPairing)

        val missing = routeStartupFailure(KeyCustodyException.KeystoreKeyMissing("swarm.content.kek"))
        assertEquals(ErrorState.REPAIR_REQUIRED, missing.state)
        assertEquals(Remedy.RE_PAIR, missing.remedy)
    }

    /**
     * THE VERDICT THAT USED TO BE RECOVERABLE, AND IS NOT (ADR-007 B133).
     *
     * This test was `an_authentication_refusal_stays_a_prompt`, and it required
     * REAUTH_REQUIRED / `Remedy.AUTHENTICATE`. Both are gone: there is no prompt anywhere in this
     * app, so telling the user to authenticate is advice they cannot carry out -- PB-APP-10's
     * failure loop reached through the remedy, exactly like the two defects at the top of this
     * file, and this one would have been reached by a user whose app simply would not open.
     *
     * WHO CAN STILL RAISE IT, because the arm is not dead code. An install provisioned BEFORE
     * B133 holds an `AUTH_BIOMETRIC_STRONG` content KEK, and `KeystoreCustodyBootstrap.ensure`
     * returns early when the alias exists -- so the new spec never reaches it and the platform
     * goes on refusing. For that handset the key really is unusable and pairing again really is
     * the fix: a re-pair discards the alias and the next provision writes one that asks for no
     * authenticator. RE_PAIR is the honest answer rather than a fallback.
     *
     * The assertion is written against REPAIR_REQUIRED specifically, and not merely "not
     * REAUTH_REQUIRED", so routing it to DEVICE_UNSUPPORTED or INTERNAL fails here too. Neither
     * would be true: the handset is capable, and nothing is broken.
     */
    @Test
    fun a_legacy_auth_gated_key_is_routed_to_the_re_pair_that_actually_fixes_it() {
        val routed = routeStartupFailure(KeyCustodyException.UserAuthenticationRequired(KeyTier.CONTENT))
        assertEquals(ErrorState.REPAIR_REQUIRED, routed.state)
        assertEquals(Remedy.RE_PAIR, routed.remedy)
        assertTrue(
            "the one remedy for this state is a pairing, so the screen has to offer the flow",
            routed.offersPairing,
        )
    }

    /**
     * The non-custody half: everything the Go core refuses with arrives as a message carrying
     * the class token, and must be read exactly as every other facade failure is.
     *
     * PB-STATE-10's corrupt durable state is the case that matters here -- the app never
     * constructs, so this routing IS the whole product for a user in that state.
     */
    @Test
    fun a_go_facade_failure_is_routed_by_the_class_it_carries() {
        val corrupt = routeStartupFailure(
            IllegalStateException(
                "swarm/state-corrupt: phonecore: corrupt phone-state file: /data/phone-state.json",
            ),
        )
        assertEquals(ErrorState.STATE_CORRUPT, corrupt.state)
        assertEquals(Remedy.CLEAR_DATA_AND_RE_PAIR, corrupt.remedy)

        val offline = routeStartupFailure(IllegalStateException("swarm/offline: no relay connection"))
        assertEquals(ErrorState.OFFLINE, offline.state)
    }

    /**
     * EXHAUSTIVENESS. UNKNOWN is the one answer no startup failure may get: it is the reserved
     * row, whose remedy is NONE, so a user in a state the app cannot name is told nothing at all
     * -- and the defect this file fences was precisely a `when` whose catch-all arm absorbed
     * cases nobody had thought about.
     *
     * The enumeration is held to the hierarchy by [named] rather than by reflection: a `when`
     * over a sealed class, used as an EXPRESSION with no `else`, fails to COMPILE when a verdict
     * is added. That is stronger than a runtime count, and it needs no kotlin-reflect on a
     * unit-test classpath that does not carry one.
     */
    @Test
    fun every_custody_verdict_routes_somewhere_a_screen_can_render() {
        val verdicts = listOf(
            KeyCustodyException.UserAuthenticationRequired(KeyTier.CONTENT),
            KeyCustodyException.KeyPermanentlyInvalidated(KeyTier.CONTENT),
            KeyCustodyException.KeystoreKeyMissing("swarm.content.kek"),
            KeyCustodyException.PlatformCapabilityMissing(KeyRole.RELAY_AUTH, PlatformCapability.KEYSTORE_ED25519),
            KeyCustodyException.KeystoreDowngrade("swarm.wake.kek", "the authorization window is 0s, requested 30s"),
            KeyCustodyException.Unexpected(KeyTier.WAKE, "boom"),
        )
        assertEquals(
            "every verdict this hierarchy declares must be exercised here: ${verdicts.map { it.named() }}",
            verdicts.map { it.named() }.distinct().size,
            verdicts.size,
        )
        for (verdict in verdicts) {
            val routed = routeStartupFailure(verdict)
            assertNotEquals(
                "${verdict.named()} routes to the reserved unknown row, whose remedy is NONE -- " +
                    "the user is shown a failure and no way out of it",
                ErrorState.UNKNOWN,
                routed.state,
            )
        }
    }

    /** The compile-time half: a verdict added to the sealed hierarchy has no arm here. */
    private fun KeyCustodyException.named(): String = when (this) {
        is KeyCustodyException.UserAuthenticationRequired -> "UserAuthenticationRequired"
        is KeyCustodyException.KeyPermanentlyInvalidated -> "KeyPermanentlyInvalidated"
        is KeyCustodyException.KeystoreKeyMissing -> "KeystoreKeyMissing"
        is KeyCustodyException.PlatformCapabilityMissing -> "PlatformCapabilityMissing"
        is KeyCustodyException.KeystoreDowngrade -> "KeystoreDowngrade"
        is KeyCustodyException.Unexpected -> "Unexpected"
    }
}
