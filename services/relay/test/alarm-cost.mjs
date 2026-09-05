import assert from "node:assert/strict";
import { createHash, createPrivateKey, createPublicKey, hkdfSync, sign } from "node:crypto";

const HTTP = process.env.RELAY_HTTP || "http://127.0.0.1:8790";
const WS = HTTP.replace(/^http/, "ws");
const DOMAIN = Buffer.from("swarm-relay-home/v1");
const AUTH_CONTEXT = Buffer.from("swarm-relay-auth-v2\0");
const CONSENT_CONTEXT = Buffer.from("swarm-relay-consent-v1\0");
const u32 = (n) => { const b = Buffer.alloc(4); b.writeUInt32BE(n); return b; };
const field = (b) => Buffer.concat([u32(b.length), b]);
const b64 = (b) => Buffer.from(b).toString("base64url");

function identity(first) {
  const seed = Buffer.from(Array.from({ length: 32 }, (_, i) => (first + i) & 255));
  const key = createPrivateKey({ key: Buffer.concat([Buffer.from("302e020100300506032b657004220420", "hex"), seed]), format: "der", type: "pkcs8" });
  const pub = createPublicKey(key).export({ format: "der", type: "spki" }).subarray(-32);
  const rid = Buffer.from(hkdfSync("sha256", pub, Buffer.from("swarm-relay-routing-id-v1"), Buffer.from("routing-id"), 16)).toString("hex");
  return { key, pub, rid };
}
function authMessage(nonce, rid, home, role, purpose) {
  return Buffer.concat([AUTH_CONTEXT, field(Buffer.from(nonce, "base64url")), field(Buffer.from(rid)), field(Buffer.from(home)), field(Buffer.from(role)), field(Buffer.from(purpose))]);
}
function consent(machineRID, ceremony, phone) {
  return Buffer.concat([field(Buffer.from(ceremony)), sign(null, Buffer.concat([CONSENT_CONTEXT, field(Buffer.from(ceremony)), Buffer.from(machineRID)]), phone.key)]);
}
function open(path) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`${WS}${path}`);
    const messages = [];
    ws.addEventListener("message", (event) => messages.push(JSON.parse(event.data)));
    ws.addEventListener("open", () => resolve({ ws, messages }));
    ws.addEventListener("error", reject, { once: true });
  });
}
async function waitFor(peer, pred, timeout = 5000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const value = peer.messages.find(pred);
    if (value) return value;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`timed out: ${JSON.stringify(peer.messages)}`);
}
async function authenticate(peer, who, role, purpose) {
  peer.ws.send(JSON.stringify({ v: 2, type: "AUTH_INIT", request_id: `init-${purpose}`, role, purpose, pub: b64(who.pub) }));
  const challenge = await waitFor(peer, (message) => message.type === "CHALLENGE");
  peer.ws.send(JSON.stringify({ v: 2, type: "AUTH_PROVE", request_id: `prove-${purpose}`, signature: b64(sign(null, authMessage(challenge.nonce, who.rid, challenge.home, role, purpose), who.key)) }));
  const result = await waitFor(peer, (message) => message.request_id === `prove-${purpose}`);
  assert.equal(result.type, "AUTHENTICATED", JSON.stringify(result));
}
async function request(peer, type, requestID, fields = {}) {
  peer.ws.send(JSON.stringify({ v: 2, type, request_id: requestID, ...fields }));
  return await waitFor(peer, (message) => message.request_id === requestID);
}

const machine = identity(0);
assert.equal(machine.rid, "88564c8ede170d2ed321e21e61354184");
assert.equal(createHash("sha256").update(Buffer.concat([field(DOMAIN), field(Buffer.from("local-test")), field(Buffer.from(machine.rid))])).digest("hex"), "cc634f54c634813fc554848c78763e63b3dbdff50975c0d789de730e5570beaa");
const control = await open(`/v2/ws?machine_rid=${machine.rid}`);
await authenticate(control, machine, "machine", "control");
const stream = await open(`/v2/ws?machine_rid=${machine.rid}`);
await authenticate(stream, machine, "machine", "stream");

const phones = Array.from({ length: 32 }, (_, i) => identity(32 + i * 7));
for (let i = 0; i < phones.length; i++) {
  const ceremony = i.toString(16).padStart(32, "0");
  const result = await request(control, "AUTHORIZE", `authorize-${i}`, { phone_pub: b64(phones[i].pub), consent: b64(consent(machine.rid, ceremony, phones[i])) });
  assert.equal(result.type, "AUTHORIZED");
}
await request(stream, "APPEND", "append-0", { peer_rid: phones[0].rid, generation: "1", msg_id: "alarm-0", ciphertext: "AA" });
const one = await request(control, "TEST_ALARM", "alarm-one");
assert.equal(one.type, "TEST_ALARMED");

const phoneStreams = await Promise.all(phones.map(async (phone) => {
  const peer = await open(`/v2/ws?machine_rid=${machine.rid}`);
  await authenticate(peer, phone, "phone", "stream");
  return peer;
}));
for (let i = 0; i < phoneStreams.length; i++) {
  const result = await request(phoneStreams[i], "APPEND", `reverse-${i}`, { peer_rid: machine.rid, generation: "1", msg_id: `reverse-${i}`, ciphertext: "AA" });
  assert.equal(result.type, "APPENDED");
}
for (let i = 1; i < 268; i++) {
  const phone = phones[i % phones.length];
  const result = await request(stream, "APPEND", `append-${i}`, { peer_rid: phone.rid, generation: "1", msg_id: `alarm-${i}`, ciphertext: "AA" });
  assert.equal(result.type, "APPENDED");
}
const pair = await request(control, "PAIR_CREATE", "pair-create-cost", { ceremony: "ff".repeat(16) });
assert.equal(pair.type, "PAIR_CREATED");
const many = await request(control, "TEST_ALARM", "alarm-300-multistream");
assert.equal(many.type, "TEST_ALARMED");
assert.equal(many.due, pair.expires_at, "alarm retains the earliest indexed rendezvous expiry");
assert.ok(many.rows_read <= one.rows_read + 256, `alarm reads exceeded the 64-stream bound: one=${one.rows_read}, many=${many.rows_read}`);

control.ws.close();
stream.ws.close();
for (const peer of phoneStreams) peer.ws.close();
console.log(`PASS alarm rowsRead bounded one=${one.rows_read} many=${many.rows_read} statements=${many.statements}`);
