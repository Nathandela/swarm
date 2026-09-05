import assert from "node:assert/strict";
import { request } from "node:http";

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

for (let i = 0; i < 60; i++) {
  assert.equal((await pairLookup()).status, 404, `native limiter rejected request ${i + 1} early`);
}
const denied = await pairLookup();
assert.equal(denied.status, 429, "native limiter admits no more than its configured local burst in the simulator");
assert.equal(denied.headers["retry-after"], "60");

console.log("PASS workerd native pre-auth route rate limit");
