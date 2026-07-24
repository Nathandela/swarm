# Investigation: pump write-timeout eviction (agents-tracker-3cm)

Item 3.2 of the v2 perf plan (INVESTIGATE-ONLY). The v1 fix ("keep the
connection alive server-side on a wedged-controller write timeout") was
REJECTED by the C1 committee. This note verifies the committee's rejection
against the actual code, and decides between closing the bead as
working-as-designed or scoping a client-side improvement.

## What happens today, traced end to end

1. A controller is attached and receiving live frames via `Server.pump`
   (`internal/protocol/server.go:454-513`). Each `TDataOut` write uses a fresh
   `pumpWriteTimeout()` deadline (5s default, `server.go:90-97`,
   `writeFrameDeadline` at `server.go:1037-1039`).
2. If that write's deadline expires — because the client isn't draining the
   socket fast enough — `pump` calls `s.evictPump(cc, local)`
   (`server.go:507-510`).
3. `evictPump` (`server.go:545-548`) releases the lease via
   `releaseFromPump(cc, local, notify=false)` — `notify=false` means it does
   **not** attempt a best-effort `OpDetach` — then calls `cc.close()`, which
   closes `cc.done` and the entire `net.Conn` (`server.go:944-949`).
4. Because the TUI uses **one** `*protocol.Client` (one `net.Conn`) for both
   `Attach` and `Subscribe` (`cmd/swarm/main.go:130,141,151,203`:
   `dialClient()` returns a single client; `dialAttach` calls
   `client.Attach(id)` on that same client; `tui.New(client, ...)` calls
   `c.Subscribe()` on it too), closing the connection kills the roster
   subscription along with the attach stream — there is no separate control
   channel.
5. Client-side, the single `readLoop` (`internal/protocol/client.go:250-280`)
   sees the closed socket as a read error, returns, and its deferred
   `closeReadLoop` (`client.go:331-340`) closes the current `Attachment`'s
   `Frames()` channel and calls `c.Close()`.
6. In `internal/attach/attach.go:214-222`, the passthrough loop's
   `case f, ok := <-frames` sees `ok == false`, calls
   `cfg.Session.Detach()` (a no-op write over an already-dead connection,
   error discarded), restores the terminal, and returns
   `(ReasonSessionEnd, nil)` — **not** `ReasonError`. There is no
   `ReasonEvicted`; session-end and eviction are indistinguishable to the
   caller.
7. `tui.NewAttachRunner` (`internal/tui/attach.go:76-88`) returns that `nil`
   error unchanged. `rootModel.Update`'s `attachDoneMsg` case
   (`internal/tui/tui.go:264-267`) **unconditionally discards `msg.err`** and
   calls `m.enterGeneral()`. Even if `Run` had returned `ReasonError` with a
   non-nil error, this case drops it — no code path in tui.go ever surfaces
   an attach error to the user.

**User-visible symptom today:** the attach screen silently drops back to the
general board, as if the user had pressed detach. No error, no banner, no
distinguishing signal. That much matches the plan's framing.

### A compounding bug found during this investigation (not in the plan)

`Client.Subscribe()` lazily creates `c.eventsCh` once
(`client.go:154-170`) and it is **never closed** — `Close()`
(`client.go:221-227`) only closes `c.done` and `c.conn`; `closeReadLoop`
(`client.go:331-340`) closes the *attachment's* `frames` channel, not
`eventsCh`. `tui.waitForEvent` (`internal/tui/tui.go:202-213`) does a bare
`ev, ok := <-ch` with no `select` on any done/closed signal, and is
re-armed after every event (`tui.go:229`). Once the connection is closed by
`evictPump`, nothing will ever send on or close `eventsCh` again: the
`waitForEvent` goroutine blocks forever, and the board **stops receiving
status updates for the remaining life of the `swarm` process** — silently,
with the last-known state frozen on screen looking perfectly normal (no
V-5 banners, no row updates, nothing). This is strictly worse than "kicked
back to the board": the user has no way to know the roster is stale short of
external verification (e.g. `swarm list` in another shell) or restarting the
TUI. This is a real, independent bug in the client's teardown path, not an
artifact of the rejected v1 fix, and it fires on ANY connection loss
(eviction, daemon restart, daemon crash), not just this scenario.

## Verifying the committee's two technical-impossibility claims

**(a) A timeout can land mid-frame, desyncing the byte stream.**
Confirmed. `wire.WriteFrame` (`internal/wire/wire.go:54-64`) builds the
entire length-prefixed frame (4-byte length + 1 type byte + payload) into one
`[]byte` and issues exactly one `w.Write(frame)` call. `net.Conn.Write` is
not required to be atomic against a deadline: if `SetWriteDeadline` fires
partway through, `Write` returns with `n < len(frame)` and a timeout error,
meaning an arbitrary-length, non-frame-aligned prefix of that frame is
already in the kernel send buffer / in flight to the peer. The wire protocol
has no resync token — `wire.ReadFrame` (`wire.go:72-`) trusts the next 4
bytes unconditionally as a length prefix. There is no way to "abort" a
partially-sent frame and resume with a clean one; the only safe recovery is
discarding the whole connection. Confirmed correct.

**(b) The wedged client's readLoop cannot read an `OpDetach` anyway.**
Confirmed. `readLoop` (`client.go:250-280`) is a single loop: it calls
`wire.ReadFrame` once, dispatches by type, and only then loops back for the
next frame. For `TDataOut` it calls `att.deliverFrame(c.done, payload)`
(`client.go:537-543`), which `select`s on sending to the 256-capacity
`a.frames` channel, `<-a.closed`, or `<-done` (`c.done`). If the attach
passthrough's consumer (`attach.Run`'s `writeAll(out, f)` at
`internal/attach/attach.go:223`) is the reason the server's write is
stalling — a slow terminal, a backed-up local pty/pipe, a suspended process —
then `a.frames` is full and `deliverFrame` blocks on that `select`, since
neither `a.closed` nor `c.done` fires from a live, un-evicted connection.
While blocked there, `readLoop` cannot call `wire.ReadFrame` again, so *any*
subsequent frame the server sends — including a graceful `OpDetach` control
frame — sits unread. A server-side "notify before closing" fix would not
help this exact failure mode: the reason the client is wedged is the same
reason it can't read the notice. Confirmed correct. (This also means
`evictPump`'s `notify=false` choice, `server.go:519-539`, is not merely an
optimization — sending the notice would be pointless in the scenario that
triggers eviction.)

