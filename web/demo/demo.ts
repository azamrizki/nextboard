import { randomBytes } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Locator, type Page } from '@playwright/test';
import { ROOT, SITE_PORT } from './playwright.config';

// What the demo specs share: the made-up projects, the board of acme-shop, and a cursor and
// captions drawn into the page, since a headless browser records neither.

export interface Made {
  slug: string;
  id: string;
  wid: string;
  path: string;
}

export function projects(): Record<string, Made> {
  const list: Made[] = JSON.parse(readFileSync(join(ROOT, '.local', 'share', 'demo-projects.json'), 'utf8'));
  return Object.fromEntries(list.map((p) => [p.slug, p]));
}

export async function call<T = any>(request: APIRequestContext, method: string, url: string, data?: unknown): Promise<T> {
  const r = await request.fetch(url, { method, data, headers: { 'X-NextBoard': '1' } });
  expect(r.ok(), `${method} ${url}: ${r.status()} ${await r.text()}`).toBeTruthy();
  return r.json();
}

export const SHOP_URL = `http://localhost:${SITE_PORT}/`;

/** The board of acme-shop: three agents, the sprint, a note, the storefront and the code. */
export async function shopBoard(request: APIRequestContext, p: Made) {
  const board = await call(request, 'GET', `/api/workspaces/${p.wid}/board`);
  if (board.nodes.length > 0) return;
  const consoles: [string, string, number, number][] = [
    ['builder', 'Builder', 0, 0],
    ['reviewer', 'Reviewer', 740, 0],
    ['tester', 'Tester', 0, 470],
  ];
  for (const [agent, name, x, y] of consoles) {
    await call(request, 'POST', '/api/sessions', { agent, workspaceId: p.wid, name, cols: 92, rows: 24, node: { x, y, w: 720, h: 450 } });
  }
  const ops = [
    {
      op: 'put',
      node: {
        id: 'n_demokanban',
        kind: 'kanban',
        x: 740,
        y: 470,
        w: 720,
        h: 450,
        data: {
          title: 'Sprint 12',
          columns: [
            { id: 'c1', title: 'To do', color: 'sky', cards: [{ id: 'k1', title: 'Gift cards', priority: 'low' }, { id: 'k2', title: 'Saved addresses' }] },
            { id: 'c2', title: 'In progress', color: 'amber', cards: [{ id: 'k3', title: 'Order history on the account page', priority: 'high' }, { id: 'k4', title: 'Coupon codes at checkout', priority: 'med' }] },
            { id: 'c3', title: 'Review', color: 'violet', cards: [{ id: 'k5', title: 'Free shipping from $50' }] },
            { id: 'c4', title: 'Done', color: 'sage', cards: [{ id: 'k6', title: 'Search suggestions', done: true }, { id: 'k7', title: 'Wishlist', done: true }] },
          ],
        },
      },
    },
    { op: 'put', node: { id: 'n_demonote01', kind: 'note', x: 1480, y: 700, w: 240, h: 220, data: { text: 'Ship coupons before the spring sale.\n\nSPRING10 = 10% off', color: 'yellow' } } },
    { op: 'put', node: { id: 'n_demoweb001', kind: 'web', x: 1480, y: 0, w: 900, h: 680, data: {} } },
  ];
  await call(request, 'POST', `/api/workspaces/${p.wid}/board/ops`, { clientId: 'c_demo', baseRev: 0, ops });
}

/** Points the storefront's tile at the dev server, as one would by typing it. */
export async function openShop(page: Page) {
  const tile = page.locator('.react-flow__node[data-id="n_demoweb001"]');
  const address = tile.getByRole('textbox', { name: 'Address' });
  await expect(address).toBeVisible({ timeout: 15_000 });
  if ((await address.inputValue()) === SHOP_URL.replace(/\/$/, '')) return;
  await address.fill(SHOP_URL);
  await address.press('Enter');
}

/**
 * A cursor that follows the mouse, a ring where it clicks, and a caption line. Playwright moves
 * the mouse with real events, so the cursor goes where a person's would.
 */
