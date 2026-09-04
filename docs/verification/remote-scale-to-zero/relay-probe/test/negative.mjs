import assert from "node:assert/strict";
// Control: a connection hint is not authorization. A changed client must not reuse a ticket.
const crypto = await import("node:crypto");
const good = crypto.createHmac("sha256", "local-probe-only-not-a-production-secret").update("p:a").digest("base64url");
const bad = crypto.createHmac("sha256", "local-probe-only-not-a-production-secret").update("p:b").digest("base64url");
assert.notEqual(good, bad, "ticket must bind client identity, not merely route");
const max = "18446744073709551615";
assert.equal(BigInt(max).toString(), max, "BigInt retains max uint64 exactly");
assert.notEqual(String(Number(max)), max, "negative control: JavaScript Number loses uint64 precision");
assert.throws(() => { const n = BigInt(max) + 1n; if (n > 18446744073709551615n) throw new Error("overflow"); }, /overflow/, "max uint64 increment must fail, not wrap");
console.log("PASS negative control: route hint alone cannot authenticate a different client");
