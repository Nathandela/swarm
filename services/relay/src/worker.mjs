const enc = new TextEncoder();
const MAX_MESSAGE_BYTES = 1 << 20;
const MAX_CIPHERTEXT_CHARS = MAX_MESSAGE_BYTES - 1024;
const MAX_UINT64 = (1n << 64n) - 1n;
const ZERO_KEY = "00000000000000000000";
const HOME_DOMAIN = enc.encode("swarm-relay-home/v1");
const AUTH_CONTEXT = enc.encode("swarm-relay-auth-v2\0");
const AUTH_TTL_MS = 30_000;
const PAIR_TTL_MS = 60_000;
const RETENTION_MS = 7 * 24 * 60 * 60 * 1000;
const MAX_PAIRINGS = 8;
const MAX_DIRECTORY_ENTRIES = 1024;
const MAX_ITEMS = 10_000;
const MAX_ACKED_RECEIPTS = 256;
const MAX_HOME_BYTES = 8 * 1024 * 1024 * 1024;
const MAX_MEMBERS = 32;
const MAX_RETIREMENTS = 64;
const MAX_CONNECTIONS = 64;
const MAX_OPS_PER_MINUTE = 600;
const MAX_PAIR_FRAMES = 8;
const MAX_PAIR_BYTES = 256 * 1024;
const MAX_PAIR_CIPHERTEXT_CHARS = Math.ceil(MAX_PAIR_BYTES / 3) * 4;
const MAX_INFLIGHT_FRAMES = 64;
const MAX_INFLIGHT_BYTES = 1 << 20;
const CLEANUP_BATCH = 256;

class ProtocolError extends Error {
  constructor(code) { super(code); this.code = code; }
}
const protocolError = (code) => { throw new ProtocolError(code); };
const costDelta = (before, after) => before && after ? {
  rowsRead: after.rowsRead - before.rowsRead,
  rowsWritten: after.rowsWritten - before.rowsWritten,
  statements: after.statements - before.statements,
} : null;

function allowedMachines(env) {
  const raw = env.ALLOWED_MACHINE_RIDS;
  if (!raw) return null;
  const ids = raw.split(",").map((v) => v.trim());
  if (!ids.length || ids.some((v) => !/^[0-9a-f]{32}$/.test(v))) return null;
  return new Set(ids);
}

function validOperatorNamespace(value) {
  return typeof value === "string" && value.length >= 1 && value.length <= 64 &&
    value[0] >= "a" && value[0] <= "z" && !/[^a-z0-9-]/.test(value);
}

function u32(n) {
  const out = new Uint8Array(4);
  new DataView(out.buffer).setUint32(0, n);
  return out;
}

function concat(...parts) {
  const size = parts.reduce((n, part) => n + part.length, 0);
  const out = new Uint8Array(size);
  let offset = 0;
  for (const part of parts) { out.set(part, offset); offset += part.length; }
  return out;
}

function field(bytes) { return concat(u32(bytes.length), bytes); }
function hex(bytes) { return [...bytes].map((v) => v.toString(16).padStart(2, "0")).join(""); }

function decodeBase64URL(value, length) {
  if (typeof value !== "string" || !/^[A-Za-z0-9_-]+$/.test(value)) protocolError("invalid_base64url");
  let raw;
  try {
    const padded = value.replaceAll("-", "+").replaceAll("_", "/") + "=".repeat((4 - value.length % 4) % 4);
    raw = Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
  } catch { protocolError("invalid_base64url"); }
  if (length !== undefined && raw.length !== length) protocolError("invalid_base64url");
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
  const remainder = value.length % 4;
  const last = alphabet.indexOf(value.at(-1));
  if (remainder === 1 || (remainder === 2 && (last & 15) !== 0) || (remainder === 3 && (last & 3) !== 0)) protocolError("invalid_base64url");
  return raw;
}

function randomToken(length) {
  const raw = crypto.getRandomValues(new Uint8Array(length));
  return btoa(String.fromCharCode(...raw)).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function parseUint64(value) {
  if (typeof value !== "string" || !/^(0|[1-9][0-9]{0,19})$/.test(value)) protocolError("invalid_uint64");
  const n = BigInt(value);
  if (n > MAX_UINT64) protocolError("invalid_uint64");
  return n;
}

function cursorKey(value) { return parseUint64(value).toString().padStart(20, "0"); }
function wireCursor(key) { return BigInt(key).toString(); }
function nextCursor(key) {
  const n = BigInt(key);
  if (n === MAX_UINT64) protocolError("cursor_exhausted");
  return (n + 1n).toString().padStart(20, "0");
}

function exact(message, fields) {
  const keys = Object.keys(message).sort();
  const wanted = [...fields, "request_id", "type", "v"].sort();
  if (keys.length !== wanted.length || keys.some((key, i) => key !== wanted[i])) protocolError("invalid_fields");
  if (message.v !== 2 || typeof message.request_id !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(message.request_id)) {
    protocolError("invalid_envelope");
  }
}

async function routingID(pub) {
  const material = await crypto.subtle.importKey("raw", pub, "HKDF", false, ["deriveBits"]);
  return hex(new Uint8Array(await crypto.subtle.deriveBits({
    name: "HKDF", hash: "SHA-256", salt: enc.encode("swarm-relay-routing-id-v1"), info: enc.encode("routing-id"),
  }, material, 128)));
}

async function homeID(namespace, machineRID) {
  const body = concat(field(HOME_DOMAIN), field(enc.encode(namespace)), field(enc.encode(machineRID)));
  return hex(new Uint8Array(await crypto.subtle.digest("SHA-256", body)));
}

function authMessage(nonce, rid, home, role, purpose) {
  return concat(AUTH_CONTEXT, field(nonce), field(enc.encode(rid)), field(enc.encode(home)), field(enc.encode(role)), field(enc.encode(purpose)));
}

function consentMessage(ceremony, machineRID) {
  return concat(enc.encode("swarm-relay-consent-v1\0"), field(enc.encode(ceremony)), enc.encode(machineRID));
}

function parseConsent(value) {
  const raw = decodeBase64URL(value);
  if (raw.length < 4 + 1 + 64) protocolError("invalid_consent");
  const size = new DataView(raw.buffer, raw.byteOffset, 4).getUint32(0);
  if (size < 1 || size > 128 || raw.length !== 4 + size + 64) protocolError("invalid_consent");
  const ceremony = new TextDecoder("utf-8", { fatal: true }).decode(raw.slice(4, 4 + size));
  if (!/^[0-9a-f]{32}$/.test(ceremony)) protocolError("invalid_consent");
  return { ceremony, signature: raw.slice(4 + size) };
}

function forwarded(request, machineRID, home, pairCeremony = "") {
  const headers = new Headers(request.headers);
  headers.set("x-swarm-machine-rid", machineRID);
  headers.set("x-swarm-home", home);
  headers.delete("x-swarm-pair-ceremony");
  if (pairCeremony) headers.set("x-swarm-pair-ceremony", pairCeremony);
  return new Request(request, { headers });
}

async function checkPreAuthRateLimit(env, key) {
  let result;
  try {
    const limiter = env.RATE_LIMITER;
    if (typeof limiter?.limit !== "function") throw new Error("missing rate limiter");
    result = await limiter.limit({ key });
  } catch {
    return new Response("relay rate limit is unavailable", { status: 503 });
  }
  if (result?.success === true) return null;
  if (result?.success === false) {
    return new Response("relay rate limit exceeded", { status: 429, headers: { "Retry-After": "60" } });
  }
  return new Response("relay rate limit is unavailable", { status: 503 });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === "/") return new Response("swarm relay v2");
    const allowed = allowedMachines(env);
    if (!allowed || !validOperatorNamespace(env.OPERATOR_NAMESPACE)) return new Response("relay admission is not configured", { status: 503 });

    let machineRID;
    let ceremony = "";
    if (url.pathname === "/v2/ws") {
      machineRID = url.searchParams.get("machine_rid") || "";
      if (!allowed.has(machineRID)) return new Response("machine not admitted", { status: 403 });
    } else if (url.pathname === "/v2/pair") {
      ceremony = url.searchParams.get("ceremony") || "";
      if (!/^[0-9a-f]{32}$/.test(ceremony)) return new Response("bad ceremony", { status: 400 });
    } else {
      return new Response("not found", { status: 404 });
    }
    if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") return new Response("websocket required", { status: 426 });
    const limited = await checkPreAuthRateLimit(env, url.pathname === "/v2/ws" ? "ws" : "pair");
    if (limited) return limited;
    if (url.pathname === "/v2/pair") {
      const directory = env.RENDEZVOUS.get(env.RENDEZVOUS.idFromName("v2"));
      const resolved = await directory.fetch(`https://rendezvous.invalid/resolve?ceremony=${ceremony}`);
      if (!resolved.ok) return new Response("pairing not found", { status: resolved.status });
      machineRID = (await resolved.json()).machine_rid;
      if (!allowed.has(machineRID)) return new Response("machine not admitted", { status: 403 });
    }
    const home = await homeID(env.OPERATOR_NAMESPACE, machineRID);
    return env.HOMES.get(env.HOMES.idFromName(home)).fetch(forwarded(request, machineRID, home, ceremony));
  },
};