export const OVERLAY = `(() => {
  const css = \`
    #demo-cursor { position: fixed; left: 0; top: 0; width: 22px; height: 22px; z-index: 2147483647; pointer-events: none;
      transform: translate(-3px, -2px); transition: opacity .2s; }
    #demo-cursor svg { filter: drop-shadow(0 1px 2px rgba(0,0,0,.45)); }
    .demo-ring { position: fixed; width: 34px; height: 34px; margin: -17px 0 0 -17px; border-radius: 50%; z-index: 2147483646;
      pointer-events: none; border: 2px solid rgba(80,120,255,.9); animation: demo-ring .45s ease-out forwards; }
    @keyframes demo-ring { from { transform: scale(.4); opacity: 1; } to { transform: scale(1.4); opacity: 0; } }
    #demo-caption { position: fixed; left: 50%; top: 84px; transform: translateX(-50%) translateY(-12px); z-index: 2147483645;
      pointer-events: none; opacity: 0; transition: opacity .35s, transform .35s; max-width: 80vw; text-align: center;
      font: 600 22px/1.35 Inter, system-ui, sans-serif; color: #fff; background: rgba(17,17,20,.86); padding: 12px 22px;
      border-radius: 14px; box-shadow: 0 8px 30px rgba(0,0,0,.25); letter-spacing: -.01em; }
    #demo-caption.on { opacity: 1; transform: translateX(-50%); }
    #demo-caption small { display: block; font-weight: 450; font-size: 15px; opacity: .78; margin-top: 2px; }
  \`;
  const add = () => {
    if (document.getElementById('demo-cursor')) return;
    const style = document.createElement('style');
    style.textContent = css;
    document.head.appendChild(style);
    const cur = document.createElement('div');
    cur.id = 'demo-cursor';
    cur.innerHTML = '<svg width="22" height="22" viewBox="0 0 22 22"><path d="M3 2 L3 18 L7.5 13.8 L10.6 20.5 L13.4 19.2 L10.4 12.6 L16.5 12.6 Z" fill="#111" stroke="#fff" stroke-width="1.4" stroke-linejoin="round"/></svg>';
    document.body.appendChild(cur);
    const cap = document.createElement('div');
    cap.id = 'demo-caption';
    document.body.appendChild(cap);
    const saved = sessionStorage.getItem('demo-cursor');
    if (saved) { const [x, y] = JSON.parse(saved); cur.style.left = x + 'px'; cur.style.top = y + 'px'; }
    else cur.style.opacity = '0';
  };
  if (document.body) add(); else document.addEventListener('DOMContentLoaded', add);
  addEventListener('mousemove', (e) => {
    const cur = document.getElementById('demo-cursor');
    if (!cur) return;
    cur.style.opacity = '1';
    cur.style.left = e.clientX + 'px';
    cur.style.top = e.clientY + 'px';
    sessionStorage.setItem('demo-cursor', JSON.stringify([e.clientX, e.clientY]));
  }, true);
  addEventListener('mousedown', (e) => {
    const r = document.createElement('div');
    r.className = 'demo-ring';
    r.style.left = e.clientX + 'px';
    r.style.top = e.clientY + 'px';
    document.body.appendChild(r);
    setTimeout(() => r.remove(), 500);
  }, true);
  window.__demoCaption = (title, sub) => {
    const cap = document.getElementById('demo-caption');
    if (!cap) return;
    if (!title) { cap.classList.remove('on'); return; }
    cap.innerHTML = '';
    cap.append(title);
    if (sub) { const s = document.createElement('small'); s.textContent = sub; cap.append(s); }
    cap.classList.add('on');
  };
})();`;

// ---- the mouse, moved as a hand would, and what is said on screen

let at = { x: 800, y: 450 };

const ease = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);

export async function glide(page: Page, x: number, y: number, ms = 600) {
  const from = { ...at };
  const start = Date.now();
  for (;;) {
    const t = Math.min(1, (Date.now() - start) / ms);
    const e = ease(t);
    await page.mouse.move(from.x + (x - from.x) * e, from.y + (y - from.y) * e);
    if (t >= 1) break;
  }
  at = { x, y };
}

export async function point(page: Page, target: Locator, ms?: number, dx = 0.5, dy = 0.5) {
  await expect(target).toBeVisible();
  const b = (await target.boundingBox())!;
  await glide(page, b.x + b.width * dx, b.y + b.height * dy, ms);
}

