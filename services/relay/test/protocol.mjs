import assert from "node:assert/strict";
import { createHash, createPrivateKey, createPublicKey, hkdfSync, sign } from "node:crypto";
import { RelayHome } from "../src/worker.mjs";

const HTTP = process.env.RELAY_HTTP || "http://127.0.0.1:8790";
const WS = HTTP.replace(/^http/, "ws");
const DOMAIN = Buffer.from("swarm-relay-home/v1");
const AUTH_CONTEXT = Buffer.from("swarm-relay-auth-v2\0");
const CONSENT_CONTEXT = Buffer.from("swarm-relay-consent-v1\0");
const u32 = (n) => { const b = Buffer.alloc(4); b.writeUInt32BE(n); return b; };
const field = (b) => Buffer.concat([u32(b.length), b]);
const b64 = (b) => Buffer.from(b).toString("base64url");

function identity(first) {
  const seed = Buffer.from(Array.from({ length: 32 }, (_, i) => first + i));
  const key = createPrivateKey({ key: Buffer.concat([Buffer.from("302e020100300506032b657004220420", "hex"), seed]), format: "der", type: "pkcs8" });
  const pub = createPublicKey(key).export({ format: "der", type: "spki" }).subarray(-32);
  const rid = Buffer.from(hkdfSync("sha256", pub, Buffer.from("swarm-relay-routing-id-v1"), Buffer.from("routing-id"), 16)).toString("hex");
  return { key, pub, rid };
}
function home(rid) {
  return createHash("sha256").update(Buffer.concat([field(DOMAIN), field(Buffer.from("local-test")), field(Buffer.from(rid))])).digest("hex");
}
function authMessage(nonce, rid, homeID, role, purpose) {
  return Buffer.concat([AUTH_CONTEXT, field(Buffer.from(nonce, "base64url")), field(Buffer.from(rid)), field(Buffer.from(homeID)), field(Buffer.from(role)), field(Buffer.from(purpose))]);
}
function consent(machineRID, ceremony) {
  const body = Buffer.concat([CONSENT_CONTEXT, field(Buffer.from(ceremony)), Buffer.from(machineRID)]);
  return Buffer.concat([field(Buffer.from(ceremony)), sign(null, body, phone.key)]);
}
function open(path) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`${WS}${path}`);
    const messages = [];
    ws.addEventListener("message", (e) => messages.push(JSON.parse(e.data)));
    ws.addEventListener("open", () => resolve({ ws, messages }));
    ws.addEventListener("error", reject, { once: true });
  });
}
async function waitFor(peer, pred, timeout = 2500) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const value = peer.messages.find(pred);
    if (value) return value;
    await new Promise((r) => setTimeout(r, 10));
  }
  throw new Error(`timed out: ${JSON.stringify(peer.messages)}`);
}
async function waitUntil(pred, timeout = 5000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (pred()) return; await new Promise((r) => setTimeout(r, 10)); }
  throw new Error("condition timed out");
}
async function authenticate(peer, who, role, purpose = "stream") {
  peer.ws.send(JSON.stringify({ v: 2, type: "AUTH_INIT", request_id: `init-${role}`, role, purpose, pub: b64(who.pub) }));
  const challenge = await waitFor(peer, (m) => m.type === "CHALLENGE");
  const signature = sign(null, authMessage(challenge.nonce, who.rid, challenge.home, role, purpose), who.key);
  peer.ws.send(JSON.stringify({ v: 2, type: "AUTH_PROVE", request_id: `prove-${role}`, signature: b64(signature) }));
  return waitFor(peer, (m) => m.type === "AUTHENTICATED" || m.type === "ERROR");
}
const send = (peer, message) => peer.ws.send(JSON.stringify({ v: 2, ...message }));
const machine = identity(0);
const phone = identity(32);
assert.equal(machine.rid, "88564c8ede170d2ed321e21e61354184", "existing Go HKDF vector remains exact");
assert.equal(home(machine.rid), "cc634f54c634813fc554848c78763e63b3dbdff50975c0d789de730e5570beaa", "home KDF vector");