export class RendezvousDirectory {
  constructor(state, env) { this.state = state; this.env = env; }

  async fetch(request) {
    const url = new URL(request.url);
    const ceremony = url.searchParams.get("ceremony") || "";
    if (!/^[0-9a-f]{32}$/.test(ceremony)) return new Response("bad ceremony", { status: 400 });
    const key = `r:${ceremony}`;
    if (url.pathname === "/resolve") {
      const entry = await this.state.storage.get(key);
      if (!entry || entry.expires_at <= Date.now()) return new Response("not found", { status: 404 });
      return Response.json({ machine_rid: entry.machine_rid });
    }
    if (url.pathname === "/register" && request.method === "POST") {
      const body = await request.json();
      if (!/^[0-9a-f]{32}$/.test(body.machine_rid) || !Number.isSafeInteger(body.expires_at)) return new Response("bad request", { status: 400 });
      const now = Date.now();
      const entries = await this.state.storage.list({ prefix: "r:", limit: MAX_DIRECTORY_ENTRIES + 1 });
      const expired = [...entries].filter(([, value]) => value.expires_at <= now).slice(0, CLEANUP_BATCH).map(([name]) => name);
      if (expired.length) await this.state.storage.delete(expired);
      const existing = entries.get(key);
      if (existing && existing.expires_at > now) {
        return existing.machine_rid === body.machine_rid ? new Response(null, { status: 204 }) : new Response("conflict", { status: 409 });
      }
      if (entries.size - expired.length >= MAX_DIRECTORY_ENTRIES && !entries.has(key)) return new Response("full", { status: 429 });
      await this.state.storage.put(key, { machine_rid: body.machine_rid, expires_at: body.expires_at });
      const alarm = await this.state.storage.getAlarm();
      if (alarm === null || body.expires_at < alarm) await this.state.storage.setAlarm(body.expires_at);
      return new Response(null, { status: 204 });
    }
    if (url.pathname === "/finish" && request.method === "POST") {
      const body = await request.json();
      const existing = await this.state.storage.get(key);
      if (existing?.machine_rid === body.machine_rid) await this.state.storage.delete(key);
      return new Response(null, { status: 204 });
    }
    return new Response("not found", { status: 404 });
  }

  async alarm() {
    const entries = await this.state.storage.list({ prefix: "r:", limit: MAX_DIRECTORY_ENTRIES + 1 });
    const now = Date.now();
    const expired = [...entries].filter(([, value]) => value.expires_at <= now).slice(0, CLEANUP_BATCH).map(([key]) => key);
    if (expired.length) await this.state.storage.delete(expired);
    const rest = await this.state.storage.list({ prefix: "r:", limit: MAX_DIRECTORY_ENTRIES + 1 });
    let next = null;
    for (const entry of rest.values()) next = next === null ? entry.expires_at : Math.min(next, entry.expires_at);
    if (next !== null) await this.state.storage.setAlarm(next <= now ? now + 1 : next);
  }
}

export class RelayHome {
  constructor(state, env) { this.state = state; this.env = env; this.initPromise = null; }

  async fetch(request) {
    const machineRID = request.headers.get("x-swarm-machine-rid") || "";
    const home = request.headers.get("x-swarm-home") || "";
    if (!/^[0-9a-f]{32}$/.test(machineRID) || !/^[0-9a-f]{64}$/.test(home)) return new Response("bad route", { status: 400 });
    if (this.state.getWebSockets().length >= MAX_CONNECTIONS) return new Response("home connection limit", { status: 429 });
    const pair = new WebSocketPair();
    const server = pair[1];
    const expiresAt = Date.now() + Number(this.env.CHALLENGE_TTL_MS || AUTH_TTL_MS);
    server.serializeAttachment({ phase: "new", machineRID, home, pairRoute: request.headers.get("x-swarm-pair-ceremony") || "", expiresAt });
    this.state.acceptWebSocket(server);
    await this.scheduleAlarm(expiresAt);
    return new Response(null, { status: 101, webSocket: pair[0] });
  }

  recordCost(cursor) {
    if (this.env.TEST_COST_METRICS === "1" && this.testCost) {
      this.testCost.rowsRead += cursor.rowsRead;
      this.testCost.rowsWritten += cursor.rowsWritten;
      this.testCost.statements++;
    }
  }

  exec(query, ...args) {
    const cursor = this.state.storage.sql.exec(query, ...args);
    if (!/^\s*SELECT\b/i.test(query) && !/\bRETURNING\b/i.test(query)) {
      for (const _ of cursor) { /* exhaust for final billing counters */ }
      this.recordCost(cursor);
    }
    return cursor;
  }

  row(query, ...args) {
    const cursor = this.state.storage.sql.exec(query, ...args);
    const result = Array.from(cursor)[0];
    this.recordCost(cursor);
    return result;
  }

  async initialized() { return await this.state.storage.get("initialized") === true; }

  async initialize(machineRID, pub) {
    if (await this.initialized()) {
      const stored = this.row("SELECT value FROM meta WHERE key='machine_rid'");
      if (!stored || stored.value !== machineRID) protocolError("wrong_home");
      return;
    }
    if (!this.initPromise) this.initPromise = this.initializeOnce(machineRID, pub);
    return await this.initPromise;
  }

