#!/usr/bin/env node
// Deterministic MODEL, not Swarm or Cloudflare runtime code.
// It exercises the proposed migration's authority fence and negative controls.
import assert from 'node:assert/strict';

class Authority {
  constructor() {
    this.epoch = 1;
    this.writer = 'legacy';
    this.frozen = false;
    this.mailbox = [];
    this.incarnation = 'i-legacy';
    this.revoked = new Set();
    this.targetMutations = 0;
  }
  append(site, value, epoch = this.epoch) {
    if (site !== this.writer || this.frozen || epoch !== this.epoch) throw new Error('FENCED');
    this.mailbox.push(value);
    if (site === 'do') this.targetMutations++;
  }
  revoke(site, peer, epoch = this.epoch) {
    if (site !== this.writer || this.frozen || epoch !== this.epoch) throw new Error('FENCED');
    this.revoked.add(peer);
    if (site === 'do') this.targetMutations++;
  }
  snapshot() {
    assert.equal(this.frozen, true, 'export only after source frozen');
    return structuredClone({ epoch: this.epoch, mailbox: this.mailbox, incarnation: this.incarnation, revoked: [...this.revoked] });
  }
  cutover(snapshot) {
    assert.equal(this.frozen, true);
    assert.equal(snapshot.epoch, this.epoch);
    this.writer = 'do'; this.epoch++; this.frozen = false;
    // New durable object imports the same logical log; it must NOT rotate incarnation.
    // Treat export as immutable evidence; a mutable shared array would make the
    // post-cutover rollback comparison lie about whether the new authority wrote.
    this.mailbox = [...snapshot.mailbox]; this.incarnation = snapshot.incarnation;
    this.revoked = new Set(snapshot.revoked);
    this.targetMutations = 0;
  }
  abortToSource() {
    if (this.targetMutations !== 0) throw new Error('STALE_ROLLBACK');
    this.writer = 'legacy'; this.epoch++; this.frozen = false;
  }
}

function throwsFence(f, name) { assert.throws(f, /FENCED/, name); }

// Crash point: freeze has persisted before the export. Legacy can no longer accept
// an append/revoke, so recovery can resume export without concurrent writers.
{ const a = new Authority(); a.append('legacy', 'before'); a.frozen = true;
  throwsFence(() => a.append('legacy', 'during-freeze'), 'old append during frozen export');
  throwsFence(() => a.revoke('legacy', 'phone', 1), 'old revoke during frozen export');
  const s = a.snapshot(); a.cutover(s); assert.deepEqual(a.mailbox, ['before']); }

// Cutover point: the old connection retains a valid Ed25519 identity but is still fenced.
// Authentication alone is not migration authority.
{ const a = new Authority(); a.append('legacy', 'before'); a.frozen = true; const s = a.snapshot(); a.cutover(s);
  throwsFence(() => a.append('legacy', 'stale-authed-client', 1), 'no automatic legacy fallback');
  throwsFence(() => a.revoke('legacy', 'victim', 1), 'no stale revoke path');
  a.append('do', 'after', 2); assert.deepEqual(a.mailbox, ['before', 'after']); }

// Rollback is safe only before new authority accepts a write. Once DO has accepted a write,
// pointing clients back at an older snapshot is a loss/split-brain event, not automatic fallback.
{ const a = new Authority(); a.append('legacy', 'before'); a.frozen = true; const s = a.snapshot(); a.cutover(s);
  assert.equal(a.mailbox.length, s.mailbox.length, 'pre-write rollback gate is satisfiable');
  a.append('do', 'committed-on-new');
  assert.notEqual(a.mailbox.length, s.mailbox.length, 'post-write rollback must be prohibited');
  assert.throws(() => a.abortToSource(), /STALE_ROLLBACK/);
  assert.deepEqual(s.mailbox, ['before'], 'export cannot share mutable target array'); }

// Comparing mailbox length alone misses a revoke-only target mutation.
{ const a = new Authority(); a.append('legacy', 'before'); a.frozen = true; const s = a.snapshot(); a.cutover(s);
  a.revoke('do', 'phone');
  assert.equal(a.mailbox.length, s.mailbox.length, 'negative: length-only rollback check would pass');
  assert.throws(() => a.abortToSource(), /STALE_ROLLBACK/);
  assert(a.revoked.has('phone')); }

// Safe pre-mutation abort still fences every previously issued target epoch.
{ const a = new Authority(); a.frozen = true; a.cutover(a.snapshot()); const targetEpoch = a.epoch;
  a.abortToSource(); a.append('legacy', 'resumed');
  throwsFence(() => a.append('do', 'late', targetEpoch), 'aborted target remains fenced'); }

// Incarnation is a cursor-generation fence. A restore-like new value requires reset;
// a cross-implementation migration of the same mailbox must retain it exactly.
{ const a = new Authority(); a.append('legacy', 'one'); const cursor = { incarnation: a.incarnation, after: 1 };
  a.frozen = true; const s = a.snapshot(); a.cutover(s);
  assert.equal(cursor.incarnation, a.incarnation, 'migration preserves cursor continuity');
  a.incarnation = 'i-restore';
  assert.notEqual(cursor.incarnation, a.incarnation, 'restore requires mailbox_cursor_reset'); }

console.log('PASS migration fence model: freeze/export, stale-authority rejection, rollback gate, cursor incarnation');