// Keep the barrier's critical ordering under a deterministic unit fence. The
// workerd assertion below separately covers the same behavior end to end.
{
  const order = [];
  const peer = "0".repeat(32);
  const generation = "00000000000000000001";
  const incarnation = "A".repeat(22);
  const relay = new RelayHome({}, {});
  relay.requireStream = () => {};
  relay.liveBinding = () => ({ generation });
  relay.pump = async () => { order.push("DELIVER"); };
  relay.send = (_ws, type) => { order.push(type); };
  await relay.probe({}, { purpose: "stream", sub: { peer, generation, incarnation } }, {
    v: 2, type: "PROBE", request_id: "probe-order", peer_rid: peer, generation: "1", incarnation,
  });
  assert.deepEqual(order, ["DELIVER", "PROBED"], "PROBE pumps queued mail before completing its barrier");
}

const denied = await fetch(`${HTTP}/v2/ws?machine_rid=${"0".repeat(32)}`);
assert.equal(denied.status, 403, "allowlist rejects before arbitrary home dispatch");

const m = await open(`/v2/ws?machine_rid=${machine.rid}`);
assert.equal((await authenticate(m, machine, "machine", "control")).home, home(machine.rid));

const wrongRole = await open(`/v2/ws?machine_rid=${machine.rid}`);
send(wrongRole, { type: "AUTH_INIT", request_id: "wrong-role", role: "machine", purpose: "control", pub: b64(phone.pub) });
assert.equal((await waitFor(wrongRole, (x) => x.request_id === "wrong-role")).code, "role_mismatch");

const stale = await open(`/v2/ws?machine_rid=${machine.rid}`);
send(stale, { type: "AUTH_INIT", request_id: "stale-init", role: "machine", purpose: "control", pub: b64(machine.pub) });
const staleChallenge = await waitFor(stale, (x) => x.type === "CHALLENGE");
assert.ok(staleChallenge.expires_at);
await waitUntil(() => stale.ws.readyState >= WebSocket.CLOSING, 2500);

const wrongHome = await open(`/v2/ws?machine_rid=${machine.rid}`);
send(wrongHome, { type: "AUTH_INIT", request_id: "wrong-home-init", role: "machine", purpose: "control", pub: b64(machine.pub) });
const wrongHomeChallenge = await waitFor(wrongHome, (x) => x.type === "CHALLENGE");
send(wrongHome, { type: "AUTH_PROVE", request_id: "wrong-home-proof", signature: b64(sign(null, authMessage(wrongHomeChallenge.nonce, machine.rid, "0".repeat(64), "machine", "control"), machine.key)) });
assert.equal((await waitFor(wrongHome, (x) => x.request_id === "wrong-home-proof")).code, "auth_failed");
send(wrongHome, { type: "AUTH_PROVE", request_id: "proof-reuse", signature: b64(sign(null, authMessage(wrongHomeChallenge.nonce, machine.rid, wrongHomeChallenge.home, "machine", "control"), machine.key)) });
assert.equal((await waitFor(wrongHome, (x) => x.request_id === "proof-reuse")).code, "auth_required", "a failed proof cannot reuse its challenge");

const ceremony = "11".repeat(16);
send(m, { type: "AUTHORIZE", request_id: "authorize-1", phone_pub: b64(phone.pub), consent: b64(consent(machine.rid, ceremony)) });
const authorized = await waitFor(m, (x) => x.request_id === "authorize-1");
assert.equal(authorized.type, "AUTHORIZED");
assert.equal(authorized.generation, "1");

let p = await open(`/v2/ws?machine_rid=${machine.rid}`);
const pauth = await authenticate(p, phone, "phone");
assert.equal(pauth.generation, "1");
send(p, { type: "PROBE", request_id: "probe-before-subscribe", peer_rid: machine.rid, generation: "1", incarnation: "A".repeat(22) });
assert.equal((await waitFor(p, (x) => x.request_id === "probe-before-subscribe")).code, "not_subscribed");
send(p, { type: "SUBSCRIBE", request_id: "sub-1", peer_rid: machine.rid, generation: "1", incarnation: "", after: "0" });
const subscribed = await waitFor(p, (x) => x.request_id === "sub-1");
assert.equal(subscribed.type, "SUBSCRIBED");
const incarnation = subscribed.incarnation;

