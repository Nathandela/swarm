package dev.swarm.phone.keys

import android.security.keystore.KeyPermanentlyInvalidatedException
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.fragment.app.FragmentActivity
import javax.crypto.Cipher
import javax.crypto.SecretKey

/**
 * THE ONLY FILE IN THE APP THAT TOUCHES `androidx.biometric`, and it is kept this thin on
 * purpose.
 *
 * WHY A SEPARATE FILE. Nothing here can be asserted by a unit test without asserting against a
 * Robolectric shadow, which would be a test proving that a shadow returned what the test told
 * it to -- the exact failure `android/gate/s16_ui_test.go` fences, and the exact shape ADR-007
 * B51 found nine times over. So everything that CAN be decided without the platform is decided
 * elsewhere ([PerUseGate], [PerUseRefusalText],
 * [dev.swarm.phone.runtime.ContentUnlockPolicy]) and asserted there, and what is left here is
 * translation: platform constants onto [PromptAvailability], platform callbacks onto
 * [PromptOutcome], a Keystore entry onto a `Cipher`. Every line of it is a statement about the
 * androidx API and none of it is a policy decision.
 *
 * NOTHING IN THIS REPOSITORY CLAIMS THIS CODE HAS RUN. No test executes it. PB-E2E-5 is
 * deferred (ADR-007 B31), and ADR-007 B56 makes the `androidTest` tier unexecutable outright --
 * the emulator's keymint reports SECURITY_LEVEL_SOFTWARE and PB-KEY-8 fails the app closed
 * before any screen renders, so an instrumented test cannot even reach a prompt. That a real
 * `BiometricPrompt` appeared, that a real finger was accepted or refused, that a real TEE
 * withheld a real key: none of it is established anywhere, here or in any evidence file.
 *
 * THE AUTHENTICATOR IS `BIOMETRIC_STRONG` AND NOT `DEVICE_CREDENTIAL`, matching the content KEK
 * (`KeystoreSpecs.kek`) and the per-use entries ([KeystoreSpecs.forOperation]) exactly. ADR-007
 * B59 records the argument, including which handsets that strands and what they are told
 * instead. It must stay in step with those two specs: a prompt that allowed a device credential
 * over a key that requires a biometric would succeed and then hand back a key the platform still
 * refuses to operate -- a prompt the user can satisfy that authorizes nothing.
 */

/**
 * The allowed authenticator set, in step with `KeystoreSpecs`. See the file header for why that
 * being in step is load-bearing rather than tidy, and ADR-007 B59 for why it is BIOMETRIC_STRONG
 * alone. `android/gate/s20_pbsec2_peruse_test.go` fails if this and the Keystore specs disagree.
 */
private const val AUTHENTICATORS: Int = BiometricManager.Authenticators.BIOMETRIC_STRONG

/**
 * What the platform says about prompting, from a plain `Context`.
 *
 * IT TAKES A CONTEXT AND NOT AN ACTIVITY, and that is what lets `PhoneRuntime` ask the question
 * BEFORE any screen exists. It has to: `KeystoreSpecs.kek(CONTENT)` requests
 * `AUTH_BIOMETRIC_STRONG`, and the platform refuses to GENERATE such a key when nothing is
 * enrolled -- so a PIN-only handset failed during provisioning, before a prompt could ever be
 * offered, and the exception surfaced as `SwarmErrorTokens.UNKNOWN` with no remedy at all
 * (`ContentUnlockPolicy.provisioningFor`).
 *
 * It is a top-level function in THIS file because this file is the only one permitted to import
 * androidx.biometric; `PhoneRuntime` calls it and stays free of the dependency.
 */
fun deviceBiometricAvailability(context: android.content.Context): PromptAvailability =
    availabilityOf(BiometricManager.from(context).canAuthenticate(AUTHENTICATORS))

/**
 * `BiometricManager.canAuthenticate` onto [PromptAvailability], one constant per constant.
 *
 * A status the platform adds later falls to TEMPORARILY_UNAVAILABLE and not to NO_HARDWARE: the
 * first says "try again", the second tells the user their handset can never do this. Guessing the
 * permanent answer over an unknown code is the wrong error to make.
 */
private fun availabilityOf(status: Int): PromptAvailability = when (status) {
    BiometricManager.BIOMETRIC_SUCCESS -> PromptAvailability.READY
    BiometricManager.BIOMETRIC_ERROR_NONE_ENROLLED -> PromptAvailability.NONE_ENROLLED
    BiometricManager.BIOMETRIC_ERROR_NO_HARDWARE -> PromptAvailability.NO_HARDWARE
    // "This device cannot satisfy the combination that was asked for" -- a Class-3 sensor the
    // platform will not vouch for is, for this app, no sensor.
    BiometricManager.BIOMETRIC_ERROR_UNSUPPORTED -> PromptAvailability.NO_HARDWARE
    BiometricManager.BIOMETRIC_ERROR_HW_UNAVAILABLE -> PromptAvailability.TEMPORARILY_UNAVAILABLE
    BiometricManager.BIOMETRIC_ERROR_SECURITY_UPDATE_REQUIRED ->
        PromptAvailability.SECURITY_UPDATE_REQUIRED

    else -> PromptAvailability.TEMPORARILY_UNAVAILABLE
}

