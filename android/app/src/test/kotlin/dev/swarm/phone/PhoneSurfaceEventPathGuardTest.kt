package dev.swarm.phone

import java.io.File
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-3nx6 -- **`drawActivity` and `inboxScreen`
 * call the facade unguarded on a path reachable from `PhoneEvents.onEvent`'s `main.post`.**
 *
 * WHAT THIS FILE CAN AND CANNOT SEE, said before the assertions rather than after -- the same
 * limit `FacadeBridgeTest`'s own class KDoc states. `bridge.journal(...)` and
 * `bridge.triageInbox()` reach `swarmmobile.App`, a gomobile class whose every method is
 * `native`; ITS OWN CONSTRUCTOR IS NATIVE TOO (`__NewApp`), so there is no way to build one on
 * this JVM to make throw on demand -- not even to construct a broken one. That is exactly why
 * `PhoneRuntime.phone()` answers [PhoneStartup.Unavailable] on every run here
 * (`PhoneSurfaceNavigationTest`'s own KDoc records the same fact), and it is why
 * `FacadeBridgeTest` states, of the structurally identical guard in
 * `FacadeBridge.terminalPeek`, that "the try/catch is placed around the right call ... is
 * reviewed rather than tested". This suite draws the line in the same place: it does not run
 * `drawActivity` or `inboxScreen`, because it cannot construct anything for them to catch.
 *
 * WHAT IS ASSERTED INSTEAD is the SOURCE fact android/gate's own suites assert about this exact
 * module in Go -- a call is wrapped in a `try { ... } catch (...) { ... }` -- expressed here in
 * Kotlin because the orchestrating task keeps this fix's tests beside the file it changes rather
 * than in `android/gate`. It is a narrower claim than a behavioural test would make (it cannot
 * see that the catch reports the right message, only that the call cannot reach `PhoneEvents`'
 * uncaught `main.post` unhandled), and it is exactly the claim this JVM is able to make.
 *
 * THE DEFECT, for the record `FacadeBridge.terminalPeek`'s own agents-tracker-9ds fix already
 * demonstrates once: a facade refusal that propagates out of a redraw crosses `PhoneEvents.
 * onEvent`'s `main.post { sink?.invoke() }` with nothing above it, which is an UNCAUGHT exception
 * on the main looper -- an app crash, on the ordinary path of a machine that refuses a request
 * (offline, revoked, rate-limited) while the user is looking at the Activity tab or the Inbox.
 */
class PhoneSurfaceEventPathGuardTest {

    private fun source(): String {
        val direct = File("android/app/src/main/kotlin/dev/swarm/phone/PhoneSurface.kt")
        val file = if (direct.isFile) direct else File("src/main/kotlin/dev/swarm/phone/PhoneSurface.kt")
        require(file.isFile) {
            "cannot find PhoneSurface.kt from ${File(".").absolutePath} under either a repo-root " +
                "or a module-root working directory"
        }
        return file.readText()
    }

    /**
     * The member's own text, up to (but not including) the next member at the same indent --
     * the next KDoc block or `fun` declaration four spaces in. Bounded rather than brace-matched
     * because [inboxScreen] is an EXPRESSION body (`= try { ... } catch { ... }`) and
     * [drawActivity] is a BLOCK body (`{ ... }`); a single boundary rule that does not care which
     * shape a member takes is simpler than two brace-matchers, one per shape.
     */
    private fun memberText(source: String, signature: String): String {
        val start = source.indexOf(signature)
        require(start >= 0) { "PhoneSurface.kt no longer declares `$signature`" }
        val bodyStart = start + signature.length
        val next = NEXT_MEMBER.find(source, bodyStart)?.range?.first ?: source.length
        return source.substring(bodyStart, next)
    }

    /**
     * Whether [call] sits inside a `try { ... } catch ( ... )` in [member], scanned over CODE
     * ONLY ([codeOnly] strips comments and string literals first) and WHITESPACE-INSENSITIVE so
     * this cannot be defeated by reformatting alone. It is not brace-matched: `try {` before the
     * call and `catch (` after it, in that order, is what "the call is guarded" means for a
     * member with exactly one try/catch, which is what the fix adds to each of these two.
     *
     * THE STRIP IS WHAT MAKES THE ORDER CHECK MEAN ANYTHING. The fix's own KDoc names the
     * guarded call in backticks one line above the try, and a log string could name it inside
     * one -- either would satisfy a raw-text `try{` -> call -> `catch(` scan with the guard
     * deleted. The three controls below hold that door shut.
     */
    private fun guarded(member: String, call: String): Boolean {
        val text = codeOnly(member).filterNot { it.isWhitespace() }
        val tryAt = text.indexOf("try{")
        if (tryAt < 0) return false
        val callAt = text.indexOf(call.filterNot { it.isWhitespace() }, tryAt)
        if (callAt < 0) return false
        val catchAt = text.indexOf("catch(", callAt)
        return catchAt > callAt
    }

    /**
     * [text] with line comments, block comments, and string literals removed -- the minimal
     * state machine android/gate's `kotlinCodeOnly` exists for, ported here because this suite
     * scans the same file for the same kind of source fact and was defeatable the same way.
     * Triple-quoted strings are consumed before double-quoted ones; escapes inside ordinary
     * strings are honoured so an escaped quote cannot end one early.
     */
    private fun codeOnly(text: String): String {
        val out = StringBuilder(text.length)
        var i = 0
        while (i < text.length) {
            when {
                text.startsWith("//", i) -> {
                    while (i < text.length && text[i] != '\n') i++
                }
                text.startsWith("/*", i) -> {
                    i += 2
                    while (i < text.length && !text.startsWith("*/", i)) i++
                    i = minOf(i + 2, text.length)
                }
                text.startsWith("\"\"\"", i) -> {
                    i += 3
                    while (i < text.length && !text.startsWith("\"\"\"", i)) i++
                    i = minOf(i + 3, text.length)
                }
                text[i] == '"' -> {
                    i++
                    while (i < text.length && text[i] != '"') i += if (text[i] == '\\') 2 else 1
                    i = minOf(i + 1, text.length)
                }
                else -> {
                    out.append(text[i])
                    i++
                }
            }
        }
        return out.toString()
    }

    @Test
    fun `drawActivity wraps bridge journal in a try so a refusal cannot reach PhoneEvents' uncaught main post`() {
        val member = memberText(source(), "private fun drawActivity(bridge: FacadeBridge?)")
        assertTrue(
            "drawActivity calls bridge.journal(...) (via `it.journal(...)`) with no try/catch " +
                "around it. A facade refusal there -- the machine offline, revoked, rate-limited " +
                "-- propagates out of drawActivity, out of render(), and into PhoneEvents.onEvent's " +
                "`main.post { sink?.invoke() }` uncaught: an app crash, on the ordinary path of a " +
                "machine that refuses a request while the user is on the Activity tab. Guard it the " +
                "way FacadeBridge.terminalPeek already guards App.peek (agents-tracker-9ds)",
            guarded(member, ".journal("),
        )
    }

    @Test
    fun `inboxScreen wraps bridge triageInbox in a try so a refusal cannot reach PhoneEvents' uncaught main post`() {
        val member = memberText(source(), "private fun inboxScreen(bridge: FacadeBridge): InboxScreen")
        assertTrue(
            "inboxScreen calls bridge.triageInbox() with no try/catch around it. It is called " +
                "unconditionally from renderReady, so a facade refusal there propagates out of " +
                "render() and into PhoneEvents.onEvent's `main.post { sink?.invoke() }` uncaught " +
                "-- an app crash reachable on every redraw while the phone is paired, not only " +
                "while a session is open. Guard it the way FacadeBridge.terminalPeek already " +
                "guards App.peek (agents-tracker-9ds)",
            guarded(member, ".triageInbox("),
        )
    }

    // ---- the scan's own controls ----------------------------------------------
    //
    // FAILING-FIRST for the audit committee's S8: this scan read RAW source, so a COMMENT could
    // satisfy `try {` -> call -> `catch (` with the guard deleted. drawActivity's real body
    // already quotes `it.journal(...)` in prose one line above its try -- one comment reflow away
    // from defeating the fence. The Go gates learned this exact lesson once
    // (android/gate/helpers_test.go built kotlinCodeOnly after a fence was defeated by its own
    // documentation); this suite scans the same file for the same kind of fact, so it must strip
    // the same things. These controls run the real [guarded] over perturbed fixtures, the
    // o6ut_pendingsync_test.go pattern.

    @Test
    fun `a comment inside an unrelated try cannot stand in for the guard`() {
        val member = """
        val lease = try {
            // the peek used to call `it.journal(cursor)` here before the split
            bridge.lease()
        } catch (refused: Exception) {
            null
        }
        val page = bridge.journal(cursor)
        """
        assertFalse(
            "the scan accepted a member whose only guarded thing is a COMMENT mentioning the " +
                "call: the real call sits after the catch, unguarded, and one comment reflow " +
                "in production would hide a deleted guard from this fence",
            guarded(member, ".journal("),
        )
    }

    @Test
    fun `a string literal naming the call cannot stand in for the guard`() {
        val member = """
        val note = try {
            log("about to run .journal( for the activity tab")
        } catch (ignored: Exception) {
        }
        val page = bridge.journal(cursor)
        """
        assertFalse(
            "the scan accepted a member whose try guards only a LOG STRING naming the call; " +
                "the real call sits after the catch, unguarded",
            guarded(member, ".journal("),
        )
    }

    @Test
    fun `the scan still accepts the genuinely guarded shape`() {
        val member = """
        // A refusal here is routed, never thrown (agents-tracker-3nx6); the guarded call is
        // `it.journal(...)`, quoted here exactly the way the production KDoc quotes it.
        val page = try {
            bridge.journal(cursor)
        } catch (refused: Exception) {
            routed(refused)
        }
        """
        assertTrue(
            "the scan rejected a member that guards the call correctly while a comment above " +
                "quotes it -- a fence nobody can satisfy",
            guarded(member, ".journal("),
        )
    }

    private companion object {
        val NEXT_MEMBER = Regex("\\n {4}(/\\*\\*|(private |internal )?fun )")
    }
}
