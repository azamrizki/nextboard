import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';

// The whole web on every screen (DESIGN.md §3.0): every page, every settings pane, the dialogs
// and every card shown on its own fit the screen, with nothing run off its side. layout.spec.ts
// checks the shell (header, tab bar, finger-sized targets) more closely.
//
// SWEEP_SHOTS=<folder> also saves a screenshot of each, to look at by eye.

const PANES = ['agents', 'lead', 'profiles', 'mcp', 'notifications', 'shortcuts', 'usage', 'studios', 'voice', 'connections', 'appearance', 'app'];

async function shot(page: Page, project: string, name: string) {
  const dir = process.env.SWEEP_SHOTS;
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  await page.screenshot({ path: join(dir, `${project}-${name.replace(/[^a-z0-9]+/gi, '_')}.png`) });
}

/**
 * What runs past the left or right edge of the screen, inside `scope` when given: an element
 * that sticks out, or is cut off there by a box around it. What a box scrolls sideways (a tab
 * strip, kanban columns) is meant to be there, as is the inside of the canvas, an editor or a
 * terminal, which do their own scrolling.
 */
function offScreen(page: Page, scope?: string) {
  return page.evaluate(([scope, W]) => {
    const root = scope ? document.querySelector(scope) : document.body;
    if (!root) return [`${scope}: not there`];
    if (window.innerWidth > W + 1) return [`the page is ${window.innerWidth}px wide on a ${W}px screen`];
    const OWN_SCROLL = '.react-flow__viewport, .monaco-editor, .xterm, .cm-editor, .minimap';
    const tag = (e: Element) => `${e.tagName.toLowerCase()}.${[...e.classList].join('.')}`;
    const out = (r: DOMRect) => r.width >= 2 && r.height >= 2 && (r.left < -1 || r.right > W + 1);
    /** Why what is in `from` may be past the edge, or '' when nothing excuses it; and what cuts it off. */
    const excuse = (from: Element, text: boolean): { fine: boolean; cutBy: string } => {
      const own = from.closest(OWN_SCROLL);
      if (own && root.contains(own) && own !== root) return { fine: true, cutBy: '' };
      let cutBy = '';
      for (let a: Element | null = from; a && a !== document.documentElement; a = a.parentElement) {
        const s = getComputedStyle(a);
        if (s.visibility === 'hidden' || s.opacity === '0' || s.display === 'none') return { fine: true, cutBy };
        // a box that scrolls sideways shows the rest when scrolled
        if (s.overflowX === 'auto' || s.overflowX === 'scroll') return { fine: true, cutBy };
        if (s.overflowX !== 'visible' && !cutBy) {
          const ar = a.getBoundingClientRect();
          if (ar.left >= -1 && ar.right <= W + 1) {
            // text cut short with an ellipsis is meant
            if (text && s.textOverflow === 'ellipsis') return { fine: true, cutBy };
            cutBy = tag(a);
          }
        }
      }
      return { fine: false, cutBy };
    };
    const seen: string[] = [];
    const flagged: Element[] = [];
    for (const el of root.querySelectorAll('*')) {
      if (!out(el.getBoundingClientRect()) || flagged.some((f) => f.contains(el))) continue;
      if (el !== root && excuse(el.parentElement ?? el, false).fine) continue;
      const cs = getComputedStyle(el);
      if (cs.visibility === 'hidden' || cs.opacity === '0') continue;
      flagged.push(el);
      const r = el.getBoundingClientRect();
      const { cutBy } = excuse(el.parentElement ?? el, false);
      const name = el.getAttribute('aria-label') || el.textContent?.trim().slice(0, 30) || '';
      seen.push(`${tag(el)} "${name}" ${Math.round(r.left)}…${Math.round(r.right)} of ${W}${cutBy ? `, cut off by ${cutBy}` : ''}`);
    }
    // text runs past a box without the box being wider: each line of it
    const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    const range = document.createRange();
    for (let t = walk.nextNode(); t; t = walk.nextNode()) {
      const parent = t.parentElement;
      if (!parent || !t.textContent?.trim() || flagged.some((f) => f.contains(parent))) continue;
      range.selectNodeContents(t);
      const r = [...range.getClientRects()].find(out);
      if (!r) continue;
      const { fine, cutBy } = excuse(parent, true);
      if (fine) continue;
      flagged.push(parent);
      seen.push(`text in ${tag(parent)} "${t.textContent.trim().slice(0, 30)}" ${Math.round(r.left)}…${Math.round(r.right)} of ${W}${cutBy ? `, cut off by ${cutBy}` : ''}`);
    }
    return seen.slice(0, 8);
  }, [scope, screenWidth(page)] as const);
}