  async initializeOnce(machineRID, pub) {
    for (const statement of [
      "CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
      "CREATE TABLE IF NOT EXISTS members (phone_rid TEXT PRIMARY KEY, pub TEXT NOT NULL, generation TEXT NOT NULL, ceremony TEXT NOT NULL, status TEXT NOT NULL)",
      "CREATE TABLE IF NOT EXISTS retired (phone_rid TEXT NOT NULL, ceremony TEXT NOT NULL, PRIMARY KEY(phone_rid, ceremony))",
      "CREATE TABLE IF NOT EXISTS streams (recipient TEXT NOT NULL, sender TEXT NOT NULL, generation TEXT NOT NULL, incarnation TEXT NOT NULL, next_cursor TEXT NOT NULL, ack_cursor TEXT NOT NULL, item_count INTEGER NOT NULL, item_bytes INTEGER NOT NULL, acked_receipts INTEGER NOT NULL, discard_old_incarnation TEXT, discard_through_cursor TEXT, PRIMARY KEY(recipient, sender, generation))",
      "CREATE TABLE IF NOT EXISTS items (recipient TEXT NOT NULL, sender TEXT NOT NULL, generation TEXT NOT NULL, cursor TEXT NOT NULL, msg_id TEXT NOT NULL, digest TEXT NOT NULL, ciphertext TEXT NOT NULL, size INTEGER NOT NULL, expires_at INTEGER NOT NULL, PRIMARY KEY(recipient, sender, generation, cursor))",
      "CREATE UNIQUE INDEX IF NOT EXISTS items_message ON items(recipient, sender, generation, msg_id)",
      "CREATE INDEX IF NOT EXISTS items_delivery ON items(recipient, sender, generation, cursor, expires_at)",
      "CREATE INDEX IF NOT EXISTS items_expiry ON items(expires_at)",
      "CREATE TABLE IF NOT EXISTS receipts (recipient TEXT NOT NULL, sender TEXT NOT NULL, generation TEXT NOT NULL, msg_id TEXT NOT NULL, digest TEXT NOT NULL, cursor TEXT NOT NULL, expires_at INTEGER NOT NULL, PRIMARY KEY(recipient, sender, generation, msg_id))",
      "CREATE INDEX IF NOT EXISTS receipts_expiry ON receipts(expires_at)",
      "CREATE INDEX IF NOT EXISTS receipts_cursor ON receipts(recipient,sender,generation,cursor)",
      "CREATE TABLE IF NOT EXISTS usage (singleton INTEGER PRIMARY KEY CHECK(singleton=1), item_bytes INTEGER NOT NULL)",
      "CREATE TABLE IF NOT EXISTS rendezvous (ceremony TEXT PRIMARY KEY, expires_at INTEGER NOT NULL, claimed INTEGER NOT NULL DEFAULT 0)",
      "CREATE INDEX IF NOT EXISTS rendezvous_expiry ON rendezvous(expires_at)",
    ]) this.exec(statement);
    this.state.storage.transactionSync(() => {
      this.exec("INSERT OR REPLACE INTO meta(key,value) VALUES('machine_rid',?),('machine_pub',?)", machineRID, pub);
      this.exec("INSERT OR IGNORE INTO usage(singleton,item_bytes) VALUES(1,0)");
    });
    await this.state.storage.put("initialized", true);
  }

  send(ws, type, requestID, fields = {}) {
    ws.send(JSON.stringify({ v: 2, type, request_id: requestID, ...fields }));
  }

  error(ws, requestID, code) {
    try { this.send(ws, "ERROR", requestID || "invalid", { code }); } catch { ws.close(4000, code.slice(0, 120)); }
  }

  async webSocketMessage(ws, raw) {
    let requestID = "invalid";
    try {
      if (typeof raw !== "string" || enc.encode(raw).length > MAX_MESSAGE_BYTES) protocolError("message_too_large");
      let message;
      try { message = JSON.parse(raw); } catch { protocolError("invalid_json"); }
      if (!message || typeof message !== "object" || Array.isArray(message)) protocolError("invalid_envelope");
      requestID = typeof message.request_id === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(message.request_id) ? message.request_id : requestID;
      let attachment = ws.deserializeAttachment();
      if (!attachment) protocolError("invalid_session");
      if (attachment.phase === "new") {
        if (message.type === "AUTH_INIT") return await this.authInit(ws, attachment, message);
        if (message.type === "PAIR_CLAIM") return await this.pairClaim(ws, attachment, message);
        protocolError("auth_required");
      }
      if (attachment.phase === "challenge") return await this.authProve(ws, attachment, message);
      if (attachment.phase === "pair") return await this.pairMessage(ws, attachment, message);
      if (attachment.phase !== "authed") protocolError("auth_required");
      attachment = this.meter(ws, attachment);
      switch (message.type) {
        case "TEST_ALARM": {
          if (this.env.TEST_COST_METRICS !== "1") protocolError("unsupported_type");
          exact(message, []);
          const result = await this.alarm(message.request_id);
          return this.send(ws, "TEST_ALARMED", message.request_id, {
            due: result.due === null || result.due === undefined ? null : String(result.due),
            rows_read: result.cost.rowsRead,
            rows_written: result.cost.rowsWritten,
            statements: result.cost.statements,
          });
        }
        case "AUTHORIZE": return await this.authorize(ws, attachment, message);
        case "APPEND": return await this.append(ws, attachment, message);
        case "SUBSCRIBE": return await this.subscribe(ws, attachment, message);
        case "PROBE": return await this.probe(ws, attachment, message);
        case "ACK": return await this.ack(ws, attachment, message);
        case "DISCARD": return await this.discard(ws, attachment, message);
        case "REVOKE": return await this.revoke(ws, attachment, message);
        case "PAIR_CREATE": return await this.pairCreate(ws, attachment, message);
        case "PAIR_SEND": return await this.pairMessage(ws, attachment, message);
        case "PAIR_FINISH": return await this.pairMessage(ws, attachment, message);
        default: protocolError("unsupported_type");
      }
    } catch (error) {
      this.error(ws, requestID, error instanceof ProtocolError ? error.code : "internal_error");
    }
  }

  meter(ws, attachment) {
    const minute = Math.floor(Date.now() / 60_000);
    const rate = attachment.rate?.minute === minute ? attachment.rate : { minute, count: 0 };
    if (++rate.count > MAX_OPS_PER_MINUTE) protocolError("rate_limited");
    const next = { ...attachment, rate };
    ws.serializeAttachment(next);
    return next;
  }

