import { NetworkingStatusCode, VoiceConnectionStatus } from '@discordjs/voice';

export const frameBytes = 960;
export const frameSamples = 480;
export const frameMS = 20;
export const maxLine = 8192;

const transports = new Set(['aead_aes256_gcm_rtpsize', 'aead_xchacha20_poly1305_rtpsize']);
const silence = Buffer.from([0xf8, 0xff, 0xfe]);

// The pinned voice stack can fall back to unencrypted Opus before MLS is active.
export function daveState(connection) {
  if (connection?.state.status !== VoiceConnectionStatus.Ready) return;
  const state = connection.state.networking.state;
  const dave = state.dave;
  if (state.code !== NetworkingStatusCode.Ready || !transports.has(state.connectionData?.encryptionMode) ||
      dave?.protocolVersion !== 1 || dave.session?.ready !== true || dave.session.protocolVersion !== 1 ||
      dave.session.epoch == null || dave.pendingTransitions.size || dave.downgraded || dave.reinitializing) return;
  return dave;
}

export function guardReceiver(receiver, gate, speakers, retire) {
  const parse = receiver.parsePacket.bind(receiver);
  const last = new Map();
  receiver.parsePacket = (rtp, mode, nonce, key, userId) => {
    const dave = gate();
    if (!dave || !speakers.has(userId) || dave.session.canPassthrough(userId) || rtp.length < 32) return null;
    const ssrc = rtp.readUInt32BE(8);
    const seq = rtp.readUInt16BE(2);
    const previous = last.get(userId);
    if (previous?.ssrc === ssrc) {
      const distance = (seq - previous.seq) & 0xffff;
      if (!distance || distance >= 0x8000) return null;
    }
    const opus = parse(rtp, mode, nonce, key, userId);
    if (!opus || opus.equals(silence)) return null;
    if (previous && previous.ssrc !== ssrc) {
      retire('speaker_rebound');
      return null;
    }
    last.set(userId, {ssrc, seq});
    return opus;
  };
}

export function pcmFrame(encoded) {
  if (typeof encoded !== 'string' || encoded.length !== 1280) return;
  const pcm = Buffer.from(encoded, 'base64');
  if (pcm.length === frameBytes && pcm.toString('base64') === encoded) return pcm;
}
