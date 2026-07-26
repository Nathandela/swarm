package dev.swarm.phone.runtime

import dev.swarm.phone.keys.GoCustodyFailure
import dev.swarm.phone.keys.PromptAvailability
import dev.swarm.phone.ui.ErrorRouter
import dev.swarm.phone.ui.ErrorState
import dev.swarm.phone.ui.Remedy
import dev.swarm.phone.ui.SwarmErrorTokens
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * ADR-007 B44's hole, closed: the content KEK can REFUSE, and until now there was no in-app way
 * to satisfy it.
 *
 * THE SHAPE OF THE DEFECT, because it is the one this phase keeps finding. B44 made a screen
 * lock drop the live content key on every screen-off, and `PhoneSurface.renderReady` calls
 * `PhoneRuntime.unlockContent` on the way back with a comment asserting that this "is the moment
 * the Keystore-backed content KEK will answer". That is FALSE in four states -- after a device
 * credential unlock (mandatory post-reboot), after the biometric idle timeout, after repeated
 * failures, and always on a handset with no enrolled Class-3 biometric, because the content KEK
 * carries `AUTH_BIOMETRIC_STRONG` and nothing else. On refusal the app had no prompt of its own:
 * a gate whose exit is unbuilt, which is exactly why `ContentLock` declined a foreground timer.
 *
 * WHAT IS MODELLED HERE: which refusals the app can answer in-app, and what it offers for the
 * ones it cannot. NOT MODELLED AND NOT CLAIMED: that any biometric prompt was shown, accepted or
 * refused on any device, or that a real Keystore honoured or withheld the content KEK. PB-E2E-5
 * is deferred (ADR-007 B31) and ADR-007 B56 makes the `androidTest` tier unexecutable besides.
 */
class ContentUnlockTest {

    /**
     * The refusal `PhoneRuntime.unlockContent` actually produces when the platform says the user
     * has not authenticated. Asserted rather than assumed: the whole recourse below hangs off
     * this one routed value, and a taxonomy edit that moved it would leave the control hidden on
     * precisely the screen that needs it, with nothing failing.
     */
    @Test
    fun the_content_keks_authentication_refusal_routes_to_a_remedy_the_user_can_perform() {
        val routed = ErrorRouter.route(GoCustodyFailure.AUTH_REQUIRED_TOKEN)
        assertEquals(ErrorState.REAUTH_REQUIRED, routed.state)
        assertEquals(Remedy.AUTHENTICATE, routed.remedy)
    }

    /**
     * The control appears for exactly the refusals a prompt can fix. B44's residual said the
     * remedy for a lapsed content window "is a fresh authentication"; this is the assertion that
     * the app now offers one.
     */
    @Test
    fun the_unlock_control_is_offered_for_an_authentication_refusal() {
        assertTrue(
            "the content KEK refused for want of authentication and the app offered nothing",
            ContentUnlockPolicy.offersUnlock(ErrorRouter.route(GoCustodyFailure.AUTH_REQUIRED_TOKEN)),
        )
    }

    /**
     * And NOT for the refusals it cannot fix. This is the other half of the same defect class:
     * a prompt offered for a destroyed key is a prompt the user can never satisfy, which is the
     * failure LOOP PB-APP-10 forbids, reached through the remedy.
     */
    @Test
    fun the_unlock_control_is_not_offered_for_a_refusal_no_prompt_can_satisfy() {
        val unfixable = listOf(
            GoCustodyFailure.KEY_INVALIDATED_TOKEN to "the key is destroyed; prompting cannot bring it back",
            SwarmErrorTokens.DEVICE_UNSUPPORTED to "the handset cannot hold the key at all",
            SwarmErrorTokens.GRANT_LOST to "only the machine can re-grant",
            SwarmErrorTokens.INTERNAL to "a bug is not an authentication state",
            SwarmErrorTokens.OFFLINE to "a socket is not an authentication state",
        )
        for ((token, why) in unfixable) {
            assertFalse(why, ContentUnlockPolicy.offersUnlock(ErrorRouter.route(token)))
        }
        assertFalse("no refusal at all is not a refusal to offer a prompt for", ContentUnlockPolicy.offersUnlock(null))
    }

