// Turns the microphone's float samples into 20 ms frames of signed 16-bit PCM (docs/API-gui.md §7).
class NbMic extends AudioWorkletProcessor {
  constructor() {
    super();
    this.frame = new Int16Array(960);
    this.at = 0;
  }

  process(inputs) {
    const samples = inputs[0]?.[0];
    if (!samples) return true;
    for (let i = 0; i < samples.length; i++) {
      this.frame[this.at++] = Math.max(-1, Math.min(1, samples[i])) * 32767;
      if (this.at === this.frame.length) {
        const out = this.frame.slice().buffer;
        this.port.postMessage(out, [out]);
        this.at = 0;
      }
    }
    return true;
  }
}

registerProcessor('nb-mic', NbMic);