**(c) close-on-evict is the documented ADR-002 S9 mechanism.**
Confirmed. ADR-002's 2026-07-17 amendment
(`docs/adr/ADR-002-protocol-control-data-split.md:46`) states: "A
wedged/slow controller's write fails at the deadline and the controller is
evicted (its lease released, its connection closed); ... This makes
supersede and detach **always** proceed within a bound — a wedged client can
never hold the lease or block the daemon (S9)." Current code matches this
exactly. Reversing it would require an ADR amendment, and per (a)/(b) above
there is no safe way to keep the connection alive regardless.

All three committee claims hold up against the code. **The v1 fix (keep the
server-side connection alive across an eviction) is correctly rejected and
should not be revisited without a wire-protocol change (a resync/framing
mechanism), which is out of scope for this plan (G-E, no architecture
changes).**

## Decision: (ii), narrowly scoped

Not a pure "close as working-as-designed." The server-side behavior *is*
working as designed and should not change. But this investigation surfaced
a genuine, independently-reproducible client-side defect — the permanently
stale, silently-frozen roster — that is squarely in scope for "client-side
improvement" per R3.2.2 option (ii), is small, and is worth fixing regardless
of whether eviction itself is ever revisited (it fires on daemon restart and
daemon crash too, not just eviction).

### Scoped fix (small — roughly 30-60 lines across 2-3 files, no protocol/wire changes)