// A phone's Chrome widens the page to what is too wide for it, and shows all of it smaller:
// window.innerWidth grows with it. The screen's own width is the measure.
const screenWidth = (page: Page) => page.viewportSize()!.width;
const sideways = async (page: Page) => (await page.evaluate(() => document.documentElement.scrollWidth)) - screenWidth(page);

async function project(hub: Hub, name: string) {
  const p = await hub.project(name);
  writeFileSync(join(p.path, 'README.md'), '# Demo\n\nA small project with a rather long line of text in it, to see that it wraps on a narrow screen.\n');
  return p;
}

test('every page and settings pane fits the screen', async ({ page, hub }, info) => {
  const p = await project(hub, 'Sweep');
  await hub.memory(p.id, '# Notes\n\nWhat the agents should know.');
  const where = [
    '/',
    '/memory',
    `/memory/${p.id}`,
    '/machines',
    '/devices',
    '/settings',
    ...PANES.map((id) => `/settings/${id}`),
    `/p/${p.id}/w/${p.wid}`,
    '/no-such-page',
  ];
  const found: string[] = [];
  for (const path of where) {
    await page.goto(path);
    await page.waitForLoadState('networkidle').catch(() => {});
    await page.waitForTimeout(400);
    const over = await sideways(page);
    if (over > 1) found.push(`${path}: scrolls ${over}px sideways`);
    for (const e of await offScreen(page)) found.push(`${path}: ${e}`);
    await shot(page, info.project.name, path === '/' ? 'home' : path);
  }
  expect(found).toEqual([]);
});

test('the dialogs fit the screen', async ({ page, hub }, info) => {
  const p = await project(hub, 'Dialogs');
  const found: string[] = [];
  const check = async (name: string) => {
    const dialog = page.getByRole('dialog').last();
    await expect(dialog).toBeVisible();
    await page.waitForTimeout(400);
    const b = (await dialog.boundingBox())!;
    const vw = page.viewportSize()!;
    if (b.x < -1 || b.x + b.width > vw.width + 1) found.push(`${name}: ${Math.round(b.x)}…${Math.round(b.x + b.width)} of ${vw.width}`);
    for (const e of await offScreen(page, '[role="dialog"]')) found.push(`${name}: ${e}`);
    await shot(page, info.project.name, `dialog-${name}`);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
  };

  await page.goto('/');
  await page.getByRole('button', { name: 'Add project' }).first().click();
  await check('add-project');

  await page.getByRole('button', { name: /^Credentials/ }).first().click();
  await check('vault');

  if (info.project.name === 'light' || info.project.name === 'dark') {
    await page.getByRole('button', { name: 'About this hub' }).click();
  } else {
    await page.getByRole('button', { name: 'More', exact: true }).click();
    await page.getByRole('menuitem', { name: 'About this hub' }).click();
  }
  await check('about');

  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: /^Credentials/ }).first().click();
  await check('vault-in-project');
  expect(found).toEqual([]);
});

