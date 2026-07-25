// Package transport is the resilient client-side relay session (PB-NET-2..7,
// slice S6). It sits directly above internal/remote/relay's Client and below the
// phone core: it owns dialing policy, reconnection, connection state, the bounded
// idempotent-op queue, and the durable relay-cursor coordinate.
//
// Two properties are structural, not incidental:
//
//   - It handles ONLY opaque sealed frames (PB-NET-3). No type in this package
//     holds a content key, a wake key, or plaintext. Sealing and opening happen
//     above it, in the core that owns key custody.
//
//   - Raw input and resize are live-only (ADR-007 D7). They are never queued and
//     never replayed; with no live authenticated connection they fail immediately
//     with an explicit "delivery unknown / not sent" error. Only high-level
//     idempotent ops may queue, bounded per the §6.0 budget, and a full queue is a
//     clean refusal — never a silent drop.
//
// The numeric budget this package implements is binding (remote-phaseB-requirements
// §6.0): reconnect backoff initial 500 ms, factor 2, ceiling 30 s, jitter +/-20%;
// non-wait request timeout 10 s; idempotent op queue 64 ops, reject-new, drained
// at 8 ops/s so the reconnect drain is never one burst against the relay's
// tumbling rate window.
//
// Both properties above are enforced by the SHAPE of the API rather than by a
// policy flag: SendLive and SendOp are separate methods, so "input is never
// queued" cannot be inverted by flipping a boolean, and Drain reads one bounded
// page per call, so hostile pagination cannot wedge the transport.
package transport
