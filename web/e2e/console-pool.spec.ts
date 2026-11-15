import type { Page } from '@playwright/test';
import { expect, test } from './hub';

// More consoles than WebGL contexts (webglPool.ts): the visible ones are drawn by the GPU, the
// rest by the DOM renderer, and swapping between them changes nothing the hub or the user sees.
// WebGL is forced on the test browser's software GPU (localStorage nb.webgl), at pixel ratio 1:
// under an emulated ratio the app drops WebGL on purpose (console-gl.spec.ts).
test.use({ launchOptions: { args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist', '--js-flags=--expose-gc'] } });

const BUDGET = 12; // a desktop browser's (budgetFor)
const COUNT = 18; // over the budget, and over Chrome's cap of 16
const COLS = 6;
const W = 560;
const H = 360;
const PITCH_X = 700;
const PITCH_Y = 500;

interface Watch {
  made: number;
  lost: number;
  swaps: number;
  blank: string[];
  domSwaps: number;
  blankDom: string[];
}

/** Counts WebGL contexts and checks every renderer swap in the same task as it happens. */
async function watchSwaps(page: Page, wid: string) {
  await page.addInitScript((vp) => {
    localStorage.setItem('nb.webgl', 'force');
    localStorage.setItem(vp.key, vp.value);
    // Headless Linux rounds glyph advances to whole pixels (JetBrains Mono at 13 px: 8), browsers
    // with subpixel text measure 7.8, and only then do the renderers' grids differ. xterm measures
    // its cell with an OffscreenCanvas: give it the fraction.
    const measure = OffscreenCanvasRenderingContext2D.prototype.measureText;
    OffscreenCanvasRenderingContext2D.prototype.measureText = function (this: OffscreenCanvasRenderingContext2D, text: string) {
      const m = measure.call(this, text);
      if (text !== 'W' || !/JetBrains/.test(this.font)) return m;
      const width = m.width * 0.975;
      return new Proxy(m, { get: (t, k) => (k === 'width' ? width : Reflect.get(t, k)) });
    };
    const w = window as unknown as { __gl: Watch };
    const gl: Watch = (w.__gl = { made: 0, lost: 0, swaps: 0, blank: [], domSwaps: 0, blankDom: [] });
    const hadCanvas = new WeakSet<Element>();
    // screens that showed text: an empty console drawn by WebGL is all background, and that is fine
    const hadText = new WeakSet<Element>();
    const colours = (c: HTMLCanvasElement) => {
      const o = document.createElement('canvas');
      o.width = c.width;
      o.height = c.height;
      const x = o.getContext('2d')!;
      x.drawImage(c, 0, 0);
      const d = x.getImageData(0, 0, o.width, o.height).data;
      const seen = new Set<number>();
      for (let k = 0; k < d.length && seen.size < 3; k += 4) seen.add((d[k] << 24) | (d[k + 1] << 16) | (d[k + 2] << 8) | d[k + 3]);
      return seen.size;
    };
    const getContext = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, type: string, ...rest: unknown[]) {
      const ctx = (getContext as (...a: unknown[]) => RenderingContext | null).call(this, type, ...rest);
      if (ctx && /webgl/.test(type) && !(this as { counted?: boolean }).counted) {
        (this as { counted?: boolean }).counted = true;
        gl.made++;
        // capture, and before the app's own listener (which stops the event)
        this.addEventListener('webglcontextlost', () => gl.lost++, { capture: true });
        const canvas = this;
        // still in the task that made it: what the first frame would show
        queueMicrotask(() => {
          const screen = canvas.closest('.xterm-screen');
          if (!screen) return;
          hadCanvas.add(screen);
          gl.swaps++;
          if (hadText.has(screen) && colours(canvas) < 2) gl.blank.push(screen.closest('[data-session]')?.getAttribute('data-session') ?? '?');
        });
      }
      return ctx as never;
    } as typeof HTMLCanvasElement.prototype.getContext;
    // WebGL → DOM: the rows put in place of the canvas must have the text in that same task
    new MutationObserver((records) => {
      for (const r of records) {
        const rows = r.target instanceof Element ? r.target.closest('.xterm-rows') : null;
        const screen = rows?.closest('.xterm-screen');
        if (screen && rows!.textContent?.trim()) hadText.add(screen);
      }
      for (const r of records)
        for (const n of r.addedNodes) {
          if (!(n instanceof HTMLElement) || !n.classList.contains('xterm-rows')) continue;
          const screen = n.closest('.xterm-screen');
          if (!screen || !hadCanvas.has(screen)) continue;
          gl.domSwaps++;
          if (!n.textContent?.trim()) gl.blankDom.push(screen.closest('[data-session]')?.getAttribute('data-session') ?? '?');
        }
    }).observe(document, { childList: true, subtree: true });
  }, { key: `nb.vp.${wid}`, value: JSON.stringify({ x: 88, y: 140, zoom: 1 }) });
}

/** Each console's state: on screen, drawn by WebGL. */
function consoles(page: Page) {
  return page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>('.react-flow__node .nb-term')].map((t) => {
      const r = t.getBoundingClientRect();
      const w = Math.min(r.right, innerWidth) - Math.max(r.left, 0);
      const h = Math.min(r.bottom, innerHeight) - Math.max(r.top, 0);
      return { id: t.closest('[data-session]')?.getAttribute('data-session') ?? '', visible: w > 40 && h > 40, gl: !!t.querySelector('.xterm-screen canvas'), text: t.querySelector('.xterm-rows')?.textContent ?? '' };
    }),
  );
}