/**
 * The platform's biometric surface, for the per-use gate and for the content tier's way back in.
 *
 * @param activity a `FragmentActivity` because that is what `BiometricPrompt` requires -- it
 *  hosts its UI in a fragment. `AppCompatActivity` is one, so the app's single Activity serves
 *  without becoming reachable from anywhere new: this object is constructed by
 *  `dev.swarm.phone.PhoneSurface`, which is where every other facade-reaching control already
 *  lives, and not by the exported Activity itself (PB-SEC-11).
 */
class BiometricPrompts(private val activity: FragmentActivity) : PerUsePrompt {

    override fun availability(): PromptAvailability = deviceBiometricAvailability(activity)

    /**
     * The per-use prompt: `BiometricPrompt` over a `CryptoObject`.
     *
     * WHAT COMES BACK IS THE PLATFORM'S CIPHER, never the one passed in. They are the same object
     * in practice, and relying on that would be relying on an implementation detail of androidx
     * to carry a security property: the caller's contract is that a null means the platform
     * released nothing, and reading `result.cryptoObject` is the only thing that can say so.
     */
    override fun show(
        operation: GatedOperation,
        cipher: Cipher,
        onResult: (PromptOutcome, Cipher?) -> Unit,
    ) {
        val prompt = BiometricPrompt(
            activity,
            activity.mainExecutor,
            object : BiometricPrompt.AuthenticationCallback() {

                override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                    onResult(PromptOutcome.SUCCEEDED, result.cryptoObject?.cipher)
                }

                override fun onAuthenticationError(code: Int, message: CharSequence) {
                    onResult(outcomeOf(code), null)
                }

                /**
                 * DELIBERATELY EMPTY, and this is the one place in the file where getting the
                 * androidx contract wrong would be a real bug rather than a wording one.
                 * `onAuthenticationFailed` is NOT terminal: it means one attempt was not
                 * recognised and the prompt is STILL ON SCREEN. Reporting it would clear the
                 * ledger's in-flight marker under a live prompt, so the user's next finger would
                 * resolve a gate that had already refused -- and the caller would have been told
                 * the action was refused while the prompt it belongs to is still up. The
                 * terminal answer arrives at `onAuthenticationError` or above.
                 */
                override fun onAuthenticationFailed() = Unit
            },
        )
        prompt.authenticate(promptInfo(titleFor(operation), subtitleFor(operation)), BiometricPrompt.CryptoObject(cipher))
    }

    /**
     * The CONTENT tier's way back in -- ADR-007 B44's missing exit.
     *
     * NO `CryptoObject`, and that is not a weaker prompt: the content KEK is a TIMED key
     * (`setUserAuthenticationParameters(60, AUTH_BIOMETRIC_STRONG)`), so what it needs is a
     * recent authentication of the right class, which is exactly what a plain authenticate
     * produces. A CryptoObject here would bind the authorization to one cipher and leave the KEK
     * -- a different Keystore entry -- as unauthorized as before.
     *
     * The caller retries `App.UnlockContent` on success. It is a retry and not an assumption:
     * the Keystore is still the gate, and it may still refuse.
     */
    fun confirmForContent(onResult: (PromptOutcome) -> Unit) {
        val prompt = BiometricPrompt(
            activity,
            activity.mainExecutor,
            object : BiometricPrompt.AuthenticationCallback() {
                override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                    onResult(PromptOutcome.SUCCEEDED)
                }

                override fun onAuthenticationError(code: Int, message: CharSequence) {
                    onResult(outcomeOf(code))
                }

                /** See [show]: not terminal, and the prompt is still on screen. */
                override fun onAuthenticationFailed() = Unit
            },
        )
        prompt.authenticate(
            promptInfo(
                "Unlock your sessions",
                "Your phone locked, so the key to your session content was dropped.",
            ),
        )
    }

    private fun promptInfo(title: String, subtitle: String): BiometricPrompt.PromptInfo =
        BiometricPrompt.PromptInfo.Builder()
            .setTitle(title)
            .setSubtitle(subtitle)
            // REQUIRED whenever the allowed set is BIOMETRIC_STRONG alone: androidx throws
            // without it. It is also the cancel path PromptOutcome.CANCELLED names.
            .setNegativeButtonText("Cancel")
            .setAllowedAuthenticators(AUTHENTICATORS)
            // An explicit confirmation step after a passive match (face, iris). Every operation
            // behind this prompt is destructive or authorising, and a glance is not a decision.
            .setConfirmationRequired(true)
            .build()

    private companion object {

        fun titleFor(operation: GatedOperation): String = when (operation) {
            GatedOperation.REVOKE -> "Revoke this device"
            GatedOperation.KILL -> "Kill this session"
            GatedOperation.LAUNCH -> "Launch on your machine"
            GatedOperation.KILL_SWITCH -> "Change the kill switch"
            // Unreachable: PerUseGate refuses a timed operation before any prompt is built.
            GatedOperation.INPUT, GatedOperation.TAKE_CONTROL -> "Confirm"
        }

        fun subtitleFor(operation: GatedOperation): String = when (operation) {
            GatedOperation.REVOKE ->
                "This phone will stop being able to reach your machine."

            GatedOperation.KILL ->
                "The session ends on your machine and anything running in it stops."

            GatedOperation.LAUNCH -> "A new agent starts on your machine."
            GatedOperation.KILL_SWITCH -> "This changes what every device may do."
            GatedOperation.INPUT, GatedOperation.TAKE_CONTROL -> ""
        }

        /**
         * `BiometricPrompt`'s terminal error codes onto [PromptOutcome].
         *
         * THE TWO LOCKOUTS ARE KEPT APART because only one of them clears on its own, and
         * `BiometricPolicy.resolve` gives them different resolutions for that reason. Everything
         * that is neither a cancel nor a lockout is retryable: a vendor error, a timeout or an
         * unprocessable image is a reason to try again, and reporting it as a cancel would tell
         * the user they declined something they did not.
         */
        fun outcomeOf(code: Int): PromptOutcome = when (code) {
            BiometricPrompt.ERROR_NEGATIVE_BUTTON,
            BiometricPrompt.ERROR_USER_CANCELED,
            BiometricPrompt.ERROR_CANCELED,
            -> PromptOutcome.CANCELLED

            BiometricPrompt.ERROR_LOCKOUT -> PromptOutcome.LOCKED_OUT
            BiometricPrompt.ERROR_LOCKOUT_PERMANENT -> PromptOutcome.LOCKED_OUT_PERMANENT
            else -> PromptOutcome.FAILED
        }
    }
}

