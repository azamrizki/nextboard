// The memory galaxy on a canvas, in the spirit of Obsidian's graph view: a force
// layout you can drag and zoom, where hovering a node lights up its neighbours and
// dims the rest. Around it, a galaxy: dust on spiral arms, nebulae in each project's
// colour, a star field with parallax, and pages that orbit their star. No React in
// here; MemoryPage talks to it through a few methods and callbacks.

import { forceCollide, forceLink, forceManyBody, forceSimulation, type Simulation, type SimulationLinkDatum, type SimulationNodeDatum } from 'd3-force';
import { CORE_HUE, CRITICAL_HUE, type LinkKind, type MemLink, type MemNode, type NodeKind } from './graph';

interface N extends SimulationNodeDatum, MemNode {
  x: number;
  y: number;
  /** Where it is drawn: x,y turned around its parent as time passes. */
  dx: number;
  dy: number;
  r: number;
  fade: number;
  p: N | null;
  neighbours: Set<N>;
  system: N;
}

interface L extends SimulationLinkDatum<N> {
  source: N;
  target: N;
  kind: LinkKind;
  hue: string;
}

export interface EngineCallbacks {
  onHover: (n: MemNode | null) => void;
  /** Called each frame while something is hovered, in canvas pixels. */
  onPointer: (x: number, y: number) => void;
  onSelect: (n: MemNode) => void;
}

const TAU = Math.PI * 2;
const MIN_K = 0.07;
const MAX_K = 5;
// Turns per second: slow enough that it reads as life, not as motion to follow.
const DISC_TURN = TAU / 3600;
const ORBIT: Record<NodeKind, number> = { core: 0, star: DISC_TURN, critical: TAU / 900, page: TAU / 1200, note: TAU / 1500, section: TAU / 420 };
const ARM_TIGHTNESS = 2.1;
const ARM_START = 390;
const ARM_STEP = 330;
/** The angle of an arm at radius r. Stars and dust use the same curve. */
const armAngle = (r: number, arm: number) => arm * Math.PI + ARM_TIGHTNESS * Math.log(Math.max(r, 1) / ARM_START) - 0.5;

const hash = (s: string) => {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return (h >>> 0) / 4294967296;
};

