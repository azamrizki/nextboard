// Which consoles are drawn by WebGL. Browsers cap live WebGL contexts (Chrome 16 on a desktop, 8 on
// Android, Safari 16 and it never restores a lost one) and drop the one that drew least recently,
// so a board with many consoles used to blank some. The pool keeps a budget below the cap and gives
// it to what is on screen: the focused console first, then the largest visible ones. A console off
// screen keeps its context until another needs it, so panning back and forth changes nothing; the
// rest are drawn by xterm's DOM renderer. A swap keeps the columns and rows (xtermPatches.ts #6015),
// so the agent's screen is never resized for it.

import { WebglAddon } from '@xterm/addon-webgl';
import type { Terminal } from '@xterm/xterm';
import { prefs } from '../lib/format';
import { patchWebgl, prepareTerminal, releaseWebgl, renderNow, watchContextLoss } from './xtermPatches';

/** A console given WebGL keeps it this long, unless the focused one needs it. */
export const HOLD_MS = 2000;
/** Contexts made in one go while panning; more wait for the next tick, so a long pan does not stall a frame. */
export const ATTACHES_PER_TICK = 4;
export const TICK_MS = 50;
/** Contexts made per frame for consoles just mounted: each costs a shader compile and an atlas, a dozen in one task stalled the page. */
export const ATTACHES_PER_FRAME = 2;
const SETTLE_MS = 200;
/** One that has WebGL ranks as if this much more of it showed: two of a size do not trade it back and forth. */
export const STICKY = 1.25;

/** What the pool knows of one console. */
export interface Slot {
  id: number;
  /** CSS pixels of it on screen (board zoom included). */
  area: number;
  focused: boolean;
  gl: boolean;
  /** When it got WebGL. */
  since: number;
  /** When it was last on screen. */
  shown: number;
  /** No WebGL for it this session. */
  never: boolean;
  /** Not before this (after a lost context). */
  retryAt: number;
  /** Its WebGL may not be taken (renderers cannot be swapped without a resize). */
  pinned?: boolean;
}

export interface Plan {
  release: number[];
  attach: number[];
  /** When to plan again, or null. */
  again: number | null;
}

/** What to swap now; `cap` is how many contexts may be made. */
export function plan(slots: readonly Slot[], budget: number, now: number, cap = ATTACHES_PER_TICK): Plan {
  const ready = (s: Slot) => !s.never && s.retryAt <= now;
  const score = (s: Slot) => s.area * (s.gl ? STICKY : 1);
  const wanted = slots
    .filter((s) => s.area > 0 && (s.gl || ready(s)))
    .sort((a, b) => Number(b.focused) - Number(a.focused) || score(b) - score(a) || Number(b.gl) - Number(a.gl) || a.id - b.id)
    .slice(0, Math.max(0, budget));
  const want = new Set(wanted.map((s) => s.id));
  // off screen first, then the one shown longest ago
  const victims = slots.filter((s) => s.gl && !s.pinned && !want.has(s.id)).sort((a, b) => Number(a.area > 0) - Number(b.area > 0) || a.shown - b.shown);
  let held = slots.filter((s) => s.gl).length;
  const release: number[] = [];
  const attach: number[] = [];
  let again: number | null = null;
  const later = (t: number) => {
    again = again === null ? t : Math.min(again, t);
  };
  // a lowered budget: give back what is over it
  while (held > budget && victims.length) {
    release.push(victims.shift()!.id);
    held--;
  }
  for (const s of wanted) {
    if (s.gl) continue;
    if (attach.length >= cap) {
      later(now + TICK_MS);
      break;
    }
    if (held < budget) {
      attach.push(s.id);
      held++;
      continue;
    }
    const i = victims.findIndex((v) => s.focused || now - v.since >= HOLD_MS);
    if (i < 0) {
      for (const v of victims) later(v.since + HOLD_MS);
      continue;
    }
    release.push(victims.splice(i, 1)[0].id);
    attach.push(s.id);
  }
  for (const s of slots) if (s.area > 0 && !s.never && !s.gl && s.retryAt > now) later(s.retryAt);
  return { release, attach, again };
}

export interface BudgetEnv {
  userAgent: string;
  platform: string;
  maxTouchPoints: number;
  /** localStorage nb.webgl: "force" allows a software GPU, "off" turns WebGL off. */
  mode: string | null;
  /** localStorage nb.webglMax: a number of contexts. */
  max: string | null;
  hardware: () => boolean;
}

/** Contexts the pool may hold: below each browser's cap, which other tabs and pages share. */
export function budgetFor(env: BudgetEnv): number {
  const mode = env.mode?.trim().toLowerCase();
  if (mode === 'off') return 0;
  if (mode !== 'force' && !env.hardware()) return 0;
  const max = env.max?.trim() ? Number(env.max) : NaN;
  if (Number.isFinite(max)) return Math.max(0, Math.floor(max));
  if (/Android/i.test(env.userAgent)) return 6; // Chrome and WebView allow 8
  // iPadOS says it is a Mac
  if (/iPhone|iPad|iPod/.test(env.userAgent) || (env.platform === 'MacIntel' && env.maxTouchPoints > 1)) return 8;
  return 12;
}

