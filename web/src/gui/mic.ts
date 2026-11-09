// The viewer's microphone, for the app in the session (docs/API-gui.md §7): raw 48 kHz mono PCM to
// the hub. Browsers only give a microphone to a secure page, so on plain HTTP this says so.

export type MicState = 'on' | 'off' | 'insecure' | 'denied' | 'missing' | 'failed';

export class MicInput {
  private ctx: AudioContext | null = null;
  private stream: MediaStream | null = null;
  private ws: WebSocket | null = null;
  private node: AudioWorkletNode | null = null;
  private wanted = false;

  constructor(
    private session: string,
    private report: (state: MicState, message?: string) => void,
  ) {}

  async start() {
    if (this.wanted) return;
    this.wanted = true;
    if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia) {
      this.wanted = false;
      this.report('insecure');
      return;
    }
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
      // a click on off while the browser was asking
      if (!this.wanted) {
        stream.getTracks().forEach((t) => t.stop());
        return;
      }
      this.stream = stream;
      const ctx = new AudioContext({ sampleRate: 48000, latencyHint: 'interactive' });
      this.ctx = ctx;
      await ctx.audioWorklet.addModule('/mic-worklet.js');
      await ctx.resume();
      const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(`${scheme}://${location.host}/api/gui/sessions/${encodeURIComponent(this.session)}/mic`);
      ws.binaryType = 'arraybuffer';
      this.ws = ws;
      await new Promise<void>((resolve, reject) => {
        ws.onopen = () => resolve();
        ws.onerror = () => reject(new Error('The hub did not take the microphone. Another viewer may be using it.'));
      });
      // The hub ended it, or the connection did: the button must not go on saying it is on.
      ws.onclose = () => {
        if (this.ws === ws) this.teardown('off');
      };
      const node = new AudioWorkletNode(ctx, 'nb-mic', { numberOfInputs: 1, numberOfOutputs: 0, channelCount: 1, channelCountMode: 'explicit' });
      node.port.onmessage = (e) => {
        if (ws.readyState === WebSocket.OPEN) ws.send(e.data as ArrayBuffer);
      };
      this.node = node;
      ctx.createMediaStreamSource(stream).connect(node);
      if (this.wanted) this.report('on');
      else this.teardown('off');
    } catch (err) {
      const name = err instanceof DOMException ? err.name : '';
      if (name === 'NotAllowedError' || name === 'SecurityError') this.teardown('denied');
      else if (name === 'NotFoundError') this.teardown('missing');
      else this.teardown('failed', err instanceof Error ? err.message : undefined);
    }
  }

  stop() {
    this.teardown('off');
  }

  private teardown(state: MicState, message?: string) {
    this.wanted = false;
    this.node?.disconnect();
    this.node = null;
    this.stream?.getTracks().forEach((t) => t.stop());
    this.stream = null;
    void this.ctx?.close();
    this.ctx = null;
    const ws = this.ws;
    this.ws = null;
    ws?.close();
    this.report(state, message);
  }
}
