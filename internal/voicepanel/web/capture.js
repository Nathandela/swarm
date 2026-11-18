// AudioWorklet owns a single bounded, little-endian recording buffer.
class Capture extends AudioWorkletProcessor {
  constructor() {
    super();
    this.pcm = new DataView(new ArrayBuffer(24000 * 2 * 120));
    this.bytes = 0;
    this.remaining = sampleRate;
    this.sum = 0;
    this.stopped = false;
    this.port.onmessage = () => this.finish();
  }

  finish() {
    if (this.stopped) return;
    this.stopped = true;
    const audio = this.pcm.buffer.slice(0, this.bytes);
    this.port.postMessage(audio, [audio]);
  }

  process(inputs) {
    if (this.stopped) return false;
    const channels = inputs[0];
    if (!channels || !channels.length) return true;
    for (let i = 0; i < channels[0].length; i++) {
      let sample = 0;
      for (const channel of channels) sample += channel[i];
      sample /= channels.length;
      // Box-filter downmix windows in integer units, continuously across blocks.
      let units = 24000;
      while (units > 0) {
        const used = Math.min(units, this.remaining);
        this.sum += (Number.isFinite(sample) ? sample : 0) * used;
        units -= used;
        this.remaining -= used;
        if (this.remaining === 0) {
          const average = Math.max(-1, Math.min(1, this.sum / sampleRate));
          this.pcm.setInt16(this.bytes, Math.round(average * (average < 0 ? 32768 : 32767)), true);
          this.bytes += 2;
          this.sum = 0;
          this.remaining = sampleRate;
          if (this.bytes === this.pcm.byteLength) {
            this.finish();
            return false;
          }
        }
      }
    }
    return true;
  }
}
registerProcessor("swarm-capture", Capture);
