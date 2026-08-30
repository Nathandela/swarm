# ADR-014: Paged interaction history — the transcript's past is read on demand, by item identity, with an honest floor

**Status**: Accepted (Wave R6, Mirror M3.1/M3.3; bead agents-tracker-hggx.7), **amended by the
R6 review fix-pack (A1-A5, 2026-08-19), by review round 2 (A6-A9, 2026-08-20) and by review
round 3 (A10, 2026-08-20), and by the bounded anchorless repair amendment (A11,
2026-08-30)** — see the "Amended by" sections below. Two of this ADR's original sentences described code that
did not exist, one ratified a defect, two rules it should have written were missing, four gaps
only became visible once the phone half was wired and then exercised, and one rule it states
correctly turned out to be held by no test on the path that serves it (A10); the amendments are
stated in place rather than by rewriting the sentences away.
**Date**: 2026-08-19
**Program**: [docs/specifications/mirror-program.md](../specifications/mirror-program.md) rows M3.1–M3.3; [docs/specifications/remote-control-product-playbook.md](../specifications/remote-control-product-playbook.md) "Wave R6".
**Companions**: [ADR-009-structured-chat-interaction.md](ADR-009-structured-chat-interaction.md) (the phone is a transcript), [docs/specifications/interaction-schema.md](../specifications/interaction-schema.md) (IS-CAP-2/-3, IS-ENV-2), [ADR-007-remote-access.md](ADR-007-remote-access.md) (the signed-op plane these reads deliberately stay OFF).
**Ratifies**: the op shapes frozen failing-first in `internal/protocol/r6_historydetail_test.go` and `internal/skeleton/r6_historydetail_test.go` (docs/verification/r6-red/chat-red.txt). ~~This ADR ratifies those shapes verbatim; nothing is amended.~~ — **struck by review round 2**: the sentence was written about the WIRE SHAPES, which are indeed unchanged, but it sat directly above a status line announcing amendments and read as a claim about the whole document. What is unamended is the two ops' shapes; what has been amended ~~nine~~ ~~**ten**~~ **eleven** times is this ADR's reasoning about them. A10 corrected the earlier stale count; A11 adds the anchorless and byte-bound semantics without changing the body fields.

## Context

The phone's transcript is a bounded fold: `phonecore.MaxItemsPerSession` caps what the handset
retains, the machine's journal rotates under its own retention bound, and the reseed channel
(PB-SYNC-2/-3) re-delivers *forward* from a cursor — it has no vocabulary for "the page before
what I hold". So a week-old session opened cold showed whatever the last reseed happened to
carry, and "load earlier" had nothing to ask. Mirror M3.1 books the missing read; M3.3 books its
sibling for one item's *depth* (the full pre-truncation body IS-CAP-2 promised was retrievable).

## Decision

### 1. Two UNSIGNED read ops, on the `terminal_watch` precedent — no new signed action

`interaction_history` (`Control.interaction_history`: `session`, `before_item`, `limit`) and
`interaction_detail` (`Control.interaction_detail`: `session`, `item_id`) are READS: they are
gateway-routed, never forwarded to the device authenticator, require no device fields, and add
**no entry** to `actionClass` — PB-SYNC-5's switch stays closed, for exactly the reasons its own
doc comment records for `journal_resync` (a signed read would demand a capability class that
makes reading require the control tier, and capability is pinned at enrollment, never wire-read).
Sealing under the epoch content key is already proof the asker is the paired device; daemon-side
gating (capability, kill switch) lives ~~behind the seams, not in the handler~~ **in the handler**
— see amendments A1 and A2.

### 2. Paging is by ITEM IDENTITY (or the empty newest-page sentinel), ascending, with an honest floor