const ms = await open(`/v2/ws?machine_rid=${machine.rid}`);
assert.equal((await authenticate(ms, machine, "machine", "stream")).purpose, "stream");
assert.equal(m.ws.readyState, WebSocket.OPEN, "stream auth does not supersede the machine control socket");
send(ms, { type: "APPEND", request_id: "append-1", peer_rid: phone.rid, generation: "1", msg_id: "msg-one", ciphertext: "AAECAwQ" });
const appended = await waitFor(ms, (x) => x.request_id === "append-1");
assert.equal(appended.cursor, "1");
assert.equal(appended.deduped, false);
// Do not wait for DELIVER first: PROBED itself is the end-to-end synchronization point.
send(p, { type: "PROBE", request_id: "probe-1", peer_rid: machine.rid, generation: "1", incarnation });
const probed = await waitFor(p, (x) => x.request_id === "probe-1");
assert.equal(probed.type, "PROBED");
const deliveryIndex = p.messages.findIndex((x) => x.type === "DELIVER" && x.msg_id === "msg-one");
assert.ok(deliveryIndex >= 0 && deliveryIndex < p.messages.findIndex((x) => x.request_id === "probe-1"), "PROBED is ordered after the delivery it barriers");
assert.equal(p.messages[deliveryIndex].ciphertext, "AAECAwQ");
send(p, { type: "PROBE", request_id: "probe-wrong-peer", peer_rid: phone.rid, generation: "1", incarnation });
assert.equal((await waitFor(p, (x) => x.request_id === "probe-wrong-peer")).code, "invalid_peer");
send(p, { type: "PROBE", request_id: "probe-stale-generation", peer_rid: machine.rid, generation: "2", incarnation });
assert.equal((await waitFor(p, (x) => x.request_id === "probe-stale-generation")).code, "stale_generation");
send(p, { type: "PROBE", request_id: "probe-stale-incarnation", peer_rid: machine.rid, generation: "1", incarnation: "B".repeat(22) });
assert.equal((await waitFor(p, (x) => x.request_id === "probe-stale-incarnation")).code, "incarnation_mismatch");

// PROBE is a read-only barrier: without an ACK, reconnecting at the same checkpoint
// receives the exact item again.
p.ws.close();
await waitUntil(() => p.ws.readyState >= WebSocket.CLOSING);
p = await open(`/v2/ws?machine_rid=${machine.rid}`);
assert.equal((await authenticate(p, phone, "phone")).generation, "1");
send(p, { type: "SUBSCRIBE", request_id: "sub-after-probe", peer_rid: machine.rid, generation: "1", incarnation, after: "0" });
assert.equal((await waitFor(p, (x) => x.request_id === "sub-after-probe")).type, "SUBSCRIBED");
assert.equal((await waitFor(p, (x) => x.type === "DELIVER" && x.msg_id === "msg-one")).ciphertext, "AAECAwQ", "PROBE must not compact delivery custody");
const deliveredBeforeResubscribe = p.messages.filter((x) => x.type === "DELIVER" && x.msg_id === "msg-one").length;
send(p, { type: "SUBSCRIBE", request_id: "repeat-sub", peer_rid: machine.rid, generation: "1", incarnation, after: "0" });
assert.equal((await waitFor(p, (x) => x.request_id === "repeat-sub")).code, "already_subscribed");
await new Promise((r) => setTimeout(r, 50));
assert.equal(p.messages.filter((x) => x.type === "DELIVER" && x.msg_id === "msg-one").length, deliveredBeforeResubscribe, "repeat subscribe cannot requeue delivery");

send(ms, { type: "APPEND", request_id: "append-dup", peer_rid: phone.rid, generation: "1", msg_id: "msg-one", ciphertext: "AAECAwQ" });
const duplicate = await waitFor(ms, (x) => x.request_id === "append-dup");
assert.equal(duplicate.cursor, "1");
assert.equal(duplicate.deduped, true);
send(ms, { type: "APPEND", request_id: "append-conflict", peer_rid: phone.rid, generation: "1", msg_id: "msg-one", ciphertext: "ZGlmZmVyZW50" });
assert.equal((await waitFor(ms, (x) => x.request_id === "append-conflict")).code, "id_conflict");
send(ms, { type: "APPEND", request_id: "noncanonical-b64", peer_rid: phone.rid, generation: "1", msg_id: "msg-bad", ciphertext: "AB" });
assert.equal((await waitFor(ms, (x) => x.request_id === "noncanonical-b64")).code, "invalid_base64url");