/** A small seeded generator: the same sky each time. */
const seeded = (seed: number) => () => {
  seed = (seed + 0x6d2b79f5) | 0;
  let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

function rgb(hex: string): [number, number, number] {
  const n = parseInt(hex.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

const rgba = (hex: string, a: number) => {
  const [r, g, b] = rgb(hex);
  return `rgba(${r},${g},${b},${a})`;
};

function mix(hex: string, to: string, t: number): string {
  const a = rgb(hex);
  const b = rgb(to);
  const c = a.map((v, i) => Math.round(v + (b[i] - v) * t));
  return `#${c.map((v) => v.toString(16).padStart(2, '0')).join('')}`;
}

function radiusOf(n: MemNode): number {
  const size = Math.sqrt(n.chars);
  switch (n.kind) {
    case 'core':
      return 22 + Math.min(8, size / 14);
    case 'star':
      return 13 + Math.min(11, size / 10);
    case 'critical':
      return 10 + Math.min(4, size / 22);
    case 'page':
      return 7 + Math.min(10, size / 9);
    case 'note':
      return 4.5 + Math.min(3, size / 20);
    case 'section':
      return 2.2 + Math.min(2.6, size / 34);
  }
}

/** Where the n-th project sits: on one of two arms, the same curve the dust follows. */
function armPosition(i: number): [number, number] {
  const r = ARM_START + ARM_STEP * Math.floor(i / 2);
  const a = armAngle(r, i % 2);
  return [Math.cos(a) * r, Math.sin(a) * r];
}

export class GalaxyEngine {
  private ctx: CanvasRenderingContext2D;
  private w = 0;
  private h = 0;
  private dpr = 1;
  private nodes: N[] = [];
  private links: L[] = [];
  private byId = new Map<string, N>();
  private sim: Simulation<N, L> | null = null;
  private raf = 0;
  private last = 0;
  private time = 0;
  private real = 0;
  private cam = { x: 0, y: 0, k: 0.5 };
  private target = { x: 0, y: 0, k: 0.5 };
  private follow: N | null = null;
  private insets = { left: 0, right: 0, top: 0, bottom: 0 };
  private hover: N | null = null;
  private selected: N | null = null;
  private highlight: Set<string> | null = null;
  private pointer = { x: 0, y: 0 };
  private pointers = new Map<number, { x: number; y: number }>();
  private pinch = 0;
  private press: { x: number; y: number; node: N | null; moved: boolean; cx: number; cy: number } | null = null;
  private dragging: N | null = null;
  private reduced = false;
  private dirty = true;
  private settle = 0;
  private sprites = new Map<string, HTMLCanvasElement>();
  private starTiles: HTMLCanvasElement[] = [];
  private twinkles: { x: number; y: number; s: number; p: number; v: number; d: number; c: string }[] = [];
  private dust: { x: Float32Array; a: Float32Array; s: Float32Array; ends: { to: number; style: string }[] } | null = null;
  private disc: HTMLCanvasElement | null = null;
  private dustReach = 0;
  private fontUi = 'system-ui, sans-serif';
  private ro: ResizeObserver;
  private mq: MediaQueryList;

  constructor(
    private canvas: HTMLCanvasElement,
    private cb: EngineCallbacks,
  ) {
    this.ctx = canvas.getContext('2d', { alpha: false })!;
    this.mq = window.matchMedia('(prefers-reduced-motion: reduce)');
    this.reduced = this.mq.matches;
    this.mq.addEventListener('change', this.onMotion);
    this.fontUi = getComputedStyle(document.documentElement).getPropertyValue('--font-ui').trim() || this.fontUi;
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(canvas);
    this.resize();
    canvas.addEventListener('pointerdown', this.onDown);
    canvas.addEventListener('pointermove', this.onMove);
    canvas.addEventListener('pointerup', this.onUp);
    canvas.addEventListener('pointercancel', this.onUp);
    canvas.addEventListener('pointerleave', this.onLeave);
    canvas.addEventListener('wheel', this.onWheel, { passive: false });
    this.raf = requestAnimationFrame(this.frame);
  }

  destroy() {
    cancelAnimationFrame(this.raf);
    this.ro.disconnect();
    this.mq.removeEventListener('change', this.onMotion);
    this.canvas.removeEventListener('pointerdown', this.onDown);
    this.canvas.removeEventListener('pointermove', this.onMove);
    this.canvas.removeEventListener('pointerup', this.onUp);
    this.canvas.removeEventListener('pointercancel', this.onUp);
    this.canvas.removeEventListener('pointerleave', this.onLeave);
    this.canvas.removeEventListener('wheel', this.onWheel);
    this.sim?.stop();
  }

  // ---- data ----

  setData(nodes: MemNode[], links: MemLink[]) {
    const old = this.byId;
    const first = old.size === 0;
    this.byId = new Map();
    this.nodes = nodes.map((m) => {
      const prev = old.get(m.id);
      const n: N = {
        ...m,
        x: prev?.x ?? 0,
        y: prev?.y ?? 0,
        dx: prev?.dx ?? 0,
        dy: prev?.dy ?? 0,
        vx: 0,
        vy: 0,
        r: radiusOf(m),
        fade: prev?.fade ?? 1,
        p: null,
        neighbours: new Set(),
        system: null as unknown as N,
      };
      this.byId.set(m.id, n);
      return n;
    });
    // Parents first, so a frame can place each node after its parent.
    const depth = (n: N): number => (n.parent ? 1 + depth(this.byId.get(n.parent)!) : 0);
    for (const n of this.nodes) n.p = n.parent ? (this.byId.get(n.parent) ?? null) : null;
    this.nodes.sort((a, b) => depth(a) - depth(b));

    let star = 0;
    for (const n of this.nodes) {
      n.system = n.kind === 'core' || n.kind === 'star' ? n : (n.p?.system ?? n);
      if (n.kind === 'core') {
        n.fx = 0;
        n.fy = 0;
        n.x = 0;
        n.y = 0;
      } else if (n.kind === 'star') {
        const prev = old.get(n.id);
        const [x, y] = prev?.fx != null ? [prev.fx, prev.fy!] : armPosition(star);
        star++;
        n.fx = x;
        n.fy = y;
        n.x = x;
        n.y = y;
      } else if (!old.has(n.id) && n.p) {
        // A stable start: evenly around its parent, turned by its own name.
        const a = (n.order / Math.max(1, n.siblings)) * TAU + hash(n.p.id) * TAU;
        const d = this.orbitOf(n);
        n.x = n.p.x + Math.cos(a) * d;
        n.y = n.p.y + Math.sin(a) * d;
      }
    }
    this.links = links.flatMap((l) => {
      const s = this.byId.get(l.source);
      const t = this.byId.get(l.target);
      if (!s || !t) return [];
      s.neighbours.add(t);
      t.neighbours.add(s);
      return [{ source: s, target: t, kind: l.kind, hue: l.hue }];
    });
    if (this.hover) this.hover = this.byId.get(this.hover.id) ?? null;
    if (this.selected) this.selected = this.byId.get(this.selected.id) ?? null;
    if (this.follow) this.follow = this.byId.get(this.follow.id) ?? null;
    this.layout(first ? 360 : 90);
    this.buildDust(Math.max(star, 2));
    this.place();
    if (first) this.fit(true);
    this.dirty = true;
  }

  private orbitOf(n: N): number {
    const base = n.p?.r ?? 10;
    switch (n.kind) {
      case 'critical':
        return base + 62;
      case 'page':
        return base + 96 + (n.order % 5) * 24;
      case 'note':
        return base + 150 + (n.order % 4) * 22;
      case 'section':
        return base + 22 + (n.order % 3) * 9;
      default:
        return 0;
    }
  }

  private layout(ticks: number) {
    this.sim?.stop();
    const links = this.links.filter((l) => l.kind !== 'galaxy');
    this.sim = forceSimulation<N>(this.nodes)
      .force(
        'link',
        forceLink<N, L>(links)
          .distance((l) => (l.kind === 'wiki' ? 170 : this.orbitOf(l.target)))
          .strength((l) => (l.kind === 'wiki' ? 0.02 : l.kind === 'section' ? 0.9 : 0.7)),
      )
      .force(
        'charge',
        forceManyBody<N>()
          .strength((n) => (n.kind === 'section' ? -14 : n.kind === 'note' ? -70 : -240))
          .distanceMax(460),
      )
      .force('collide', forceCollide<N>((n) => n.r + (n.kind === 'section' ? 3 : 12)))
      .stop();
    for (let i = 0; i < ticks; i++) this.sim.tick();
    this.sim.alpha(0);
  }

  // ---- what the page asks for ----

  /** How much of the canvas the panels cover, so "centre" means the free part. */
  setInsets(left: number, right: number, top = 0, bottom = 0) {
    this.insets = { left, right, top, bottom };
    this.dirty = true;
  }

  setSelected(id: string | null) {
    this.selected = id ? (this.byId.get(id) ?? null) : null;
    this.dirty = true;
  }

  setHighlight(ids: Set<string> | null) {
    this.highlight = ids;
    this.dirty = true;
  }

  /** Fly to a node. A star shows its whole system. */
  focus(id: string, keepZoom = false) {
    const n = this.byId.get(id);
    if (!n) return;
    this.follow = n;
    if (keepZoom) {
      this.target.k = Math.max(this.target.k, 0.55);
      return;
    }
    let reach = n.r + 60;
    for (const o of this.nodes) {
      if (o.system === n.system) reach = Math.max(reach, Math.hypot(o.x - n.system.x, o.y - n.system.y) + o.r + 50);
    }
    const free = Math.min(this.w - this.insets.left - this.insets.right, this.h - this.insets.top - this.insets.bottom);
    this.target.k = clamp(free / (2 * reach), 0.35, 2.4);
    this.dirty = true;
  }

  /** Bring these nodes into view, as close as they all fit. */
  reveal(ids: Set<string>) {
    const list = this.nodes.filter((n) => ids.has(n.id));
    if (!list.length) return;
    const xs = list.map((n) => n.dx);
    const ys = list.map((n) => n.dy);
    const [x0, x1, y0, y1] = [Math.min(...xs), Math.max(...xs), Math.min(...ys), Math.max(...ys)];
    const freeW = this.w - this.insets.left - this.insets.right;
    const freeH = this.h - this.insets.top - this.insets.bottom;
    this.follow = null;
    this.target = { x: (x0 + x1) / 2, y: (y0 + y1) / 2, k: clamp(Math.min(freeW / (x1 - x0 + 320), freeH / (y1 - y0 + 240)), 0.3, 1.1) };
    this.dirty = true;
  }

  /** Show everything. */
  fit(now = false) {
    this.follow = null;
    if (!this.nodes.length) return;
    let reach = 200;
    for (const n of this.nodes) reach = Math.max(reach, Math.hypot(n.x, n.y) + n.r);
    const free = Math.min(this.w - this.insets.left - this.insets.right, this.h - this.insets.top - this.insets.bottom);
    this.target = { x: 0, y: 0, k: clamp(free / (2 * (reach + 60)), MIN_K, 1) };
    if (now) this.cam = { ...this.target };
    this.dirty = true;
  }

  zoomBy(f: number) {
    this.target.k = clamp(this.target.k * f, MIN_K, MAX_K);
    this.dirty = true;
  }

  // ---- input ----

  private onMotion = () => {
    this.reduced = this.mq.matches;
    this.dirty = true;
  };

  private toScreen(x: number, y: number): [number, number] {
    const c = this.cam;
    return [(x - c.x) * c.k + this.cx(), (y - c.y) * c.k + this.cy()];
  }

  private cx() {
    return this.insets.left + (this.w - this.insets.left - this.insets.right) / 2;
  }

  private cy() {
    return this.insets.top + (this.h - this.insets.top - this.insets.bottom) / 2;
  }

  private local(e: PointerEvent | WheelEvent) {
    const b = this.canvas.getBoundingClientRect();
    return { x: e.clientX - b.left, y: e.clientY - b.top };
  }

  /** Size on screen: grows with the zoom, slower than the distances do. */
  private shown(n: N) {
    const least = n.kind === 'core' ? 13 : n.kind === 'star' ? 8.5 : n.kind === 'critical' ? 5.5 : n.kind === 'page' ? 3.6 : n.kind === 'note' ? 2.4 : 1;
    return Math.max(least, n.r * Math.pow(this.cam.k, 0.72));
  }

  private nodeAt(x: number, y: number): N | null {
    let best: N | null = null;
    let score = Infinity;
    for (const n of this.nodes) {
      const [sx, sy] = this.toScreen(n.dx, n.dy);
      const d = Math.hypot(sx - x, sy - y);
      const reach = Math.max(this.shown(n) + 5, n.kind === 'section' ? (this.cam.k > 0.8 ? 9 : 0) : 13);
      if (d <= reach && d - reach < score) {
        best = n;
        score = d - reach;
      }
    }
    return best;
  }

  private onDown = (e: PointerEvent) => {
    const p = this.local(e);
    this.canvas.setPointerCapture(e.pointerId);
    this.pointers.set(e.pointerId, p);
    if (this.pointers.size === 2) {
      const [a, b] = [...this.pointers.values()];
      this.pinch = Math.hypot(a.x - b.x, a.y - b.y);
      this.press = null;
      return;
    }
    this.press = { x: p.x, y: p.y, node: this.nodeAt(p.x, p.y), moved: false, cx: this.target.x, cy: this.target.y };
  };

  private onMove = (e: PointerEvent) => {
    const p = this.local(e);
    this.pointer = p;
    if (this.pointers.has(e.pointerId)) this.pointers.set(e.pointerId, p);
    if (this.pointers.size === 2) {
      const [a, b] = [...this.pointers.values()];
      const d = Math.hypot(a.x - b.x, a.y - b.y);
      if (this.pinch > 0) this.zoomAt((a.x + b.x) / 2, (a.y + b.y) / 2, d / this.pinch);
      this.pinch = d;
      return;
    }
    const pr = this.press;
    if (pr) {
      if (!pr.moved && Math.hypot(p.x - pr.x, p.y - pr.y) < 4) return;
      pr.moved = true;
      this.dirty = true;
      if (pr.node && pr.node.kind !== 'core') {
        this.dragging = pr.node;
        this.dragTo(pr.node, p.x, p.y);
        return;
      }
      this.follow = null;
      this.target.x = pr.cx - (p.x - pr.x) / this.cam.k;
      this.target.y = pr.cy - (p.y - pr.y) / this.cam.k;
      this.cam.x = this.target.x;
      this.cam.y = this.target.y;
      return;
    }
    this.setHover(this.nodeAt(p.x, p.y));
  };

  private dragTo(n: N, x: number, y: number) {
    const wx = (x - this.cx()) / this.cam.k + this.cam.x;
    const wy = (y - this.cy()) / this.cam.k + this.cam.y;
    // The node's own x,y are before the turn of its parents.
    const a = n.p ? this.angleOf(n) : 0;
    const dx = wx - (n.p?.dx ?? 0);
    const dy = wy - (n.p?.dy ?? 0);
    const fx = dx * Math.cos(-a) - dy * Math.sin(-a) + (n.p?.x ?? 0);
    const fy = dx * Math.sin(-a) + dy * Math.cos(-a) + (n.p?.y ?? 0);
    n.fx = fx;
    n.fy = fy;
    this.sim?.alpha(Math.max(this.sim.alpha(), 0.25));
    this.settle = 1;
  }

  private onUp = (e: PointerEvent) => {
    this.pointers.delete(e.pointerId);
    this.pinch = 0;
    const pr = this.press;
    this.press = null;
    const dragged = this.dragging;
    this.dragging = null;
    if (dragged && dragged.kind !== 'star') {
      dragged.fx = null;
      dragged.fy = null;
    }
    if (pr && !pr.moved && pr.node) this.cb.onSelect(pr.node);
  };

  private onLeave = () => {
    if (!this.press) this.setHover(null);
  };

  private zoomAt(x: number, y: number, f: number) {
    const k = clamp(this.target.k * f, MIN_K, MAX_K);
    const real = k / this.target.k;
    this.target.x += ((x - this.cx()) / this.target.k) * (1 - 1 / real);
    this.target.y += ((y - this.cy()) / this.target.k) * (1 - 1 / real);
    this.target.k = k;
    this.dirty = true;
  }

  private onWheel = (e: WheelEvent) => {
    e.preventDefault();
    const p = this.local(e);
    this.follow = null;
    this.zoomAt(p.x, p.y, Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0016)));
  };

  private setHover(n: N | null) {
    if (n === this.hover) return;
    this.hover = n;
    this.canvas.style.cursor = n ? 'pointer' : 'grab';
    this.cb.onHover(n);
    this.dirty = true;
  }

  // ---- sprites, once ----

  private sprite(key: string, size: number, paint: (c: CanvasRenderingContext2D, s: number) => void) {
    let s = this.sprites.get(key);
    if (!s) {
      s = document.createElement('canvas');
      s.width = size;
      s.height = size;
      paint(s.getContext('2d')!, size);
      this.sprites.set(key, s);
    }
    return s;
  }

  private glow(hex: string) {
    return this.sprite(`glow:${hex}`, 256, (c, s) => {
      const g = c.createRadialGradient(s / 2, s / 2, 0, s / 2, s / 2, s / 2);
      g.addColorStop(0, rgba(hex, 0.85));
      g.addColorStop(0.12, rgba(hex, 0.42));
      g.addColorStop(0.32, rgba(hex, 0.13));
      g.addColorStop(0.62, rgba(hex, 0.035));
      g.addColorStop(1, rgba(hex, 0));
      c.fillStyle = g;
      c.fillRect(0, 0, s, s);
    });
  }

  /** The soft cross of a bright star through a lens. */
  private spikes(hex: string) {
    return this.sprite(`spikes:${hex}`, 512, (c, s) => {
      c.translate(s / 2, s / 2);
      c.globalCompositeOperation = 'lighter';
      for (const [a, len, w] of [
        [0, 1, 1.6],
        [Math.PI / 2, 1, 1.6],
        [Math.PI / 4, 0.42, 1],
        [-Math.PI / 4, 0.42, 1],
      ]) {
        c.save();
        c.rotate(a);
        const half = (s / 2) * len;
        const g = c.createLinearGradient(-half, 0, half, 0);
        g.addColorStop(0, rgba(hex, 0));
        g.addColorStop(0.5, rgba('#ffffff', 0.95));
        g.addColorStop(1, rgba(hex, 0));
        c.fillStyle = g;
        c.fillRect(-half, -w, half * 2, w * 2);
        c.restore();
      }
    });
  }

  private planet(hex: string) {
    return this.sprite(`planet:${hex}`, 128, (c, s) => {
      const r = s / 2 - 3;
      c.translate(s / 2, s / 2);
      const body = c.createRadialGradient(-r * 0.38, -r * 0.42, r * 0.05, 0, 0, r * 1.05);
      body.addColorStop(0, mix(hex, '#ffffff', 0.72));
      body.addColorStop(0.3, mix(hex, '#ffffff', 0.18));
      body.addColorStop(0.68, hex);
      body.addColorStop(1, mix(hex, '#03040c', 0.78));
      c.fillStyle = body;
      c.beginPath();
      c.arc(0, 0, r, 0, TAU);
      c.fill();
      // Bands: a page has some texture to it.
      c.save();
      c.clip();
      c.globalCompositeOperation = 'overlay';
      c.rotate(-0.42);
      const rnd = seeded(Math.floor(hash(hex) * 1e6));
      for (let i = 0; i < 7; i++) {
        c.fillStyle = `rgba(${rnd() > 0.5 ? '255,255,255' : '0,0,0'},${0.05 + rnd() * 0.1})`;
        c.fillRect(-r * 1.3, -r + (i / 7) * r * 2, r * 2.6, r * (0.08 + rnd() * 0.22));
      }
      c.restore();
      const rim = c.createRadialGradient(0, 0, r * 0.82, 0, 0, r);
      rim.addColorStop(0, rgba(mix(hex, '#ffffff', 0.5), 0));
      rim.addColorStop(1, rgba(mix(hex, '#ffffff', 0.5), 0.5));
      c.fillStyle = rim;
      c.beginPath();
      c.arc(0, 0, r, 0, TAU);
      c.fill();
    });
  }

  /** A cloud in a project's colour, brightest around its star. */
  private nebula(hex: string) {
    return this.sprite(`nebula:${hex}`, 640, (c, s) => {
      const rnd = seeded(Math.floor(hash(`nebula${hex}`) * 1e9));
      c.globalCompositeOperation = 'lighter';
      const blob = (x: number, y: number, r: number, col: string, a: number) => {
        const g = c.createRadialGradient(x, y, 0, x, y, r);
        g.addColorStop(0, rgba(col, a));
        g.addColorStop(0.45, rgba(col, a * 0.4));
        g.addColorStop(1, rgba(col, 0));
        c.fillStyle = g;
        c.fillRect(x - r, y - r, r * 2, r * 2);
      };
      const near = mix(hex, '#ffffff', 0.2);
      const deep = mix(hex, '#3a22a0', 0.5);
      const far = mix(hex, '#0f2a70', 0.6);
      blob(s / 2, s / 2, s * 0.46, deep, 0.2);
      blob(s / 2, s / 2, s * 0.24, near, 0.14);
      for (let i = 0; i < 46; i++) {
        const a = rnd() * TAU;
        const d = Math.pow(rnd(), 0.75) * s * 0.34;
        const tone = rnd();
        blob(s / 2 + Math.cos(a) * d, s / 2 + Math.sin(a) * d, s * (0.025 + rnd() * rnd() * 0.13), tone < 0.3 ? near : tone < 0.7 ? deep : far, 0.05 + rnd() * 0.1);
      }
      // Dark lanes give a cloud its shape.
      c.globalCompositeOperation = 'destination-out';
      for (let i = 0; i < 14; i++) {
        const a = rnd() * TAU;
        const d = (0.12 + rnd() * 0.3) * s;
        const r = s * (0.03 + rnd() * 0.07);
        const x = s / 2 + Math.cos(a) * d;
        const y = s / 2 + Math.sin(a) * d;
        const g = c.createRadialGradient(x, y, 0, x, y, r);
        g.addColorStop(0, 'rgba(0,0,0,0.5)');
        g.addColorStop(1, 'rgba(0,0,0,0)');
        c.fillStyle = g;
        c.fillRect(x - r, y - r, r * 2, r * 2);
      }
    });
  }

  /** The arms as light: a disc painted once, turned as a whole. */
  private buildDisc(reach: number) {
    const size = 1536;
    const d = document.createElement('canvas');
    d.width = size;
    d.height = size;
    const c = d.getContext('2d')!;
    const unit = size / 2 / reach;
    c.translate(size / 2, size / 2);
    c.globalCompositeOperation = 'lighter';
    const rnd = seeded(11);
    const tone = (t: number) => (t < 0.12 ? '#ffd9a8' : t < 0.28 ? mix('#ffd9a8', '#86a2ff', (t - 0.12) / 0.16) : t < 0.7 ? mix('#86a2ff', '#5573ff', (t - 0.28) / 0.42) : mix('#5573ff', '#a557ff', (t - 0.7) / 0.3));
    const stamp = (x: number, y: number, r: number, col: string, a: number) => {
      c.globalAlpha = a;
      c.drawImage(this.glow(col), x - r, y - r, r * 2, r * 2);
    };
    // Light along each arm, in clumps, with faint spurs between them.
    for (const [arm, power] of [
      [0, 1],
      [1, 1],
      [0.5, 0.13],
      [1.5, 0.13],
    ]) {
      for (let i = 0; i < 1150; i++) {
        const t = Math.pow(rnd(), 0.8);
        const r = 50 + t * (reach - 50);
        const clump = 0.5 + 0.5 * Math.sin(r * 0.023 + arm * 3.1);
        const a = armAngle(r, arm) + (0.055 + 0.17 * (1 - t)) * (rnd() + rnd() - 1);
        const edge = 1 - Math.pow(t, 2.8);
        stamp(Math.cos(a) * r * unit, Math.sin(a) * r * unit, (9 + rnd() * rnd() * 50) * unit * (1.5 + t), tone(t), (0.022 + rnd() * 0.05) * power * edge * (0.45 + 0.55 * clump));
      }
    }
    // Lanes of dust on the inner side of each arm.
    c.globalCompositeOperation = 'destination-out';
    for (const arm of [0, 1]) {
      for (let i = 0; i < 260; i++) {
        const t = 0.1 + rnd() * 0.72;
        const r = t * reach;
        const a = armAngle(r, arm) - 0.16 + 0.05 * (rnd() + rnd() - 1);
        const x = Math.cos(a) * r * unit;
        const y = Math.sin(a) * r * unit;
        const dr = (10 + rnd() * 26) * unit * 2.2;
        const g = c.createRadialGradient(x, y, 0, x, y, dr);
        g.addColorStop(0, `rgba(0,0,0,${0.1 + rnd() * 0.12})`);
        g.addColorStop(1, 'rgba(0,0,0,0)');
        c.globalAlpha = 1;
        c.fillStyle = g;
        c.fillRect(x - dr, y - dr, dr * 2, dr * 2);
      }
    }
    // Grains of light: clusters of young stars.
    c.globalCompositeOperation = 'lighter';
    for (let i = 0; i < 90; i++) {
      const t = 0.14 + rnd() * 0.8;
      const r = t * reach;
      const a = armAngle(r, rnd() < 0.5 ? 0 : 1) + 0.12 * (rnd() + rnd() - 1);
      stamp(Math.cos(a) * r * unit, Math.sin(a) * r * unit, (5 + rnd() * 15) * unit * 2.4, rnd() < 0.72 ? '#a8c2ff' : '#ff9fd0', 0.1 + rnd() * 0.22);
    }
    // The bulge.
    stamp(0, 0, 300 * unit * 2.4, '#ffbf80', 0.55);
    stamp(0, 0, 150 * unit * 2.4, '#ffe3bd', 0.55);
    this.disc = d;
  }

  private vignette() {
    return this.sprite('vignette', 256, (c, s) => {
      const g = c.createRadialGradient(s / 2, s / 2, s * 0.28, s / 2, s / 2, s * 0.72);
      g.addColorStop(0, 'rgba(2,3,10,0)');
      g.addColorStop(1, 'rgba(2,3,10,0.8)');
      c.fillStyle = g;
      c.fillRect(0, 0, s, s);
    });
  }

  private buildStars() {
    this.starTiles = [];
    const rnd = seeded(7);
    const tile = Math.round(900 * this.dpr);
    const layers: [number, number, number][] = [
      [520, 0.55, 0.42],
      [150, 0.95, 0.7],
    ];
    for (const [count, size, alpha] of layers) {
      const t = document.createElement('canvas');
      t.width = tile;
      t.height = tile;
      const c = t.getContext('2d')!;
      for (let i = 0; i < count; i++) {
        const tone = rnd();
        const col = tone < 0.2 ? '#ffe2c0' : tone < 0.55 ? '#cfdcff' : '#ffffff';
        c.fillStyle = rgba(col, alpha * (0.3 + rnd() * 0.7));
        const s = size * this.dpr * (0.6 + rnd() * 0.8);
        c.beginPath();
        c.arc(rnd() * tile, rnd() * tile, s, 0, TAU);
        c.fill();
      }
      this.starTiles.push(t);
    }
    this.twinkles = Array.from({ length: 70 }, () => {
      const tone = rnd();
      return { x: rnd(), y: rnd(), s: 0.7 + rnd() * 1.1, p: rnd() * TAU, v: 0.4 + rnd() * 1.3, d: 0.02 + rnd() * 0.07, c: tone < 0.25 ? '#ffd9b0' : tone < 0.6 ? '#bcd0ff' : '#ffffff' };
    });
  }

  /** Dust along two spiral arms, grouped by colour so a frame sets few styles. */
  private buildDust(stars: number) {
    const reach = ARM_START + ARM_STEP * (Math.ceil(stars / 2) + 0.6);
    if (this.dust && this.dustReach === reach) return;
    this.dustReach = reach;
    this.buildDisc(reach);
    const rnd = seeded(42);
    const count = this.w < 700 ? 1800 : 4200;
    const palette = ['#ffe6bf', '#ffcf9e', '#c9d6ff', '#8ea8ff', '#b78cff'];
    const levels = [0.28, 0.55, 0.92];
    const groups: { r: number; a: number; s: number }[][] = Array.from({ length: palette.length * levels.length }, () => []);
    for (let i = 0; i < count; i++) {
      const inBulge = rnd() < 0.2;
      let r: number;
      let a: number;
      if (inBulge) {
        r = Math.pow(rnd(), 1.7) * reach * 0.2 + 8;
        a = rnd() * TAU;
      } else {
        r = 60 + Math.pow(rnd(), 1.15) * (reach - 60);
        const spread = (0.14 + 0.42 * (1 - r / reach)) * (rnd() + rnd() - 1);
        a = armAngle(r, rnd() < 0.5 ? 0 : 1) + spread;
      }
      const t = r / reach;
      const c = t < 0.1 ? 0 : t < 0.22 ? (rnd() < 0.6 ? 1 : 0) : t < 0.55 ? (rnd() < 0.65 ? 2 : 3) : rnd() < 0.55 ? 3 : 4;
      const l = rnd() < 0.55 ? 0 : rnd() < 0.7 ? 1 : 2;
      groups[c * levels.length + l].push({ r, a, s: 0.5 + rnd() * rnd() * 1.7 });
    }
    const x = new Float32Array(count);
    const an = new Float32Array(count);
    const s = new Float32Array(count);
    const ends: { to: number; style: string }[] = [];
    let i = 0;
    groups.forEach((g, gi) => {
      for (const d of g) {
        x[i] = d.r;
        an[i] = d.a;
        s[i] = d.s;
        i++;
      }
      ends.push({ to: i, style: rgba(palette[Math.floor(gi / levels.length)], levels[gi % levels.length]) });
    });
    this.dust = { x, a: an, s, ends };
  }

  // ---- a frame ----

  private resize() {
    const b = this.canvas.getBoundingClientRect();
    this.dpr = Math.min(window.devicePixelRatio || 1, 2);
    this.w = Math.max(1, b.width);
    this.h = Math.max(1, b.height);
    this.canvas.width = Math.round(this.w * this.dpr);
    this.canvas.height = Math.round(this.h * this.dpr);
    this.buildStars();
    this.dirty = true;
  }

  private angleOf(n: N): number {
    let a = 0;
    for (let c: N | null = n; c?.p; c = c.p) a += ORBIT[c.kind] * this.time;
    return a;
  }

  /** Draw positions: each node turned around its parent, parents first. */
  private place() {
    for (const n of this.nodes) {
      const p = n.p;
      if (!p) {
        n.dx = n.x;
        n.dy = n.y;
        continue;
      }
      const a = ORBIT[n.kind] * this.time;
      const ca = Math.cos(a);
      const sa = Math.sin(a);
      const rx = n.x - p.x;
      const ry = n.y - p.y;
      // Stays with where the parent has been turned to.
      const pa = this.angleOf(p);
      const cp = Math.cos(pa);
      const sp = Math.sin(pa);
      const tx = rx * ca - ry * sa;
      const ty = rx * sa + ry * ca;
      n.dx = p.dx + tx * cp - ty * sp;
      n.dy = p.dy + tx * sp + ty * cp;
    }
  }

  private frame = (now: number) => {
    this.raf = requestAnimationFrame(this.frame);
    const dt = Math.min(0.05, (now - this.last) / 1000 || 0.016);
    this.last = now;
    this.real += dt;
    // Time stands still under the pointer, so nothing runs away from a click.
    if (!this.reduced && !this.hover && !this.dragging) this.time += dt;

    const camMoving = Math.abs(this.cam.k - this.target.k) > 0.0005 || Math.hypot(this.cam.x - this.target.x, this.cam.y - this.target.y) > 0.05;
    const fading = this.nodes.some((n) => Math.abs(n.fade - this.fadeTarget(n)) > 0.01);
    if (this.reduced && !this.dirty && !camMoving && !fading && !this.settle) return;
    this.dirty = false;

    if (this.settle && this.sim) {
      this.sim.tick();
      if (this.sim.alpha() < 0.03) this.settle = 0;
    }
    this.place();
    if (this.follow) {
      this.target.x = this.follow.dx;
      this.target.y = this.follow.dy;
    }
    const ease = this.reduced ? 1 : 1 - Math.exp(-dt * 5.5);
    this.cam.x += (this.target.x - this.cam.x) * ease;
    this.cam.y += (this.target.y - this.cam.y) * ease;
    this.cam.k *= Math.exp(Math.log(this.target.k / this.cam.k) * ease);
    for (const n of this.nodes) n.fade += (this.fadeTarget(n) - n.fade) * (this.reduced ? 1 : Math.min(1, dt * 9));

    const c = this.ctx;
    c.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    this.drawSky(c);
    this.drawNebulae(c);
    this.drawDust(c);
    this.drawOrbits(c);
    this.drawLinks(c);
    this.drawNodes(c);
    this.drawLabels(c);
    c.globalCompositeOperation = 'source-over';
    c.globalAlpha = 1;
    c.drawImage(this.vignette(), 0, 0, this.w, this.h);
    if (this.hover) this.cb.onPointer(this.pointer.x, this.pointer.y);
  };

  private fadeTarget(n: N): number {
    if (this.hover) return n === this.hover || this.hover.neighbours.has(n) ? 1 : 0.13;
    if (this.highlight) return this.highlight.has(n.id) ? 1 : 0.1;
    return 1;
  }

  private drawSky(c: CanvasRenderingContext2D) {
    c.globalCompositeOperation = 'source-over';
    c.globalAlpha = 1;
    c.fillStyle = '#04050e';
    c.fillRect(0, 0, this.w, this.h);
    const [cx, cy] = this.toScreen(0, 0);
    const size = Math.max(this.w, this.h) * 1.3 + this.dustReach * this.cam.k;
    const g = c.createRadialGradient(cx, cy, 0, cx, cy, size);
    g.addColorStop(0, 'rgba(46,36,104,0.55)');
    g.addColorStop(0.4, 'rgba(20,22,64,0.38)');
    g.addColorStop(1, 'rgba(4,5,14,0)');
    c.fillStyle = g;
    c.fillRect(0, 0, this.w, this.h);

    this.starTiles.forEach((t, i) => {
      const f = i === 0 ? 0.035 : 0.09;
      const size = t.width / this.dpr;
      const ox = -(((this.cam.x * this.cam.k * f) % size) + size) % size;
      const oy = -(((this.cam.y * this.cam.k * f) % size) + size) % size;
      for (let x = ox; x < this.w; x += size) {
        for (let y = oy; y < this.h; y += size) c.drawImage(t, x, y, size, size);
      }
    });
    for (const t of this.twinkles) {
      const a = this.reduced ? 0.5 : 0.28 + 0.5 * (0.5 + 0.5 * Math.sin(this.real * t.v + t.p));
      const x = ((t.x * this.w - this.cam.x * this.cam.k * t.d) % this.w + this.w) % this.w;
      const y = ((t.y * this.h - this.cam.y * this.cam.k * t.d) % this.h + this.h) % this.h;
      c.fillStyle = rgba(t.c, a);
      c.beginPath();
      c.arc(x, y, t.s, 0, TAU);
      c.fill();
    }
  }

  private drawNebulae(c: CanvasRenderingContext2D) {
    c.globalCompositeOperation = 'lighter';
    for (const n of this.nodes) {
      if (n.kind !== 'star' && n.kind !== 'core') continue;
      const [x, y] = this.toScreen(n.dx, n.dy);
      const size = (n.kind === 'core' ? 1100 : 1180) * this.cam.k;
      if (x + size < 0 || y + size < 0 || x - size > this.w || y - size > this.h) continue;
      c.globalAlpha = (n.kind === 'core' ? 0.4 : 0.78) * (0.35 + 0.65 * n.fade);
      c.save();
      c.translate(x, y);
      c.rotate(hash(n.id) * TAU + this.time * 0.004);
      c.drawImage(this.nebula(n.kind === 'core' ? '#8a6cff' : n.hue), -size / 2, -size / 2, size, size);
      c.restore();
    }
    c.globalAlpha = 1;
  }

  private drawDust(c: CanvasRenderingContext2D) {
    const d = this.dust;
    if (!d) return;
    const k = this.cam.k;
    const turn = this.time * DISC_TURN;
    const dim = this.hover || this.highlight ? 0.5 : 1;
    const cx = this.cx() - this.cam.x * k;
    const cy = this.cy() - this.cam.y * k;
    c.globalCompositeOperation = 'lighter';
    if (this.disc) {
      // The arms fade as you close in on one system.
      c.globalAlpha = clamp(1.25 - k * 0.75, 0.16, 1) * dim;
      const size = this.dustReach * 2 * k;
      c.save();
      c.translate(cx, cy);
      c.rotate(turn);
      c.drawImage(this.disc, -size / 2, -size / 2, size, size);
      c.restore();
    }
    c.globalAlpha = clamp(1.5 - k * 0.65, 0.22, 1) * dim;
    const sizeK = clamp(Math.sqrt(k) * 1.15, 0.8, 2.2);
    let i = 0;
    for (const g of d.ends) {
      c.fillStyle = g.style;
      for (; i < g.to; i++) {
        const r = d.x[i];
        const a = d.a[i] + turn;
        const x = cx + Math.cos(a) * r * k;
        const y = cy + Math.sin(a) * r * k;
        if (x < -2 || y < -2 || x > this.w + 2 || y > this.h + 2) continue;
        const s = d.s[i] * sizeK;
        c.fillRect(x, y, s, s);
      }
    }
    c.globalAlpha = 1;
  }

  private drawOrbits(c: CanvasRenderingContext2D) {
    const k = this.cam.k;
    const show = clamp((k - 0.22) / 0.4, 0, 1);
    if (show <= 0) return;
    c.globalCompositeOperation = 'lighter';
    c.lineWidth = 1;
    for (const n of this.nodes) {
      if (!n.p || n.kind === 'star' || n.kind === 'section') continue;
      const [x, y] = this.toScreen(n.p.dx, n.p.dy);
      const r = Math.hypot(n.dx - n.p.dx, n.dy - n.p.dy) * k;
      if (x + r < 0 || y + r < 0 || x - r > this.w || y - r > this.h) continue;
      c.strokeStyle = rgba(n.hue, 0.1 * show * (0.25 + 0.75 * n.fade));
      c.beginPath();
      c.arc(x, y, r, 0, TAU);
      c.stroke();
    }
  }

  private drawLinks(c: CanvasRenderingContext2D) {
    const k = this.cam.k;
    c.lineCap = 'round';
    for (const l of this.links) {
      const s = l.source;
      const t = l.target;
      const [x1, y1] = this.toScreen(s.dx, s.dy);
      const [x2, y2] = this.toScreen(t.dx, t.dy);
      if ((x1 < 0 && x2 < 0) || (y1 < 0 && y2 < 0) || (x1 > this.w && x2 > this.w) || (y1 > this.h && y2 > this.h)) continue;
      const touches = this.hover && (s === this.hover || t === this.hover);
      const f = Math.min(s.fade, t.fade);
      let a: number;
      let w: number;
      switch (l.kind) {
        case 'galaxy':
          a = 0.12;
          w = 1;
          break;
        case 'orbit':
          a = 0.34;
          w = 1.2;
          break;
        case 'section':
          a = clamp((k - 0.4) / 0.5, 0, 1) * 0.3;
          w = 0.8;
          break;
        default:
          a = 0.5;
          w = 1;
      }
      a *= 0.2 + 0.8 * f;
      if (touches) {
        a = 0.95;
        w += 0.7;
      }
      if (a < 0.01) continue;
      // A slight bow, the way the edge of a real orbit looks.
      const mx = (x1 + x2) / 2;
      const my = (y1 + y2) / 2;
      const bow = l.kind === 'wiki' ? 0.16 : 0.05;
      const qx = mx - (y2 - y1) * bow;
      const qy = my + (x2 - x1) * bow;
      c.globalCompositeOperation = 'lighter';
      const g = c.createLinearGradient(x1, y1, x2, y2);
      const col = l.kind === 'wiki' ? '#d7e0ff' : l.hue;
      g.addColorStop(0, rgba(mix(col, '#ffffff', 0.25), a));
      g.addColorStop(1, rgba(col, a * 0.55));
      c.strokeStyle = g;
      c.lineWidth = w;
      if (l.kind === 'wiki') c.setLineDash([4, 5]);
      c.beginPath();
      c.moveTo(x1, y1);
      c.quadraticCurveTo(qx, qy, x2, y2);
      c.stroke();
      c.setLineDash([]);
      if (touches && !this.reduced) {
        // Something travels along the links of what you are looking at.
        const p = (this.real * 0.5 + hash(t.id)) % 1;
        const u = 1 - p;
        const px = u * u * x1 + 2 * u * p * qx + p * p * x2;
        const py = u * u * y1 + 2 * u * p * qy + p * p * y2;
        const size = 14;
        c.globalAlpha = 0.9;
        c.drawImage(this.glow(mix(col, '#ffffff', 0.4)), px - size / 2, py - size / 2, size, size);
        c.globalAlpha = 1;
      }
    }
  }

  private drawNodes(c: CanvasRenderingContext2D) {
    const k = this.cam.k;
    for (const n of this.nodes) {
      const [x, y] = this.toScreen(n.dx, n.dy);
      const r = this.shown(n);
      const reach = r * 8;
      if (x + reach < 0 || y + reach < 0 || x - reach > this.w || y - reach > this.h) continue;
      if (n.kind === 'section' && k < 0.28) continue;
      const a = n.fade * (n.kind === 'section' ? clamp((k - 0.28) / 0.3, 0, 1) : 1);
      if (a < 0.01) continue;
      const lit = n === this.hover || n === this.selected;
      switch (n.kind) {
        case 'core':
        case 'star':
          this.drawStar(c, n, x, y, r, a, lit);
          break;
        case 'critical':
          this.drawCritical(c, n, x, y, r, a, lit);
          break;
        case 'page':
          this.drawPlanet(c, n, x, y, r, a, lit);
          break;
        case 'note':
          this.drawNote(c, n, x, y, r, a, lit);
          break;
        case 'section':
          this.drawSection(c, n, x, y, r, a, lit);
          break;
      }
      if (n === this.selected) this.drawSelection(c, x, y, r);
    }
    c.globalAlpha = 1;
    c.globalCompositeOperation = 'source-over';
  }

  private drawStar(c: CanvasRenderingContext2D, n: N, x: number, y: number, r: number, a: number, lit: boolean) {
    const col = n.kind === 'core' ? CORE_HUE : n.hue;
    const empty = n.chars === 0 && n.kind === 'star';
    const breathe = this.reduced ? 1 : 1 + Math.sin(this.real * 0.8 + hash(n.id) * TAU) * 0.05;
    c.globalCompositeOperation = 'lighter';
    c.globalAlpha = a * (empty ? 0.45 : 0.95);
    const glow = r * (lit ? 13 : 10.5) * breathe;
    c.drawImage(this.glow(mix(col, '#ffffff', 0.12)), x - glow / 2, y - glow / 2, glow, glow);
    if (!empty) {
      const len = r * (n.kind === 'core' ? 13 : 9.5) * breathe;
      c.globalAlpha = a * 0.85;
      c.drawImage(this.spikes(col), x - len / 2, y - len / 2, len, len);
    }
    c.globalCompositeOperation = 'source-over';
    c.globalAlpha = a;
    const core = c.createRadialGradient(x, y, 0, x, y, r * 0.85);
    core.addColorStop(0, '#ffffff');
    core.addColorStop(0.55, mix(col, '#ffffff', empty ? 0.2 : 0.72));
    core.addColorStop(1, mix(col, '#ffffff', 0.25));
    c.fillStyle = core;
    c.beginPath();
    c.arc(x, y, r * 0.72, 0, TAU);
    c.fill();
    if (empty) {
      // Nothing recorded yet: an unlit star, waiting for Init memory.
      c.strokeStyle = rgba('#ffffff', 0.55 * a);
      c.lineWidth = 1;
      c.setLineDash([3, 4]);
      c.beginPath();
      c.arc(x, y, r * 1.3, 0, TAU);
      c.stroke();
      c.setLineDash([]);
    }
  }

  private drawCritical(c: CanvasRenderingContext2D, n: N, x: number, y: number, r: number, a: number, lit: boolean) {
    const col = CRITICAL_HUE;
    const beat = this.reduced ? 0.5 : (this.real * 0.45 + hash(n.id)) % 1;
    c.globalCompositeOperation = 'lighter';
    c.globalAlpha = a * (lit ? 1 : 0.85);
    const glow = r * 7.5;
    c.drawImage(this.glow(col), x - glow / 2, y - glow / 2, glow, glow);
    // A slow pulse, so the eye finds the rules first.
    c.strokeStyle = rgba(col, a * 0.6 * (1 - beat));
    c.lineWidth = 1.4;
    c.beginPath();
    c.arc(x, y, r * (1.3 + beat * 1.9), 0, TAU);
    c.stroke();
    c.globalCompositeOperation = 'source-over';
    c.globalAlpha = a;
    // A diamond: told apart by its shape, not only its red.
    c.save();
    c.translate(x, y);
    c.rotate(this.reduced ? 0 : Math.sin(this.real * 0.3 + hash(n.id) * TAU) * 0.08);
    const s = r * 1.15;
    const fill = c.createLinearGradient(-s, -s, s, s);
    fill.addColorStop(0, mix(col, '#ffffff', 0.72));
    fill.addColorStop(0.45, col);
    fill.addColorStop(1, mix(col, '#1a0208', 0.55));
    c.fillStyle = fill;
    c.beginPath();
    c.moveTo(0, -s);
    c.lineTo(s * 0.78, 0);
    c.lineTo(0, s);
    c.lineTo(-s * 0.78, 0);
    c.closePath();
    c.fill();
    c.strokeStyle = rgba('#ffffff', 0.45);
    c.lineWidth = 0.8;
    c.beginPath();
    c.moveTo(-s * 0.78, 0);
    c.lineTo(s * 0.78, 0);
    c.moveTo(0, -s);
    c.lineTo(0, s);
    c.stroke();
    c.restore();
  }

  private drawPlanet(c: CanvasRenderingContext2D, n: N, x: number, y: number, r: number, a: number, lit: boolean) {
    c.globalCompositeOperation = 'lighter';
    c.globalAlpha = a * (lit ? 0.85 : 0.5);
    const glow = r * 5.4;
    c.drawImage(this.glow(n.hue), x - glow / 2, y - glow / 2, glow, glow);
    c.globalCompositeOperation = 'source-over';
    c.globalAlpha = a;
    c.drawImage(this.planet(n.hue), x - r, y - r, r * 2, r * 2);
  }

  private drawNote(c: CanvasRenderingContext2D, n: N, x: number, y: number, r: number, a: number, lit: boolean) {
    c.globalCompositeOperation = 'lighter';
    c.globalAlpha = a * (lit ? 0.7 : 0.35);
    const glow = r * 4.5;
    c.drawImage(this.glow(n.hue), x - glow / 2, y - glow / 2, glow, glow);
    c.globalCompositeOperation = 'source-over';
    c.globalAlpha = a;
    c.strokeStyle = mix(n.hue, '#ffffff', 0.55);
    c.lineWidth = Math.max(1, r * 0.3);
    c.fillStyle = 'rgba(6,8,20,0.75)';
    c.beginPath();
    c.arc(x, y, r, 0, TAU);
    c.fill();
    c.stroke();
  }

  private drawSection(c: CanvasRenderingContext2D, n: N, x: number, y: number, r: number, a: number, lit: boolean) {
    c.globalCompositeOperation = 'lighter';
    c.globalAlpha = a * (lit ? 1 : 0.65);
    const glow = r * 5;
    c.drawImage(this.glow(n.hue), x - glow / 2, y - glow / 2, glow, glow);
    c.globalAlpha = a;
    c.fillStyle = mix(n.hue, '#ffffff', 0.65);
    c.beginPath();
    c.arc(x, y, Math.max(0.9, r * 0.75), 0, TAU);
    c.fill();
    c.globalCompositeOperation = 'source-over';
  }

  private drawSelection(c: CanvasRenderingContext2D, x: number, y: number, r: number) {
    const spin = this.reduced ? 0 : this.real * 0.6;
    const R = r * 1.5 + 6;
    c.globalCompositeOperation = 'source-over';
    c.globalAlpha = 1;
    c.strokeStyle = 'rgba(255,255,255,0.92)';
    c.lineWidth = 1.5;
    for (let i = 0; i < 3; i++) {
      c.beginPath();
      c.arc(x, y, R, spin + (i * TAU) / 3, spin + (i * TAU) / 3 + 0.8);
      c.stroke();
    }
  }

  private drawLabels(c: CanvasRenderingContext2D) {
    const k = this.cam.k;
    c.globalCompositeOperation = 'source-over';
    c.textAlign = 'center';
    c.textBaseline = 'top';
    c.lineJoin = 'round';
    const placed: [number, number, number, number][] = [];
    const rank = (n: N) => (n === this.hover || n === this.selected ? 0 : n.kind === 'core' ? 1 : n.kind === 'star' ? 2 : n.kind === 'critical' ? 3 : n.kind === 'page' ? 4 : n.kind === 'note' ? 5 : 6);
    const list = [...this.nodes].sort((a, b) => rank(a) - rank(b));
    for (const n of list) {
      const lit = n === this.hover || n === this.selected || this.hover?.neighbours.has(n) || this.highlight?.has(n.id);
      let show: number;
      switch (n.kind) {
        case 'core':
        case 'star':
          show = 1;
          break;
        case 'critical':
          show = clamp((k - 0.42) / 0.14, 0, 1);
          break;
        case 'page':
          show = clamp((k - 0.42) / 0.16, 0, 1);
          break;
        case 'note':
          show = clamp((k - 0.5) / 0.2, 0, 1);
          break;
        default:
          show = clamp((k - 1) / 0.35, 0, 1);
      }
      if (lit) show = 1;
      const a = show * (lit ? 1 : 0.2 + 0.8 * n.fade) * (n.fade < 0.2 ? n.fade * 5 : 1);
      if (a < 0.02) continue;
      const [x, y] = this.toScreen(n.dx, n.dy);
      const r = this.shown(n);
      if (x < -120 || y < -40 || x > this.w + 120 || y > this.h + 40) continue;
      const big = n.kind === 'core' || n.kind === 'star';
      c.font = `${big ? 600 : n.kind === 'section' ? 400 : 500} ${n.kind === 'core' ? 14 : big ? 13 : n.kind === 'section' ? 11 : 12}px ${this.fontUi}`;
      const text = n.title.length > 34 ? `${n.title.slice(0, 33)}…` : n.title;
      const tw = c.measureText(text).width;
      const ty = y + r * (big ? 1.25 : n.kind === 'critical' ? 1.35 : 1.15) + 4;
      const box: [number, number, number, number] = [x - tw / 2 - 3, ty - 2, x + tw / 2 + 3, ty + 15];
      if (rank(n) > 0 && placed.some((b) => box[0] < b[2] && box[2] > b[0] && box[1] < b[3] && box[3] > b[1])) continue;
      placed.push(box);
      c.globalAlpha = a;
      c.strokeStyle = 'rgba(3,4,12,0.82)';
      c.lineWidth = 3;
      c.strokeText(text, x, ty);
      c.fillStyle = n.kind === 'section' ? 'rgba(198,208,232,0.9)' : big ? '#f6f8ff' : '#e4e9f7';
      c.fillText(text, x, ty);
    }
    c.globalAlpha = 1;
  }
}