Non-empty `before_item` names an `item_id` — never a cursor, never a position (IS-ENV-2: identity is the
item_id; a daemon restart's reconciliation legitimately re-delivers the same items at new
cursors). The reply is the window immediately preceding that item's FIRST record: every record
the named session's, every one strictly older than the boundary, ascending by cursor, normally
at most `limit` — **and closed over every ITEM IDENTITY it contains, never carrying a delta tail
whose head precedes the page (amendment A3, fenced on the
SERVED path only by amendment A10)**. The reply
rides the EXISTING `Control.journal` carrier — history is journal records, and
a second carrier would be a second folding path for the same bytes — plus one additive flag,
`history_floor`: true when nothing older than the returned page is retained, so the phone renders
a retention floor instead of offering "load earlier" forever. Refusals are coded: an unknown
`before_item` and a non-positive `limit` are `invalid_field` (a caller bug surfaced, not
defaulted). Empty `before_item` is the explicit anchorless spelling: its boundary is immediately
after the retained transcript, so the same operation returns the newest retained page. It is not
an unknown id and does not weaken item-identity paging once the first page supplies an anchor.

The limit is a record-count ceiling with one indivisibility exception: when no suffix within it
is closed over item identity, the minimal closed suffix ships over the count rather than a
headless tail (usually one multi-record item; for A(head), B, A(delta), the whole interleaving).
It is not a sufficient frame bound. The serialized `journal` slice is also capped at 512 KiB
and trimmed from the old edge only at whole-item boundaries. This leaves
headroom for `Control`, command-reply JSON, AEAD overhead, and the relay append's base64 expansion
under its 1 MiB `MaxFrame`. If one whole item cannot fit, the daemon refuses `unavailable`; it
never returns a headless delta tail and never attempts an oversized relay append.

The daemon serves the page from its own journal (`JournalReadFrom(0)`, filtered to the session's
`interaction` records **and its `structured_gap` boundaries — amendment A4**). That is a scan, not an index — adequate at the journal's retention bound,
and the "daemon session-item index" M3.1 names becomes worth building only when the scan is
measured to hurt; the op shape does not change either way, which is why the shape is ratified now.

### 3. Detail is retained AT CAPTURE TIME, byte-bounded, and refuses `unavailable` — never a partial body

`fitItem` already serializes every item once untruncated (that is how `full_bytes` is honest);
when truncation fires, that serialization IS the detail body, so retention costs no second
marshal. The daemon keeps one store for the machine (M3.3's 64 MiB bound,
`skeleton.maxDetailBytes`), evicting oldest-first, entries retired with their session. A
truncated item ships `detail: true` — IS-CAP-2's "the full body is retrievable" made true by
construction, because the same code path that sets the flag stores the body. `interaction_detail`
answers exactly ONE journal record whose `item` is that body verbatim; outside retention — never
captured, evicted, or another session's id — it answers the sealed `unavailable` with **zero**
records (IS-CAP-3: a partial body beside a refusal is the ambiguity the code exists to forbid).

### 4. Deep-links resolve by `(session, item_id)`, or miss honestly

A notification minted before a daemon restart holds cursors reconciliation has since moved; only
its item_id is stable. `phonecore.ItemStore.Resolve(session, item_id)` answers the item at its
CURRENT fold — and answers `ok=false` for an unretained or cross-session id. The screen renders
the miss as a NAMED state (`DeepLinkLanding.NotRetained`), never a silent landing somewhere else.

## Amended by the R6 review fix-pack (2026-08-19)

Three independent adversarial reviewers read this ADR against the code it ratifies. **Three of
its own claims did not survive** (A1, A2, A3); A4 and A5 are not corrections of sentences this
ADR wrote but rules it should have written and a gap that only became visible once the phone
half was wired. That is five amendments over three failed claims, and the count is spelled out
here because the previous wording ("Three of its claims did not survive") sat above five
headings and read as an arithmetic error. They are amended HERE, in place, rather than by
editing the original sentences out — an ADR whose wrong sentences are deleted teaches nothing.

### A1 — "daemon-side gating lives behind the seams" was false, and the reads leaked (finding B2)

Section 1 asserted that capability and kill-switch gating lived behind the seams. Both
handlers called their seams directly and honored NEITHER gate. Probed: with the kill switch
OFF, `journal_read` — the very precedent this ADR cites — refused, while `interaction_history`
served `{"item_id":"01JA","kind":"user_message","text":"SECRET PROMPT"}` and
`interaction_detail` served the full `output_excerpt`. With NO capability negotiated,
`journal_read` refused and `interaction_history` still served.

