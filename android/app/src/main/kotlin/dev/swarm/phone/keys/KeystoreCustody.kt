package dev.swarm.phone.keys

import swarmmobile.KeyCustody

/**
 * PB-KEY-9's Android end: the object the Go core calls to obtain the key that seals its own
 * state directory.
 *
 * IT IS THE ONLY THING THAT MAKES THE SEAM REAL. `swarmmobile.NewApp` requires a
 * `swarmmobile.KeyCustody` and there is no constructor without one, so this class is on the
 * path of every phone the app ever constructs. Without it the facade verb would be a seam with
 * nothing on the far side -- which is the shape S14a's residual warned about from the other
 * direction, a Go-side gate green over a path the Android app cannot take.
 *
 * THE DIRECTION IS INBOUND, and B8 permits exactly this artifact. What crosses is the transient
 * per-tier DATA KEY, which the Android Keystore's authenticated AES KEK unwrapped on this side.
 * The Keystore key itself never crosses and cannot -- Keystore does not export key material --
 * and nothing carrying key material goes the other way: the interface has no parameters.
 *
 * WHAT IS NOT ASSERTED ANYWHERE, and cannot be on this machine: that the KEK behind
 * [KekProvider] is really in a TEE, that a real biometric prompt gates its unwrap, or that
 * StrongBox behaves as advertised. That is PB-E2E-5, the deferred physical-handset gate. The
 * JVM and Robolectric tests around this class cover POLICY -- which tier, which refusal, which
 * recovery -- and never hardware.
 */
class KeystoreKeyCustody(private val store: SealedStore) : KeyCustody {

    /**
     * The wake-tier data key. Its KEK is deliberately not user-authentication-gated
     * (ADR-007 B9/B16): a push arrives with nobody there, and a wake key behind a biometric
     * makes push useless in exactly the state it exists for.
     */
    override fun wakeKEK(): ByteArray = store.open(CustodyBlobs.stateKek(KeyTier.WAKE))

    /**
     * The content-tier data key, behind the biometric. This is the one that legitimately
     * refuses, and the refusal IS the gate -- not a flag beside it.
     *
     * The exception is allowed to propagate exactly as thrown. gomobile flattens it into a Go
     * error carrying only the message, and [KeyCustodyException.UserAuthenticationRequired] and
     * [KeyCustodyException.KeyPermanentlyInvalidated] carry the verdict token the Go core reads
     * to tell a per-operation refusal from a corrupt blob. Wrapping or re-messaging it here
     * would strip that: `phonecore.openSealedDeviceKeys` refuses a Resume outright for any
     * content-tier error that is NOT one of the two sentinels, so an unclassifiable refusal
     * turns a locked handset into an app that cannot start.
     */
    override fun contentKEK(): ByteArray = store.open(CustodyBlobs.stateKek(KeyTier.CONTENT))
}
