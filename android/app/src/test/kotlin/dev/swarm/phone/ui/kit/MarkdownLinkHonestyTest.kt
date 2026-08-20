package dev.swarm.phone.ui.kit

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R6 review finding B11 -- the markdown renderer's link
 * is allowed to LIE ABOUT WHERE IT GOES, and the file's own header claims a linearity the code
 * does not have. Bead agents-tracker-hggx.7 (Mirror M2.1).
 *
 * ## The spoof
 *
 * [Markdown.parse] allowlists http/https on the TARGET (`javascript:` gets no href, which is
 * right) and places NO RELATIONSHIP between the LABEL and the href. Agent prose is
 * adversarially sourced -- an `agent_message` routinely quotes a README, a WebFetch body or a
 * tool's stdout from a repository nobody on this phone chose -- so
 *
 *     [https://your-bank.example/login](https://evil.example/login)
 *
 * rendered a span whose visible text was a trusted URL and whose href was not. That is the oldest
 * phishing shape there is, drawn by the app's own renderer, inside the one surface a user reads a
 * machine's output on.
 *
 * IT IS A READ-AND-RETYPE SPOOF, NOT A CLICK HIJACK (corrected in review round 3, finding F3).
 * No markdown link on this surface is tappable: [Markdown.Span.linkHref] is consumed only by the
 * renderer's type-role pass, and nothing in production Kotlin builds a URLSpan or a ClickableSpan,
 * sets a movement method, or enables autoLink. What the rule defends is the URL a reader believes
 * and types into a browser by hand -- a smaller claim than this file used to make, and the true
 * one.
 *
 * THE FIX IS NOT TO DROP THE LINK. A dropped construct is a message the phone silently altered,
 * and the subset renderer's whole posture is that everything outside the subset renders as the
 * text it is. What the fix owes is that WHAT IS SHOWN IS WHERE IT GOES: when the label itself
 * reads as a URL to a different host, the rendered text becomes the REAL target. A label that is
 * ordinary prose is untouched -- prose is not a claim about a destination -- and a label whose
 * host MATCHES the target is untouched too, because it is telling the truth.
 *
 * ## The second half: the header's "single linear scan"
 *
 * The class KDoc states "no pathological blow-up (every pass below is a single linear scan)".
 * The unmatched-marker paths re-scanned the whole remaining string for a terminator that is not
 * there, once per marker, which is quadratic: `"[".repeat(n)` costs n^2/2 comparisons on a text
 * the agent did not write and the phone did not choose. Either the code is linear or the claim
 * comes out; this suite pins the code, because a renderer whose cost an attacker chooses is a
 * renderer that can be made to hitch the main looper (`PhoneEvents` posts a redraw per event).
 */
class MarkdownLinkHonestyTest {

    private fun spansOf(text: String): List<Markdown.Span> =
        (Markdown.parse(text).single() as MarkdownBlock.Paragraph).spans

    private fun linkIn(text: String): Markdown.Span =
        spansOf(text).single { it.linkHref != null }

    // ---- the spoof itself ---------------------------------------------------

    @Test
    fun `a label naming another host is replaced by the real destination`() {
        val span = linkIn("[https://your-bank.example/login](https://evil.example/pay)")
        assertEquals(
            "the phone drew a span reading like a bank whose href pointed at another host. " +
                "What is shown must be where it goes",
            "https://evil.example/pay",
            span.text,
        )
        assertEquals("https://evil.example/pay", span.linkHref)
    }

    @Test
    fun `a bare-host label pointing elsewhere is replaced too`() {
        // No scheme on the label -- which is how a URL is written in prose, and how a spoof is
        // most likely to be typed.
        val span = linkIn("[your-bank.example](https://evil.example)")
        assertEquals("https://evil.example", span.text)
    }

    @Test
    fun `a label whose host matches the target is left exactly as written`() {
        val span = linkIn("[https://docs.example/guide](https://docs.example/guide)")
        assertEquals(
            "an honest link was rewritten, which punishes the ordinary case for the hostile one",
            "https://docs.example/guide",
            span.text,
        )
    }

    @Test
    fun `ordinary prose as a label is never rewritten`() {
        val span = linkIn("[the release notes](https://example.com/notes)")
        assertEquals(
            "prose is not a claim about a destination, and replacing it drops the words the " +
                "machine actually wrote",
            "the release notes",
            span.text,
        )
    }

    @Test
    fun `a refused scheme still shows the label and carries no href`() {
        // The existing rule, re-pinned here because the honesty fix must not disturb it: an
        // unallowlisted scheme renders as the text it is, with NO href.
        val spans = spansOf("[click me](javascript:alert(1))")
        assertTrue(spans.none { it.linkHref != null })
        assertEquals("click me", spans.first().text)
    }

    @Test
    fun `a URL-shaped label under a refused scheme still carries no href`() {
        // RENAMED in review round 3 (finding F3), assertions untouched. It used to be called
        // "...is still not tappable", asserting a property NO link on this surface has: nothing
        // in production Kotlin makes a markdown link tappable at all (no URLSpan, no
        // ClickableSpan, no movement method, no autoLink), so the old name described a defence
        // that was never on trial here. What this asserts, and always did, is that the span
        // carries no href.
        val spans = spansOf("[https://your-bank.example](javascript:steal())")
        assertNull(
            "a refused scheme gained an href on the way through the honesty rule",
            spans.firstOrNull { it.linkHref != null },
        )
    }

    // ---- the evasions (Wave R6 review round 3, finding F2) -------------------
    //
    // Each of the four below was an EXECUTED probe against the shipped rule: URL_SHAPED is
    // anchored and requires the host be followed by [:/?#] or end-of-input, so one character the
    // eye cannot see made hostOf(label) answer null and the label was shown VERBATIM -- the
    // spoof B11 closed, reopened four ways. The rule now normalizes the label before testing it.
    // What is at stake is a URL the reader believes and retypes; no link here is tappable (F3).

    @Test
    fun `a trailing space does not hide a lying label`() {
        val span = linkIn("[https://your-bank.example ](https://evil.example/pay)")
        assertEquals(
            "one trailing space walked the label past the honesty rule and the phone drew a " +
                "bank's URL over another host's destination",
            "https://evil.example/pay",
            span.text,
        )
    }

    @Test
    fun `a trailing dot does not hide a lying label`() {
        val span = linkIn("[https://your-bank.example.](https://evil.example/pay)")
        assertEquals(
            "a trailing FQDN dot -- the same host, spelled the way DNS allows -- walked the " +
                "label past the honesty rule",
            "https://evil.example/pay",
            span.text,
        )
    }

    @Test
    fun `a zero-width space does not hide a lying label`() {
        val trailing = linkIn("[https://your-bank.example\u200B](https://evil.example/pay)")
        assertEquals(
            "a zero-width space walked the label past the honesty rule: the reader sees a bank",
            "https://evil.example/pay",
            trailing.text,
        )
        val interior = linkIn("[https://your-bank\u200B.example](https://evil.example/pay)")
        assertEquals(
            "a zero-width space INSIDE the host is the same evasion one character further in",
            "https://evil.example/pay",
            interior.text,
        )
    }

    @Test
    fun `an uppercase scheme does not hide a lying label`() {
        val onTheLabel = linkIn("[HTTPS://your-bank.example](https://evil.example/pay)")
        assertEquals(
            "a shift key walked the label past the honesty rule: schemes are case-insensitive " +
                "(RFC 3986) and the guard was not",
            "https://evil.example/pay",
            onTheLabel.text,
        )
        // The same fold on the TARGET: an uppercase scheme there used to fail the allowlist, so
        // the span got no href AND no host test, and the lying label was drawn untouched.
        val onTheTarget = linkIn("[https://your-bank.example](HTTPS://evil.example/pay)")
        assertEquals("HTTPS://evil.example/pay", onTheTarget.text)
        assertEquals("HTTPS://evil.example/pay", onTheTarget.linkHref)
    }

    @Test
    fun `the same decorations on an HONEST label change nothing`() {
        // THE ANTI-VACUITY CONTROL for the four above: normalization must decide only WHETHER a
        // label is a URL claim, never rewrite one that is telling the truth. A rule that replaced
        // every decorated label would pass all four evasion tests and be wrong.
        val honest = linkIn("[HTTPS://docs.example. ](https://docs.example/guide)")
        assertEquals(
            "an honest label was rewritten because it had a trailing dot and a capital scheme; " +
                "the words the machine wrote are gone for a spoof nobody attempted",
            "HTTPS://docs.example. ",
            honest.text,
        )
        val prose = linkIn("[the release notes\u200B](https://example.com/notes)")
        assertEquals("the release notes\u200B", prose.text)
    }

    // ---- the linearity the header claims ------------------------------------

    @Test
    fun `an adversarial run of unmatched markers parses without quadratic blow-up`() {
        // 40k of one unmatched marker each. At n^2/2 comparisons per marker this is ~3.2e9
        // character comparisons; linear it is 40k. The assertion is the WALL CLOCK, because the
        // defect is a cost and not a wrong answer -- and the bound is loose enough that a slow
        // CI box cannot fail it while a quadratic scan cannot pass it.
        val n = 40_000
        val started = System.nanoTime()
        for (marker in listOf("[", "`", "*", "**")) {
            val blocks = Markdown.parse(marker.repeat(n))
            assertTrue(
                "an unmatched marker run vanished instead of rendering as the text it is",
                blocks.isNotEmpty(),
            )
        }
        val elapsedMs = (System.nanoTime() - started) / 1_000_000
        assertTrue(
            "parsing four unmatched-marker runs took ${elapsedMs}ms. The class KDoc claims " +
                "every pass is a single linear scan; a quadratic re-scan per marker is a cost " +
                "the agent's output chooses, on the thread PhoneEvents redraws from",
            elapsedMs < 2_000,
        )
    }

    @Test
    fun `the unmatched markers still render as their own text`() {
        // The linearity fix must not turn a miss into a drop: the marker characters stay VISIBLE.
        assertEquals("[[[", (Markdown.parse("[[[").single() as MarkdownBlock.Paragraph)
            .spans.joinToString("") { it.text })
        assertEquals("a*b", (Markdown.parse("a*b").single() as MarkdownBlock.Paragraph)
            .spans.joinToString("") { it.text })
    }
}
