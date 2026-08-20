package dev.swarm.phone.ui.kit

import android.content.Context
import android.text.SpannableStringBuilder
import android.text.Spanned
import android.text.style.TextAppearanceSpan
import dev.swarm.phone.R

/**
 * Mirror M2.1 (Wave R6) -- the markdown subset renderer: `Markdown.parse(text)` yields blocks and
 * spans, [Markdown.plainText] flattens them into the sentence a row prints, and [markdownBody]
 * is the one place those spans become type. Fenced by android/gate/r6_chat_ui_test.go and driven
 * by MarkdownTest, MarkdownLinkHonestyTest and TranscriptChatRenderTest.
 *
 * THE SUBSET, and nothing else: paragraphs, headings (1..3), fenced code blocks, inline code,
 * bold, italic, links, and flat bullet lists. Agent prose is markdown-shaped but ADVERSARIALLY
 * SOURCED -- a tool's output quoted into an `agent_message` can contain anything a hostile repo
 * put in a README -- so everything outside the subset is TEXT, verbatim:
 *
 *  - There is NO HTML pass and NO entity decoding: `<script>` is prose, byte for byte. The
 *    injection surface an interpreter would open is the reason this is a parser and never a
 *    markup-interpreting platform type or an embedded browser view (the gate fences both by
 *    name).
 *  - **NO LINK ON THIS SURFACE IS TAPPABLE, AND THAT IS THE DECISION** (Wave R6 review round 3,
 *    finding F3). [Span.linkHref] is read by exactly one thing -- [styleRun], which spends a type
 *    role on it -- and nothing in this app's production Kotlin constructs a `URLSpan`, a
 *    `ClickableSpan`, a movement method, `linksClickable` or `autoLink`. A link is TEXT that reads
 *    as a link. Outbound navigation launched from adversarially-sourced agent output is a new
 *    attack surface, and this wave does not open it. Both rules below therefore protect WHAT THE
 *    READER SEES AND MIGHT RETYPE, not where a tap would land -- which is a smaller promise than
 *    "you cannot be click-hijacked", and it is the true one.
 *  - Link schemes are ALLOWLISTED to http/https. A `javascript:`/`data:`/`file:` construct
 *    renders as the text it is and its span carries NO href, so if a later slice ever makes a
 *    link tappable those schemes start out refused rather than needing to be remembered.
 *  - **A LINK MAY NOT LIE ABOUT WHERE IT GOES** (Wave R6 review finding B11). The allowlist
 *    constrained the target and placed no relationship between the LABEL and the href, so
 *    `[https://your-bank.example](https://evil.example)` drew a span whose visible text was a
 *    trusted URL and whose href was not -- the oldest phishing shape there is, rendered by this
 *    app, over prose the machine quoted from somewhere nobody chose. It could not be TAPPED (see
 *    above), and it could be read, believed and typed into a browser by hand, which is the whole
 *    of what a phishing URL needs. [linkAt] now replaces a URL-SHAPED label whose host differs
 *    from the target's with the REAL target. Prose labels are untouched (prose is not a claim
 *    about a destination) and a matching host is untouched (it is telling the truth). The label
 *    is NORMALIZED before that test -- see [hostOf]: while it was not, four one-character edits
 *    walked straight past the rule.
 *  - Markdown inside inline code or a fence stays LITERAL, and an unterminated fence swallows
 *    nothing: the tail renders as a code block to the end rather than disappearing into
 *    parser state.
 *  - Parse is TOTAL and PURE: same input, equal output, no shared mutable state, and no
 *    pathological blow-up -- the inline pass is linear, which it was NOT until finding B11's
 *    second half: every unmatched marker re-scanned the whole remaining string for a terminator
 *    that was not there, so `"[".repeat(n)` cost n^2/2 comparisons on a text the agent wrote and
 *    the phone did not choose, on the thread `PhoneEvents` posts its redraws to. [Scan] is the
 *    repair: a miss for one marker is REMEMBERED, because "no `]` at or after i" settles every
 *    later question about `]` too.
 *
 * WHAT THIS FILE PAINTS, and why that is here rather than in a screen. [markdownBody] applies
 * type roles, which is a KIT act (PB-DS-6/PB-DS-9): a screen that chose a face would be a screen
 * owning the design. It spends exactly two roles that already exist and mints none --
 * `Title.Row` is the app's one 600-weight sans role and `Mono.InlineStrong` is row 14's declared
 * inline identifier register, already spent by [activityRow]'s own emphasis.
 *
 * **ITALIC HAS NO REGISTER IN THIS DESIGN AND IS RENDERED AS PROSE.** Substrate declares no
 * italic face and this kit chooses no typeface (the gate forbids `Typeface.` by name), so an
 * italic span's WORDS are shown with its markers consumed and nothing distinguishes it. That is
 * a disclosed gap rather than an invention: a bold-looking italic would report a different
 * emphasis from the one the machine wrote, and inventing a face here is the PB-DS-6 violation
 * this kit exists to prevent.
 */
