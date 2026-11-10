import { execFileSync } from 'node:child_process';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';

// The page as Chrome paints it, frame by frame, at full quality: Playwright's own video is a
// low-bitrate VP8 that smears text. Chrome only sends a frame when something changed, so each
// keeps its time and lasts until the next one.

export class Recorder {
  private frames: { file: string; at: number }[] = [];
  private stopped = 0;
  private dir: string;
  private session: Awaited<ReturnType<ReturnType<Page['context']>['newCDPSession']>> | null = null;

  constructor(
    private page: Page,
    out: string,
  ) {
    this.dir = join(out, 'frames');
    rmSync(this.dir, { recursive: true, force: true });
    mkdirSync(this.dir, { recursive: true });
  }

  async start(width: number, height: number) {
    const cdp = await this.page.context().newCDPSession(this.page);
    this.session = cdp;
    cdp.on('Page.screencastFrame', ({ data, metadata, sessionId }) => {
      const file = join(this.dir, `${String(this.frames.length).padStart(5, '0')}.jpg`);
      writeFileSync(file, Buffer.from(data, 'base64'));
      this.frames.push({ file, at: metadata.timestamp ?? Date.now() / 1000 });
      void cdp.send('Page.screencastFrameAck', { sessionId }).catch(() => {});
    });
    await cdp.send('Page.startScreencast', { format: 'jpeg', quality: 95, maxWidth: width, maxHeight: height, everyNthFrame: 1 });
  }

  async stop() {
    this.stopped = Date.now() / 1000;
    await this.session?.send('Page.stopScreencast').catch(() => {});
    await this.session?.detach().catch(() => {});
  }

  /** Seconds into the video now, to cut a clip at later. */
  mark(): number {
    return this.frames.length ? Date.now() / 1000 - this.frames[0].at : 0;
  }

  /**
   * A looping GIF of part of the MP4: a README plays a GIF from the repository by itself, while a
   * video plays there only once uploaded through GitHub's own editor.
   */
  gif(mp4: string, out: string, from: number, to: number, width = 960) {
    execFileSync(
      'ffmpeg',
      ['-y', '-loglevel', 'error', '-ss', from.toFixed(2), '-t', (to - from).toFixed(2), '-i', mp4,
        '-vf', `fps=12,scale=${width}:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128:stats_mode=diff[p];[b][p]paletteuse=dither=none:diff_mode=rectangle`,
        out],
      { stdio: 'inherit' },
    );
  }

  /** An H.264 MP4 at 30 frames a second, as GitHub and every browser play it. */
  encode(mp4: string, width: number, height: number) {
    if (this.frames.length === 0) throw new Error('no frames were recorded');
    const list = join(this.dir, 'list.txt');
    const lines: string[] = [];
    for (const [i, f] of this.frames.entries()) {
      const next = this.frames[i + 1]?.at ?? this.stopped;
      lines.push(`file '${f.file}'`, `duration ${Math.max(0.001, next - f.at).toFixed(4)}`);
    }
    // the concat demuxer takes the last file's duration only when it is listed again
    lines.push(`file '${this.frames[this.frames.length - 1].file}'`);
    writeFileSync(list, lines.join('\n') + '\n');
    execFileSync(
      'ffmpeg',
      ['-y', '-loglevel', 'error', '-f', 'concat', '-safe', '0', '-i', list,
        // JPEG frames are full range; players expect the TV range of yuv420p
        '-vf', `scale=${width}:${height}:flags=lanczos:in_range=pc:out_range=tv,fps=30,format=yuv420p`,
        '-c:v', 'libx264', '-pix_fmt', 'yuv420p', '-color_range', 'tv', '-preset', 'slow', '-crf', '20', '-tune', 'animation', '-movflags', '+faststart', mp4],
      { stdio: 'inherit' },
    );
  }
}
