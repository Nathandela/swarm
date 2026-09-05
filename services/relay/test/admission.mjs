import assert from "node:assert/strict";
import Worker from "../src/worker.mjs";

const admittedRID = "00".repeat(16);
const unknownRID = "11".repeat(16);
const routes = [
  `/v2/ws?machine_rid=${admittedRID}`,
  `/v2/pair?ceremony=${"22".repeat(16)}`,
];

function envWithoutBindings(config) {
  const env = { ...config };
  for (const name of ["HOMES", "RENDEZVOUS"]) {
    Object.defineProperty(env, name, {
      get() { throw new Error(`${name} binding was accessed before admission`); },
    });
  }
  return env;
}

for (const [name, config] of [
  ["missing allowlist", { OPERATOR_NAMESPACE: "owner" }],
  ["empty allowlist", { ALLOWED_MACHINE_RIDS: "", OPERATOR_NAMESPACE: "owner" }],
  ["malformed allowlist", { ALLOWED_MACHINE_RIDS: "not-a-routing-id", OPERATOR_NAMESPACE: "owner" }],
  ["partly malformed allowlist", { ALLOWED_MACHINE_RIDS: `${admittedRID},bad`, OPERATOR_NAMESPACE: "owner" }],
  ["missing namespace", { ALLOWED_MACHINE_RIDS: admittedRID }],
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

console.log("PASS relay admission fails closed before Durable Object access");
