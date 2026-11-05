// What the command bar shows while it listens (DESIGN.md §3.12): the orb, the status, a
// waveform of the last moments and the words so far. Everything moves with the microphone's
// level, measured here in the browser; no audio goes anywhere because of it.

import { useEffect, useRef } from 'react';

const BARS = 12;
const BAR_EVERY_MS = 90;
const BAR_MIN_PX = 3;
const BAR_MAX_PX = 20;
const TAU = Math.PI * 2;

const stillPlease = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches;

/**
 * The microphone's level (0–1) in a ref, updated every frame without a render. Android gives
 * the microphone to speech recognition alone, and a refused or missing one just means no
 * measurement: then the level follows the words arriving instead.
 */
function useVoiceLevel(text: string) {
  const level = useRef(0);
  const measured = useRef(false);

  useEffect(() => {
    if (!text || measured.current) return;
    level.current = Math.max(level.current, 0.45 + Math.random() * 0.4);
  }, [text]);

  useEffect(() => {
    if (/Android/i.test(navigator.userAgent) || !navigator.mediaDevices?.getUserMedia) return;
    let stop = false;
    let stream: MediaStream | null = null;
    let ctx: AudioContext | null = null;
    let raf = 0;
    void navigator.mediaDevices
      .getUserMedia({ audio: true })
      .then((s) => {
        // listening may have ended while the browser opened the microphone
        if (stop) {
          s.getTracks().forEach((t) => t.stop());
          return;
        }
        stream = s;
        ctx = new AudioContext();
        const an = ctx.createAnalyser();
        an.fftSize = 512;
        ctx.createMediaStreamSource(s).connect(an);
        const buf = new Float32Array(an.fftSize);
        measured.current = true;
        const tick = () => {
          an.getFloatTimeDomainData(buf);
          let sum = 0;
          for (const v of buf) sum += v * v;
          const now = Math.min(1, Math.sqrt(sum / buf.length) * 6);
          // quick up, slow down, like a level meter
          level.current = now > level.current ? now : level.current * 0.9 + now * 0.1;
          raf = requestAnimationFrame(tick);
        };
        tick();
      })
      .catch(() => undefined);
    return () => {
      stop = true;
      cancelAnimationFrame(raf);
      stream?.getTracks().forEach((t) => t.stop());
      void ctx?.close();
    };
  }, []);

  return { level, measured };
}

interface OrbColors {
  core: string;
  mid: string;
  edge: string;
}

/** Soft flames around a bright core, added together so they glow where they overlap. */
function drawOrb(g: CanvasRenderingContext2D, size: number, t: number, lvl: number, c: OrbColors) {
  const m = size / 2;
  const R = size * (0.3 + lvl * 0.06);
  g.clearRect(0, 0, size, size);
  g.globalCompositeOperation = 'lighter';

  const halo = g.createRadialGradient(m, m, R * 0.6, m, m, m);
  halo.addColorStop(0, `rgba(${c.edge}, ${0.28 + lvl * 0.2})`);
  halo.addColorStop(1, `rgba(${c.edge}, 0)`);
  g.fillStyle = halo;
  g.fillRect(0, 0, size, size);

  for (let i = 0; i < 9; i++) {
    const a = (i / 9) * TAU + t * (i % 2 ? 0.7 : -0.5) + Math.sin(t * 1.3 + i) * 0.4;
    const d = R * (0.55 + 0.35 * Math.sin(t * 2.3 + i * 1.9) + lvl * 0.5);
    const x = m + Math.cos(a) * d;
    const y = m + Math.sin(a) * d - size * 0.02;
    const r = R * (0.6 + 0.2 * Math.sin(t * 3.1 + i * 0.7));
    const f = g.createRadialGradient(x, y, 0, x, y, r);
    f.addColorStop(0, `rgba(${c.mid}, 0.55)`);
    f.addColorStop(0.6, `rgba(${c.edge}, 0.3)`);
    f.addColorStop(1, `rgba(${c.edge}, 0)`);
    g.fillStyle = f;
    g.fillRect(0, 0, size, size);
  }

  const core = g.createRadialGradient(m, m, 0, m, m, R * 1.05);
  core.addColorStop(0, `rgba(${c.core}, 1)`);
  core.addColorStop(0.35, `rgba(${c.core}, 0.85)`);
  core.addColorStop(0.65, `rgba(${c.mid}, 0.95)`);
  core.addColorStop(1, `rgba(${c.edge}, 0)`);
  g.fillStyle = core;
  g.fillRect(0, 0, size, size);
}

export function VoiceStatus({ label, text }: { label: string; text: string }) {
  const { level, measured } = useVoiceLevel(text);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const waveRef = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    const g = canvas?.getContext('2d');
    const bars = Array.from(waveRef.current?.children ?? []) as HTMLElement[];
    if (!canvas || !g) return;
    const css = getComputedStyle(canvas);
    const colors: OrbColors = {
      core: css.getPropertyValue('--voice-orb-core'),
      mid: css.getPropertyValue('--voice-orb-mid'),
      edge: css.getPropertyValue('--voice-orb-edge'),
    };
    const dpr = window.devicePixelRatio || 1;
    const size = Math.round(canvas.clientWidth * dpr);
    canvas.width = canvas.height = size;

    const heights = new Array<number>(BARS).fill(0);
    const paintBars = () => bars.forEach((b, i) => (b.style.height = `${BAR_MIN_PX + heights[i] * (BAR_MAX_PX - BAR_MIN_PX)}px`));

    if (stillPlease()) {
      drawOrb(g, size, 1, 0.3, colors);
      paintBars();
      return;
    }
    let raf = 0;
    let lastBar = 0;
    const frame = (now: number) => {
      // without a measurement the level only comes from words arriving: let it fall again
      if (!measured.current) level.current *= 0.96;
      drawOrb(g, size, now / 1000, level.current, colors);
      if (now - lastBar >= BAR_EVERY_MS) {
        lastBar = now;
        heights.shift();
        heights.push(level.current);
        paintBars();
      }
      raf = requestAnimationFrame(frame);
    };
    raf = requestAnimationFrame(frame);
    return () => cancelAnimationFrame(raf);
  }, [level, measured]);

  return (
    <div className="voice-status">
      <canvas ref={canvasRef} className="voice-orb" aria-hidden="true" />
      <span className="voice-dot" aria-hidden="true" />
      <span className="voice-label">{label}</span>
      <span className="voice-wave" ref={waveRef} aria-hidden="true">
        {Array.from({ length: BARS }, (_, i) => (
          <span key={i} />
        ))}
      </span>
    </div>
  );
}

/** The words so far, under the bar. Long ones keep their end: that's what is being said. */
export function VoiceCaption({ text, hint }: { text: string; hint: string }) {
  return (
    <div className="voice-caption" role="status" aria-live="polite">
      <span className={text ? '' : 'is-hint'}>{text || hint}</span>
    </div>
  );
}
