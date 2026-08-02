# Phase B slice S3 — pairing QR: symbol and payload (PB-PAIR-1, PB-PAIR-7)

**Requirements**: PB-PAIR-1 (render a genuinely scannable symbol) and PB-PAIR-7 (the QR must
carry a destination).

## The problems (verified before any change)

**PB-PAIR-7**: `internal/skeleton/pairing.go` minted the QR as
`EncodeQR(QRPayload{RendezvousID, PairingSecret})` — `RelayURL` was never set, though the
field exists and `loadRelayURL` already read the URL two frames up and then discarded it. A
scanning phone got a rendezvous id and a secret and **no endpoint to dial**, so it could never
claim the rendezvous. PB-PAIR-6's threat model ("a malicious QR cannot silently point the
phone at an attacker-chosen relay") presupposed a destination the QR did not carry.

**PB-PAIR-1**: `cmd/swarm/remote.go` printed the payload as a bare string under "Scan this QR
on your phone to pair:". There was no QR encoder in the repo. Nothing was scannable.

## RED (failing first, GG-5)

```
=== RUN   TestBeginPairing_QRCarriesTheConfiguredRelayURL
    decoded pairing QR carries an EMPTY RelayURL (PB-PAIR-7)
=== RUN   TestPairing_PhoneDrivenOnlyByTheQR
    the scanned QR carries no relay endpoint: a phone holding only this payload has a
    rendezvous id and a pairing secret but no address to dial
=== RUN   TestRemotePair_RendersAScannableQRSymbol
    `swarm remote pair` printed NO QR symbol -- stdout carries only text
# internal/remote/qrterm: undefined: Encode  (renderer did not exist)
```

## GREEN

`internal/remote/qrterm` renders a real QR symbol at half-block density; `BeginPairing`
carries the configured relay URL verbatim (no normalisation — the machine's own dial target is
the one endpoint known reachable, and the one PB-PAIR-6 displays before joining).

Dependency: `rsc.io/qr v0.2.0` — one module, **zero transitive dependencies** (verified via
`go mod graph`), pure Go. Confirmed absent from `internal/phonecore`'s closure, so PB-BIND-0 is
unaffected (the S1 guard still passes on host, android and ios).

Decodability was verified out-of-band, because the library always uses mask 0: the real
payload was re-encoded in a throwaway module, rasterised, and decoded with an independent
library (`gozxing`) — `version=6 size=41 match=true`.

## Three defects found by review that the tests could not see

1. **The symbol scrolled off screen.** It fit 80x24 *exactly*, leaving no room for the heading
   printed above it, so on a real terminal the top finder patterns scrolled away and the code
   was unscannable. The tests render into a buffer with no viewport and are structurally blind
   to this. Fixed by moving all chrome above the symbol (rows above scroll harmlessly; rows
   below displace it) and letting the renderer use the full box.
2. **The relay URL was unbounded** — the single worst defect in the slice. See the table below.
3. **At LINES=23 no symbol was drawn at all** (found while fixing 1) — the row reserve dropped
   the box below the threshold.

### The relay-URL cliff (PB-PAIR-7 x PB-PAIR-1(b) coupling)

| URL length | outcome before the fix |
|---|---|
| 0-39 | QR drawn |
| **40-89** | **no QR at all**, and the operator was told "terminal too small" on an 80x24 terminal |
| **90+** | `swarm remote pair` failed outright |

`wss://swarm-relay.us-east-1.example.com:8443` is 44 characters. No test coupled the URL
length to the QR size budget, though both are specified in the same document.

**Ceiling derived, not guessed: 39 characters.** Payload is `13 + base64url(3 + L + 16 + 32)`;
L=39 gives 133 chars -> ECC-L v6 (41 modules); L=40 jumps to v7 (45 modules, 49x25) which no
24-row terminal can show. The proof test is **two-sided** — it was verified to fail for a
constant of 38 (rejects URLs that work) and 40 (accepts URLs that do not render).

`swarm remote init --relay-url` now refuses blank, whitespace-only, unparseable, non-ws/wss,
host-less and over-length URLs **before any filesystem write**, and the fallback distinguishes
cannot-draw-glyphs / this-window-too-small / too-large-for-any-standard-terminal.

## Row budget achieved

| LINES | before | after | quiet zone |
|---|---|---|---|
| 23 | **no symbol** | 45x23 | 2 |
| 24 | 45x23 | **47x24** | **3** |
| 25+ | 47x24 | 49x25 | **4 (the QR standard's full zone)** |

## A test that could never pass

`TestEncode_SymbolDependsOnThePayload` built its "different" payload with
`strings.Replace(payload, "relay.example.com", ...)` — but the URL is base64url-encoded inside
the payload, so the literal never appears and the replace was a silent no-op, leaving the test
comparing `Encode(x)` against `Encode(x)`. Every deterministic encoder fails that: identical
payloads give identical grids and execution reaches `t.Fatal`, so it failed loudly rather than
passing vacuously. The implementer diagnosed it and **declined to weaken the assertion to make
its own work look green**, which was the right call. Diagnosis independently confirmed before
any change; replaced with a `mutateOneChar` helper that flips one base64 body character,
preserves length (so the size early-return does not short-circuit the real assertion), and
produces 78 of 1681 modules differing.

## Gates

```
go test ./internal/remote/qrterm/ ./cmd/swarm/ ./internal/skeleton/ -count=1   ok / ok / ok
go build ./... && go vet ./...                                                 exit 0
go test ./internal/phonecore/ -run TestBoundClosure                            PASS (PB-BIND-0 unaffected)
go mod tidy -diff                                                              clean
```

## Outstanding for this slice

**PB-PAIR-1 requires an evidenced manual scan** — a real phone camera reading the drawn symbol
off a real terminal. No test can supply it: glyph rendering, cell aspect ratio, contrast and
font line-height are only observable on hardware. Now lower-risk than when first flagged
(quiet zone is 3 at 24 rows and 4 at 25+, rather than 2), but still the check that matters.

Reviewer's recommendation for that run: the encoder always uses mask 0, and every pairing
mints a fresh random rendezvous id and secret, so a single decode is weak evidence — re-run
the out-of-band decode over ~1000 randomly-minted payloads to convert anecdote into evidence.
