package dev.swarm.phone.push

/**
 * Wave R3 scope 1's Kotlin half: the TOKEN-EVENT ROUTING decision table (ADR-015 P5,
 * push-gateway-api.md sections 3.1-3.2, PG-AUTH-5).
 *
 * Android hands this app an FCM token at exactly two moments -- `SwarmApplication.onCreate`'s
 * initial `getToken` and `SwarmMessagingService.onNewToken` -- and both funnel into one
 * `PushTokens.register`. What the gateway must be told depends only on what this installation
 * durably held BEFORE the event, and the failure modes differ in kind: a REGISTER where a
 * ROTATE belonged mints a second durable installation holding a live token for 180 days; a
 * ROTATE where a REGISTER belonged retries an unauthorized PUT forever while the phone
 * silently never receives a wake. So the decision is a named, exhaustively testable object --
 * pure policy, no Context, no network, no Firebase, no AAR type -- and the Go core
 * (phonecore's EnsurePushRegistration) owns the durable state and the wire calls; this object
 * owns only the vocabulary, so the Kotlin callers and the facade agree on WHICH verb a token
 * event is.
 */
object PushRegistrationRouting {

    /** The three verbs a token event can be at the gateway. */
    enum class Action {
        /** No durable installation: POST /v1/installations (spec 3.1). */
        REGISTER,

        /** The token changed under a live installation: one signed PUT, never a second REGISTER (PG-ROT-1). */
        ROTATE,

        /** The same token re-presented: the PG-AUTH-5 inactivity refresh -- never dropped as a no-op. */
        REFRESH,
    }

    /**
     * Decide what one token event is, from the previously durable token alone.
     *
     * A null AND an empty previous token both route as first-run: two spellings of "nothing
     * durable" that routed differently would be a fresh install rotating against an
     * installation it never registered.
     */
    fun decide(previousToken: String?, currentToken: String): Action = when {
        previousToken.isNullOrEmpty() -> Action.REGISTER
        previousToken == currentToken -> Action.REFRESH
        else -> Action.ROTATE
    }
}