object Markdown {

    /** One inline span. Data classes give [parse] its purity check for free (equality). */
    data class Span(
        val text: String,
        val bold: Boolean = false,
        val italic: Boolean = false,
        val code: Boolean = false,
        val linkHref: String? = null,
    )

    fun parse(text: String): List<MarkdownBlock> {
        val blocks = mutableListOf<MarkdownBlock>()
        val lines = text.split("\n")
        var i = 0
        val paragraph = StringBuilder()

        fun flushParagraph() {
            if (paragraph.isNotEmpty()) {
                blocks.add(MarkdownBlock.Paragraph(spans(paragraph.toString())))
                paragraph.setLength(0)
            }
        }

        while (i < lines.size) {
            val line = lines[i]
            val trimmed = line.trimEnd()
            when {
                trimmed.startsWith("```") -> {
                    flushParagraph()
                    val language = trimmed.removePrefix("```").trim()
                    val body = mutableListOf<String>()
                    i++
                    // An unterminated fence runs to the end: the tail stays VISIBLE as code,
                    // never swallowed (M2.1's own control), and no state leaks past parse.
                    while (i < lines.size && !lines[i].trimEnd().startsWith("```")) {
                        body.add(lines[i])
                        i++
                    }
                    blocks.add(MarkdownBlock.CodeBlock(language, body.joinToString("\n")))
                }
                headingLevel(trimmed) > 0 -> {
                    flushParagraph()
                    val level = headingLevel(trimmed)
                    blocks.add(MarkdownBlock.Heading(level, spans(trimmed.drop(level).trim())))
                }
                trimmed.startsWith("- ") || trimmed.startsWith("* ") -> {
                    flushParagraph()
                    val items = mutableListOf<List<Span>>()
                    while (i < lines.size) {
                        val l = lines[i].trimEnd()
                        if (!(l.startsWith("- ") || l.startsWith("* "))) break
                        items.add(spans(l.drop(2).trim()))
                        i++
                    }
                    blocks.add(MarkdownBlock.Bullets(items))
                    continue // i already advanced past the list
                }
                trimmed.isBlank() -> flushParagraph()
                else -> {
                    if (paragraph.isNotEmpty()) paragraph.append('\n')
                    paragraph.append(line)
                }
            }
            i++
        }
        flushParagraph()
        // Total on ANY input: a caller handed something is handed something back, so a
        // degenerate message still renders as itself rather than as nothing.
        if (blocks.isEmpty()) blocks.add(MarkdownBlock.Paragraph(listOf(Span(text))))
        return blocks
    }

    /**
     * The prose the row PRINTS: every block except the fences, flattened, with the markers
     * consumed and the words kept.
     *
     * IT IS THE ONE FLATTENING, and that is structural rather than tidy. The screen model puts
     * this string on [TranscriptBlock.line] and the view hands the SAME string to [markdownBody]
     * to be styled; two implementations of "what does this markdown read as" would drift, and the
     * drift would land as spans applied at offsets that no longer name the words they were
     * computed for -- an emphasis on the wrong half of a sentence.
     *
     * A FENCE IS NOT IN IT. Column-aligned text inside a body-copy layout is silently re-wrapped,
     * which misreports what the machine printed; [codeText] carries it to the mono well instead.
     */
    fun plainText(blocks: List<MarkdownBlock>): String = blocks
        .mapNotNull { block ->
            when (block) {
                is MarkdownBlock.Paragraph -> block.spans.joinToString("") { it.text }
                is MarkdownBlock.Heading -> block.spans.joinToString("") { it.text }
                // The marker is re-emitted because parse consumed it and a list with no marks
                // reads as a run-on paragraph. It is the SCREEN's separator in every other
                // respect -- see joined()/lines() one file over -- and it is here because the
                // flattening has to be one function (see above).
                is MarkdownBlock.Bullets -> block.items.joinToString("\n") { item ->
                    BULLET + item.joinToString("") { it.text }
                }
                is MarkdownBlock.CodeBlock -> null
            }
        }
        .joinToString("\n")

