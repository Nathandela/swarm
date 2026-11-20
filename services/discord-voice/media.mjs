import { NetworkingStatusCode, VoiceConnectionStatus } from '@discordjs/voice';

export const frameBytes = 960;
export const frameSamples = 480;
export const frameMS = 20;
export const maxLine = 8192;
const speechGapMS = 420;

const transports = new Set(['aead_aes256_gcm_rtpsize', 'aead_xchacha20_poly1305_rtpsize']);
const silence = Buffer.from([0xf8, 0xff, 0xfe]);

// The pinned voice stack can fall back to unencrypted Opus before MLS is active.
export function daveState(connection) {
  if (connection?.state.status !== VoiceConnectionStatus.Ready) return;
  const state = connection.state.networking.state;
  const dave = state.dave;
  if (state.code !== NetworkingStatusCode.Ready || !transports.has(state.connectionData?.encryptionMode) ||
      dave?.protocolVersion !== 1 || dave.session?.ready !== true || dave.session.canEncrypt !== true ||
      dave.session.protocolVersion !== 1 ||
      dave.session.epoch == null || dave.pendingTransitions.size || dave.downgraded || dave.reinitializing) return;
  return dave;
}

export function guardReceiver(receiver, gate, speakers, retire, now = () => performance.now()) {
  const parse = receiver.parsePacket.bind(receiver);
  const last = new Map();
  receiver.parsePacket = (rtp, mode, nonce, key, userId, ssrc) => {
    const dave = gate();
    if (!dave || !speakers.has(userId) || dave.session.canPassthrough(userId) || rtp.length < 32) return null;
    const seq = rtp.readUInt16BE(2);
    const previous = last.get(userId);
    const sameSSRC = previous?.ssrc === ssrc;
    const distance = sameSSRC ? (seq - previous.seq) & 0xffff : 1;
    if (!distance || distance >= 0x8000) return null;

    const at = now();
    const packet = parse(rtp, mode, nonce, key, userId, ssrc);
    if (sameSSRC && at - previous.audioAt < speechGapMS && (!packet || distance !== 1)) {
      retire('receive_discontinuity');
      return null;
    }
    if (!packet) return null;
    if (packet.payload.equals(silence)) {
      if (sameSSRC) previous.seq = seq;
      return null;
    }
    if (previous && !sameSSRC) {
      retire('speaker_rebound');
      return null;
    }
    last.set(userId, {ssrc, seq, audioAt: at});
    return packet;
  };
}

export function pcmFrame(encoded) {
  if (typeof encoded !== 'string' || encoded.length !== 1280) return;
  const pcm = Buffer.from(encoded, 'base64');
  if (pcm.length === frameBytes && pcm.toString('base64') === encoded) return pcm;
}
