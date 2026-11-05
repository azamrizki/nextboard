// The session's sound (docs/API-gui.md §7): raw 48 kHz stereo PCM from the hub, played with
// scheduled buffers, which unlike AudioWorklet and WebCodecs also work on plain http.

const RATE = 48000;
/** Delay to start playing with, to ride out network jitter. */
const LEAD_S = 0.06;
/** Further behind than this, drop sound until caught up: late sound is worse than a gap. */
const MAX_AHEAD_S = 0.25;

export class AudioOutput {
  private ctx: AudioContext | null = null;
  private ws: WebSocket | null = null;
  private wanted = false;
  private nextTime = 0;
  private retry = 1000;
  private retryTimer = 0;

  constructor(private session: string) {}

  /**
   * Browsers only start sound after a user gesture, and a request made earlier doesn't
   * count. So keep asking: with `allow="autoplay"`, a click anywhere on the board will do.
   */
  gesture() {
    if (this.wanted && this.ctx?.state !== 'running') void this.context().resume();
  }

  setWanted(on: boolean) {
    this.wanted = on;
    if (on) void this.context().resume();
    this.sync();
  }

  private context(): AudioContext {
    if (!this.ctx) {
      this.ctx = new AudioContext({ sampleRate: RATE, latencyHint: 'interactive' });
      this.ctx.onstatechange = () => this.sync();
    }
    return this.ctx;
  }

  private sync() {
    const run = this.wanted && this.ctx?.state === 'running';
    if (run && !this.ws) this.open();
    if (!run && this.ws) {
      this.ws.close();
      this.ws = null;
    }
    if (!run) window.clearTimeout(this.retryTimer);
  }

  private open() {
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
    const ws = new WebSocket(`${scheme}://${location.host}/api/gui/sessions/${encodeURIComponent(this.session)}/audio`);
    ws.binaryType = 'arraybuffer';
    ws.onmessage = (e) => {
      this.retry = 1000;
      this.play(e.data as ArrayBuffer);
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      this.retryTimer = window.setTimeout(() => this.sync(), this.retry);
      this.retry = Math.min(this.retry * 2, 10000);
    };
    this.ws = ws;
  }

  private play(data: ArrayBuffer) {
    const ctx = this.ctx;
    if (!ctx) return;
    const pcm = new Int16Array(data);
    const frames = pcm.length >> 1;
    if (frames === 0) return;
    const now = ctx.currentTime;
    if (this.nextTime < now) this.nextTime = now + LEAD_S;
    else if (this.nextTime - now > MAX_AHEAD_S) return;
    const buffer = ctx.createBuffer(2, frames, RATE);
    const left = buffer.getChannelData(0);
    const right = buffer.getChannelData(1);
    for (let i = 0; i < frames; i++) {
      left[i] = pcm[i * 2] / 32768;
      right[i] = pcm[i * 2 + 1] / 32768;
    }
    const source = ctx.createBufferSource();
    source.buffer = buffer;
    source.connect(ctx.destination);
    source.start(this.nextTime);
    this.nextTime += frames / RATE;
  }
}
