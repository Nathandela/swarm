package dev.swarm.phone.ui

import java.lang.reflect.Modifier
import org.junit.Assert.assertFalse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-9ds -- **opening a session with no frame yet
 * crashes the app.**
 *
 * THE DEFECT. `mobile/app.go:815` answers `App.Peek` with `classed(ErrClassNotFound, ...)` when the
 * router holds no snapshot for a session. [FacadeBridge.terminalPeek] did not catch it,
 * `PhoneSurface.renderReady` does not catch it, and `PhoneEvents` dispatches redraws through
 * `main.post { }` -- so the refusal arrived as an UNCAUGHT exception on the main looper, which is an
 * app crash. "No frame has arrived yet" is the NORMAL state, not an edge case: every session is in
 * it for the whole round trip after `terminalWatch`, and `SessionDetailPanel.hasSnapshot` exists
 * precisely to draw it. The screen was designed for the state that killed it first.
 *
 * WHAT THIS FILE CAN AND CANNOT SEE, said plainly because a fence that overclaims is worse than one
 * that states its limit. [FacadeBridge] takes a `swarmmobile.App`, which is a gomobile class backed
 * by .so files cross-compiled for Android ABIs, so it CANNOT BE CONSTRUCTED on the unit-test JVM
 * and no assertion here calls `terminalPeek`. What is asserted is the seam where the error class is
 * INTERPRETED -- the decision that turns a refusal into "no frame yet" or leaves it to propagate --
 * which is pure Kotlin and is the part that can be got wrong in a way review would miss. That the
 * `try/catch` is placed around the right call, and that it rethrows what this predicate refuses, is
 * reviewed rather than tested. Recorded here rather than left to be assumed.
 *
 * THE SECOND TEST IS THE ONE THAT MATTERS. Catching is easy; catching too much is the defect. A
 * bare `catch (e: Exception)` here would render a revoked device, a repair-required custody state,
 * a rate limit and a transport refusal all as an empty terminal -- a screen that silently says
 * "nothing has printed yet" about a machine that has disowned this phone, which is worse than the
 * crash it replaced. So every OTHER class in the taxonomy is required to propagate, and the list is
 * read by REFLECTION rather than typed out: a token added to [SwarmErrorTokens] tomorrow is refused
 * by default, which is the direction this project's ledgers already fail in.
 */
class FacadeBridgeTest {

    /**
     * Every class the Go side can hand back, read off [SwarmErrorTokens] itself.
     *
     * The tokens are `const val String`s, so they compile to static final String fields and Java
     * reflection sees them without kotlin-reflect on the test classpath.
     */
    private fun everyErrorClass(): List<String> = SwarmErrorTokens::class.java.declaredFields
        .filter { Modifier.isStatic(it.modifiers) && it.type == String::class.java }
        .onEach { it.isAccessible = true }
        .mapNotNull { it.get(null) as? String }

    @Test
    fun `the scan can see the taxonomy it is asking about`() {
        // Defect class (i): the two assertions below are "nothing was found" in shape, and a
        // reflection walk that returned nothing would satisfy the second one vacuously while
        // proving that no class propagates.
        val classes = everyErrorClass()
        assertTrue(
            "the reflection walk found ${classes.size} error class(es) on SwarmErrorTokens, so " +
                "the propagation assertion below is about almost nothing",
            classes.size >= 15,
        )
        assertTrue(
            "the walk did not find the one class the fix turns on",
            classes.contains(SwarmErrorTokens.NOT_FOUND),
        )
    }

    @Test
    fun `a not-found refusal is the machine saying no frame has arrived yet`() {
        assertTrue(
            "App.Peek's not-found refusal is not recognised as the benign 'no snapshot yet' " +
                "state, so it reaches PhoneEvents' main.post and crashes the app -- on the " +
                "ordinary path of opening a session that has not printed",
            FacadeBridge.isAwaitingFirstFrame(SwarmErrorTokens.NOT_FOUND),
        )
    }

    @Test
    fun `no other refusal is quietly rendered as an empty terminal`() {
        for (errorClass in everyErrorClass().filter { it != SwarmErrorTokens.NOT_FOUND }) {
            assertFalse(
                "`$errorClass` is swallowed and drawn as a terminal that has printed nothing. " +
                    "That is a real failure presented as a quiet session: a revoked device, a " +
                    "custody state needing repair or a refused transport would each render as an " +
                    "empty grid with no remedy on screen, which is worse than the crash this fix " +
                    "replaced. Only not-found means 'no frame yet'",
                FacadeBridge.isAwaitingFirstFrame(errorClass),
            )
        }
    }

    @Test
    fun `a refusal the machine cannot classify is not the benign one`() {
        assertFalse(
            "an unclassifiable refusal reads as 'no frame yet'. `App.ErrorClass` fails through " +
                "`a.ready()` like every other verb, so the case is reachable -- and a failure " +
                "that could not even be named is the last one to render as a quiet terminal",
            FacadeBridge.isAwaitingFirstFrame(""),
        )
    }

    @Test
    fun `a render boundary keeps the original stamped class when live classification fails`() {
        val routed = routeFacadeErrorSafely(
            message = "${SwarmErrorTokens.OFFLINE}: swarmmobile: no relay connection",
            classify = { throw IllegalStateException("${SwarmErrorTokens.APP_CLOSED}: core closed") },
        )

        assertEquals(
            "the secondary App.ErrorClass refusal must not replace or escape the original stamped " +
                "failure that the screen was already trying to render",
            ErrorState.OFFLINE,
            routed.state,
        )
        assertEquals(Remedy.WAIT_FOR_CONNECTION, routed.remedy)
    }

    @Test
    fun `a successful live classification remains authoritative`() {
        val routed = routeFacadeErrorSafely(
            message = "platform wrapper without a token",
            classify = { SwarmErrorTokens.REVOKED },
        )

        assertEquals(ErrorState.REVOKED, routed.state)
    }

    // `the answer carries no grid, and claims nothing about one` is DELETED rather than amended.
    // It asserted `FacadeBridge.noFrameYet`, the fallback `App.Peek`'s not-found refusal produced;
    // `docs/adr/ADR-009-structured-chat-interaction.md` (2)/(3) deletes the terminal well and the
    // `terminal_watch` this app issued to fill it, so there is no grid fallback left to have an
    // opinion about. `isAwaitingFirstFrame` survives the same deletion -- it now guards
    // [FacadeBridge.pendingApproval] instead, per that method's own KDoc -- and the four tests
    // above are its coverage, unchanged.
}
