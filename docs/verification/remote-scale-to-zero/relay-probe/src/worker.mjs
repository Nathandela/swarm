const encoder = new TextEncoder();
// TEST FIXTURE ONLY: this shared HMAC does not resemble Swarm pairing auth.
// Production must implement Swarm Ed25519 auth and canonical home/epoch checks;
// this ticket is not a proposed production credential format.

function b64url(bytes) {
  return btoa(String.fromCharCode(...bytes)).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}
const ZERO_CURSOR = "00000000000000000000";
function cursorKey(value) {
  if (!/^\d{1,20}$/.test(value)) throw new Error("cursor must be a decimal uint64 string");
  const n = BigInt(value);
  if (n > 18446744073709551615n) throw new Error("cursor out of uint64 range");
  return n.toString().padStart(20, "0");
}
function nextCursor(value) { return cursorKey((BigInt(value) + 1n).toString()); }

async function proof(secret, route, client) {
  const key = await crypto.subtle.importKey("raw", encoder.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return b64url(new Uint8Array(await crypto.subtle.sign("HMAC", key, encoder.encode(`${route}:${client}`))));
}

// The route is only a deterministic sharding hint. The DO verifies the signed
// ticket before creating a WebSocket; neither route nor client is authority.
export default {
  fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname !== "/v2/ws") return new Response("not found", { status: 404 });
    const route = url.searchParams.get("route");
    if (!route || !/^[a-z0-9-]{1,64}$/.test(route)) return new Response("bad route", { status: 400 });
    return env.RELAY.get(env.RELAY.idFromName(route)).fetch(request);
  },
};

export class Relay {
  constructor(state, env) { this.state = state; this.env = env; }
  retentionMs() { return Number(this.env.RETENTION_MS || "604800000"); }

  async fetch(request) {
    const url = new URL(request.url);
    const route = url.searchParams.get("route");
    const client = url.searchParams.get("client");
    const ticket = url.searchParams.get("ticket");
    if (request.headers.get("Upgrade") !== "websocket" || !route || !["phone", "machine"].includes(client) || !ticket ||
        ticket !== await proof(this.env.AUTH_SECRET, route, client)) {
      return new Response("unauthorized", { status: 401 });
    }
    if (await this.state.storage.get("revoked")) return new Response("revoked", { status: 403 });
    const pair = new WebSocketPair();
    const server = pair[1];
    server.serializeAttachment({ client, route, v: 2 });
    this.state.acceptWebSocket(server);
    await this.state.storage.put(`presence:${client}`, Date.now());
    server.send(JSON.stringify({ type: "READY", v: 2, client, cursor: await this.state.storage.get(`ack:${client}`) || ZERO_CURSOR }));
    await this.catchup(server, client);
    return new Response(null, { status: 101, webSocket: pair[0] });
  }

  peers(client) {
    return this.state.getWebSockets().filter((ws) => ws.deserializeAttachment()?.client === client);
  }
  async catchup(ws, client) {
    const ack = await this.state.storage.get(`ack:${client}`) || ZERO_CURSOR;
    const next = await this.state.storage.get(`next:${client}`) || ZERO_CURSOR;
    for (let cursor = nextCursor(ack); cursor <= next; cursor = nextCursor(cursor)) {
      const entry = await this.state.storage.get(`mail:${client}:${cursor}`);
      if (entry) ws.send(JSON.stringify({ type: "DELIVER", cursor, ...entry }));
    }
  }
  async webSocketMessage(ws, raw) {
    const sender = ws.deserializeAttachment();
    if (!sender || sender.v !== 2 || await this.state.storage.get("revoked")) { ws.close(4003, "revoked"); return; }
    let m; try { m = JSON.parse(raw); } catch { ws.close(4000, "bad frame"); return; }
    if (m.type === "APPEND" && ["phone", "machine"].includes(m.target) && m.target !== sender.client && typeof m.ciphertext === "string") {
      const next = nextCursor(await this.state.storage.get(`next:${m.target}`) || ZERO_CURSOR);
      // Durably store the opaque E2EE envelope before any best-effort send.
      // ttl_ms exists solely to make the local alarm test deterministic. Production
      // retention is server policy and must never be caller-controlled.
      const ttlMs = Number.isInteger(m.ttl_ms) && m.ttl_ms >= 100 && m.ttl_ms <= 10_000
        ? m.ttl_ms : this.retentionMs();
      const entry = { sender: sender.client, msg_id: m.msg_id, ciphertext: m.ciphertext, created_at: Date.now(), ttl_ms: ttlMs };
      await this.state.storage.put({ [`mail:${m.target}:${next}`]: entry, [`next:${m.target}`]: next });
      // Alarms execute only when due; unlike setInterval, they do not keep a DO resident.
      const due = entry.created_at + entry.ttl_ms;
      const existing = await this.state.storage.getAlarm();
      await this.state.storage.setAlarm(existing === null ? due : Math.min(existing, due));
      for (const peer of this.peers(m.target)) peer.send(JSON.stringify({ type: "DELIVER", cursor: next, ...entry }));
      ws.send(JSON.stringify({ type: "APPENDED", target: m.target, cursor: next }));
      return;
    }
    if (m.type === "ACK" && typeof m.cursor === "string") {
      let cursor; try { cursor = cursorKey(m.cursor); } catch { ws.close(4000, "bad cursor"); return; }
      const prior = await this.state.storage.get(`ack:${sender.client}`) || ZERO_CURSOR;
      const high = await this.state.storage.get(`next:${sender.client}`) || ZERO_CURSOR;
      if (cursor > high) { ws.close(4000, "ack beyond high-water"); return; }
      if (cursor > prior) await this.state.storage.put(`ack:${sender.client}`, cursor);
      return;
    }
    if (m.type === "REVOKE") {
      await this.state.storage.put("revoked", true);
      for (const peer of this.state.getWebSockets()) peer.close(4003, "revoked");
      return;
    }
    ws.close(4000, "unsupported or unauthorized frame");
  }
  async webSocketClose(ws) {
    const attachment = ws.deserializeAttachment();
    if (attachment) await this.state.storage.put(`presence:${attachment.client}`, { disconnected_at: Date.now() });
  }
  async alarm() {
    const now = Date.now();
    const entries = await this.state.storage.list({ prefix: "mail:" });
    const deleteKeys = [];
    let nextDue = null;
    for (const [key, entry] of entries) {
      const due = entry.created_at + entry.ttl_ms;
      if (due <= now) deleteKeys.push(key);
      else nextDue = nextDue === null ? due : Math.min(nextDue, due);
    }
    if (deleteKeys.length) await this.state.storage.delete(deleteKeys);
    if (nextDue !== null) await this.state.storage.setAlarm(nextDue);
  }
}
