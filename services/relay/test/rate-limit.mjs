import assert from "node:assert/strict";
import { request } from "node:http";
import { setTimeout as delay } from "node:timers/promises";

const HTTP = process.env.RELAY_HTTP || "http://127.0.0.1:8790";
const path = `/v2/pair?ceremony=${"44".repeat(16)}`;

function pairLookup() {
  return new Promise((resolve, reject) => {
    const req = request(`${HTTP}${path}`, {
      headers: {
        Connection: "Upgrade",
        Upgrade: "websocket",
        "Sec-WebSocket-Key": "AAAAAAAAAAAAAAAAAAAAAA==",
        "Sec-WebSocket-Version": "13",
      },
    }, (response) => {
      response.resume();
      resolve({ status: response.statusCode, headers: response.headers });
    });
    req.on("upgrade", (response, socket) => {
      socket.destroy();
      resolve({ status: response.statusCode, headers: response.headers });
    });
    req.on("error", reject);
    req.end();
  });
}

// Miniflare resets this counter at wall-clock minute boundaries. Reserve time
// for the 61 local requests so the test measures one configured burst.
const periodMs = 60_000;
let start = Date.now();
while (periodMs - start % periodMs < 10_000) {
  await delay(periodMs - start % periodMs);
  start = Date.now();
}
const window = Math.floor(start / periodMs);
for (let i = 0; i < 60; i++) {
  assert.equal((await pairLookup()).status, 404, `native limiter rejected request ${i + 1} early`);
}
const denied = await pairLookup();
assert.equal(Math.floor(Date.now() / periodMs), window, "test burst crossed a simulator window");
assert.equal(denied.status, 429, "native limiter admits no more than its configured local burst in the simulator");
assert.equal(denied.headers["retry-after"], "60");

console.log("PASS workerd native pre-auth route rate limit");