The gap was not exploitable at the time only because no gateway arm routed either op (A2), and
A2 is what makes it live. **The gate is now in the handler** (`requireJournalPlaneRead`): the
negotiated `journal` capability, then the remote-tier kill switch — the same two, in the same
order, with the same refusals, that `handleJournalRead` applies. Remote tier only: the owner
tier shares the kill-switch-implementing core and must never be gated.

The lesson generalizes past this ADR: "the gate lives somewhere else" is not a decision, it is
a forward reference, and a forward reference in an ADR is indistinguishable from a gate until
somebody probes it. A seam may legitimately be absent. A gate may not.

### A2 — "they are gateway-routed" described a route that did not exist (finding B6)

Section 1 stated as an ACCEPTED DECISION that the two reads "are gateway-routed", and
`docs/verification/r6-chat.md` marked M3.1/M3.3 GREEN on the strength of it. There was no arm
in `remotegw.routeCommand`, no arm in `opForAction`, and no `ActionInteractionHistory` /
`ActionInteractionDetail` constant anywhere in the tree: a phone-issued read fell to
`opForAction`'s default and was refused `unsupported command action`. M3 was unreachable from a
handset in both directions.

The fix-pack **built the route** rather than deferring it, because deferring it would have left
M3 with a wire shape and no way to speak it. The two actions join `journal_resync`'s UNSIGNED
class verbatim (no `actionClass` entry, no device signature), are forwarded to the daemon like
any other op — they have no gateway-local plane to serve them from, since the journal and the
retained bodies live on the daemon — and a read whose body was stripped in transit is refused
at the gateway rather than forwarded bodyless, the rule `launch` / `approve` / `session_launch`
/ `composer_send` already ride. The phone half (`App.LoadEarlierInteractions`,
`App.LoadInteractionDetail`, `App.HistoryFloor`) landed with it, for the same reason: an arm
with no producer is the same unreachable claim one layer over.

What remains deferred, and is now the ONLY deferred half, is the **Kotlin affordance** — the
"load earlier" control and the full-output expander.

**Superseded within the same fix-pack** (see A5): the Android lane built that affordance too,
so nothing of M3.1/M3.3 is deferred. What IS still missing is a read this ADR never specified —
see A5.

### A3 — section 2 ratified a hole: the page was trimmed by RECORD (finding B5)

Section 2 said "at most `limit`", and the implementation delivered that by slicing
`older[len(older)-limit:]` over RAW JOURNAL RECORDS — while the phone pages by ITEM ID. An
`agent_message` grown through IS-DELTA-1 increments occupies several records under one
`item_id`, so a page could deliver its TAIL with the head missing. The phone folds by
`item_id` and has no way to know a head is absent, so it renders the fragment as a whole
message — and the missing records become PERMANENTLY unreachable, because the next page asks
for what is older than that item's FIRST record, which is below the records just delivered.

This ADR ratified that behaviour by describing the bound without saying what it bounds. It is
amended: **`limit` bounds records, and the page begins on an item boundary.** The window is the
largest suffix of WHOLE items that fits `limit`. A `structured_gap` is atomic and is always its
own boundary. An item too large to fit ships alone and OVER `limit`, deliberately: refusing it
would return an empty page with `history_floor` false — "there is more, and you may not have
it" — and the phone would ask forever for a page it can never receive. An over-limit page is a
bounded, honest answer; a livelock is not.

### A4 — the page must carry the tear (finding B4)

Not a correction of a sentence this ADR wrote, but a rule it should have written. The daemon's
history scan kept only `interaction` records, so every `structured_gap` (ADR-017 T2 rule 2) was
dropped and each page spanned a proven capability tear CONTIGUOUSLY, with nothing marking it.
The page now carries `structured_gap` records beside the `interaction` ones, and their payload
crosses to the wire so the phone has something to render. ADR-017 forbids exactly one move —
silently bridging a proven gap — and a page that omits the boundary makes it.

### A5 — the affordance landed, and it exposed a read this ADR never specified: there is no ANCHORLESS page

