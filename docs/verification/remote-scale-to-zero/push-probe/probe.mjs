import assert from 'node:assert/strict';
import {Firestore} from '@google-cloud/firestore';

const db = new Firestore({projectId: 'demo-swarm-push-probe'});
const now = Date.now();
const tx = fn => db.runTransaction(fn);
async function createOnce(ref, value) {
  return tx(async t => { const s=await t.get(ref); if(s.exists) return false; t.create(ref,value); return true; });
}

// Negative control: two process-local maps both accept the same nonce.
const memA=new Set(), memB=new Set();
assert.equal(!memA.has('n') && (memA.add('n'),true), true);
assert.equal(!memB.has('n') && (memB.add('n'),true), true);

// Durable nonce: exactly one transaction creates it.
let results=await Promise.allSettled(Array.from({length:12},()=>createOnce(db.doc('nonces/i1-n1'),{expiresAt:now+120000})));
assert.equal(results.filter(x=>x.status==='fulfilled'&&x.value).length,1);
assert.equal(results.filter(x=>x.status==='fulfilled'&&!x.value).length,11);

// Registration idempotency: one stable installation is minted under concurrency.
const idem=db.doc('registrationIdempotency/k1');
async function register(candidate, bodyHash='body1') { return tx(async t=>{const s=await t.get(idem); if(s.exists){if(s.data().bodyHash!==bodyHash)throw new Error('IDEMPOTENCY_BODY_MISMATCH');return s.data().installationId;} t.create(idem,{bodyHash,installationId:candidate,expiresAt:now+600000});t.create(db.doc(`installations/${candidate}`),{tokenGeneration:1,token:'cipher'});return candidate;}); }
const ids=await Promise.all(Array.from({length:12},(_,i)=>register(`candidate${i}`)));
assert.equal(new Set(ids).size,1);
await assert.rejects(()=>register('must-not-exist','different-body'),/IDEMPOTENCY_BODY_MISMATCH/);
assert.equal((await db.doc('installations/must-not-exist').get()).exists,false);

// Strict global allocation window: contention retries preserve the hard cap.
const quota=db.doc('rateWindows/global-allocation');
async function allocate(i){return tx(async t=>{const q=await t.get(quota);const count=q.exists?q.data().count:0;if(count>=4)return false;t.set(quota,{count:count+1,expiresAt:now+60000});t.create(db.doc(`addresses/a${i}`),{installationId:'i1'});return true;});}
results=await Promise.all(Array.from({length:20},(_,i)=>allocate(i)));
assert.equal(results.filter(Boolean).length,4);

// Stale UNREGISTERED must CAS on the token generation captured before provider I/O.
const inst=db.doc('installations/i-rotate'); await inst.set({token:'old',tokenGeneration:1,tokenDead:false});
const captured=1; await inst.update({token:'new',tokenGeneration:2,tokenDead:false});
await tx(async t=>{const s=await t.get(inst);if(s.data().tokenGeneration===captured)t.update(inst,{token:null,tokenDead:true});});
assert.deepEqual((await inst.get()).data(),{token:'new',tokenGeneration:2,tokenDead:false});

// Revoke wins before a wake claim: transaction observes deleted address and does not claim/send.
const addr=db.doc('addresses/revoke-race');await addr.set({installationId:'i1'});await tx(async t=>{const s=await t.get(addr);assert(s.exists);t.delete(addr);t.create(db.doc('tombstones/revoke-race'),{expiresAt:now+604800000});});
let claimed=false;await tx(async t=>{const s=await t.get(addr);if(s.exists){t.create(db.doc('wakeAttempts/w-revoked'),{state:'claimed'});claimed=true;}});assert.equal(claimed,false);

// Retry callback negative control: an external side effect inside a retrying transaction duplicates.
const retryDoc=db.doc('retry/control');await retryDoc.set({n:0});let callbacks=0,sendsInside=0;
let release;const gate=new Promise(r=>release=r);let first=true;
const p=tx(async t=>{callbacks++;const s=await t.get(retryDoc);sendsInside++;if(first){first=false;await gate;}t.update(retryDoc,{n:s.data().n+1});});
await new Promise(r=>setTimeout(r,100));await retryDoc.update({n:10});release();await p;
assert(callbacks>=2);assert(sendsInside>=2);
// Correct shape: claim/commit transaction first, provider send exactly once outside callback.
let sendsOutside=0;const attempt=db.doc('wakeAttempts/w1');const won=await createOnce(attempt,{state:'claimed',expiresAt:now+300000});if(won)sendsOutside++;assert.equal(sendsOutside,1);

// Expiry is enforced at read time; physical deletion is intentionally separate/eventual.
const exp=db.doc('wakeAttempts/expired');await exp.set({state:'claimed',expiresAt:now-1});const e=await exp.get();assert(e.exists&&e.data().expiresAt<now);const usable=e.exists&&e.data().expiresAt>=Date.now();assert.equal(usable,false);await exp.delete();assert.equal((await exp.get()).exists,false);

console.log(JSON.stringify({nonce:true,registrationIdempotency:true,idempotencyBodyMismatchRejected:true,allocationHardCap:4,staleUnregisteredCAS:true,revokeBeforeClaim:true,transactionCallbacks:callbacks,unsafeDuplicateSends:sendsInside,safeOutsideSend:sendsOutside,expiryAtUse:true}));
await db.terminate();
