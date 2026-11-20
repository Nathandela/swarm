import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { setImmediate } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { Networking, NetworkingStatusCode, VoiceConnectionStatus, VoiceReceiver } from '@discordjs/voice';
import OpusScript from 'opusscript';
import { daveState, frameBytes, frameSamples, guardReceiver, maxLine, pcmFrame } from './media.mjs';

const helper = fileURLToPath(new URL('./swarm-discord-voice', import.meta.url));
const childTimeout = 10000;
const start = {type: 'start', guildId: '1', channelId: '2', botId: '3', speakers: ['4']};

function run(...messages) {
  const result = spawnSync(process.execPath, [helper], {
    input: messages.map((message) => JSON.stringify(message) + '\n').join(''),
    encoding: 'utf8',
    timeout: childTimeout,
  });
  assert.ifError(result.error);
  return result;
}

test('dependency probe and a single scoped gateway join', () => {
  assert.equal(spawnSync(process.execPath, [helper, '--check'], {timeout: childTimeout}).status, 0);
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
    session: {ready: true, canEncrypt: true, protocolVersion: 1, epoch: 1n},
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
  const opus = {payload: Buffer.from('test-fixture')};
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
    return receiver.parsePacket(rtp, '', null, null, user, ssrc);
  }
  assert.equal(packet(65534, 2, 'other'), null);
  assert.equal(packet(65534, 2), opus);
  assert.equal(packet(65534, 2), null);
  assert.equal(packet(65533, 2), null);
  assert.equal(packet(65535, 2), opus);
  assert.equal(packet(0, 2), opus);
  passthrough = true;
  assert.equal(packet(1, 2), null);
  passthrough = false;
  active = false;
  assert.equal(packet(2, 2), null);
  active = true;
  assert.equal(packet(2, 3), null);
  assert.equal(decrypts, 4);
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

const state = {type: 'voice_state', data: {
  guild_id: '1', channel_id: '2', user_id: '3', session_id: 'fixture-session',
}};
const server = {type: 'voice_server', data: {
  guild_id: '1', endpoint: 'voice.invalid', token: 'fixture-token',
}};

function mockConnection(change, ...messages) {
  const voice = import.meta.resolve('@discordjs/voice');
  const opus = import.meta.resolve('opusscript');
  const mock = `
    import { EventEmitter } from 'node:events';
    import OpusScript from ${JSON.stringify(opus)};
    import { Networking, NetworkingStatusCode, VoiceConnection, VoiceConnectionStatus } from ${JSON.stringify(voice)};
    VoiceConnection.prototype.configureNetworking = function() {
      if (!this.packets.state || !this.packets.server) return;
      const dave = {
        protocolVersion: 1,
        session: {ready: true, canEncrypt: true, protocolVersion: 1, epoch: 1n, getUserIds: () => ['3', '4']},
        pendingTransitions: new Map(),
        downgraded: false,
        reinitializing: false,
      };
      const networking = new EventEmitter();
      const ws = new EventEmitter();
      networking.destroy = () => {};
      networking.state = {
        code: NetworkingStatusCode.Ready,
        connectionData: {encryptionMode: 'aead_aes256_gcm_rtpsize'},
        dave, ws,
      };
      if (this.packets.server.token === 'wrong-roster-fixture-token') dave.session.getUserIds = () => ['3', '6'];
      this.state = {...this.state, status: VoiceConnectionStatus.Ready, networking};
      ${change}
    };
  `;
  const result = spawnSync(process.execPath, ['--import', 'data:text/javascript,' + encodeURIComponent(mock), helper], {
    input: [start, ...messages].map((message) => JSON.stringify(message) + '\n').join(''),
    encoding: 'utf8',
    timeout: childTimeout,
  });
  assert.ifError(result.error);
  assert.equal(result.stderr, '');
  return {status: result.status, events: result.stdout.trim().split('\n').map(JSON.parse)};
}

test('initial dispatch pair works in either order and exact late duplicates preserve the call', () => {
  for (const pair of [[state, server], [server, state]]) {
    const result = mockConnection('', ...pair, ...pair, {type: 'stop'});
    assert.equal(result.status, 0);
    assert.deepEqual(result.events.map((event) => event.type), ['gateway', 'ready']);
  }
  const changed = mockConnection('', state, server, {
    ...server, data: {...server.data, token: 'changed-fixture-token'},
  });
  assert.equal(changed.status, 1);
  assert.deepEqual(changed.events.at(-1), {type: 'fault', code: 'conflicting_voice_server'});
});

test('late downgrade or MLS epoch change retires the active generation', () => {
  for (const mutation of [
    'dave.pendingTransitions.set(2, 0);',
    'dave.session.epoch = 2n;',
  ]) {
    const result = mockConnection(mutation + "ws.emit('packet', {op: 24, d: {}});", state, server);
    assert.equal(result.status, 1);
    assert.deepEqual(result.events.map((event) => event.type), ['gateway', 'ready', 'fault']);
    assert.equal(result.events.at(-1).code, 'dave_changed');
  }
});

test('sole member without an established MLS group cannot open the media gate', () => {
  const fixture = `
    import { DAVESession } from '@snazzah/davey';
    const session = new DAVESession(1, '3', '2');
    session.getSerializedKeyPackage();
    if (session.ready || session.epoch !== null) process.exit(1);
  `;
  assert.equal(spawnSync(process.execPath, ['--input-type=module', '-e', fixture], {
    cwd: fileURLToPath(new URL('.', import.meta.url)),
    timeout: childTimeout,
  }).status, 0);
});

test('MLS group must match the admitted Discord roster', () => {
  const result = mockConnection('', state, {
    ...server, data: {...server.data, token: 'wrong-roster-fixture-token'},
  });
  assert.equal(result.status, 1);
  assert.deepEqual(result.events.map((event) => event.type), ['gateway', 'fault']);
  assert.equal(result.events.at(-1).code, 'dave_roster_mismatch');
});

test('pinned voice package round-trips both negotiated RTP AEAD modes', async () => {
  await import('@noble/ciphers/chacha.js');
  await setImmediate();
  const packet = Buffer.from('synthetic Opus payload');
  const header = Buffer.alloc(12);
  header[0] = 0x80;
  for (const [encryptionMode, nonceBytes] of [
    ['aead_aes256_gcm_rtpsize', 12],
    ['aead_xchacha20_poly1305_rtpsize', 24],
  ]) {
    const secretKey = Buffer.alloc(32, 0x22);
    const transport = {encryptionMode, secretKey, nonceBuffer: Buffer.alloc(nonceBytes), nonce: 0};
    const encrypted = Networking.prototype.encryptOpusPacket(packet, transport, header);
    const rtp = Buffer.concat([header, ...encrypted]);
    const decrypt = () => VoiceReceiver.prototype.decrypt(rtp, encryptionMode, Buffer.alloc(nonceBytes), secretKey, 12);
    assert.deepEqual(decrypt(), packet);
    rtp[12] ^= 1;
    assert.throws(decrypt);
  }
});

test('receive rejects failed authentication and gaps even on silence', () => {
  const audio = {payload: Buffer.from('voice')};
  const silence = {payload: Buffer.from([0xf8, 0xff, 0xfe])};
  const dave = {session: {canPassthrough: () => false}};
  for (const [seq, parse, length] of [
    [2, () => null, 32],
    [2, () => { throw new Error('fixture authentication failure'); }, 32],
    [2, () => audio, 31],
    [3, () => silence, 32],
  ]) {
    const faults = [];
    let next = () => audio;
    const receiver = {parsePacket: () => next()};
    guardReceiver(receiver, () => faults.length === 0 && dave, new Map([['speaker', {}]]),
      (code) => faults.push(code));
    function packet(sequence, size = 32) {
      const rtp = Buffer.alloc(size);
      rtp.writeUInt16BE(sequence, 2);
      return receiver.parsePacket(rtp, '', null, null, 'speaker', 0);
    }
    assert.equal(packet(1), audio);
    next = parse;
    assert.equal(packet(seq, length), null);
    assert.deepEqual(faults, ['receive_discontinuity']);
    next = () => audio;
    assert.equal(packet(seq + 1), null);
  }
});

test('an 870 ms RTP gap retires before unequal forwarding delays can splice speech', () => {
  const receiveGap = 870;
  const childDelay = [55, 20];
  const parentDelay = [10, 0];
  const [first, second] = childDelay.map((delay, i) => delay + parentDelay[i]);
  assert.equal(receiveGap + second - first, 825);
  const result = receiveAcrossPause(receiveGap, childDelay, false);
  assert.deepEqual(result.events.map((event) => event.type), ['gateway', 'ready', 'audio', 'fault']);
  assert.equal(result.status, 1);
  assert.equal(result.events.at(-1).code, 'receive_discontinuity');
});

test('long idle with contiguous RTP and authenticated silence preserves the call', () => {
  const result = receiveAcrossPause(10000, [20, 20], true);
  assert.equal(result.status, 0);
  assert.deepEqual(result.events.map((event) => event.type), ['gateway', 'ready', 'audio', 'audio']);
});

function receiveAcrossPause(receiveGap, childDelay, contiguous) {
  return mockConnection(`
    let now = 1000;
    Object.defineProperty(performance, 'now', {value: () => now});
    const codec = new OpusScript(24000, 1, OpusScript.Application.VOIP);
    const audio = codec.encode(Buffer.alloc(960), 480);
    codec.delete();
    dave.session.canPassthrough = () => false;
    dave.decrypt = (payload) => payload;
    const mode = networking.state.connectionData.encryptionMode;
    const key = Buffer.alloc(32, 0x22);
    const transport = {encryptionMode: mode, secretKey: key, nonceBuffer: Buffer.alloc(12), nonce: 0};
    const receiver = this.receiver;
    const stream = receiver.subscriptions.get('4');
    const receive = (seq, silent = false) => {
      const header = Buffer.alloc(12);
      header[0] = 0x80;
      header.writeUInt16BE(seq, 2);
      header.writeUInt32BE(2, 8);
      const payload = silent ? Buffer.from([0xf8, 0xff, 0xfe]) : audio;
      const encrypted = Networking.prototype.encryptOpusPacket(payload, transport, header);
      const packet = receiver.parsePacket(Buffer.concat([header, ...encrypted]), mode, Buffer.alloc(12), key, '4', 2);
      if (packet) stream.emit('data', packet);
    };
    let pendingTick;
    const setTimer = globalThis.setTimeout;
    globalThis.setTimeout = (callback, delay, ...args) => {
      if (callback.name !== 'tick') return setTimer(callback, delay, ...args);
      return pendingTick = callback;
    };
    const tickAfter = (delay) => {
      const tick = pendingTick;
      pendingTick = undefined;
      now += delay;
      tick?.();
    };
    receive(1);
    tickAfter(${childDelay[0]});
    if (${contiguous}) {
      now = 1080;
      receive(2, true);
    }
    now = 1000 + ${receiveGap};
    receive(3);
    tickAfter(${childDelay[1]});
  `, state, server);
}

test('receive overflow faults before exporting a partial burst', () => {
  const result = mockConnection(`
    const codec = new OpusScript(24000, 1, OpusScript.Application.VOIP);
    const payload = codec.encode(Buffer.alloc(960), 480);
    const stream = this.receiver.subscriptions.get('4');
    for (let i = 0; i < 4; i++) stream.emit('data', {payload});
    codec.delete();
  `, state, server);
  assert.equal(result.status, 1);
  assert.deepEqual(result.events.map((event) => event.type), ['gateway', 'ready', 'fault']);
  assert.equal(result.events.at(-1).code, 'receive_backlog');
});