    /**
     * The vacuity control on the two tests above: the predicate is not constant. If a mutation
     * made `offersUnlock` return true always, the NOT-offered test fails; if it returned false
     * always, the offered test fails. This states the pair as one property so neither can be
     * satisfied by a constant.
     */
    @Test
    fun the_unlock_offer_discriminates_rather_than_answering_the_same_thing_every_time() {
        val everyToken = listOf(
            SwarmErrorTokens.UNKNOWN, SwarmErrorTokens.INTERNAL, SwarmErrorTokens.INVALID_REQUEST,
            SwarmErrorTokens.NOT_FOUND, SwarmErrorTokens.APP_CLOSED, SwarmErrorTokens.OFFLINE,
            SwarmErrorTokens.NOT_PAIRED, SwarmErrorTokens.STATE_CORRUPT,
            SwarmErrorTokens.DEVICE_UNSUPPORTED, SwarmErrorTokens.SYNCING,
            SwarmErrorTokens.AWAITING_KEY, SwarmErrorTokens.GRANT_LOST,
            SwarmErrorTokens.REAUTH_REQUIRED, SwarmErrorTokens.REPAIR_REQUIRED,
            SwarmErrorTokens.REVOKED, SwarmErrorTokens.NEEDS_LEASE, SwarmErrorTokens.RATE_LIMITED,
            SwarmErrorTokens.PAIRING_FAILED,
        )
        val (offered, withheld) = everyToken.map { ErrorRouter.route(it) }
            .partition { ContentUnlockPolicy.offersUnlock(it) }

        assertTrue("no routed error offers the unlock control", offered.isNotEmpty())
        assertTrue("every routed error offers the unlock control", withheld.isNotEmpty())
        assertTrue(
            "only an AUTHENTICATE remedy may offer it; got $offered",
            offered.all { it.remedy == Remedy.AUTHENTICATE },
        )
    }

    /**
     * THE HOLE THAT REFUSING `AUTH_DEVICE_CREDENTIAL` OPENS, and the assertion that it is named.
     *
     * `KeystoreSpecs.kek(CONTENT)` requests `AUTH_BIOMETRIC_STRONG`, and the platform will not
     * GENERATE such a key with nothing enrolled -- so a PIN-only handset failed inside
     * provisioning, long before any prompt could be offered. `DeviceCapabilities.probe` cannot see
     * it: USER_AUTH_PER_USE is answered from the API LEVEL, a fact about the platform rather than
     * about this handset. What the user got was `SwarmErrorTokens.UNKNOWN` -- "something failed in
     * a way the app does not recognise" -- whose remedy is [Remedy.NONE]. An app that will not
     * start, over something the user could fix in thirty seconds, saying nothing.
     */
    @Test
    fun a_handset_with_nothing_enrolled_is_refused_by_name_rather_than_failing_in_provisioning() {
        assertEquals(
            ContentProvisioning.NEEDS_ENROLMENT,
            ContentUnlockPolicy.provisioningFor(PromptAvailability.NONE_ENROLLED),
        )
        assertEquals(
            "no Class-3 sensor is a fact about the hardware, not something to enrol",
            ContentProvisioning.UNSUPPORTED,
            ContentUnlockPolicy.provisioningFor(PromptAvailability.NO_HARDWARE),
        )
    }

    /**
     * The other direction, and the one residuals section 2.8 is about: an app that refuses to
     * start. Key GENERATION checks enrolment, not whether the sensor is free this second, so a
     * transient answer must not block a launch -- the handset can provision and the prompt can
     * wait.
     */
    @Test
    fun a_transient_platform_answer_does_not_stop_the_app_starting() {
        for (transient in listOf(
            PromptAvailability.READY,
            PromptAvailability.TEMPORARILY_UNAVAILABLE,
            PromptAvailability.SECURITY_UPDATE_REQUIRED,
        )) {
            assertEquals(
                "$transient is not a reason the content KEK cannot be generated",
                ContentProvisioning.PROCEED,
                ContentUnlockPolicy.provisioningFor(transient),
            )
        }
        // The set is closed, so a sixth availability cannot default into PROCEED unnoticed.
        assertEquals(
            PromptAvailability.entries.size,
            PromptAvailability.entries.map { ContentUnlockPolicy.provisioningFor(it) }.size,
        )
    }

    /**
     * THE STANDING TRAP as an assertion, on the content tier this time. If the platform cannot
     * prompt, offering an unlock control is offering a button that does nothing -- so the
     * availability answer decides what the user is told, and the one state with no way forward
     * is the one where the handset has no biometric hardware.
     */
    @Test
    fun an_unlock_offer_that_cannot_be_prompted_says_what_to_do_instead() {
        for (state in PromptAvailability.entries) {
            val advice = ContentUnlockPolicy.adviceFor(state)
            assertTrue("$state has no advice for the user", advice.isNotBlank())
        }
        assertFalse(
            "a handset that can prompt needs no advice in place of the prompt",
            ContentUnlockPolicy.canPrompt(PromptAvailability.NONE_ENROLLED),
        )
        assertTrue(ContentUnlockPolicy.canPrompt(PromptAvailability.READY))
    }
}