/**
 * The Keystore side of the per-use gate: the entry named by [KeystoreAliases.forOperation], as a
 * `Cipher` ready to be handed to a `CryptoObject`.
 *
 * `Cipher.init` SUCCEEDING IS NOT AUTHORIZATION, and the difference is the whole reason the
 * per-use tier can exist. For a key with a timed window, `init` itself throws
 * `UserNotAuthenticatedException` once the window lapses. For a per-use key -- timeout 0 --
 * `init` succeeds unauthenticated and it is the OPERATION that the platform refuses, which is
 * precisely what lets the initialised cipher be carried into a prompt and used afterwards.
 * [PerUseGate] is what performs that operation; nothing here does.
 *
 * IT HOLDS NO KEY IN A FIELD, for the reason `KeystoreKekProvider` states: a cached handle
 * answers from whatever the process saw at startup, which is exactly the window in which an
 * enrollment change destroys a key.
 */
class KeystorePerUseCiphers(
    private val provisioning: CustodyProvisioning,
) : PerUseCipherSource {

    /**
     * A cipher under [operation]'s own entry, generating the entry on first use.
     *
     * THE ONE RETRY IS FOR AN ENROLLMENT CHANGE, and it is right here and wrong for the tier
     * KEKs. `setInvalidatedByBiometricEnrollment(true)` destroys these entries when a fingerprint
     * is added or removed -- and unlike a KEK, a gate entry SEALS NOTHING. There is no material
     * behind it to lose, so the honest recovery is to make a new one, which is what the user
     * would get on a fresh install anyway. Doing the same to a content KEK would silently discard
     * the pairing; that is why `KeystoreCustodyBootstrap.ensure` refuses instead, and why this is
     * a separate path rather than a shared one.
     */
    override fun cipherFor(operation: GatedOperation): Cipher {
        val alias = KeystoreAliases.forOperation(operation)
        if (!KeystoreEntries.exists(alias)) provisioning.provisionGate(operation)
        return try {
            initialised(alias)
        } catch (invalidated: KeyPermanentlyInvalidatedException) {
            KeystoreEntries.drop(alias)
            provisioning.provisionGate(operation)
            initialised(alias)
        }
    }

    private fun initialised(alias: String): Cipher {
        val key = KeystoreEntries.keystore().getKey(alias, null) as? SecretKey
            ?: throw KeyCustodyException.KeystoreKeyMissing(alias)
        // ENCRYPT, and no IV supplied. Keystore requires randomized encryption for these keys and
        // refuses a caller-chosen nonce; the ciphertext is discarded by the gate regardless. What
        // is being asked of the platform is permission, not confidentiality.
        return Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key) }
    }
}
