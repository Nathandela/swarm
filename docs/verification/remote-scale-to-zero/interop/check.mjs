// Node WebCrypto interop is NOT proof that production workerd auth is implemented.
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const dir = fileURLToPath(new URL('.', import.meta.url));
const v = JSON.parse(execFileSync('go', ['run', '.'], { cwd: dir, encoding: 'utf8', timeout: 120000 }));
const { subtle } = webcrypto;
const buf = s => Buffer.from(s, 'base64');
const u32 = n => { const b = Buffer.alloc(4); b.writeUInt32BE(n); return b; };
const msg = (ctx, part, rid) => Buffer.concat([Buffer.from(ctx), u32(part.length), part, Buffer.from(rid)]);
const key = await subtle.importKey('raw', buf(v.pub), 'Ed25519', false, ['verify']);
const hkdfKey = await subtle.importKey('raw', buf(v.pub), 'HKDF', false, ['deriveBits']);
const rid = Buffer.from(await subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256',
  salt: Buffer.from('swarm-relay-routing-id-v1'), info: Buffer.from('routing-id') }, hkdfKey, 128)).toString('hex');
assert.equal(rid, v.rid);
const auth = msg('swarm-relay-auth-v1\0', buf(v.nonce), rid);
const consent = msg('swarm-relay-consent-v1\0', Buffer.from(v.ceremony), rid);
assert.deepEqual(auth, buf(v.auth));
assert.deepEqual(consent, buf(v.consent));
assert(await subtle.verify('Ed25519', key, buf(v.authSignature), auth));
assert(await subtle.verify('Ed25519', key, buf(v.consentSignature), consent));
assert.equal(await subtle.verify('Ed25519', key, buf(v.authSignature), consent), false);
const mutated = Buffer.from(auth); mutated[mutated.length - 1] ^= 1;
assert.equal(await subtle.verify('Ed25519', key, buf(v.authSignature), mutated), false);
assert.deepEqual(Buffer.concat([u32(Buffer.byteLength(v.ceremony)), Buffer.from(v.ceremony), buf(v.consentSignature)]), buf(v.credential));
function decodeFrame(b) {
  assert(b.length >= 4, 'truncated header');
  const n = b.readUInt32BE();
  assert(n >= 1 && n <= v.maxFrame, 'invalid length before allocation');
  assert.equal(b.length, n + 4, 'truncated or extra body');
  return { tag: b[4], payload: b.subarray(5) };
}
const decoded = decodeFrame(buf(v.frame));
assert.equal(decoded.tag, 2);
assert.throws(() => decodeFrame(u32(0)));
assert.throws(() => decodeFrame(u32(v.maxFrame + 1)));
assert.throws(() => decodeFrame(buf(v.frame).subarray(0, -1)));
// Expected bad design: JSON Number cannot preserve Go's full uint64 range.
const unsafe = JSON.parse(decoded.payload).cursor;
assert.notEqual(BigInt(unsafe).toString(), v.maxCursor);
const max = (1n << 64n) - 1n;
function key20(value) {
  assert.equal(typeof value, 'string');
  assert(/^(0|[1-9][0-9]{0,19})$/.test(value), 'canonical wire decimal');
  assert(BigInt(value) <= max, 'uint64 overflow');
  return value.padStart(20, '0');
}
const cursors = ['0', '9', '10', '9007199254740991', '9007199254740992', '9007199254740993', '9223372036854775808', v.maxCursor];
assert.deepEqual(cursors.map(key20).sort(), cursors.map(key20));
assert.throws(() => key20('01'));
assert.throws(() => key20(1));
assert.throws(() => key20('-1'));
assert.throws(() => key20((max + 1n).toString()));
assert.equal(BigInt(key20(v.maxCursor)).toString(), v.maxCursor);
console.log(JSON.stringify({goWebCryptoHKDF:true, ed25519Auth:true, ed25519Consent:true,
  wrongContextRejected:true, tamperRejected:true, actualGoFrameDecoded:true, malformedFramesRejected:true,
  uint64NumberLossDetected:true, decimalStorageOrdering:true}));