A2 closed by naming the Kotlin affordance as the one deferred half. The fix-pack's Android lane
built it in the same round: "Load earlier" sits above the conversation
(`PhoneSurface.kt:527` → `App.LoadEarlierInteractions`) and drops away once `App.HistoryFloor`
is true; a clipped card carries a tappable expander (`TranscriptView.kt:107,275` →
`App.LoadInteractionDetail`). M3.1 and M3.3 have no deferred layer left.

Wiring it surfaced a gap in THIS ADR's decision 2, and it is recorded here rather than in the
verification file alone. Section 2 defines the page as "the window immediately preceding that
item's FIRST record" — paging is always relative to a named `item_id`, by identity and never by
cursor (IS-ENV-2), which is the right rule and is not amended. But it means **a phone holding no
items for a session has no id to name, and therefore no page it can ask for.** M3.2's cold-open
backfill fires correctly and then has nothing to send: `App.LoadEarlierInteractions` refuses an
empty `before_item` (`mobile/interactionread.go:55-58`), and no "newest page" spelling exists on
this boundary.

The user-visible consequence: **a session this phone has never held an item for cannot be opened
into its history.** The reader gets the transcript's empty state — which says the conversation
has not reached this phone, not that the agent said nothing — beside PB-SYNC-1's Repair control.
That is honest but it is not M3.2's stated exit ("cold-open shows history without tapping
Repair").

This is left OPEN rather than fixed in the fix-pack, deliberately: an anchorless read is a NEW
op shape, not a bug in a ratified one, and minting a wire shape inside a review round is how the
three defects above got in. It is one facade verb wide, it belongs with the M3.2 slice that owns
the cold open, and the call site (`PhoneSurface.kt:2300-2318`) is already written so that the
moment the read exists, nothing else changes.

**Superseded by A11 (2026-08-30):** empty `before_item` is now the additive newest-page sentinel;
the body shape did not change. Cold-open and conversation Reload both claim that bounded reply.
This page does not prove the global journal stream contiguous, so it does not clear that stale
verdict; transport repair remains a separate operation.

## Amended by review round 2 (2026-08-20)

The fix-pack's own reviewer re-read this ADR against the wired code and drove the two reads end
to end for the first time. Four more amendments. Three of them are the same shape as A2: a layer
that existed and had never been exercised.

### A6 — the item-boundary rule is ASYMMETRIC, and section 2 states it as if it were not

A3 amended section 2 to "the page begins on an item boundary". It does — on the OLD side. On the
NEW side the page is still cut at the boundary CURSOR: `interactionHistory` takes every record
whose cursor is below `before_item`'s first record. An item whose first record precedes that
boundary while its later deltas follow it is therefore delivered HEAD-ONLY.

That interleaving is not exotic: a phone `composer_send` echo opens a `user_message` inside a
growing `agent_message`, so the older item's increments continue past the newer item's first
record. What saves it from being a lie is the CONSUMER, not the producer: `applyLocked` drops a
record that does not strictly advance its item's cursor, so the trailing increments are dropped
rather than concatenated in the wrong place, and the reader sees a message that stops early
rather than a message with a hole in the middle. It is a silent hole all the same, and this ADR
should have said so instead of implying symmetry. Recorded, not fixed: fixing it means extending
the page's NEW edge past the boundary to close every item it opened, which changes what "at most
`limit`" bounds, and minting that inside a review round is how A1-A3 got in.

**Re-read by review round 3 (finding F7) and left standing, deliberately.** The disclosure above
is accurate — including the live case, which is the one that matters: a phone `composer_send`
echo opens a `user_message` inside a growing `agent_message`, so the older item's increments
continue past the newer item's first record. What round 3 added is that same case in
docs/verification/r6-chat.md's CANNOT YET (xii), which had stated the asymmetry without naming
what produces it, so a reader could take it for a theoretical shape.

### A7 — a `structured_gap` id is not a `before_item`, and the phone was sending one