    /** The fenced blocks, in order: what the mono well prints for this message. */
    fun codeText(blocks: List<MarkdownBlock>): String = blocks
        .filterIsInstance<MarkdownBlock.CodeBlock>()
        .joinToString("\n") { it.text }
        .trimEnd()

    /** 1..3 for `# `/`## `/`### `; 0 otherwise (a 4th `#` is prose, outside the subset). */
    private fun headingLevel(line: String): Int {
        var n = 0
        while (n < line.length && line[n] == '#') n++
        if (n in 1..3 && n < line.length && line[n] == ' ') return n
        return 0
    }

    /**
     * A scan over one string that REMEMBERS ITS MISSES.
     *
     * `indexOf(needle, from) < 0` does not merely answer this question: it settles every later
     * one, because there is no occurrence at any index at or after `from`. Recording that is what
     * makes the inline pass linear rather than quadratic in the number of unmatched markers --
     * finding B11's second half, and the reason the class KDoc can claim linearity at all.
     */
    private class Scan(val text: String) {
        private val missFrom = HashMap<String, Int>()

        fun find(needle: String, from: Int): Int {
            val known = missFrom[needle]
            if (known != null && from >= known) return -1
            val at = text.indexOf(needle, from)
            if (at < 0) missFrom[needle] = if (known == null) from else minOf(known, from)
            return at
        }
    }

    /**
     * The inline pass: ONE linear scan. Inline code wins over every other marker (markdown
     * inside backticks stays literal), then links, bold, italic. Anything unpaired falls
     * through as plain text -- the marker characters stay visible rather than vanishing.
     */
    private fun spans(text: String): List<Span> {
        val out = mutableListOf<Span>()
        val plain = StringBuilder()
        val scan = Scan(text)
        var i = 0

        fun flushPlain() {
            if (plain.isNotEmpty()) {
                out.add(Span(plain.toString()))
                plain.setLength(0)
            }
        }

        while (i < text.length) {
            when {
                text[i] == '`' -> {
                    val end = scan.find("`", i + 1)
                    if (end < 0) {
                        plain.append(text[i]); i++
                    } else {
                        flushPlain()
                        out.add(Span(text.substring(i + 1, end), code = true))
                        i = end + 1
                    }
                }
                text.startsWith("**", i) -> {
                    val end = scan.find("**", i + 2)
                    if (end < 0) {
                        plain.append(text, i, i + 2); i += 2
                    } else {
                        flushPlain()
                        out.add(Span(text.substring(i + 2, end), bold = true))
                        i = end + 2
                    }
                }
                text[i] == '*' -> {
                    val end = scan.find("*", i + 1)
                    if (end < 0) {
                        plain.append(text[i]); i++
                    } else {
                        flushPlain()
                        out.add(Span(text.substring(i + 1, end), italic = true))
                        i = end + 1
                    }
                }
                text[i] == '[' -> {
                    val link = linkAt(scan, text, i)
                    if (link == null) {
                        plain.append(text[i]); i++
                    } else {
                        flushPlain()
                        out.add(link.first)
                        i = link.second
                    }
                }
                else -> {
                    plain.append(text[i]); i++
                }
            }
        }
        flushPlain()
        if (out.isEmpty()) out.add(Span(""))
        return out
    }