/** After the n-th lost context of a console: 2 s, doubling to 64 s. */
export function retryDelay(losses: number): number {
  return Math.min(64_000, 2000 * 2 ** Math.max(0, losses - 1));
}

/**
 * Losing contexts the pool held means the browser's real cap is lower (other tabs in the same
 * process, extensions): keep to what was left. Losing all of them at once is the GPU resetting.
 */
export function budgetAfterLoss(budget: number, held: number, lost: number): number {
  if (lost <= 0 || lost >= held) return budget;
  return Math.min(budget, held - lost);
}

/**
 * WebGL only pays off on a hardware GPU. Software rasterisers (SwiftShader, llvmpipe — headless
 * browsers, VMs, GPU-blocklisted machines) are slower than xterm's DOM renderer, so we skip WebGL there.
 */
function hasHardwareWebgl(): boolean {
  try {
    const canvas = document.createElement('canvas');
    const gl = (canvas.getContext('webgl2') ?? canvas.getContext('webgl')) as WebGLRenderingContext | null;
    if (!gl) return false;
    const info = gl.getExtension('WEBGL_debug_renderer_info');
    const renderer = String(info ? gl.getParameter(info.UNMASKED_RENDERER_WEBGL) : gl.getParameter(gl.RENDERER));
    gl.getExtension('WEBGL_lose_context')?.loseContext();
    return !/swiftshader|llvmpipe|softpipe|software|basic render/i.test(renderer);
  } catch {
    return false;
  }
}

// ---- the pool of this page ----

interface Entry extends Slot {
  term: Terminal;
  el: HTMLElement;
  refit: () => void;
  size: () => { cols: number; rows: number } | undefined;
  addon: WebglAddon | null;
  stop: (() => void) | null;
  losses: number;
}

let budget: number | null = null;
let seq = 0;
const entries = new Map<number, Entry>();
const byElement = new Map<Element, Entry>();
let io: IntersectionObserver | null = null;
let timer = 0;
let timerAt = Infinity;
let queued = false;
/** A burst is under way: plans meanwhile (the tick, a focus) keep to its pace. */
let bursting = false;
let lossBatch: { held: number; lost: number } | null = null;

function currentBudget(): number {
  if (budget === null) {
    budget = budgetFor({
      userAgent: navigator.userAgent,
      platform: navigator.platform,
      maxTouchPoints: navigator.maxTouchPoints || 0,
      mode: prefs.get('webgl'),
      max: prefs.get('webglMax'),
      hardware: hasHardwareWebgl,
    });
  }
  return budget;
}

const now = () => performance.now();

function schedule(delay: number) {
  const at = now() + delay;
  if (timer && timerAt <= at) return;
  window.clearTimeout(timer);
  timerAt = at;
  timer = window.setTimeout(() => {
    timer = 0;
    timerAt = Infinity;
    reconcile();
  }, delay);
}

/** Plans and swaps; true when more would have been attached but for `cap`. */
function reconcile(cap = bursting ? ATTACHES_PER_FRAME : ATTACHES_PER_TICK): boolean {
  // nothing changes in a background tab; visibilitychange plans again
  if (document.hidden || !entries.size) return false;
  const t = now();
  const p = plan([...entries.values()], currentBudget(), t, cap);
  for (const id of p.release) {
    const e = entries.get(id);
    if (e) release(e, true);
  }
  for (const id of p.attach) {
    const e = entries.get(id);
    if (e) attach(e);
  }
  if (p.again !== null) schedule(Math.max(0, p.again - t));
  return p.attach.length >= cap;
}

/** Consoles just mounted: ranked together, their contexts made ATTACHES_PER_FRAME a frame, best first. */
function burst() {
  bursting = reconcile(ATTACHES_PER_FRAME);
  if (bursting) requestAnimationFrame(burst);
}

function areaOf(r: DOMRectReadOnly): number {
  const w = Math.min(r.right, window.innerWidth) - Math.max(r.left, 0);
  const h = Math.min(r.bottom, window.innerHeight) - Math.max(r.top, 0);
  return w > 0 && h > 0 ? w * h : 0;
}

function observer(): IntersectionObserver {
  if (!io) {
    io = new IntersectionObserver(
      (list) => {
        const t = now();
        for (const x of list) {
          const e = byElement.get(x.target);
          if (!e) continue;
          e.area = x.isIntersecting ? x.intersectionRect.width * x.intersectionRect.height : 0;
          if (e.area > 0) e.shown = t;
        }
        schedule(SETTLE_MS);
      },
      { threshold: [0, 0.05, 0.15, 0.3, 0.5, 0.7, 0.85, 1] },
    );
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden) schedule(SETTLE_MS);
    });
  }
  return io;
}

/** In development: a swap must not change what fit gives (xtermPatches.ts #6015). */
function sameSize(e: Entry, before: { cols: number; rows: number } | undefined) {
  if (!import.meta.env.DEV || !before) return;
  const after = e.size();
  if (after && (after.cols !== before.cols || after.rows !== before.rows)) {
    console.error(`webglPool: a renderer swap changed a console from ${before.cols}×${before.rows} to ${after.cols}×${after.rows}`);
  }
}