  async authInit(ws, attachment, message) {
    exact(message, ["pub", "purpose", "role"]);
    if (message.role !== "machine" && message.role !== "phone") protocolError("invalid_role");
    if (!(["control", "stream"].includes(message.purpose)) || (message.role === "phone" && message.purpose !== "stream")) protocolError("invalid_purpose");
    const pub = decodeBase64URL(message.pub, 32);
    ws.serializeAttachment({ ...attachment, phase: "initializing" });
    const rid = await routingID(pub);
    if (message.role === "machine" && rid !== attachment.machineRID) protocolError("role_mismatch");
    const nonce = crypto.getRandomValues(new Uint8Array(32));
    const expiresAt = Date.now() + Number(this.env.CHALLENGE_TTL_MS || AUTH_TTL_MS);
    const next = { ...attachment, phase: "challenge", role: message.role, purpose: message.purpose, pub: message.pub, rid, nonce: btoa(String.fromCharCode(...nonce)), expiresAt };
    ws.serializeAttachment(next);
    this.send(ws, "CHALLENGE", message.request_id, { nonce: btoa(String.fromCharCode(...nonce)).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, ""), home: attachment.home, expires_at: String(expiresAt) });
  }

  async authProve(ws, attachment, message) {
    exact(message, ["signature"]);
    if (message.type !== "AUTH_PROVE") protocolError("auth_required");
    if (Date.now() > attachment.expiresAt) protocolError("auth_expired");
    ws.serializeAttachment({ ...attachment, phase: "verifying", nonce: undefined });
    const signature = decodeBase64URL(message.signature, 64);
    const pub = decodeBase64URL(attachment.pub, 32);
    const nonce = Uint8Array.from(atob(attachment.nonce), (c) => c.charCodeAt(0));
    const key = await crypto.subtle.importKey("raw", pub, "Ed25519", false, ["verify"]);
    if (!await crypto.subtle.verify("Ed25519", key, signature, authMessage(nonce, attachment.rid, attachment.home, attachment.role, attachment.purpose))) {
      protocolError("auth_failed");
    }
    if (Date.now() > attachment.expiresAt) protocolError("auth_expired");
    if (attachment.role === "machine") {
      await this.initialize(attachment.machineRID, attachment.pub);
    } else {
      if (!await this.initialized()) protocolError("unknown_home");
      const member = this.row("SELECT pub,generation,status FROM members WHERE phone_rid=?", attachment.rid);
      if (!member || member.pub !== attachment.pub || member.status !== "live") protocolError("not_authorized");
      attachment.generation = wireCursor(member.generation);
    }
    for (const other of this.state.getWebSockets()) {
      if (other === ws) continue;
      const old = other.deserializeAttachment();
      if (old?.phase === "authed" && old.rid === attachment.rid && old.purpose === attachment.purpose) other.close(4001, "superseded");
    }
    const authed = { ...attachment, phase: "authed", nonce: undefined, expiresAt: undefined, sub: undefined };
    ws.serializeAttachment(authed);
    this.send(ws, "AUTHENTICATED", message.request_id, { rid: attachment.rid, role: attachment.role, purpose: attachment.purpose, home: attachment.home, ...(attachment.role === "phone" ? { generation: attachment.generation } : {}) });
  }

  requireMachine(attachment) { if (attachment.role !== "machine" || attachment.rid !== attachment.machineRID || attachment.purpose !== "control") protocolError("not_authorized"); }
  requireStream(attachment) { if (attachment.purpose !== "stream") protocolError("not_authorized"); }

  liveBinding(attachment, peerRID, generation) {
    if (!/^[0-9a-f]{32}$/.test(peerRID)) protocolError("invalid_peer");
    const supplied = cursorKey(generation);
    const phoneRID = attachment.role === "machine" ? peerRID : attachment.rid;
    if (attachment.role === "phone" && peerRID !== attachment.machineRID) protocolError("invalid_peer");
    const member = this.row("SELECT pub,generation,status FROM members WHERE phone_rid=?", phoneRID);
    if (!member || member.status !== "live" || member.generation !== supplied) protocolError("stale_generation");
    if (attachment.role === "phone" && member.pub !== attachment.pub) protocolError("not_authorized");
    return { phoneRID, generation: supplied };
  }

  async authorize(ws, attachment, message) {
    exact(message, ["consent", "phone_pub"]);
    this.requireMachine(attachment);
    const pub = decodeBase64URL(message.phone_pub, 32);
    const phoneRID = await routingID(pub);
    if (phoneRID === attachment.machineRID) protocolError("not_authorized");
    const credential = parseConsent(message.consent);
    const key = await crypto.subtle.importKey("raw", pub, "Ed25519", false, ["verify"]);
    if (!await crypto.subtle.verify("Ed25519", key, credential.signature, consentMessage(credential.ceremony, attachment.machineRID))) protocolError("invalid_consent");
    const result = this.state.storage.transactionSync(() => {
      if (this.row("SELECT 1 AS yes FROM retired WHERE phone_rid=? AND ceremony=?", phoneRID, credential.ceremony)) protocolError("consent_retired");
      const old = this.row("SELECT generation,ceremony,status FROM members WHERE phone_rid=?", phoneRID);
      if (old?.status === "live" && old.ceremony === credential.ceremony) return { generation: old.generation, changed: false };
      if (!old && this.row("SELECT COUNT(*) AS n FROM members").n >= MAX_MEMBERS) protocolError("member_limit");
      if (old?.ceremony && old.ceremony !== credential.ceremony) {
        const count = this.row("SELECT COUNT(*) AS n FROM retired WHERE phone_rid=?", phoneRID).n;
        if (count >= MAX_RETIREMENTS) protocolError("retirement_limit");
        this.exec("INSERT OR IGNORE INTO retired(phone_rid,ceremony) VALUES(?,?)", phoneRID, old.ceremony);
      }
      const prior = old ? BigInt(old.generation) : 0n;
      if (prior === MAX_UINT64) protocolError("generation_exhausted");
      const generation = (prior + 1n).toString().padStart(20, "0");
      this.exec("INSERT OR REPLACE INTO members(phone_rid,pub,generation,ceremony,status) VALUES(?,?,?,?, 'live')", phoneRID, message.phone_pub, generation, credential.ceremony);
      this.purgeBinding(phoneRID);
      return { generation, changed: true };
    });
    if (result.changed) this.closeBinding(phoneRID, "re-paired");
    this.send(ws, "AUTHORIZED", message.request_id, { phone_rid: phoneRID, generation: wireCursor(result.generation) });
  }

  purgeBinding(phoneRID) {
    const machineRID = this.machineRID();
    this.deleteItems("DELETE FROM items WHERE (recipient=? AND sender=?) OR (recipient=? AND sender=?)", phoneRID, machineRID, machineRID, phoneRID);
    for (const table of ["receipts", "streams"]) this.exec(`DELETE FROM ${table} WHERE (recipient=? AND sender=?) OR (recipient=? AND sender=?)`, phoneRID, machineRID, machineRID, phoneRID);
  }

  machineRID() { return this.row("SELECT value FROM meta WHERE key='machine_rid'").value; }

  deleteItems(statement, ...args) {
    const removed = new Map();
    let itemCount = 0;
    let homeBytes = 0;
    const cursor = this.state.storage.sql.exec(`${statement} RETURNING recipient,sender,generation,size`, ...args);
    for (const item of cursor) {
      const key = `${item.recipient}\0${item.sender}\0${item.generation}`;
      const prior = removed.get(key) || { ...item, count: 0, bytes: 0 };
      prior.count++;
      prior.bytes += item.size;
      itemCount++;
      homeBytes += item.size;
      removed.set(key, prior);
    }
    this.recordCost(cursor);
    for (const value of removed.values()) this.exec(
      "UPDATE streams SET item_count=MAX(0,item_count-?),item_bytes=MAX(0,item_bytes-?) WHERE recipient=? AND sender=? AND generation=?",
      value.count, value.bytes, value.recipient, value.sender, value.generation,
    );
    if (homeBytes) this.exec("UPDATE usage SET item_bytes=MAX(0,item_bytes-?) WHERE singleton=1", homeBytes);
    return itemCount;
  }

  deleteReceipts(statement, ...args) {
    const cursor = this.state.storage.sql.exec(`${statement} RETURNING recipient,sender,generation,cursor`, ...args);
    const removed = new Map();
    const receipts = Array.from(cursor);
    this.recordCost(cursor);
    for (const receipt of receipts) {
      const stream = this.row("SELECT ack_cursor FROM streams WHERE recipient=? AND sender=? AND generation=?", receipt.recipient, receipt.sender, receipt.generation);
      if (stream && receipt.cursor <= stream.ack_cursor) {
        const key = `${receipt.recipient}\0${receipt.sender}\0${receipt.generation}`;
        const prior = removed.get(key) || { ...receipt, count: 0 };
        prior.count++;
        removed.set(key, prior);
      }
    }
    for (const value of removed.values()) this.exec(
      "UPDATE streams SET acked_receipts=MAX(0,acked_receipts-?) WHERE recipient=? AND sender=? AND generation=?",
      value.count, value.recipient, value.sender, value.generation,
    );
    return receipts.length;
  }

  pruneReceiptWindow(recipient, sender, generation, limit = CLEANUP_BATCH) {
    const count = this.row("SELECT acked_receipts FROM streams WHERE recipient=? AND sender=? AND generation=?", recipient, sender, generation)?.acked_receipts || 0;
    const excess = Math.min(Math.max(0, count - MAX_ACKED_RECEIPTS), limit);
    if (!excess) return 0;
    const cursor = this.state.storage.sql.exec(
      "DELETE FROM receipts WHERE rowid IN (SELECT rowid FROM receipts WHERE recipient=? AND sender=? AND generation=? AND cursor<=(SELECT ack_cursor FROM streams WHERE recipient=? AND sender=? AND generation=?) ORDER BY cursor LIMIT ?) RETURNING cursor",
      recipient, sender, generation, recipient, sender, generation, excess,
    );
    let removed = 0;
    for (const _ of cursor) removed++;
    this.recordCost(cursor);
    if (removed) this.exec("UPDATE streams SET acked_receipts=MAX(0,acked_receipts-?) WHERE recipient=? AND sender=? AND generation=?", removed, recipient, sender, generation);
    return removed;
  }

  streamSides(attachment, peerRID) {
    return attachment.role === "machine"
      ? { sender: attachment.rid, recipient: peerRID }
      : { sender: attachment.rid, recipient: attachment.machineRID };
  }

  async append(ws, attachment, message) {
    if (this.env.TEST_COST_METRICS === "1" && message.request_id === "cost-append-0") this.testCost = { rowsRead: 0, rowsWritten: 0, statements: 0 };
    const costBefore = this.testCost ? { ...this.testCost } : null;
    exact(message, ["ciphertext", "generation", "msg_id", "peer_rid"]);
    this.requireStream(attachment);
    const binding = this.liveBinding(attachment, message.peer_rid, message.generation);
    if (typeof message.msg_id !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(message.msg_id)) protocolError("invalid_msg_id");
    if (typeof message.ciphertext !== "string" || message.ciphertext.length > MAX_CIPHERTEXT_CHARS) protocolError("message_too_large");
    const ciphertext = decodeBase64URL(message.ciphertext);
    const digest = hex(new Uint8Array(await crypto.subtle.digest("SHA-256", ciphertext)));
    const { sender, recipient } = this.streamSides(attachment, message.peer_rid);
    const generation = binding.generation;
    const now = Date.now();
    const expiresAt = now + Number(this.env.RETENTION_MS || RETENTION_MS);
    const incarnation = randomToken(16);
    const size = message.ciphertext.length + 256;
    const result = this.state.storage.transactionSync(() => {
      this.liveBinding(attachment, message.peer_rid, message.generation);
      this.deleteItems("DELETE FROM items WHERE recipient=? AND sender=? AND generation=? AND msg_id=? AND expires_at<=?", recipient, sender, generation, message.msg_id, now);
      this.deleteReceipts("DELETE FROM receipts WHERE recipient=? AND sender=? AND generation=? AND msg_id=? AND expires_at<=?", recipient, sender, generation, message.msg_id, now);
      const receipt = this.row("SELECT digest,cursor FROM receipts WHERE recipient=? AND sender=? AND generation=? AND msg_id=?", recipient, sender, generation, message.msg_id);
      if (receipt) {
        if (receipt.digest !== digest) protocolError("id_conflict");
        return { cursor: receipt.cursor, deduped: true };
      }
      this.exec("INSERT OR IGNORE INTO streams(recipient,sender,generation,incarnation,next_cursor,ack_cursor,item_count,item_bytes,acked_receipts) VALUES(?,?,?,?,?,?,0,0,0)", recipient, sender, generation, incarnation, ZERO_KEY, ZERO_KEY);
      let stream = this.row("SELECT next_cursor,item_count,item_bytes FROM streams WHERE recipient=? AND sender=? AND generation=?", recipient, sender, generation);
      let homeUsage = this.row("SELECT item_bytes FROM usage WHERE singleton=1").item_bytes;
      if (stream.item_count >= MAX_ITEMS || homeUsage + size > MAX_HOME_BYTES) {
        this.deleteItems("DELETE FROM items WHERE rowid IN (SELECT rowid FROM items WHERE expires_at<=? LIMIT ?)", now, CLEANUP_BATCH);
        stream = this.row("SELECT next_cursor,item_count,item_bytes FROM streams WHERE recipient=? AND sender=? AND generation=?", recipient, sender, generation);
        homeUsage = this.row("SELECT item_bytes FROM usage WHERE singleton=1").item_bytes;
        if (stream.item_count >= MAX_ITEMS || homeUsage + size > MAX_HOME_BYTES) {
          return { error: this.row("SELECT 1 AS yes FROM items WHERE expires_at<=? LIMIT 1", now) ? "cleanup_pending" : "mailbox_full" };
        }
      }
      const cursor = nextCursor(stream.next_cursor);
      this.exec("INSERT INTO items(recipient,sender,generation,cursor,msg_id,digest,ciphertext,size,expires_at) VALUES(?,?,?,?,?,?,?,?,?)", recipient, sender, generation, cursor, message.msg_id, digest, message.ciphertext, size, expiresAt);
      this.exec("INSERT INTO receipts(recipient,sender,generation,msg_id,digest,cursor,expires_at) VALUES(?,?,?,?,?,?,?)", recipient, sender, generation, message.msg_id, digest, cursor, expiresAt);
      this.exec("UPDATE streams SET next_cursor=?,item_count=item_count+1,item_bytes=item_bytes+? WHERE recipient=? AND sender=? AND generation=?", cursor, size, recipient, sender, generation);
      this.exec("UPDATE usage SET item_bytes=item_bytes+? WHERE singleton=1", size);
      return { cursor, deduped: false };
    });
    if (result.error) {
      if (result.error === "cleanup_pending") await this.scheduleAlarm(now + 1);
      protocolError(result.error);
    }
    await this.scheduleAlarm(expiresAt);
    this.send(ws, "APPENDED", message.request_id, { peer_rid: message.peer_rid, generation: wireCursor(generation), cursor: wireCursor(result.cursor), deduped: result.deduped });
    if (!result.deduped) await this.pumpSubscribers(recipient, sender, generation);
    if (this.env.TEST_COST_METRICS === "1" && (message.request_id === "cost-append-0" || message.request_id === "cost-append-99")) console.log(`RELAY_V2_COST_OP ${message.request_id} ${JSON.stringify(costDelta(costBefore, this.testCost))}`);
  }

  async subscribe(ws, attachment, message) {
    exact(message, ["after", "generation", "incarnation", "peer_rid"]);
    this.requireStream(attachment);
    if (attachment.sub) protocolError("already_subscribed");
    const binding = this.liveBinding(attachment, message.peer_rid, message.generation);
    const { sender: peerSender, recipient } = attachment.role === "machine"
      ? { sender: message.peer_rid, recipient: attachment.machineRID }
      : { sender: attachment.machineRID, recipient: attachment.rid };
    const after = cursorKey(message.after);
    const generation = binding.generation;
    if (typeof message.incarnation !== "string" || (message.incarnation !== "" && !/^[A-Za-z0-9_-]{22}$/.test(message.incarnation))) protocolError("invalid_incarnation");
    const freshIncarnation = randomToken(16);
    const stream = this.state.storage.transactionSync(() => {
      this.liveBinding(attachment, message.peer_rid, message.generation);
      this.exec("INSERT OR IGNORE INTO streams(recipient,sender,generation,incarnation,next_cursor,ack_cursor,item_count,item_bytes,acked_receipts) VALUES(?,?,?,?,?,?,0,0,0)", recipient, peerSender, generation, freshIncarnation, ZERO_KEY, ZERO_KEY);
      return this.row("SELECT incarnation,next_cursor,ack_cursor FROM streams WHERE recipient=? AND sender=? AND generation=?", recipient, peerSender, generation);
    });
    if (!(message.incarnation === "" && after === ZERO_KEY) && message.incarnation !== stream.incarnation) protocolError("incarnation_mismatch");
    const blankRecovery = message.incarnation === "" && after === ZERO_KEY;
    if ((!blankRecovery && after < stream.ack_cursor) || after > stream.next_cursor) protocolError("invalid_cursor");
    const sub = { peer: message.peer_rid, recipient, sender: peerSender, generation, incarnation: stream.incarnation, sentHigh: blankRecovery ? stream.ack_cursor : after, sentCount: 0, sentBytes: 0 };
    ws.serializeAttachment({ ...attachment, sub });
    this.send(ws, "SUBSCRIBED", message.request_id, { peer_rid: message.peer_rid, generation: wireCursor(generation), incarnation: stream.incarnation, after: wireCursor(after) });
    await this.pump(ws);
  }

  async pumpSubscribers(recipient, sender, generation) {
    for (const ws of this.state.getWebSockets()) {
      const a = ws.deserializeAttachment();
      if (a?.phase === "authed" && a.sub?.recipient === recipient && a.sub?.sender === sender && a.sub?.generation === generation) await this.pump(ws);
    }
  }

  async pump(ws) {
    const attachment = ws.deserializeAttachment();
    const sub = attachment?.sub;
    if (!sub || sub.sentCount >= MAX_INFLIGHT_FRAMES || sub.sentBytes >= MAX_INFLIGHT_BYTES) return;
    try {
      this.liveBinding(attachment, sub.peer, wireCursor(sub.generation));
    } catch {
      ws.close(4003, "stale generation");
      return;
    }
    if (this.row("SELECT incarnation FROM streams WHERE recipient=? AND sender=? AND generation=?", sub.recipient, sub.sender, sub.generation)?.incarnation !== sub.incarnation) {
      ws.close(4003, "stale incarnation");
      return;
    }
    const now = Date.now();
    const rows = this.state.storage.sql.exec(
      "SELECT cursor,msg_id,ciphertext,size FROM items WHERE recipient=? AND sender=? AND generation=? AND cursor>? AND expires_at>? ORDER BY cursor LIMIT ?",
      sub.recipient, sub.sender, sub.generation, sub.sentHigh, now, MAX_INFLIGHT_FRAMES - sub.sentCount,
    );
    for (const item of rows) {
      if (sub.sentCount > 0 && sub.sentBytes + item.size > MAX_INFLIGHT_BYTES) break;
      this.send(ws, "DELIVER", `delivery-${wireCursor(item.cursor)}`, { peer_rid: sub.peer, generation: wireCursor(sub.generation), incarnation: sub.incarnation, cursor: wireCursor(item.cursor), msg_id: item.msg_id, ciphertext: item.ciphertext });
      sub.sentHigh = item.cursor;
      sub.sentCount++;
      sub.sentBytes += item.size;
    }
    this.recordCost(rows);
    ws.serializeAttachment({ ...attachment, sub });
  }

  async probe(ws, attachment, message) {
    exact(message, ["generation", "incarnation", "peer_rid"]);
    this.requireStream(attachment);
    const binding = this.liveBinding(attachment, message.peer_rid, message.generation);
    const sub = attachment.sub;
    if (!sub || sub.peer !== message.peer_rid || sub.generation !== binding.generation) protocolError("not_subscribed");
    if (message.incarnation !== sub.incarnation) protocolError("incarnation_mismatch");
    await this.pump(ws);
    this.send(ws, "PROBED", message.request_id, {
      peer_rid: message.peer_rid,
      generation: wireCursor(sub.generation),
      incarnation: sub.incarnation,
    });
  }

  async ack(ws, attachment, message) {
    const costBefore = this.testCost ? { ...this.testCost } : null;
    exact(message, ["cursor", "generation", "incarnation", "peer_rid"]);
    this.requireStream(attachment);
    const binding = this.liveBinding(attachment, message.peer_rid, message.generation);
    const cursor = cursorKey(message.cursor);
    const sub = attachment.sub;
    if (!sub || sub.peer !== message.peer_rid || sub.generation !== binding.generation) protocolError("not_subscribed");
    if (message.incarnation !== sub.incarnation) protocolError("incarnation_mismatch");
    if (cursor > sub.sentHigh) protocolError("ack_beyond_sent");
    const stream = this.row("SELECT incarnation,ack_cursor FROM streams WHERE recipient=? AND sender=? AND generation=?", sub.recipient, sub.sender, sub.generation) || { ack_cursor: ZERO_KEY };
    if (stream.incarnation !== message.incarnation) protocolError("incarnation_mismatch");
    if (cursor < stream.ack_cursor) protocolError("ack_regression");
    this.state.storage.transactionSync(() => {
      this.deleteItems("DELETE FROM items WHERE rowid IN (SELECT rowid FROM items WHERE recipient=? AND sender=? AND generation=? AND cursor<=? LIMIT ?)", sub.recipient, sub.sender, sub.generation, cursor, CLEANUP_BATCH);
      const newlyAcked = this.row("SELECT COUNT(*) AS n FROM receipts WHERE recipient=? AND sender=? AND generation=? AND cursor>(SELECT ack_cursor FROM streams WHERE recipient=? AND sender=? AND generation=?) AND cursor<=?", sub.recipient, sub.sender, sub.generation, sub.recipient, sub.sender, sub.generation, cursor).n;
      this.exec("UPDATE streams SET acked_receipts=acked_receipts+? WHERE recipient=? AND sender=? AND generation=?", newlyAcked, sub.recipient, sub.sender, sub.generation);
      this.exec("UPDATE streams SET ack_cursor=? WHERE recipient=? AND sender=? AND generation=? AND ack_cursor<?", cursor, sub.recipient, sub.sender, sub.generation, cursor);
      this.pruneReceiptWindow(sub.recipient, sub.sender, sub.generation);
    });
    const pending = this.row("SELECT COUNT(*) AS n,COALESCE(SUM(size),0) AS bytes FROM items WHERE recipient=? AND sender=? AND generation=? AND cursor>? AND cursor<=? AND expires_at>?", sub.recipient, sub.sender, sub.generation, cursor, sub.sentHigh, Date.now());
    sub.sentCount = pending.n;
    sub.sentBytes = pending.bytes;
    ws.serializeAttachment({ ...attachment, sub });
    this.send(ws, "ACKED", message.request_id, { peer_rid: message.peer_rid, generation: wireCursor(sub.generation), incarnation: sub.incarnation, cursor: wireCursor(cursor) });
    if (this.row("SELECT 1 AS yes FROM items WHERE recipient=? AND sender=? AND generation=? AND cursor<=? LIMIT 1", sub.recipient, sub.sender, sub.generation, cursor)) await this.scheduleAlarm(Date.now() + 1);
    await this.pump(ws);
    if (this.env.TEST_COST_METRICS === "1" && (message.request_id === "cost-ack-0" || message.request_id === "cost-ack-99")) console.log(`RELAY_V2_COST_OP ${message.request_id} ${JSON.stringify(costDelta(costBefore, this.testCost))}`);
    if (this.env.TEST_COST_METRICS === "1" && message.request_id === "cost-ack-99") console.log(`RELAY_V2_COST ${JSON.stringify(this.testCost)}`);
  }

  async discard(ws, attachment, message) {
    exact(message, ["generation", "incarnation", "peer_rid"]);
    this.requireStream(attachment);
    const binding = this.liveBinding(attachment, message.peer_rid, message.generation);
    const { sender, recipient } = attachment.role === "machine"
      ? { sender: message.peer_rid, recipient: attachment.machineRID }
      : { sender: attachment.machineRID, recipient: attachment.rid };
    const current = this.row("SELECT incarnation,discard_old_incarnation,discard_through_cursor FROM streams WHERE recipient=? AND sender=? AND generation=?", recipient, sender, binding.generation);
    if (!current) protocolError("incarnation_mismatch");
    if (current.incarnation !== message.incarnation) {
      if (current.discard_old_incarnation !== message.incarnation) protocolError("incarnation_mismatch");
      this.send(ws, "DISCARDED", message.request_id, { peer_rid: message.peer_rid, generation: wireCursor(binding.generation), incarnation: current.incarnation, cursor: wireCursor(current.discard_through_cursor) });
      return;
    }
    const nextIncarnation = randomToken(16);
    const result = this.state.storage.transactionSync(() => {
      this.liveBinding(attachment, message.peer_rid, message.generation);
      const stream = this.row("SELECT incarnation,next_cursor,ack_cursor FROM streams WHERE recipient=? AND sender=? AND generation=?", recipient, sender, binding.generation);
      if (!stream || stream.incarnation !== message.incarnation) protocolError("incarnation_mismatch");
      const newlyAcked = this.row("SELECT COUNT(*) AS n FROM receipts WHERE recipient=? AND sender=? AND generation=? AND cursor>? AND cursor<=?", recipient, sender, binding.generation, stream.ack_cursor, stream.next_cursor).n;
      this.exec("UPDATE streams SET acked_receipts=acked_receipts+? WHERE recipient=? AND sender=? AND generation=?", newlyAcked, recipient, sender, binding.generation);
      this.exec("UPDATE streams SET incarnation=?,ack_cursor=?,discard_old_incarnation=?,discard_through_cursor=? WHERE recipient=? AND sender=? AND generation=?", nextIncarnation, stream.next_cursor, stream.incarnation, stream.next_cursor, recipient, sender, binding.generation);
      this.deleteItems("DELETE FROM items WHERE rowid IN (SELECT rowid FROM items WHERE recipient=? AND sender=? AND generation=? LIMIT ?)", recipient, sender, binding.generation, CLEANUP_BATCH);
      this.pruneReceiptWindow(recipient, sender, binding.generation);
      return stream.next_cursor;
    });
    ws.serializeAttachment({ ...attachment, sub: undefined });
    await this.scheduleAlarm(Date.now() + 1);
    this.send(ws, "DISCARDED", message.request_id, { peer_rid: message.peer_rid, generation: wireCursor(binding.generation), incarnation: nextIncarnation, cursor: wireCursor(result) });
  }

  async revoke(ws, attachment, message) {
    exact(message, ["generation", "peer_rid"]);
    const binding = this.liveBinding(attachment, message.peer_rid, message.generation);
    const phoneRID = binding.phoneRID;
    this.state.storage.transactionSync(() => {
      const member = this.row("SELECT generation,ceremony FROM members WHERE phone_rid=?", phoneRID);
      this.exec("INSERT OR IGNORE INTO retired(phone_rid,ceremony) VALUES(?,?)", phoneRID, member.ceremony);
      const next = BigInt(member.generation) === MAX_UINT64 ? member.generation : nextCursor(member.generation);
      this.exec("UPDATE members SET generation=?,status='revoked' WHERE phone_rid=?", next, phoneRID);
      this.purgeBinding(phoneRID);
    });
    this.send(ws, "REVOKED", message.request_id, { peer_rid: message.peer_rid });
    this.closeBinding(phoneRID, "revoked");
  }

  closeBinding(phoneRID, reason) {
    for (const socket of this.state.getWebSockets()) {
      const a = socket.deserializeAttachment();
      if (a?.rid === phoneRID || a?.sub?.peer === phoneRID) socket.close(4003, reason);
    }
  }

  async pairCreate(ws, attachment, message) {
    exact(message, ["ceremony"]);
    this.requireMachine(attachment);
    if (attachment.pair) protocolError("pairing_exists");
    if (!/^[0-9a-f]{32}$/.test(message.ceremony)) protocolError("invalid_ceremony");
    const now = Date.now();
    const expiresAt = now + Number(this.env.RENDEZVOUS_TTL_MS || PAIR_TTL_MS);
    this.state.storage.transactionSync(() => {
      this.exec("DELETE FROM rendezvous WHERE expires_at<=?", now);
      const count = this.row("SELECT COUNT(*) AS n FROM rendezvous").n;
      if (this.row("SELECT 1 AS yes FROM rendezvous WHERE ceremony=?", message.ceremony)) protocolError("pairing_exists");
      if (count >= MAX_PAIRINGS) protocolError("pairing_full");
      this.exec("INSERT INTO rendezvous(ceremony,expires_at,claimed) VALUES(?,?,0)", message.ceremony, expiresAt);
    });
    ws.serializeAttachment({ ...attachment, pair: { ceremony: message.ceremony, side: "machine", frames: 0, bytes: 0 } });
    const directory = this.env.RENDEZVOUS.get(this.env.RENDEZVOUS.idFromName("v2"));
    const response = await directory.fetch(`https://rendezvous.invalid/register?ceremony=${message.ceremony}`, {
      method: "POST", body: JSON.stringify({ machine_rid: attachment.machineRID, expires_at: expiresAt }),
    });
    if (!response.ok) {
      this.exec("DELETE FROM rendezvous WHERE ceremony=?", message.ceremony);
      ws.serializeAttachment({ ...attachment, pair: undefined });
      protocolError("pairing_directory_full");
    }
    await this.scheduleAlarm(expiresAt);
    this.send(ws, "PAIR_CREATED", message.request_id, { ceremony: message.ceremony, expires_at: String(expiresAt) });
  }

  async pairClaim(ws, attachment, message) {
    exact(message, ["ceremony"]);
    if (!attachment.pairRoute || attachment.pairRoute !== message.ceremony || !/^[0-9a-f]{32}$/.test(message.ceremony)) protocolError("not_authorized");
    ws.serializeAttachment({ ...attachment, phase: "claiming" });
    if (!await this.initialized()) protocolError("pairing_not_found");
    const now = Date.now();
    const pairExpiresAt = this.state.storage.transactionSync(() => {
      const row = this.row("SELECT expires_at,claimed FROM rendezvous WHERE ceremony=?", message.ceremony);
      if (!row || row.expires_at <= now || row.claimed) return null;
      this.exec("UPDATE rendezvous SET claimed=1 WHERE ceremony=?", message.ceremony);
      return row.expires_at;
    });
    if (pairExpiresAt === null) protocolError("pairing_not_found");
    ws.serializeAttachment({ ...attachment, phase: "pair", expiresAt: pairExpiresAt, pair: { ceremony: message.ceremony, side: "phone", frames: 0, bytes: 0 } });
    this.send(ws, "PAIR_CLAIMED", message.request_id, { ceremony: message.ceremony });
  }

  async pairMessage(ws, attachment, message) {
    if (message.type === "PAIR_SEND") {
      exact(message, ["ceremony", "ciphertext"]);
      const pair = attachment.pair;
      if (!pair || pair.ceremony !== message.ceremony || typeof message.ciphertext !== "string" || message.ciphertext.length > MAX_PAIR_CIPHERTEXT_CHARS) protocolError("invalid_pair_frame");
      const frame = decodeBase64URL(message.ciphertext);
      if (pair.frames >= MAX_PAIR_FRAMES || pair.bytes + frame.length > MAX_PAIR_BYTES) protocolError("pairing_rate_limited");
      const row = this.row("SELECT expires_at,claimed FROM rendezvous WHERE ceremony=?", message.ceremony);
      if (!row || row.expires_at <= Date.now() || !row.claimed) protocolError("pairing_not_found");
      const peer = this.state.getWebSockets().find((candidate) => {
        const other = candidate.deserializeAttachment();
        return candidate !== ws && other?.pair?.ceremony === pair.ceremony && other.pair.side !== pair.side;
      });
      if (!peer) protocolError("pair_peer_offline");
      this.send(peer, "PAIR_FRAME", message.request_id, { ceremony: message.ceremony, ciphertext: message.ciphertext });
      pair.frames++;
      pair.bytes += frame.length;
      ws.serializeAttachment({ ...attachment, pair });
      this.send(ws, "PAIR_SENT", message.request_id, { ceremony: message.ceremony });
      return;
    }
    if (message.type === "PAIR_FINISH") {
      exact(message, ["ceremony"]);
      if (attachment.pair?.side !== "machine" || attachment.pair.ceremony !== message.ceremony) protocolError("not_authorized");
      this.exec("DELETE FROM rendezvous WHERE ceremony=?", message.ceremony);
      const directory = this.env.RENDEZVOUS.get(this.env.RENDEZVOUS.idFromName("v2"));
      await directory.fetch(`https://rendezvous.invalid/finish?ceremony=${message.ceremony}`, {
        method: "POST", body: JSON.stringify({ machine_rid: attachment.machineRID }),
      });
      this.send(ws, "PAIR_FINISHED", message.request_id, { ceremony: message.ceremony });
      return;
    }
    protocolError("unsupported_type");
  }

  async scheduleAlarm(candidate) {
    if (this.env.TEST_DISABLE_ALARMS === "1") return;
    const current = await this.state.storage.getAlarm();
    if (current === null || candidate < current) await this.state.storage.setAlarm(candidate);
  }

  async alarm(testLabel = "scheduled") {
    const priorCost = this.testCost;
    if (this.env.TEST_COST_METRICS === "1") this.testCost = { rowsRead: 0, rowsWritten: 0, statements: 0 };
    const now = Date.now();
    let socketDue = null;
    for (const ws of this.state.getWebSockets()) {
      const attachment = ws.deserializeAttachment();
      if (attachment?.phase !== "authed" && attachment?.expiresAt) {
        if (attachment.expiresAt <= now) ws.close(4008, "authentication timeout");
        else socketDue = socketDue === null ? attachment.expiresAt : Math.min(socketDue, attachment.expiresAt);
      }
    }
    if (!await this.initialized()) {
      if (socketDue !== null) await this.state.storage.setAlarm(socketDue);
      const cost = this.testCost || { rowsRead: 0, rowsWritten: 0, statements: 0 };
      if (this.env.TEST_COST_METRICS === "1") console.log(`RELAY_V2_ALARM_COST ${testLabel} ${JSON.stringify(cost)}`);
      this.testCost = priorCost;
      return { due: socketDue, cost };
    }
    this.state.storage.transactionSync(() => {
      this.deleteItems("DELETE FROM items WHERE rowid IN (SELECT rowid FROM items WHERE expires_at<=? LIMIT ?)", now, CLEANUP_BATCH);
      let itemBudget = CLEANUP_BATCH;
      let receiptBudget = CLEANUP_BATCH;
      const streamCursor = this.state.storage.sql.exec("SELECT recipient,sender,generation,ack_cursor,acked_receipts FROM streams");
      const streams = Array.from(streamCursor);
      this.recordCost(streamCursor);
      for (const stream of streams) {
        if (itemBudget && stream.ack_cursor !== ZERO_KEY) itemBudget -= this.deleteItems(
          "DELETE FROM items WHERE rowid IN (SELECT rowid FROM items WHERE recipient=? AND sender=? AND generation=? AND cursor<=? ORDER BY cursor LIMIT ?)",
          stream.recipient, stream.sender, stream.generation, stream.ack_cursor, itemBudget,
        );
        if (receiptBudget && stream.acked_receipts > MAX_ACKED_RECEIPTS) receiptBudget -= this.pruneReceiptWindow(
          stream.recipient, stream.sender, stream.generation, receiptBudget,
        );
      }
      this.deleteReceipts("DELETE FROM receipts WHERE rowid IN (SELECT rowid FROM receipts WHERE expires_at<=? LIMIT ?)", now, CLEANUP_BATCH);
      this.exec("DELETE FROM rendezvous WHERE rowid IN (SELECT rowid FROM rendezvous WHERE expires_at<=? LIMIT ?)", now, CLEANUP_BATCH);
    });
    let compactable = false;
    const streamCursor = this.state.storage.sql.exec("SELECT recipient,sender,generation,ack_cursor FROM streams WHERE ack_cursor>?", ZERO_KEY);
    const streams = Array.from(streamCursor);
    this.recordCost(streamCursor);
    for (const stream of streams) {
      if (this.row("SELECT 1 AS yes FROM items WHERE recipient=? AND sender=? AND generation=? AND cursor<=? LIMIT 1", stream.recipient, stream.sender, stream.generation, stream.ack_cursor)) {
        compactable = true;
        break;
      }
    }
    const receiptCompactable = this.row("SELECT 1 AS yes FROM streams WHERE acked_receipts>? LIMIT 1", MAX_ACKED_RECEIPTS);
    const expiries = [
      this.row("SELECT MIN(expires_at) AS due FROM items")?.due,
      this.row("SELECT MIN(expires_at) AS due FROM receipts")?.due,
      this.row("SELECT MIN(expires_at) AS due FROM rendezvous")?.due,
    ].filter((due) => due !== null && due !== undefined);
    const next = expiries.length ? Math.min(...expiries) : null;
    const storageDue = compactable || receiptCompactable ? now + 1 : next;
    const due = socketDue === null ? storageDue : storageDue === null || storageDue === undefined ? socketDue : Math.min(socketDue, storageDue);
    if (due !== null && due !== undefined && this.env.TEST_DISABLE_ALARMS !== "1") await this.state.storage.setAlarm(due <= now ? now + 1 : due);
    const cost = this.testCost || { rowsRead: 0, rowsWritten: 0, statements: 0 };
    if (this.env.TEST_COST_METRICS === "1") console.log(`RELAY_V2_ALARM_COST ${testLabel} ${JSON.stringify(cost)}`);
    this.testCost = priorCost;
    return { due, cost };
  }
}