/** Waits until every console on screen has WebGL, and checks the pool stays within its budget. */
async function settled(page: Page) {
  await expect
    .poll(async () => {
      const all = await consoles(page);
      return all.filter((c) => c.visible && !c.gl).map((c) => c.id);
    }, { timeout: 10_000 })
    .toEqual([]);
  const all = await consoles(page);
  expect(all.filter((c) => c.gl).length).toBeLessThanOrEqual(BUDGET);
  // the probe for a hardware GPU is lost at once: what is left is the pool's
  const live = await page.evaluate(() => (window as unknown as { __gl: Watch }).__gl.made - (window as unknown as { __gl: Watch }).__gl.lost);
  expect(live).toBeLessThanOrEqual(BUDGET);
  return all;
}

/** Pans the board by about dx, dy by scrolling over an empty part of it (React Flow pans at half the wheel's delta). */
async function pan(page: Page, dx: number, dy: number) {
  const at = await page.evaluate(() => {
    for (let y = 200; y < innerHeight - 120; y += 25)
      for (let x = 120; x < innerWidth - 120; x += 25) if (document.elementFromPoint(x, y)?.classList.contains('react-flow__pane')) return { x, y };
    return null;
  });
  expect(at, 'an empty spot of the board').not.toBeNull();
  const transform = () => page.locator('.react-flow__viewport').evaluate((v) => (v as HTMLElement).style.transform);
  const before = await transform();
  await page.mouse.move(at!.x, at!.y);
  for (let k = 0; k < 6; k++) await page.mouse.wheel(dx / 3, dy / 3);
  await expect.poll(transform).not.toBe(before);
}

