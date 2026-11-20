import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { DAVESession, MediaType, ProposalsOperationType, VERSION } from '@snazzah/davey';
import OpusScript from 'opusscript';
import { frameBytes, frameSamples } from '../media.mjs';

const signer = process.argv[2];
assert.ok(signer, 'pass the path to the offline MLS proposal signer');
assert.equal(VERSION, '0.2.0');

const scratch = mkdtempSync(path.join(os.tmpdir(), 'swarm-dave-'));
const key = path.join(scratch, 'external-signer');
const keyPackage = path.join(scratch, 'member-key-package');
const codec = new OpusScript(24000, 1, OpusScript.Application.VOIP);
const room = '741';
const botId = '491';
const ownerId = '492';
const guestId = '493';

function sign(...args) {
  return execFileSync(signer, args, {timeout: 5000});
}

function participant(id, channelId = room) {
  const session = new DAVESession(1, id, channelId);
  assert.equal(session.channelId, channelId);
  session.setExternalSender(externalSender);
  assert.equal(session.ready, false);
  assert.equal(session.epoch, 0n);
  assert.deepEqual(session.getUserIds(), [id]);
  return session;
}

function add(group, member, channelId = room) {
  writeFileSync(keyPackage, member.getSerializedKeyPackage());
  return sign('add', key, channelId, group.epoch.toString(), keyPackage);
}

function change(proposal, current, joining, roster, transitionId) {
  const drafts = current.map((member) =>
    member.processProposals(ProposalsOperationType.APPEND, proposal, roster));
  const {commit, welcome} = drafts[0];
  assert.ok(commit.length);
  for (const member of current) assert.equal(member.processCommit(commit), true);
  if (joining) {
    assert.ok(welcome.length);
    joining.processWelcome(welcome, roster);
  }
  for (const member of joining ? [...current, joining] : current) {
    member.prepareTransition(transitionId, 1);
    if (transitionId) assert.equal(member.executeTransition(transitionId), true);
    assert.equal(member.canEncrypt, true);
  }
}

function exactRoster(member, ...ids) {
  assert.equal(member.ready, true);
  assert.deepEqual(member.getUserIds().sort(), ids.sort());
}

function packet() {
  const pcm = Buffer.alloc(frameBytes);
  for (let i = 0; i < frameSamples; i++) {
    pcm.writeInt16LE(Math.round(Math.sin(i * 2 * Math.PI * 440 / 24000) * 16000), i * 2);
  }
  return codec.encode(pcm, frameSamples);
}

function exchange(sender, receiver, removed) {
  const opus = packet();
  const encrypted = sender.encryptOpus(opus);
  assert.notDeepEqual(encrypted, opus);
  if (removed) assert.throws(() => removed.decrypt(sender.userId, MediaType.AUDIO, encrypted));
  assert.throws(() => receiver.decrypt(sender.userId, MediaType.AUDIO, opus));
  const decoded = receiver.decrypt(sender.userId, MediaType.AUDIO, encrypted);
  assert.deepEqual(decoded, opus);
  assert.equal(codec.decode(decoded).length, frameBytes);
}

let externalSender;
try {
  externalSender = sign('init', key);
  const bot = participant(botId);
  const owner = participant(ownerId);

  const wrongRoomProposal = add(bot, owner, '742');
  assert.throws(() => bot.processProposals(ProposalsOperationType.APPEND, wrongRoomProposal, [botId, ownerId]));
  change(add(bot, owner), [bot], owner, [botId, ownerId], 0);
  exactRoster(bot, botId, ownerId);
  exactRoster(owner, botId, ownerId);
  assert.equal(bot.epoch, 1n);
  assert.equal(owner.epoch, bot.epoch);
  exchange(bot, owner);
  exchange(owner, bot);

  const guest = participant(guestId);
  change(add(bot, guest), [bot, owner], guest, [botId, ownerId, guestId], 2);
  for (const member of [bot, owner, guest]) {
    exactRoster(member, botId, ownerId, guestId);
    assert.equal(member.epoch, 2n);
  }
  exchange(guest, bot);

  const remove = sign('remove', key, room, bot.epoch.toString(), String(guest.ownLeafIndex));
  change(remove, [bot, owner], null, [botId, ownerId], 3);
  for (const member of [bot, owner]) {
    exactRoster(member, botId, ownerId);
    assert.equal(member.epoch, 3n);
  }
  exchange(bot, owner, guest);

  process.stdout.write('Native DAVE join, bidirectional Opus, rekey and removal passed.\n');
} finally {
  codec.delete();
  rmSync(scratch, {recursive: true, force: true});
}
