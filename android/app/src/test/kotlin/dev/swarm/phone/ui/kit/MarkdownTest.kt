package dev.swarm.phone.ui.kit

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Mirror M2.1 -- the markdown subset renderer as a PURE model:
 * `Markdown.parse(text): List<MarkdownBlock>`, no Context, no View, no interpretation of markup
 * beyond the subset it names (bead agents-tracker-hggx.7; compile-RED is the r5 gradle-lane
 * convention -- every unresolved reference below is a frozen contract symbol).
 *
 * THE SUBSET, and nothing else: paragraphs, headings (1..3), fenced code blocks, inline code,
 * bold, italic, links, and flat bullet lists. Agent prose is markdown-shaped but ADVERSARIALLY
 * SOURCED -- a tool's output quoted into an agent_message can contain anything a hostile repo
 * put in a README -- so everything outside the subset is TEXT, verbatim. There is no HTML pass,
 * no entity decoding, and no scheme the renderer follows blindly: injection controls are the
 * point of the suite, not an appendix (M2.1: "escaping-safe").
 *
 * THE MODEL IS PURE so this suite runs on the JVM with no Robolectric: same input, equal output,
 * no shared mutable state, total on pathological input.
 */
class MarkdownTest {

    // ---- the subset renders -------------------------------------------------

    @Test
    fun plainProseIsOneParagraphVerbatim() {
        val blocks = Markdown.parse("The tests pass on the first run.")
        assertEquals(1, blocks.size)
        val p = blocks[0] as MarkdownBlock.Paragraph
        assertEquals("The tests pass on the first run.", p.spans.joinToString("") { it.text })
    }

    @Test
    fun boldItalicAndInlineCodeBecomeSpansNotText() {
        val p = Markdown.parse("run **all** the `go test` checks *now*")[0] as MarkdownBlock.Paragraph
        assertTrue("a bold span exists", p.spans.any { it.bold && it.text == "all" })
        assertTrue("a code span exists", p.spans.any { it.code && it.text == "go test" })
        assertTrue("an italic span exists", p.spans.any { it.italic && it.text == "now" })
        assertFalse("no span keeps the ** markers", p.spans.any { it.text.contains("**") })
    }

    @Test
    fun headingsCarryTheirLevel() {
        val blocks = Markdown.parse("# Plan\n\nbody\n\n### Step three")
        val h1 = blocks[0] as MarkdownBlock.Heading
        assertEquals(1, h1.level)
        assertEquals("Plan", h1.spans.joinToString("") { it.text })
        val h3 = blocks[2] as MarkdownBlock.Heading
        assertEquals(3, h3.level)
    }

    @Test
    fun fencedCodeIsOneBlockWithItsLanguage() {
        val blocks = Markdown.parse("```go\nfunc main() {}\n```")
        val code = blocks[0] as MarkdownBlock.CodeBlock
        assertEquals("go", code.language)
        assertEquals("func main() {}", code.text)
    }

    @Test
    fun bulletListsKeepTheirItems() {
        val blocks = Markdown.parse("- first\n- second\n- third")
        val list = blocks[0] as MarkdownBlock.Bullets
        assertEquals(3, list.items.size)
        assertEquals("second", list.items[1].joinToString("") { it.text })
    }

    @Test
    fun anHttpsLinkCarriesItsHref() {
        val p = Markdown.parse("see [the spec](https://example.com/spec)")[0] as MarkdownBlock.Paragraph
        val link = p.spans.first { it.linkHref != null }
        assertEquals("the spec", link.text)
        assertEquals("https://example.com/spec", link.linkHref)
    }

    // ---- injection controls -------------------------------------------------

    @Test
    fun htmlIsTextNeverMarkup() {
        val p = Markdown.parse("<script>alert(1)</script>")[0] as MarkdownBlock.Paragraph
        assertEquals(
            "the renderer must not interpret, strip or entity-decode HTML; it is prose, verbatim",
            "<script>alert(1)</script>",
            p.spans.joinToString("") { it.text },
        )
    }

    @Test
    fun aScriptSchemeLinkYieldsNoHref() {
        val p = Markdown.parse("[tap me](javascript:alert(1))")[0] as MarkdownBlock.Paragraph
        // The scheme allowlist is http/https. Outside it, the construct renders as the text it
        // is -- NO span carries a tappable href, because a followed javascript:/data:/file: link
        // is the one thing worse than a broken one.
        assertNull(p.spans.firstOrNull { it.linkHref != null }?.linkHref)
        assertTrue(
            "the link text is not silently dropped",
            p.spans.joinToString("") { it.text }.contains("tap me"),
        )
    }

    @Test
    fun aDataSchemeLinkYieldsNoHrefEither() {
        val p = Markdown.parse("[x](data:text/html;base64,PHNjcmlwdD4=)")[0] as MarkdownBlock.Paragraph
        assertNull(p.spans.firstOrNull { it.linkHref != null }?.linkHref)
    }

    @Test
    fun markdownInsideInlineCodeStaysLiteral() {
        val p = Markdown.parse("run `**not bold** [not a link](https://x)` now")[0] as MarkdownBlock.Paragraph
        val code = p.spans.first { it.code }
        assertEquals("**not bold** [not a link](https://x)", code.text)
        assertFalse(code.bold)
        assertNull(code.linkHref)
    }

    @Test
    fun markdownInsideAFenceStaysLiteral() {
        val code = Markdown.parse("```\n# not a heading\n**not bold**\n```")[0] as MarkdownBlock.CodeBlock
        assertEquals("# not a heading\n**not bold**", code.text)
    }

    @Test
    fun anUnterminatedFenceSwallowsNothingSilently() {
        // The tail of the message must still be VISIBLE -- as a code block to the end, not
        // dropped, and above all not held in parser state that leaks into the next parse.
        val blocks = Markdown.parse("before\n```\nnever closed")
        assertTrue(blocks.isNotEmpty())
        val rendered = blocks.joinToString("") { b ->
            when (b) {
                is MarkdownBlock.Paragraph -> b.spans.joinToString("") { it.text }
                is MarkdownBlock.CodeBlock -> b.text
                is MarkdownBlock.Heading -> b.spans.joinToString("") { it.text }
                is MarkdownBlock.Bullets -> b.items.joinToString("") { it.joinToString("") { s -> s.text } }
            }
        }
        assertTrue("the unterminated tail is rendered somewhere", rendered.contains("never closed"))
    }

    @Test
    fun pathologicalInputIsTotalNotFatal() {
        // Adversarial shapes: unclosed emphasis at depth, marker floods, and a long single
        // line. The contract is totality -- parse returns, whatever blocks it chooses.
        val floods = listOf(
            "*".repeat(10_000),
            "[".repeat(5_000) + "]".repeat(5_000),
            "`".repeat(9_999),
            "a".repeat(100_000),
        )
        for (input in floods) {
            assertTrue(Markdown.parse(input).isNotEmpty())
        }
    }

    @Test
    fun parseIsPure() {
        val input = "# h\n\n`code` **b** [l](https://x)\n\n```k\nfence\n```"
        assertEquals(
            "same input, equal output -- no shared mutable state between parses",
            Markdown.parse(input),
            Markdown.parse(input),
        )
    }
}
