import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { NetworkingStatusCode, VoiceConnectionStatus } from '@discordjs/voice';
import OpusScript from 'opusscript';
import { daveState, frameBytes, frameSamples, guardReceiver, maxLine, pcmFrame } from './media.mjs';

const helper = fileURLToPath(new URL('./swarm-discord-voice', import.meta.url));
const start = {type: 'start', guildId: '1', channelId: '2', botId: '3', speakers: ['4']};

function run(...messages) {
  return spawnSync(process.execPath, [helper], {
    input: messages.map((message) => JSON.stringify(message) + '\n').join(''),
    encoding: 'utf8',
    timeout: 3000,
  });
}

test('dependency probe and a single scoped gateway join', () => {
  assert.equal(spawnSync(process.execPath, [helper, '--check']).status, 0);
  const result = run(start, {type: 'clear'}, {type: 'stop'});
  assert.equal(result.status, 0);
  assert.equal(result.stderr, '');
  assert.deepEqual(JSON.parse(result.stdout), {
    type: 'gateway',
    data: {op: 4, d: {guild_id: '1', channel_id: '2', self_deaf: false, self_mute: false}},
  });
});

test('rejects unscoped gateway material, self audio, and oversized commands', () => {
  const badStart = run({...start, speakers: ['3']});
  assert.equal(badStart.status, 1);
  assert.deepEqual(JSON.parse(badStart.stdout), {type: 'fault', code: 'invalid_start'});

  const badScope = run(start, {type: 'voice_state', data: {
    guild_id: '1', channel_id: '5', user_id: '3', session_id: 'fixture',
  }});
  assert.equal(badScope.status, 1);
  assert.deepEqual(JSON.parse(badScope.stdout.trim().split('\n').at(-1)),
    {type: 'fault', code: 'invalid_voice_state'});

  const oversized = run({type: 'x', body: 'x'.repeat(maxLine)});
  assert.equal(oversized.status, 1);
  assert.deepEqual(JSON.parse(oversized.stdout), {type: 'fault', code: 'oversized_command'});
});

test('MLS readiness, rekey, and legacy RTP checks', () => {
  const dave = {
    protocolVersion: 1,
    session: {ready: true, protocolVersion: 1, epoch: 1n},
    pendingTransitions: new Map(),
    downgraded: false,
    reinitializing: false,
  };
  const state = {
    code: NetworkingStatusCode.Ready,
    dave,
    connectionData: {encryptionMode: 'aead_aes256_gcm_rtpsize'},
  };
  const connection = {state: {status: VoiceConnectionStatus.Ready, networking: {state}}};

  assert.equal(daveState(connection), dave);
  state.connectionData.encryptionMode = 'aead_xchacha20_poly1305_rtpsize';
  assert.equal(daveState(connection), dave);
  dave.pendingTransitions.set(1, 1);
  assert.equal(daveState(connection), undefined);
  dave.pendingTransitions.clear();
  dave.reinitializing = true;
  assert.equal(daveState(connection), undefined);
  dave.reinitializing = false;
  dave.protocolVersion = 0;
  assert.equal(daveState(connection), undefined);
  dave.protocolVersion = 1;
  dave.session.ready = false;
  assert.equal(daveState(connection), undefined);
  dave.session.ready = true;
  state.connectionData.encryptionMode = 'xsalsa20_poly1305';
  assert.equal(daveState(connection), undefined);
});

test('receive gate refuses replay, late packets, passthrough, and SSRC rebinding', () => {
  const opus = Buffer.from('test-fixture');
  let decrypts = 0;
  const receiver = {parsePacket: () => { decrypts++; return opus; }};
  let active = true;
  let passthrough = false;
  const dave = {session: {canPassthrough: () => passthrough}};
  const retires = [];
  guardReceiver(receiver, () => active && dave, new Map([['speaker', {}]]),
    (code) => retires.push(code));

  function packet(seq, ssrc, user = 'speaker') {
    const rtp = Buffer.alloc(32);
    rtp.writeUInt16BE(seq, 2);
    rtp.writeUInt32BE(ssrc, 8);
    return receiver.parsePacket(rtp, '', null, null, user);
  }
  assert.equal(packet(65534, 2, 'other'), null);
  assert.equal(packet(65534, 2), opus);
  assert.equal(packet(65534, 2), null);
  assert.equal(packet(65533, 2), null);
  assert.equal(packet(0, 2), opus);
  passthrough = true;
  assert.equal(packet(1, 2), null);
  passthrough = false;
  active = false;
  assert.equal(packet(2, 2), null);
  active = true;
  assert.equal(packet(2, 3), null);
  assert.equal(decrypts, 3);
  assert.deepEqual(retires, ['speaker_rebound']);
});

test('20 ms Opus exchange between Discord and 24 kHz mono PCM', () => {
  const client = new OpusScript(24000, 1, OpusScript.Application.VOIP);
  const discord = new OpusScript(48000, 1, OpusScript.Application.VOIP);
  try {
    const original = Buffer.alloc(frameBytes * 2);
    for (let i = 0; i < frameSamples * 2; i++) {
      original.writeInt16LE(Math.round(Math.sin(i * 2 * Math.PI * 440 / 48000) * 20000), i * 2);
    }
    const decoded = client.decode(discord.encode(original, frameSamples * 2));
    assert.equal(decoded.length, frameBytes);
    const rendered = discord.decode(client.encode(decoded, frameSamples));
    assert.equal(rendered.length, frameBytes * 2);
    assert.ok(rendered.some((byte) => byte !== 0));
    assert.deepEqual(pcmFrame(decoded.toString('base64')), decoded);
    assert.equal(pcmFrame('!'.repeat(1280)), undefined);
  } finally {
    client.delete();
    discord.delete();
  }
});
