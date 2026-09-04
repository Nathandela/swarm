import assert from "node:assert/strict";
import { createHmac } from "node:crypto";

const base = process.env.RELAY_URL || "ws://127.0.0.1:8788/v2/ws";
const secret = "local-probe-only-not-a-production-secret";
const ticket = (route, client) => createHmac("sha256", secret).update(`${route}:${client}`).digest("base64url");
function connect(route, client) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`${base}?route=${route}&client=${client}&ticket=${ticket(route, client)}`);
    const messages = [];
    const timer = setTimeout(() => reject(new Error(`open timeout ${client}`)), 4000);
    ws.addEventListener("message", (e) => { messages.push(JSON.parse(e.data)); if (messages.some((m) => m.type === "READY")) { clearTimeout(timer); resolve({ ws, messages }); } });
    ws.addEventListener("error", reject, { once: true });
  });
}
async function waitFor(peer, type, timeout = 3000) {
  const until = Date.now() + timeout;
  while (Date.now() < until) { const found = peer.messages.find((m) => m.type === type); if (found) return found; await new Promise((r) => setTimeout(r, 10)); }
  throw new Error(`timeout waiting ${type}: ${JSON.stringify(peer.messages)}`);
}

const route = `pair-${Date.now()}`;
const unauthorized = await fetch(`http://127.0.0.1:8788/v2/ws?route=${route}&client=machine&ticket=not-a-ticket`);
assert.equal(unauthorized.status, 401, "bad pre-upgrade ticket is rejected");
const a = await connect(route, "phone");
const b = await connect(route, "machine");
assert.equal(a.messages[0].v, 2, "version negotiation baseline");
const forbidden = await connect(route, "machine");
forbidden.ws.send(JSON.stringify({ type: "APPEND", target: "machine", msg_id: "forbidden", ciphertext: "AA" }));
await new Promise((r) => setTimeout(r, 60));
assert.ok(forbidden.ws.readyState === WebSocket.CLOSED || forbidden.ws.readyState === WebSocket.CLOSING, "source cannot append to an unauthorized target");
a.ws.send(JSON.stringify({ type: "APPEND", target: "machine", msg_id: "m1", ciphertext: "AAECAwQ" }));
const delivered = await waitFor(b, "DELIVER");
assert.equal(delivered.ciphertext, "AAECAwQ", "relay preserves opaque ciphertext");
assert.equal((await waitFor(a, "APPENDED")).cursor, "00000000000000000001", "sender sees committed uint64 cursor");
// A forged ACK cannot discard unseen mail beyond the receiver's durable high-water.
const attacker = await connect(route, "machine");
attacker.ws.send(JSON.stringify({ type: "ACK", cursor: "00000000000000000002" }));
await new Promise((r) => setTimeout(r, 60));
assert.ok(attacker.ws.readyState === WebSocket.CLOSED || attacker.ws.readyState === WebSocket.CLOSING, "ACK above high-water is rejected");
b.ws.close();
await new Promise((r) => setTimeout(r, 50));
const again = await connect(route, "machine");
const replay = await waitFor(again, "DELIVER");
assert.equal(replay.cursor, "00000000000000000001", "unacked ciphertext catches up after reconnect");
again.ws.send(JSON.stringify({ type: "ACK", cursor: "00000000000000000001" }));
await new Promise((r) => setTimeout(r, 30));
again.ws.close();
const afterAck = await connect(route, "machine");
await new Promise((r) => setTimeout(r, 100));
assert.equal(afterAck.messages.filter((m) => m.type === "DELIVER").length, 0, "acked entry is not replayed");
// A deliberately unacked entry must be physically bounded by the DO alarm, not merely hidden on read.
a.ws.send(JSON.stringify({ type: "APPEND", target: "machine", msg_id: "expires", ciphertext: "ZXhwaXJl", ttl_ms: 500 }));
await waitFor(afterAck, "DELIVER");
// Wide TTL separation proves a later write does not postpone the earlier alarm.
await new Promise((r) => setTimeout(r, 100));
a.ws.send(JSON.stringify({ type: "APPEND", target: "machine", msg_id: "later", ciphertext: "bGF0ZXI", ttl_ms: 6000 }));
await new Promise((r) => setTimeout(r, 100));
afterAck.ws.close();
// No forced alarm call: the local runtime must dispatch the due storage alarm.
await new Promise((r) => setTimeout(r, 1800));
const afterRetention = await connect(route, "machine");
await new Promise((r) => setTimeout(r, 100));
assert.equal(afterRetention.messages.filter((m) => m.type === "DELIVER").length, 1, "earlier abandoned mail expires while a later entry remains");
assert.equal(afterRetention.messages.find((m) => m.type === "DELIVER").ciphertext, "bGF0ZXI", "retention alarm did not postpone earliest expiry");
a.ws.send(JSON.stringify({ type: "REVOKE" }));
await new Promise((r) => setTimeout(r, 100));
assert.ok(afterRetention.ws.readyState === WebSocket.CLOSED || afterRetention.ws.readyState === WebSocket.CLOSING, "revoke closes subscriber");
for (const x of [a, afterRetention]) if (x.ws.readyState < WebSocket.CLOSING) x.ws.close();
console.log("PASS local DO: pre-upgrade ticket, attachments API, opaque durable append, reconnect cursor, ack high-water fence, staggered retention alarm, revoke");