A4 made the tear a first-class element on the phone as well as in the page, and the phone's
"page before the oldest thing I hold" then had a new kind of thing to be oldest.
`applyStructuredGapLocked` mints `structured_gap:<emission ts>` — an identity the PHONE derives,
because no producer stamps one — and `historyItemID` on the daemon answers "" for every
non-`interaction` record. So a `before_item` naming a gap could never match, `interactionHistory`
refused `invalid_field`, and the refusal was PERMANENT: only a successful page could change which
element is oldest.

Amended: **`before_item` names an item, and a boundary element is not an item.** The screen skips
tears when it picks its anchor (`TranscriptScreen.pageableAnchorOf`), and a phone whose only
element is a tear offers no control at all, because there is nothing it can name. Reachable
whenever a reseed floor (IS-CAP-4) cut just before a proven gap.

### A8 — a page must survive the READER'S phone, and it did not

Decision 2 says what the machine sends. It says nothing about what the phone does with it, and
what the phone did was throw it away. `insertLocked` places a page's older records at the FRONT
and `trimLocked` evicts oldest-first, so at `MaxItemsPerSession` the page and the trim targeted
the same end: probed, 200 items held, a 50-record page applied, 200 items held, none of the 50
surviving — while the screen went on offering the control, which is the livelock A3's own last
paragraph refused to create on the daemon side, reproduced on the phone.

Amended: **the phone holds a paged read in a SECOND bounded region** (`MaxBackfillPerSession`)
that the live trim neither counts nor evicts, and a page that does not fit in what remains is
refused WHOLE rather than partly — half a page is a hole in the middle of a conversation with
nothing marking it, which is the move ADR-017 forbids one plane over. The refusal is a fact the
screen SAYS (`App.HistoryAtCapacity` → the control is dropped): "this phone can hold no more" is
a different sentence from `history_floor`'s "nothing older is retained", and collapsing them
would tell a reader they had reached the beginning of a conversation that goes further back.

### A9 — the detail reply is not a delta, and could not fold at all

Decision 3 says `interaction_detail` "answers exactly ONE journal record whose `item` is that
body verbatim". True, and the phone could not absorb it. That record carries NO cursor — the body
comes out of the capture-time side store, not the journal — and the card a user taps is
`truncated` and `completed`. Both of the fold's guards therefore reject it: the cursor does not
strictly advance the item, and IS-ST-1 refuses any record after a terminal status. Probed: the
fetch folded nothing while the press reported success. Forcing a cursor above the high water does
not help while the item is terminal, and with a non-terminal item the `agent_message` branch
CONCATENATES the reply onto the clipped head and produces a garble presented as the whole of it
— which is precisely the ambiguity IS-CAP-3 exists to forbid.

Amended: **a detail reply is a REPLACEMENT and travels its own path** (`ItemStore.ApplyDetail`).
The item keeps its identity, its position and its region; its body, its text and its truncation
flag become the machine's. A reply for an item this phone does not hold folds nothing — there is
no cursor to insert it at, and inventing a position would drop a body somewhere in a conversation
nobody can defend.

## Amended by review round 3 (2026-08-20)

### A10 — A3's rule is right, is served, and was FENCED BY NOTHING (finding F1)

A3 amended section 2 to "the page begins on an item boundary", and the code does it:
`interactionHistory` calls `historyPageStart`. What no test held was the CALL SITE. All three of
B5's tests call `historyPageStart` directly on a hand-built slice, so reverting
`start := historyPageStart(older, limit)` to the pre-fix record trim
(`start := 0; if len(older) > limit { start = len(older) - limit }`) left the whole suite green —
the rule could be deleted from the served path without a single test noticing, and this ADR would
have gone on stating it.

That is the shape this review family keeps producing: a fix proven at the layer it was written
in, over a path nobody drives. It is closed by
`TestR6R3_TheServedHistoryPageNeverBeginsInTheMiddleOfAnItem`
(`internal/skeleton/r6r3_chat_test.go`), which drives `InteractionHistory` itself over a real
journal holding a real multi-record `agent_message` and asserts the served page begins at that
item's FIRST record; the mutation above now fails it. Nothing about the DECISION changed — what
changed is that this document's claim is checkable.

