import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const config = await readFile(new URL("../wrangler.toml", import.meta.url), "utf8");
const name = config.match(/^name = "([a-z0-9-]+)"$/m)?.[1];

assert.equal(name, "s", "the single live Worker keeps the QR relay URL within its 39-byte field");
assert.match(config, /^workers_dev = true$/m, "the one public workers.dev URL is explicit");
assert.match(config, /^preview_urls = false$/m, "version preview URLs stay disabled");
assert.doesNotMatch(config, /^\[env\./m, "a second Worker environment is not configured");
assert.doesNotMatch(config, /^\[vars\]$/m, "runtime variables are not deployment defaults");
assert.doesNotMatch(config, /^(?:route|routes)\s*=|^\[\[routes\]\]$/m, "workers.dev is the only public route");
assert.doesNotMatch(config, /\b(?:OPERATOR_NAMESPACE|ALLOWED_MACHINE_RIDS|CHALLENGE_TTL_MS|RENDEZVOUS_TTL_MS|RETENTION_MS|TEST_[A-Z_]+)\b/, "admission and test fixtures are local-only");
assert.equal((config.match(/^\[\[durable_objects\.bindings\]\]$/gm) || []).length, 2);
assert.match(config, /\[\[durable_objects\.bindings\]\]\nname = "HOMES"\nclass_name = "RelayHome"/);
assert.match(config, /\[\[durable_objects\.bindings\]\]\nname = "RENDEZVOUS"\nclass_name = "RendezvousDirectory"/);
assert.match(config, /\[\[migrations\]\]\ntag = "v1"\nnew_sqlite_classes = \["RelayHome", "RendezvousDirectory"\]/);

const relayURL = `wss://${name}.nathan-delacretaz.workers.dev`;
assert.equal(relayURL, "wss://s.nathan-delacretaz.workers.dev");
assert.ok(Buffer.byteLength(relayURL) <= 39, `${relayURL} exceeds the QR relay URL field`);