    /**
     * Reads `[label](target)` at [at], or null when the construct is not there. The scheme
     * allowlist is http/https: outside it the LABEL still renders (a span with no href), so
     * the text is never silently dropped while the tap is refused.
     *
     * AND THE SPAN'S TEXT IS WHERE IT GOES (finding B11). See the class KDoc: a label that is
     * itself URL-shaped is a CLAIM about a destination, and a claim that disagrees with the href
     * is replaced by the href. Nothing is dropped -- the reader still gets a link, rendered in the
     * inline identifier register -- and nothing is invented; what changes is which of the two
     * strings the machine wrote is shown.
     *
     * IT IS NOT TAPPABLE, HERE OR ANYWHERE (round 3, finding F3). This KDoc used to say "the
     * reader still gets a TAPPABLE link", which was never true of this app: no production Kotlin
     * builds a `URLSpan`/`ClickableSpan`, sets a movement method, or enables `autoLink`. What the
     * rule protects is a URL the reader could believe and retype.
     */
    private fun linkAt(scan: Scan, text: String, at: Int): Pair<Span, Int>? {
        val closeLabel = scan.find("]", at + 1)
        if (closeLabel < 0 || closeLabel + 1 >= text.length || text[closeLabel + 1] != '(') return null
        val closeHref = scan.find(")", closeLabel + 2)
        if (closeHref < 0) return null
        val label = text.substring(at + 1, closeLabel)
        val target = text.substring(closeLabel + 2, closeHref)
        // The scheme is compared CASE-FOLDED (round 3, finding F2). RFC 3986 makes a scheme
        // case-insensitive, so `HTTPS://evil.example` IS an https target; while this compared
        // raw bytes it was neither allowlisted nor host-checked, and a URL-shaped label over it
        // was therefore shown verbatim -- the spoof B11 closed, reopened by one shift key.
        val scheme = target.trimStart().lowercase()
        val href = if (scheme.startsWith(HTTP) || scheme.startsWith(HTTPS)) target else null
        return Span(shownFor(label, target, href), linkHref = href) to closeHref + 1
    }

    /** The text a link SHOWS: the label, unless the label claims a different destination. */
    private fun shownFor(label: String, target: String, href: String?): String {
        if (href == null) return label
        val labelHost = hostOf(label) ?: return label
        val targetHost = hostOf(target) ?: return target
        return if (labelHost.equals(targetHost, ignoreCase = true)) label else target
    }

    /**
     * The host a string names, or null when the string is not URL-shaped at all.
     *
     * DELIBERATELY NARROW. Anything with a space, or with no dotted host at its head, is prose --
     * and prose must be left exactly as the machine wrote it, because rewriting it would drop the
     * words in the name of a spoof that was never being attempted.
     *
     * NARROW IS NOT THE SAME AS BLIND (Wave R6 review round 3, finding F2). [URL_SHAPED] is
     * anchored, so ANY character the eye does not see -- a trailing space, a zero-width space, a
     * bidi control, a trailing FQDN dot -- used to make a URL-shaped label answer null here, and
     * the honesty rule then showed that label verbatim. Four one-character edits defeated the
     * whole guard, each proven by an executed probe. The value is therefore NORMALIZED before it
     * is tested: the normalization decides only WHETHER a string is a URL claim, never what is
     * rendered, so an honest label still reaches the screen exactly as the machine wrote it.
     */
    private fun hostOf(value: String): String? {
        val match = URL_SHAPED.find(normalizedForHostTest(value)) ?: return null
        return match.groupValues[1]
    }

    /**
     * [value] with what an anchored test cannot see removed: every Unicode FORMAT character
     * (category Cf -- the zero-width spaces and joiners, the word joiner, a BOM, and the bidi
     * marks, embeddings, overrides and isolates, which print nothing and can reorder what
     * follows), then surrounding whitespace, then a trailing FQDN dot -- `your-bank.example.`
     * and `your-bank.example` are the same host, and the dot is the cheapest way to hide one
     * from a `[:/?#]|$` tail.
     *
     * THE CATEGORY AND NOT A LIST. A hand-written set of code points is a list of the evasions
     * somebody thought of, spelled in escapes nobody can read; the category is the property
     * itself, and it covers the ones not thought of.
     *
     * It is used for the HOST TEST ONLY. Nothing here reaches the screen.
     */
    private fun normalizedForHostTest(value: String): String =
        value.filterNot { Character.getType(it) == Character.FORMAT.toInt() }.trim().trimEnd('.')

    /**
     * `[scheme://]host[/...]`, anchored: the label has to BE a URL, not merely contain one.
     * CASE-INSENSITIVE, because `HTTPS://` is the same scheme as `https://` (RFC 3986) and while
     * it was not, one shift key hid a URL-shaped label from the honesty rule entirely.
     *
     * A QUOTED STRING AND NOT A RAW ONE. `android/gate/s23_kit_test.go`'s metric accounting
     * strips `"` and `'` literals before it counts numbers and REFUSES a raw string outright,
     * because it cannot see inside one -- so a `{2,}` written in triple quotes would be a
     * quantifier the fence reads as a design value, and every number after it would be missed.
     */
    private val URL_SHAPED =
        Regex(
            "^(?:https?://)?([A-Za-z0-9][A-Za-z0-9.\\-]*\\.[A-Za-z]{2,})(?:[:/?#]|\$)",
            RegexOption.IGNORE_CASE,
        )

