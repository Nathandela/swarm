import assert from "node:assert/strict";
import Worker from "../src/worker.mjs";

const admittedRID = "00".repeat(16);
const unknownRID = "11".repeat(16);
const routes = [
  `/v2/ws?machine_rid=${admittedRID}`,
  `/v2/pair?ceremony=${"22".repeat(16)}`,
];

function envWithoutBindings(config) {
  const env = Object.create(Object.getPrototypeOf(config), Object.getOwnPropertyDescriptors(config));
  for (const name of ["HOMES", "RENDEZVOUS"]) {
    Object.defineProperty(env, name, {
      get() { throw new Error(`${name} binding was accessed before admission`); },
    });
  }
  return env;
}

function websocketRequest(path) {
  return new Request(`https://relay.invalid${path}`, {
    headers: { Upgrade: "websocket" },
  });
}

function fakeBindings({ success = true, machineRID = admittedRID } = {}) {
  const limitCalls = [];
  const homeCalls = [];
  const env = {
    ALLOWED_MACHINE_RIDS: admittedRID,
    OPERATOR_NAMESPACE: "owner",
    RATE_LIMITER: {
      async limit(arg) {
        limitCalls.push(arg);
        return { success };
      },
    },
    HOMES: {
      idFromName(name) { return name; },
      get(name) {
        return {
          async fetch(request) {
            homeCalls.push({ name, request });
            return new Response(null, { status: 204 });
          },
        };
      },
    },
    RENDEZVOUS: {
      idFromName(name) { return name; },
      get() {
        return {
          async fetch() { return Response.json({ machine_rid: machineRID }); },
        };
      },
    },
  };
  return { env, limitCalls, homeCalls };
}

for (const [name, config] of [
  ["missing allowlist", { OPERATOR_NAMESPACE: "owner" }],
  ["empty allowlist", { ALLOWED_MACHINE_RIDS: "", OPERATOR_NAMESPACE: "owner" }],
  ["malformed allowlist", { ALLOWED_MACHINE_RIDS: "not-a-routing-id", OPERATOR_NAMESPACE: "owner" }],
  ["partly malformed allowlist", { ALLOWED_MACHINE_RIDS: `${admittedRID},bad`, OPERATOR_NAMESPACE: "owner" }],
  ["missing namespace", { ALLOWED_MACHINE_RIDS: admittedRID }],
  ["uppercase namespace", { ALLOWED_MACHINE_RIDS: admittedRID, OPERATOR_NAMESPACE: "Owner" }],
  ["space namespace", { ALLOWED_MACHINE_RIDS: admittedRID, OPERATOR_NAMESPACE: "owner " }],
  ["newline namespace", { ALLOWED_MACHINE_RIDS: admittedRID, OPERATOR_NAMESPACE: "owner\n" }],
  ["oversized namespace", { ALLOWED_MACHINE_RIDS: admittedRID, OPERATOR_NAMESPACE: `a${"9".repeat(64)}` }],
  ["non-string namespace", { ALLOWED_MACHINE_RIDS: admittedRID, OPERATOR_NAMESPACE: 7 }],
]) {
  for (const route of routes) {
    const response = await Worker.fetch(new Request(`https://relay.invalid${route}`), envWithoutBindings(config));
    assert.equal(response.status, 503, `${name} ${route}`);
  }
}

const rootEnv = new Proxy({}, {
  get(_target, name) { throw new Error(`public root accessed env.${String(name)}`); },
});
assert.equal((await Worker.fetch(new Request("https://relay.invalid/"), rootEnv)).status, 200);

const denied = await Worker.fetch(
  new Request(`https://relay.invalid/v2/ws?machine_rid=${unknownRID}`),
  envWithoutBindings({ ALLOWED_MACHINE_RIDS: admittedRID, OPERATOR_NAMESPACE: "owner" }),
);
assert.equal(denied.status, 403);

for (const route of routes) {
  const env = envWithoutBindings({ ALLOWED_MACHINE_RIDS: admittedRID, OPERATOR_NAMESPACE: "owner" });
  const response = await Worker.fetch(websocketRequest(route), env);
  assert.equal(response.status, 503, `missing limiter ${route}`);
}

for (const route of routes) {
  const env = envWithoutBindings({
    ALLOWED_MACHINE_RIDS: admittedRID,
    OPERATOR_NAMESPACE: "owner",
    get RATE_LIMITER() { throw new Error("limiter unavailable"); },
  });
  const response = await Worker.fetch(websocketRequest(route), env);
  assert.equal(response.status, 503, `throwing limiter ${route}`);
}

for (const [name, limit] of [
  ["limiter call throws", async () => { throw new Error("limiter unavailable"); }],
  ["limiter result is empty", async () => ({})],
  ["limiter result is non-boolean", async () => ({ success: "yes" })],
]) {
  for (const route of routes) {
    const env = envWithoutBindings({
      ALLOWED_MACHINE_RIDS: admittedRID,
      OPERATOR_NAMESPACE: "owner",
      RATE_LIMITER: { limit },
    });
    const response = await Worker.fetch(websocketRequest(route), env);
    assert.equal(response.status, 503, `${name} ${route}`);
  }
}

{
  const { env, limitCalls, homeCalls } = fakeBindings({ success: false });
  const response = await Worker.fetch(websocketRequest(`/v2/ws?machine_rid=${admittedRID}`), env);
  assert.equal(response.status, 429);
  assert.equal(response.headers.get("Retry-After"), "60");
  assert.deepEqual(limitCalls, [{ key: "ws" }]);
  assert.equal(homeCalls.length, 0);
}

{
  const { env, limitCalls, homeCalls } = fakeBindings({ success: false });
  Object.defineProperty(env, "RENDEZVOUS", {
    get() { throw new Error("RENDEZVOUS binding was accessed after limiter denial"); },
  });
  const response = await Worker.fetch(websocketRequest(`/v2/pair?ceremony=${"22".repeat(16)}`), env);
  assert.equal(response.status, 429);
  assert.equal(response.headers.get("Retry-After"), "60");
  assert.deepEqual(limitCalls, [{ key: "pair" }]);
  assert.equal(homeCalls.length, 0);
}

{
  const { env, limitCalls } = fakeBindings();
  Object.defineProperty(env, "RENDEZVOUS", {
    get() { throw new Error("RENDEZVOUS binding was accessed before Upgrade validation"); },
  });
  const response = await Worker.fetch(new Request(`https://relay.invalid/v2/pair?ceremony=${"22".repeat(16)}`), env);
  assert.equal(response.status, 426);
  assert.deepEqual(limitCalls, [], "a non-WebSocket pair request must not consume the native limit");
}

{
  const { env, limitCalls } = fakeBindings();
  const response = await Worker.fetch(
    new Request(`https://relay.invalid/v2/ws?machine_rid=${unknownRID}`),
    env,
  );
  assert.equal(response.status, 403);
  assert.deepEqual(limitCalls, [], "an unknown RID must not consume the admitted ws limit");
}

for (const [route, key] of [
  [`/v2/ws?machine_rid=${admittedRID}`, "ws"],
  [`/v2/pair?ceremony=${"22".repeat(16)}`, "pair"],
]) {
  const { env, limitCalls, homeCalls } = fakeBindings();
  const response = await Worker.fetch(websocketRequest(route), env);
  assert.equal(response.status, 204, route);
  assert.deepEqual(limitCalls, [{ key }], `${route} must consume exactly its constant route key`);
  assert.equal(homeCalls.length, 1, `${route} must dispatch once after admission`);
}

console.log("PASS relay admission fails closed before Durable Object access");