test('a board of more consoles than WebGL contexts: the visible ones get it, swaps change nothing', async ({ page, hub }) => {
  test.setTimeout(120_000);
  const p = await hub.project('Pool');
  const made: { id: string; node: string }[] = [];
  for (let i = 0; i < COUNT; i++) made.push(await hub.console(p.wid));
  await hub.ops(
    p.wid,
    made.map((c, i) => ({ op: 'patch', id: c.node, set: { x: (i % COLS) * PITCH_X, y: Math.floor(i / COLS) * PITCH_Y, w: W, h: H } })),
  );

  const warnings: string[] = [];
  page.on('console', (m) => {
    if (/too many active webgl contexts|context lost|not restored/i.test(m.text())) warnings.push(m.text());
  });
  // every size each console sent the hub, and which have their prompt
  const sizes = new Map<string, string[]>();
  const prompted = new Set<string>();
  page.on('websocket', (ws) => {
    const sid = /sessions\/([^/]+)\/ws/.exec(ws.url())?.[1];
    if (!sid) return;
    ws.on('framereceived', (f) => {
      if (typeof f.payload !== 'string' && f.payload.includes('>')) prompted.add(sid);
    });
    ws.on('framesent', (f) => {
      if (typeof f.payload !== 'string' || !f.payload.includes('"t":"r"')) return;
      const m = JSON.parse(f.payload) as { c: number; r: number };
      sizes.set(sid, [...(sizes.get(sid) ?? []), `${m.c}x${m.r}`]);
    });
  });

  await watchSwaps(page, p.wid);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await expect(page.locator('.react-flow__node .xterm')).toHaveCount(COUNT, { timeout: 20_000 });
  await expect.poll(() => made.filter((c) => !prompted.has(c.id)).length, { timeout: 15_000 }).toBe(0);
  const first = await settled(page);
  expect(first.filter((c) => c.visible).length).toBeGreaterThan(1);
  const shown = new Set<string>();

  // across the whole board and back: each console is on screen at some point
  for (const [dx, dy] of [
    [PITCH_X, 0],
    [PITCH_X, 0],
    [PITCH_X, 0],
    [PITCH_X, 0],
    [0, PITCH_Y],
    [-PITCH_X, 0],
    [-PITCH_X, 0],
    [-PITCH_X, 0],
    [-PITCH_X, 0],
    [0, PITCH_Y],
    [PITCH_X, 0],
    [PITCH_X, 0],
    [0, -2 * PITCH_Y],
    [-2 * PITCH_X, 0],
  ]) {
    await pan(page, dx, dy);
    for (const c of await settled(page)) if (c.visible) shown.add(c.id);
  }

  const gl = await page.evaluate(() => (window as unknown as { __gl: Watch }).__gl);
  expect(warnings).toEqual([]);
  // each was on screen (and so drawn by WebGL), with contexts taken from consoles off screen
  expect(shown.size).toBe(COUNT);
  expect(gl.made).toBeGreaterThan(BUDGET + 1);
  expect(gl.domSwaps).toBeGreaterThan(0);
  expect(gl.blank).toEqual([]);
  expect(gl.blankDom).toEqual([]);
  // the hub was never told of another size: swaps keep the columns and rows
  for (const c of made) {
    const sent = sizes.get(c.id) ?? [];
    expect(sent.length, `sizes sent by ${c.id}`).toBeGreaterThan(0);
    expect(new Set(sent).size, `sizes sent by ${c.id}: ${sent.join(' ')}`).toBe(1);
  }
  // the ones the DOM draws show their prompt
  for (const c of await consoles(page)) if (!c.gl) expect(c.text, c.id).toContain('>');
});

test('a closed console that had focus and WebGL is garbage-collected', async ({ page, hub }) => {
  const p = await hub.project('Pool gc');
  const keep = await hub.console(p.wid);
  const gone = await hub.console(p.wid);
  await hub.ops(p.wid, [
    { op: 'patch', id: keep.node, set: { x: 0, y: 0, w: W, h: H } },
    { op: 'patch', id: gone.node, set: { x: PITCH_X, y: 0, w: W, h: H } },
  ]);
  await watchSwaps(page, p.wid);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const term = page.locator(`.react-flow__node[data-id="${gone.node}"] .xterm`);
  await expect(term.locator('.xterm-screen canvas')).toHaveCount(2, { timeout: 15_000 });
  await expect(page.locator(`.react-flow__node[data-id="${gone.node}"] .xterm-rows`)).toHaveCount(0);
  // focused: its cursor blinks, which kept the whole terminal alive (#5817)
  await term.click();
  await expect(term.locator('.xterm-helper-textarea')).toBeFocused();
  await page.waitForTimeout(800);
  await page.evaluate((node) => {
    const n = document.querySelector(`.react-flow__node[data-id="${node}"]`)!;
    (window as unknown as { refs: WeakRef<Element>[] }).refs = [new WeakRef(n.querySelector('.xterm-helper-textarea')!), new WeakRef(n.querySelector('.xterm-screen canvas')!)];
  }, gone.node);

  await hub.call('DELETE', `/api/sessions/${gone.id}`);
  await expect(page.locator(`.react-flow__node[data-id="${gone.node}"]`)).toHaveCount(0, { timeout: 10_000 });
  await expect
    .poll(
      () =>
        page.evaluate(async () => {
          (window as unknown as { gc: () => void }).gc();
          await new Promise((r) => setTimeout(r, 200));
          (window as unknown as { gc: () => void }).gc();
          return (window as unknown as { refs: WeakRef<Element>[] }).refs.filter((r) => r.deref()).length;
        }),
      { timeout: 15_000, intervals: [500] },
    )
    .toBe(0);
  // the other console is untouched
  await expect(page.locator(`.react-flow__node[data-id="${keep.node}"] .xterm-screen canvas`)).toHaveCount(2);
});