export async function click(page: Page, target: Locator, opts: { double?: boolean; ms?: number; dx?: number; dy?: number } = {}) {
  await point(page, target, opts.ms, opts.dx, opts.dy);
  await page.waitForTimeout(140);
  await page.mouse.down();
  await page.waitForTimeout(70);
  await page.mouse.up();
  if (opts.double) {
    await page.waitForTimeout(70);
    await page.mouse.down({ clickCount: 2 });
    await page.mouse.up({ clickCount: 2 });
  }
}

export async function type(page: Page, text: string, delay = 45) {
  await page.keyboard.type(text, { delay });
}

export async function caption(page: Page, title?: string, sub?: string) {
  await page.evaluate(([t, s]) => (window as any).__demoCaption?.(t, s), [title ?? '', sub ?? ''] as const);
}

/** A full-screen title, for the start and the end of the video. */
export async function titleCard(page: Page, title: string, sub: string, lines: string[] = []) {
  await page.evaluate(
    ([title, sub, lines]) => {
      document.getElementById('demo-card')?.remove();
      const card = document.createElement('div');
      card.id = 'demo-card';
      card.style.cssText =
        'position:fixed;inset:0;z-index:2147483640;display:grid;place-items:center;background:#0e0e10;color:#fff;opacity:0;transition:opacity .5s;font-family:Inter,system-ui,sans-serif;text-align:center';
      const box = document.createElement('div');
      const logo = document.createElement('div');
      logo.textContent = '>_';
      logo.style.cssText = 'display:inline-grid;place-items:center;width:84px;height:84px;border-radius:22px;background:#fff;color:#0e0e10;font:700 34px ui-monospace,monospace;margin-bottom:26px';
      const h = document.createElement('div');
      h.textContent = title;
      h.style.cssText = 'font-size:64px;font-weight:700;letter-spacing:-.03em';
      const p = document.createElement('div');
      p.textContent = sub;
      p.style.cssText = 'font-size:26px;opacity:.75;margin-top:10px';
      box.append(logo, h, p);
      for (const l of lines) {
        const d = document.createElement('div');
        d.textContent = l;
        d.style.cssText = 'font:500 20px ui-monospace,monospace;opacity:.6;margin-top:18px';
        box.append(d);
      }
      card.append(box);
      document.body.append(card);
      requestAnimationFrame(() => (card.style.opacity = '1'));
    },
    [title, sub, lines] as const,
  );
}

export async function hideTitleCard(page: Page) {
  await page.evaluate(() => {
    const card = document.getElementById('demo-card');
    if (!card) return;
    card.style.opacity = '0';
    setTimeout(() => card.remove(), 600);
  });
}

// ---- the vault

export const MASTER = 'demo vault password';

const secret = () => randomBytes(12).toString('base64url');

/** Makes the vault with three made-up logins, in a vault window showing its gate. */
export async function seedVault(page: Page) {
  await page.getByLabel('Master password', { exact: true }).fill(MASTER);
  await page.getByLabel('Type it again').fill(MASTER);
  await page.getByLabel('I understand it cannot be recovered').check();
  await page.getByRole('button', { name: 'Create vault' }).click();
  await expect(page.getByRole('button', { name: 'Lock the vault' })).toBeVisible();
  for (const [name, user] of [
    ['Staging database', 'acme_app'],
    ['Payments sandbox', 'sandbox-acme'],
    ['Admin panel', 'admin@example.com'],
    ['Error tracker', 'ops@example.com'],
  ]) {
    await page.getByRole('button', { name: 'New', exact: true }).click();
    const form = page.locator('#vault-item-form');
    await form.getByLabel('Name', { exact: true }).fill(name);
    await form.getByLabel('Username', { exact: true }).fill(user);
    await form.getByLabel('Password', { exact: true }).fill(secret());
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(form).toBeHidden();
  }
}

/** The node of each agent's console on a board, by agent id. */
export async function consoles(request: APIRequestContext, wid: string): Promise<Record<string, string>> {
  const board = await call(request, 'GET', `/api/workspaces/${wid}/board`);
  const sessions: { id: string; agent?: string; preset?: string }[] = await call(request, 'GET', '/api/sessions');
  const out: Record<string, string> = {};
  for (const n of board.nodes) {
    if (n.kind !== 'terminal') continue;
    const s = sessions.find((x) => x.id === n.data.sessionId);
    if (s) out[s.agent ?? s.preset ?? ''] = n.id;
  }
  return out;
}