function attach(e: Entry) {
  if (e.addon || !e.term.element) return;
  const before = import.meta.env.DEV ? e.size() : undefined;
  let addon: WebglAddon | null = null;
  try {
    addon = new WebglAddon();
    e.term.loadAddon(addon);
  } catch {
    // no WebGL2 here: the DOM renderer stays
    try {
      addon?.dispose();
    } catch {
      /* ignore */
    }
    e.never = true;
    return;
  }
  patchWebgl(addon);
  e.addon = addon;
  e.gl = true;
  e.since = now();
  const mine = addon;
  const stopLoss = watchContextLoss(addon, () => lost(e, mine));
  const stopGuard = guard(e);
  e.stop = () => {
    stopLoss();
    stopGuard();
  };
  renderNow(e.term);
  e.refit();
  sameSize(e, before);
}

/** Back to the DOM renderer; `show` draws it now (not for a console about to close). */
function release(e: Entry, show: boolean) {
  if (!e.addon) return;
  const before = show && import.meta.env.DEV ? e.size() : undefined;
  e.stop?.();
  e.stop = null;
  releaseWebgl(e.addon);
  e.addon = null;
  e.gl = false;
  if (!show) return;
  renderNow(e.term);
  e.refit();
  sameSize(e, before);
}

function lost(e: Entry, addon: WebglAddon) {
  if (e.addon !== addon) return;
  if (!lossBatch) {
    lossBatch = { held: [...entries.values()].filter((x) => x.gl).length, lost: 0 };
    window.setTimeout(() => {
      const b = lossBatch!;
      lossBatch = null;
      budget = budgetAfterLoss(currentBudget(), b.held, b.lost);
      schedule(SETTLE_MS);
    }, 100);
  }
  lossBatch.lost++;
  release(e, true);
  e.losses++;
  e.retryAt = now() + retryDelay(e.losses);
}

/**
 * The renderer sizes its canvas by the device pixels the browser reports for it. Under an emulated
 * pixel ratio (Chrome's device mode, test browsers) the browser reports CSS pixels, the text lands
 * outside the canvas and the console shows nothing. That holds for the whole page, so no console
 * gets WebGL again.
 */
function guard(e: Entry): () => void {
  const watch = new ResizeObserver(([x]) => {
    const device = x?.devicePixelContentBoxSize?.[0]?.inlineSize;
    const css = x?.contentRect.width ?? 0;
    const ratio = window.devicePixelRatio || 1;
    if (!device || css <= 0 || Math.abs(device - css * ratio) <= Math.max(2, ratio)) return;
    for (const other of entries.values()) {
      other.never = true;
      release(other, true);
    }
  });
  try {
    watch.observe(e.term.element!, { box: 'device-pixel-content-box' });
  } catch {
    // not reported by this browser; the renderer keeps its own canvas size then
  }
  return () => watch.disconnect();
}

/**
 * Lets the pool decide this terminal's renderer; call right after term.open(). `refit` fits the
 * terminal to its element, `size` is what fitting would give. Gives back what lets it go: the
 * terminal is disposed after that.
 */
export function registerTerminal(term: Terminal, el: HTMLElement, refit: () => void, size: () => { cols: number; rows: number } | undefined): () => void {
  const b = currentBudget();
  const swappable = prepareTerminal(term, b > 0);
  if (b === 0) return () => undefined;
  const t = now();
  const e: Entry = { id: ++seq, area: areaOf(el.getBoundingClientRect()), focused: false, gl: false, since: 0, shown: t, never: false, retryAt: 0, term, el, refit, size, addon: null, stop: null, losses: 0 };
  entries.set(e.id, e);
  if (!swappable) {
    // a swap would resize the PTY: WebGL now, before the first fit, or never
    if ([...entries.values()].filter((x) => x.gl).length < b) attach(e);
    if (e.gl) e.pinned = true;
    else e.never = true;
  }
  byElement.set(el, e);
  observer().observe(el);
  const ta = term.textarea;
  const onFocus = () => {
    e.focused = true;
    reconcile();
  };
  const onBlur = () => {
    e.focused = false;
    schedule(SETTLE_MS);
  };
  ta?.addEventListener('focus', onFocus);
  ta?.addEventListener('blur', onBlur);
  // Every console mounted in this task, ranked together; their contexts then come a few a frame
  // (burst), not all in one task. Contexts spread over many ticks once left the GPU process holding
  // more (SwiftShader, 16 consoles at ratio 2: about 50 MB), so the burst runs back to back.
  if (!queued) {
    queued = true;
    queueMicrotask(() => {
      queued = false;
      // a burst under way takes them in its next frame
      if (!bursting) burst();
    });
  }
  return () => {
    entries.delete(e.id);
    byElement.delete(el);
    io?.unobserve(el);
    ta?.removeEventListener('focus', onFocus);
    ta?.removeEventListener('blur', onBlur);
    release(e, false);
    schedule(SETTLE_MS);
  };
}