    private const val HTTP = "http://"
    private const val HTTPS = "https://"

    /** The list marker [plainText] re-emits. See its own paragraph; [markdownBody] steps over it. */
    internal const val BULLET = "- "
}

/**
 * The block vocabulary [Markdown.parse] emits. Sealed and small on purpose: a renderer walks
 * exactly these four shapes and everything else is a paragraph's text.
 */
sealed class MarkdownBlock {
    data class Paragraph(val spans: List<Markdown.Span>) : MarkdownBlock()
    data class Heading(val level: Int, val spans: List<Markdown.Span>) : MarkdownBlock()
    data class CodeBlock(val language: String, val text: String) : MarkdownBlock()
    data class Bullets(val items: List<List<Markdown.Span>>) : MarkdownBlock()
}

/**
 * [line], with the type roles [blocks] asks for applied over it.
 *
 * @param line the sentence the screen decided this message reads as. It is passed IN rather than
 *  recomputed here because the screen may have added its own marks to it (§2's clipped ellipsis,
 *  IS-COMPAT-4's degraded phrase), and a second flattening would put the spans at offsets that
 *  name different words. Its prose prefix must be [Markdown.plainText] of the same blocks, which
 *  is what `TranscriptScreen` composes it from; anything past that prefix is left unstyled.
 *
 * TWO ROLES, BOTH ALREADY IN THE LADDER. Bold takes `Title.Row`, the app's one 600-weight sans
 * role; inline code and a link take `Mono.InlineStrong`, row 14's declared inline identifier
 * register -- a URL and a symbol are the same kind of thing and the design has one face for it.
 * Italic takes none; see the class KDoc for why that is a disclosure rather than an omission.
 */
fun markdownBody(context: Context, blocks: List<MarkdownBlock>, line: String): CharSequence {
    val styled = SpannableStringBuilder(line)
    var at = 0
    for (block in blocks) {
        when (block) {
            // The fence is the WELL's, not the sentence's, so it advances nothing: plainText
            // skips it in exactly the same way, which is what keeps the two walks in step.
            is MarkdownBlock.CodeBlock -> Unit
            is MarkdownBlock.Paragraph -> at = styleRun(context, styled, block.spans, at, line.length)
            is MarkdownBlock.Heading -> {
                val start = at
                at = styleRun(context, styled, block.spans, at, line.length)
                val end = minOf(at, line.length)
                if (end > start) {
                    styled.role(context, R.style.TextAppearance_Swarm_Title_Row, start, end)
                }
            }
            is MarkdownBlock.Bullets -> {
                block.items.forEachIndexed { index, item ->
                    at += Markdown.BULLET.length // the marker plainText re-emits
                    at = styleRun(context, styled, item, at, line.length)
                    if (index < block.items.size - 1) at++ // the newline between items
                }
            }
        }
        // The newline plainText joins blocks with. A fence contributed nothing to the sentence,
        // so nothing separates it from the next block either.
        if (block !is MarkdownBlock.CodeBlock) at++
    }
    return styled
}

/** Applies one run of spans starting at [at], and answers where the run ended. */
private fun styleRun(
    context: Context,
    styled: SpannableStringBuilder,
    spans: List<Markdown.Span>,
    at: Int,
    limit: Int,
): Int {
    var cursor = at
    for (span in spans) {
        val start = cursor
        val end = minOf(cursor + span.text.length, limit)
        cursor += span.text.length
        if (end <= start) continue
        when {
            span.code || span.linkHref != null ->
                styled.role(context, R.style.TextAppearance_Swarm_Mono_InlineStrong, start, end)
            span.bold -> styled.role(context, R.style.TextAppearance_Swarm_Title_Row, start, end)
            // Italic: no register in this design. See Markdown's class KDoc.
            else -> Unit
        }
    }
    return cursor
}

private fun SpannableStringBuilder.role(context: Context, style: Int, start: Int, end: Int) {
    setSpan(TextAppearanceSpan(context, style), start, end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
}
