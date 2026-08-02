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
 * [KekProvider] is really in a TEE, or that StrongBox behaves as advertised. That is PB-E2E-5,
 * the deferred physical-handset gate. The JVM and Robolectric tests around this class cover
 * POLICY -- which tier and which refusal -- and never hardware.
 */
class KeystoreKeyCustody(private val store: SealedStore) : KeyCustody {

    /**
     * The wake-tier data key. A push arrives with nobody there, so this unwrap must succeed on
     * a locked handset (ADR-007 B9/B16) or the sole background wake path is dead in exactly the
     * state it exists for.
     */
    override fun wakeKEK(): ByteArray = store.open(CustodyBlobs.stateKek(KeyTier.WAKE))

    /**
     * The content-tier data key.
     *
     * IT ASKS FOR NO AUTHENTICATION (ADR-007 B133). What separates it from [wakeKEK] is the
     * ALIAS and what each key seals, not a gate in front of one of them; the split's surviving
     * justification is that FCM reads push payloads, so the wake key must be readable by a path
     * Google's carrier can observe and the content key must not be derivable from it.
     *
     * The exception is still allowed to propagate exactly as thrown. gomobile flattens it into a
     * Go error carrying only the message, and [KeyCustodyException.UserAuthenticationRequired]
     * and [KeyCustodyException.KeyPermanentlyInvalidated] carry the verdict token the Go core
     * reads. Wrapping or re-messaging it here would strip that: `phonecore.openSealedDeviceKeys`
     * refuses a Resume outright for any content-tier error that is NOT one of the two sentinels,
     * so an unclassifiable refusal turns a handset into an app that cannot start.
     */
    override fun contentKEK(): ByteArray = store.open(CustodyBlobs.stateKek(KeyTier.CONTENT))
}