Section 1 was re-read against the code in the same pass and needs no further amendment: both ops
are gateway-routed (`internal/remotegw/command_loop.go`), both handlers honour the negotiated
`journal` capability and the remote kill switch (A1/A2), and neither adds an `actionClass` entry.

### A11 — anchorless newest-page repair, byte-bounded through the relay envelope (2026-08-30)

The empty-anchor gap in A5 is closed without a new operation or cursor vocabulary: an empty
`before_item` means the boundary after the newest retained record. `Load earlier` still sends a
non-empty stable item id; cold-open and conversation Reload deliberately send empty and claim the
reply into the session transcript. Reload no longer invokes global `journal_resync`, which could
aggregate a multi-megabyte retained journal into one command-reply mailbox frame. Claiming one
session page does not prove every session contiguous, so global journal staleness stays visible.

The original `limit=50` was not itself safe: fifty 16 KiB item payloads are about 800 KiB before
the command reply is encrypted and base64-encoded by the relay append. The daemon now also caps
the serialized record slice at 512 KiB, trimming only on whole-item boundaries. The worst-case
test seals the real command reply and marshals the real base64 relay-append shape under
`relay.MaxFrame`; a single indivisible item above the budget is refused rather than split.

Whole means every identity in the suffix, not merely the identity at its first record. A live
stream may interleave A(head), B, A(delta); starting at B still carries A's headless tail. The
old edge therefore closes the suffix over every repeated identity, exceeding the record-count
request only when that closure is indivisible. The independent 512 KiB ceiling stays hard.

## What this amends

PB-SYNC-3's reseed remains the transport repair channel, unchanged; this ADR adds the bounded,
session-scoped conversation read beside it. Conversation Reload uses the latter to recover the
latest retained page; it does not optimistically clear the global journal stale verdict.

## Deferred, disclosed

- **IS-CAP-4's byte-aware reseed bound** (`internal/daemon/interaction.go:36-39`'s recorded gap):
  M3.1 names it and this wave did not build it; `Gateway.Resync` still seals every record above
  the phone's cursor. Pre-existing, now one paging channel less load-bearing, still open.
- ~~**The phone's "load earlier" affordance and the facade read verb**~~: superseded by
  amendment A2. The facade verbs (`App.LoadEarlierInteractions`, `App.LoadInteractionDetail`,
  `App.HistoryFloor`) and the gateway route landed in the fix-pack. ~~**Only the Kotlin
  affordance** — the "load earlier" control and the full-output expander — rides the M3 view
  slice~~ — superseded again by amendment A5: the affordance landed in the same round. Nothing
  of M3.1/M3.3 is deferred.
- ~~**An ANCHORLESS "newest page" read** (amendment A5)~~: closed by A11. Empty
  `before_item` requests the newest retained page; cold-open and conversation Reload use it.
- **A paged read is not persisted.** The records a page delivers fold into the phone's live
  `ItemStore` and are NOT written into the durable transcript snapshot, so they are gone after a
  process death and a screen that wants them asks again. Deliberate: persisting them would let
  one "load earlier" walk the phone past `MaxItemsPerSession` and evict the LIVE tail to make
  room for history, which is the wrong trade on a handset — the recent conversation is what the
  retention bound exists to protect.

  **This was half true when it was written, and the other half pointed the wrong way** (review
  round 2). Nothing wrote the records into the transcript SNAPSHOT — and `Core.RecordOutcome`
  persisted the WHOLE reply, records included, into `OpOutcomes`, a map this codebase's own
  comment records as "never pruned, so every launch re-offers every outcome ever recorded". So
  every page wrote up to `limit` full item bodies into the phone's durable state file
  permanently, and every detail read wrote the FULL PRE-TRUNCATION BODY — the exact payload that
  was too large to ship inline — with none of the benefit, since no screen could read them back
  as transcript. A durable outcome is now a VERDICT and carries no journal records, and the fold
  happens where the reply is taken. The bullet above is true in both halves as of this
  amendment.
- **The daemon session-item index**: see decision 2 — the journal scan is the implementation
  until measurement says otherwise; the ratified shape is index-agnostic.
