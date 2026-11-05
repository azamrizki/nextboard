// Hardware encoded video from the hub (docs/API-gui.md §7), decoded with WebCodecs. Every
// decoded picture is acknowledged: the hub holds the encoder back when answers are late, so
// a slow link lowers the frame rate instead of building up delay.

export type VideoCodec = 'h264' | 'av1';

/** Any AV1 main profile stream the hub sends: level 5.1, 8 bit. */
const AV1_CODEC = 'av01.0.13M.08';

/**
 * Which format to ask for. AV1 looks better at 40 % less bitrate, but is only worth its
 * decoding cost with hardware; on a link too slow for H.264 it is worth it anyway.
 */
export async function av1Decoding(): Promise<{ hardware: boolean; software: boolean }> {
  const check = async (hardwareAcceleration: HardwareAcceleration) => {
    try {
      return (await VideoDecoder.isConfigSupported({ codec: AV1_CODEC, hardwareAcceleration })).supported === true;
    } catch {
      return false;
    }
  };
  return { hardware: await check('prefer-hardware'), software: await check('no-preference') };
}

export interface VideoEvents {
  onHello: (mbps: number, codec: VideoCodec) => void;
  /** A picture arrived, before decoding. */
  onFrame: (bytes: number, key: boolean) => void;
  /** The hub had to wait too long: this bitrate doesn't fit the link. */
  onSlow: () => void;
  onClosed: () => void;
}

const FRAME_US = 16667;
const ACK = new Uint8Array(1);

/** The codec string comes from the stream's own SPS: avc1.<profile><constraints><level>. */
function codecOf(au: Uint8Array): string {
  for (let i = 0; i + 7 < au.length; i++) {
    if (au[i] === 0 && au[i + 1] === 0 && au[i + 2] === 1 && (au[i + 3] & 0x1f) === 7) {
      const hex = (v: number) => v.toString(16).padStart(2, '0');
      return `avc1.${hex(au[i + 4])}${hex(au[i + 5])}${hex(au[i + 6])}`;
    }
  }
  return '';
}

export class VideoStream {
  /** Draw decoded pictures. Decoding continues without it: every picture depends on the last. */
  draw = false;

  private ws: WebSocket;
  private decoder: VideoDecoder | null = null;
  private ctx: CanvasRenderingContext2D | null;
  private timestamp = 0;
  private closed = false;
  private codec: VideoCodec = 'h264';

  constructor(
    session: string,
    private canvas: HTMLCanvasElement,
    readonly width: number,
    readonly height: number,
    mbps: number,
    codec: VideoCodec,
    private events: VideoEvents,
  ) {
    this.ctx = canvas.getContext('2d', { alpha: false, desynchronized: true });
    const q = new URLSearchParams({ w: String(width), h: String(height), codec });
    if (mbps > 0) q.set('mbps', mbps.toFixed(2));
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
    this.ws = new WebSocket(`${scheme}://${location.host}/api/gui/sessions/${encodeURIComponent(session)}/video?${q}`);
    this.ws.binaryType = 'arraybuffer';
    this.ws.onmessage = (e) => {
      if (typeof e.data === 'string') this.control(e.data);
      else this.picture(new Uint8Array(e.data as ArrayBuffer));
    };
    this.ws.onclose = () => this.finish();
  }

  private control(text: string) {
    const m = JSON.parse(text) as { t?: string; mbps?: number; codec?: VideoCodec };
    if (m.t === 'hello' && m.mbps) {
      // The hub says what it can really do.
      this.codec = m.codec === 'av1' ? 'av1' : 'h264';
      this.events.onHello(m.mbps, this.codec);
    }
    if (m.t === 'slow') this.events.onSlow();
  }

  private ack() {
    if (this.ws.readyState === WebSocket.OPEN) this.ws.send(ACK);
  }

  private picture(msg: Uint8Array) {
    const key = (msg[0] & 1) === 1;
    const au = msg.subarray(1);
    this.events.onFrame(au.length, key);
    if (!this.decoder) {
      const codec = !key ? '' : this.codec === 'av1' ? AV1_CODEC : codecOf(au);
      if (!codec) return this.ack();
      this.decoder = new VideoDecoder({
        output: (frame) => this.output(frame),
        error: () => this.stop(),
      });
      this.decoder.configure({ codec, optimizeForLatency: true });
    }
    this.decoder.decode(new EncodedVideoChunk({ type: key ? 'key' : 'delta', timestamp: (this.timestamp += FRAME_US), data: au }));
  }

  private output(frame: VideoFrame) {
    if (this.draw && this.ctx) {
      if (this.canvas.width !== frame.displayWidth || this.canvas.height !== frame.displayHeight) {
        this.canvas.width = frame.displayWidth;
        this.canvas.height = frame.displayHeight;
      }
      this.ctx.drawImage(frame, 0, 0);
    }
    frame.close();
    this.ack();
  }

  private finish() {
    if (this.closed) return;
    this.closed = true;
    const d = this.decoder;
    this.decoder = null;
    if (d && d.state !== 'closed') d.close();
    this.events.onClosed();
  }

  stop() {
    this.ws.close();
    this.finish();
  }
}