1. **Close `eventsCh` on client teardown.** In `Client.Close()`
   (`client.go:221-227`) or `closeReadLoop` (`client.go:331-340`), close
   `c.eventsCh` (guarded by the existing `c.mu`) if non-nil. This turns the
   permanent silent hang into an observable channel close: `waitForEvent`'s
   `ev, ok := <-ch` returns `ok == false` and the existing `if !ok { return
   nil }` (`tui.go:207-209`) already does the right thing — it stops
   re-arming instead of hanging. This alone fixes the "board looks fine but
   is frozen forever" failure mode.
2. **Surface a "connection lost" banner** instead of silently doing nothing.
   `waitForEvent`'s `nil` return on channel-close is currently
   indistinguishable from "no subscription" — add a distinct message (e.g. a
   `connectionLostMsg`) so `rootModel.Update` can call
   `m.general.setBanner(...)` (`general.go:333`, already exists) with
   something like "connection to daemon lost — restart swarm to
   reconnect". This is the same banner mechanism V-5 already uses for status
   transitions, no new UI primitive needed.
3. **Stop discarding `attachDoneMsg.err`.** In `tui.go:264-267`, when
   `msg.err != nil`, route it through `setBanner` instead of silently
   calling `enterGeneral()`. Low-risk, and independently correct regardless
   of eviction (any future `attach.Run` error should be visible).
4. **Do NOT build auto-reconnect/re-subscribe in this pass.** No reconnect
   scaffolding exists anywhere in `internal/tui` or `cmd/swarm` today (only
   daemon-to-shim reconnect exists, a different layer:
   `internal/daemon/reconcile.go`, `daemon/shimclient.go`). Building a
   correct client redial + re-`List()` + re-`Subscribe()` + re-attach path is
   a real design effort (backoff policy, avoiding duplicate/missed events
   during the gap, re-render of the whole board, deciding whether an
   in-progress attach should also try to resume) and is not justified by
   this bug alone — a clear, truthful "connection lost, restart to
   reconnect" message is a proportionate fix for a P2 item. If field
   evidence shows eviction/reconnect is common enough to matter, file it as
   its own bead with its own design.

### Test list for the scoped fix

- `TestClientClose_ClosesEventsChannel`: after `Subscribe()` then `Close()`,
  reading from the returned channel returns `(_, false)` rather than
  blocking (bounded by a short timeout in the test, failing today).
- `TestWaitForEvent_ChannelClosed_EmitsConnectionLostNotNil`: feed
  `waitForEvent` a closed channel, assert it returns a `connectionLostMsg`
  (or equivalent), not a bare `nil` `tea.Msg`.
- `TestUpdate_ConnectionLost_SetsBanner`: drive `rootModel.Update` with the
  new message, assert the banner text is set.
- `TestUpdate_AttachDoneWithError_SetsBanner`: drive `attachDoneMsg{err:
  errors.New("x")}` through `Update`, assert the banner is set (fails today
  — `err` is currently silently dropped).
- Existing `TestUpdate_AttachDone*`-style tests (if any) must still pass for
  the `err == nil` path (still returns to the general board with no banner).

## Recommendation

**(ii)**, narrowly scoped to the two client-side teardown gaps above
(`eventsCh` never closing; `attachDoneMsg.err` discarded) plus a
"connection lost" banner. Do not build reconnect/re-subscribe now. Do not
touch server-side eviction — the committee's rejection of v1 is correct and
grounded in code, and ADR-002's S9 close-on-evict text stays authoritative
with no amendment needed for the server side.

**Bead disposition:** do not close agents-tracker-3cm as pure
working-as-designed. Re-scope it to the client-side fix above (or split: keep
3cm for a short WAD note referencing this investigation + ADR-002, and file
a new bead for the client-side teardown fix, since it is a distinct,
independently-valuable change unrelated to the rejected v1 approach). Sizing:
small — one implementer session, no protocol/wire/ADR changes, TDD as usual
(G-A/G-B).