send(p, { type: "ACK", request_id: "ack-beyond", peer_rid: machine.rid, generation: "1", incarnation, cursor: "2" });
assert.equal((await waitFor(p, (x) => x.request_id === "ack-beyond")).code, "ack_beyond_sent");
send(p, { type: "ACK", request_id: "ack-1", peer_rid: machine.rid, generation: "1", incarnation, cursor: "1" });
assert.equal((await waitFor(p, (x) => x.request_id === "ack-1")).cursor, "1");

const largeCiphertext = Buffer.alloc(700_000, 7).toString("base64url");
send(ms, { type: "APPEND", request_id: "append-large", peer_rid: phone.rid, generation: "1", msg_id: "msg-large", ciphertext: largeCiphertext });
assert.equal((await waitFor(ms, (x) => x.request_id === "append-large", 10000)).type, "APPENDED");
assert.equal((await waitFor(p, (x) => x.type === "DELIVER" && x.msg_id === "msg-large", 10000)).ciphertext.length, largeCiphertext.length, "near-limit canonical base64url avoids argument-spread failure");
send(p, { type: "ACK", request_id: "ack-large", peer_rid: machine.rid, generation: "1", incarnation, cursor: "2" });
assert.equal((await waitFor(p, (x) => x.request_id === "ack-large")).cursor, "2");

send(p, { type: "ACK", request_id: "numeric-cursor", peer_rid: machine.rid, generation: "1", incarnation, cursor: 2 });
assert.equal((await waitFor(p, (x) => x.request_id === "numeric-cursor")).code, "invalid_uint64");
send(p, { type: "ACK", request_id: "overflow-cursor", peer_rid: machine.rid, generation: "1", incarnation, cursor: "18446744073709551616" });
assert.equal((await waitFor(p, (x) => x.request_id === "overflow-cursor")).code, "invalid_uint64");

send(ms, { type: "APPEND", request_id: "append-recovery", peer_rid: phone.rid, generation: "1", msg_id: "msg-recovery", ciphertext: "cmVjb3Zlcg" });
await waitFor(p, (x) => x.type === "DELIVER" && x.msg_id === "msg-recovery");
send(p, { type: "DISCARD", request_id: "discard", peer_rid: machine.rid, generation: "1", incarnation });
const discarded = await waitFor(p, (x) => x.request_id === "discard");
assert.equal(discarded.type, "DISCARDED");
assert.notEqual(discarded.incarnation, incarnation);
p.ws.close();
await waitUntil(() => p.ws.readyState >= WebSocket.CLOSING);
p = await open(`/v2/ws?machine_rid=${machine.rid}`);
assert.equal((await authenticate(p, phone, "phone")).generation, "1");
send(p, { type: "SUBSCRIBE", request_id: "stale-incarnation", peer_rid: machine.rid, generation: "1", incarnation, after: "1" });
assert.equal((await waitFor(p, (x) => x.request_id === "stale-incarnation")).code, "incarnation_mismatch");
send(p, { type: "SUBSCRIBE", request_id: "sub-after-discard", peer_rid: machine.rid, generation: "1", incarnation: discarded.incarnation, after: discarded.cursor });
const afterDiscardSub = await waitFor(p, (x) => x.request_id === "sub-after-discard");
assert.equal(afterDiscardSub.type, "SUBSCRIBED");