// Every kind of card on its own (focus mode), as a phone shows each one.
const CARDS: { kind: string; w: number; h: number; data: (path: string) => Record<string, unknown> }[] = [
  { kind: 'note', w: 220, h: 200, data: () => ({ text: 'A note with a fairly long first line to wrap.' }) },
  { kind: 'checklist', w: 300, h: 340, data: () => ({ title: 'Checklist', items: [{ id: 'i1', text: 'One thing to do, said at some length' }] }) },
  { kind: 'kanban', w: 1000, h: 540, data: () => ({ title: 'Kanban', columns: [{ id: 'c1', title: 'To do', cards: [{ id: 'a', title: 'A card' }] }, { id: 'c2', title: 'Done', cards: [] }] }) },
  { kind: 'calendar', w: 600, h: 500, data: () => ({}) },
  { kind: 'orchestrator', w: 380, h: 460, data: () => ({}) },
  { kind: 'memory', w: 320, h: 300, data: () => ({ blocks: [{ name: 'alpha' }] }) },
  { kind: 'file', w: 560, h: 480, data: (path) => ({ machineId: 'local', path: `${path}/README.md`, view: 'markdown' }) },
  { kind: 'file', w: 320, h: 420, data: (path) => ({ machineId: 'local', path, view: 'folder' }) },
  { kind: 'branches', w: 320, h: 420, data: () => ({}) },
  { kind: 'weblog', w: 460, h: 380, data: () => ({}) },
  { kind: 'link', w: 320, h: 112, data: () => ({ url: 'https://example.com/a/rather/long/address/to/see/it/cut' }) },
  { kind: 'document', w: 560, h: 640, data: () => ({}) },
  { kind: 'uptime', w: 1000, h: 640, data: () => ({}) },
  { kind: 'credentials', w: 960, h: 640, data: () => ({}) },
  { kind: 'editor', w: 1240, h: 780, data: () => ({}) },
];

test('every card fits the screen when shown on its own', async ({ page, hub }, info) => {
  test.setTimeout(120_000);
  const p = await project(hub, 'Cards');
  const found: string[] = [];
  for (const [i, c] of CARDS.entries()) {
    const id = `n_sweep${String(i).padStart(5, '0')}`;
    await hub.ops(p.wid, [{ op: 'put', node: { id, kind: c.kind, x: (i % 4) * 1400, y: Math.floor(i / 4) * 900, w: c.w, h: c.h, data: c.data(p.path) } }]);
    await page.goto(`/p/${p.id}/w/${p.wid}?card=${id}`);
    await expect(page.locator('.react-flow__node.is-focused')).toBeVisible();
    await page.waitForTimeout(700);
    const name = `${c.kind}${c.data(p.path).view ? `-${c.data(p.path).view}` : ''}`;
    for (const e of await offScreen(page, '.react-flow__node.is-focused')) found.push(`${name}: ${e}`);
    await shot(page, info.project.name, `card-${name}`);
  }
  // a card at the board's far edge, made the size of the screen, is moved back inside the board
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_sweepedge1', kind: 'note', x: 19_800, y: 19_850, w: 180, h: 140, data: { text: 'At the edge' } } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_sweepedge1`);
  await expect(page.locator('.react-flow__node.is-focused')).toBeVisible();
  await page.waitForTimeout(500);
  for (const e of await offScreen(page, '.react-flow__node.is-focused')) found.push(`note at the edge: ${e}`);
  const edge = (await page.locator('.react-flow__node.is-focused').boundingBox())!;
  if (edge.y + edge.height > page.viewportSize()!.height + 1) found.push(`note at the edge: its bottom is at ${Math.round(edge.y + edge.height)}`);

  // a console too
  const con = await hub.console(p.wid);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=${con.node}`);
  await expect(page.locator('.react-flow__node.is-focused .xterm')).toBeVisible({ timeout: 15_000 });
  await page.waitForTimeout(700);
  for (const e of await offScreen(page, '.react-flow__node.is-focused')) found.push(`console: ${e}`);
  await shot(page, info.project.name, 'card-console');
  expect(found).toEqual([]);
});