for (let i = 0; i < 257; i++) {
  const appendRequest = i < 100 ? `cost-append-${i}` : `burst-${i}`;
  send(ms, { type: "APPEND", request_id: appendRequest, peer_rid: phone.rid, generation: "1", msg_id: `burst-${i}`, ciphertext: "AA" });
  await waitFor(ms, (x) => x.type === "APPENDED" && x.request_id === appendRequest);
  const delivered = await waitFor(p, (x) => x.type === "DELIVER" && x.msg_id === `burst-${i}`);
  const ackRequest = i < 100 ? `cost-ack-${i}` : `ack-burst-${i}`;
  send(p, { type: "ACK", request_id: ackRequest, peer_rid: machine.rid, generation: "1", incarnation: afterDiscardSub.incarnation, cursor: delivered.cursor });
  await waitFor(p, (x) => x.type === "ACKED" && x.request_id === ackRequest);
}
send(ms, { type: "APPEND", request_id: "burst-last-duplicate", peer_rid: phone.rid, generation: "1", msg_id: "burst-256", ciphertext: "AA" });
assert.equal((await waitFor(ms, (x) => x.request_id === "burst-last-duplicate")).deduped, true, "sliding receipt window keeps throughput open and the newest retry exact");
send(p, { type: "DISCARD", request_id: "discard-burst", peer_rid: machine.rid, generation: "1", incarnation: afterDiscardSub.incarnation });
await waitFor(p, (x) => x.request_id === "discard-burst" && x.type === "DISCARDED");
send(ms, { type: "APPEND", request_id: "window-trigger", peer_rid: phone.rid, generation: "1", msg_id: "window-trigger", ciphertext: "AA" });
await waitFor(ms, (x) => x.request_id === "window-trigger");
send(ms, { type: "APPEND", request_id: "old-retry", peer_rid: phone.rid, generation: "1", msg_id: "msg-one", ciphertext: "AAECAwQ" });
assert.equal((await waitFor(ms, (x) => x.request_id === "old-retry")).deduped, false, "an ACKed retry older than the 256-receipt window is explicitly transport-replayable");

const pairCeremony = "22".repeat(16);
send(m, { type: "PAIR_CREATE", request_id: "pair-create", ceremony: pairCeremony });
assert.equal((await waitFor(m, (x) => x.request_id === "pair-create")).type, "PAIR_CREATED");
send(m, { type: "PAIR_CREATE", request_id: "pair-duplicate", ceremony: pairCeremony });
assert.equal((await waitFor(m, (x) => x.request_id === "pair-duplicate")).code, "pairing_exists", "duplicate create cannot reset a claimed-capable slot");
const pairPhone = await open(`/v2/pair?ceremony=${pairCeremony}`);
send(pairPhone, { type: "PAIR_CLAIM", request_id: "pair-claim", ceremony: pairCeremony });
assert.equal((await waitFor(pairPhone, (x) => x.request_id === "pair-claim")).type, "PAIR_CLAIMED");
send(pairPhone, { type: "PAIR_SEND", request_id: "pair-send", ceremony: pairCeremony, ciphertext: "bm9pc2UtbXNnMQ" });
assert.equal((await waitFor(m, (x) => x.type === "PAIR_FRAME")).ciphertext, "bm9pc2UtbXNnMQ");

send(m, { type: "REVOKE", request_id: "revoke", peer_rid: phone.rid, generation: "1" });
assert.equal((await waitFor(m, (x) => x.request_id === "revoke")).type, "REVOKED");
send(ms, { type: "PROBE", request_id: "probe-revoked", peer_rid: phone.rid, generation: "1", incarnation: afterDiscardSub.incarnation });
assert.equal((await waitFor(ms, (x) => x.request_id === "probe-revoked")).code, "stale_generation");
await new Promise((r) => setTimeout(r, 50));
assert.ok(p.ws.readyState >= WebSocket.CLOSING, "revoke closes old phone socket");

const m2 = await open(`/v2/ws?machine_rid=${machine.rid}`);
await authenticate(m2, machine, "machine", "control");
send(m2, { type: "AUTHORIZE", request_id: "resurrect", phone_pub: b64(phone.pub), consent: b64(consent(machine.rid, ceremony)) });
assert.equal((await waitFor(m2, (x) => x.request_id === "resurrect")).code, "consent_retired");

for (const peer of [m, ms, m2, wrongRole, wrongHome, stale, p, pairPhone]) if (peer.ws.readyState < WebSocket.CLOSING) peer.ws.close();
console.log("PASS workerd relay-v2 auth/home/pair/mailbox/dedupe/ack/revoke negative controls");
