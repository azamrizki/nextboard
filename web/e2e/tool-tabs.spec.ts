import { writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { join } from 'node:path';
import type { Locator, Page, TestInfo } from '@playwright/test';
import { expect, test, type Hub } from './hub';
import { openVault } from './vault';

// Tools as tabs in the Code view (docs/API-studio.md §7): Chats, Memory, the four studios, the
// vault, the browser, Loops and Mission Map each open as an editor tab, on the board the view is
// bound to and with no canvas drawn. Opening one that is open focuses it. A browser tab shows a
// page by its address and follows it; the editor's Ports panel opens one. The vault locks and
// unlocks in its tab. Every tool fits a phone, a tablet and a desktop, in both themes.

const SHOTS = process.env.NEXTBOARD_E2E_SHOTS;

// a dev server of the project, on This PC
const PAGES: Record<string, string> = {
  '/': '<!doctype html><title>Shop</title><h1>Shop</h1><a href="/cart">Cart</a>',
  '/cart': '<!doctype html><title>Shop · Cart</title><h1>Shop · Cart</h1><p>Linen shirt × 1</p><a href="/checkout">Checkout</a>',
  '/checkout': '<!doctype html><title>Checkout</title><h1>Checkout</h1>',
};
let site: Server;
let port = 0;
test.beforeAll(async () => {
  site = createServer((req, res) => {
    const body = PAGES[(req.url ?? '/').split('?')[0]];
    res.statusCode = body ? 200 : 404;
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(body ?? 'not found');
  });
  await new Promise<void>((done) => site.listen(0, '127.0.0.1', done));
  port = (site.address() as AddressInfo).port;
});
test.afterAll(() => site?.close());

const TOOLS = [
  { label: 'Chats', kind: 'chats', heading: 'Chats' },
  { label: 'Memory', kind: 'memory', heading: 'Memory' },
  { label: 'Documents', kind: 'documents', heading: 'Documents', studio: true },
  { label: 'Diagrams', kind: 'diagrams', heading: 'Diagrams', studio: true },
  { label: 'Markdown', kind: 'markdown', heading: 'Markdown', studio: true },
  { label: 'Skills', kind: 'skills', heading: 'Skills', studio: true },
  { label: 'Vault', kind: 'vault', heading: 'Vault' },
  { label: 'Browser', kind: 'browser', heading: '' },
  { label: 'Loops', kind: 'loops', heading: 'Loops' },
  { label: 'Mission Map', kind: 'mission', heading: 'Mission Map' },
] as const;
type Tool = (typeof TOOLS)[number];

/** A project with a file in its folder, in its Code view. */
async function codeView(page: Page, hub: Hub, name: string) {
  const p = await hub.project(name);
  writeFileSync(join(p.path, 'app.ts'), 'export const api = 1;\n');
  await page.goto(`/p/${p.id}/code`);
  await expect(page.locator('.cv-bar')).toBeVisible({ timeout: 20_000 });
  return p;
}

const phoneNav = (page: Page) => page.getByRole('navigation', { name: 'Code view' });

/** From the activity bar (a studio from Studios' menu); a phone: from the Files view's ⋯ Tools. */
async function openTool(page: Page, t: Tool) {
  const views = page.getByRole('navigation', { name: 'Views' });
  if (await phoneNav(page).isVisible()) {
    await phoneNav(page).getByRole('button', { name: 'Files', exact: true }).click();
    await views.getByRole('button', { name: 'Tools', exact: true }).click();
    await page.getByRole('menuitem', { name: t.label, exact: true }).click();
  } else if ('studio' in t) {
    await views.getByRole('button', { name: 'Studios', exact: true }).click();
    await page.getByRole('menuitem', { name: t.label, exact: true }).click();
  } else {
    await views.getByRole('button', { name: t.label, exact: true }).click();
  }
}

/** An editor tab by its title. */
const tabOf = (page: Page, title: string) => page.locator('.wb-tab').filter({ has: page.locator('.wb-tab-name').getByText(title, { exact: true }) });
/** The tool tab shown: a group keeps a few more alive, hidden, behind it (two browsers, say). */
const toolTab = (page: Page, kind: string) => page.locator(`.tool-tab[data-tool="${kind}"]:visible`);

/** The tool shows in its tab: its heading (the browser: its address bar), and nothing wider than the tab. */
async function shows(page: Page, t: Tool): Promise<Locator> {
  const tool = toolTab(page, t.kind);
  await expect(tool).toBeVisible({ timeout: 20_000 });
  if (t.heading) await expect(tool.getByRole('heading', { name: t.heading, exact: true }).first()).toBeVisible({ timeout: 20_000 });
  else await expect(tool.getByRole('textbox', { name: 'Address' })).toBeVisible();
  await expect(tool.locator('.spinner')).toHaveCount(0, { timeout: 20_000 });
  const fits = await tool.evaluate((el) => el.scrollWidth <= el.clientWidth + 1);
  expect(fits, `${t.label} is wider than its tab`).toBe(true);
  return tool;
}

async function shot(page: Page, info: TestInfo, name: string) {
  if (SHOTS) await page.screenshot({ path: join(SHOTS, `${info.project.name}-${name}.png`) });
}

test('every tool opens as a tab of the Code view, and again focuses the one open', async ({ page, hub }, info) => {
  test.slow();
  await codeView(page, hub, 'Tidepool tools');
  for (const t of TOOLS) {
    await openTool(page, t);
    const title = t.kind === 'browser' ? 'Browser' : t.label;
    await expect(tabOf(page, title)).toHaveAttribute('aria-selected', 'true');
    const tool = await shows(page, t);
    await shot(page, info, `desktop-${t.kind}`);
    // board-only parts stay out: a panel's own close (the tab has one), a card for the board
    await expect(tool.locator('.side-panel-head').getByRole('button', { name: /^Close/ })).toHaveCount(0);
    if (t.kind === 'memory') {
      await expect(tool.locator('#memory-text')).toBeVisible();
      await expect(tool.getByRole('button', { name: 'Add memory tile' })).toHaveCount(0);
    }
    if (t.kind === 'diagrams') await expect(tool.getByRole('note')).toContainText('lands on the board');
    if (t.kind === 'mission') await expect(tool.getByRole('region', { name: 'Directors' })).toContainText('Command bar');
    if (t.kind === 'loops') await expect(tool.getByRole('button', { name: 'New loop' })).toBeVisible();
  }
  for (const t of TOOLS) await expect(tabOf(page, t.kind === 'browser' ? 'Browser' : t.label)).toHaveCount(1);

  // again: the open tab comes to the front, no second one
  for (const t of [TOOLS[0], TOOLS[6], TOOLS[2]]) {
    await openTool(page, t);
    await expect(tabOf(page, t.label)).toHaveCount(1);
    await expect(tabOf(page, t.label)).toHaveAttribute('aria-selected', 'true');
    await shows(page, t);
  }
  // a tab's × closes it, and the tool opens again
  await tabOf(page, 'Loops').click();
  await tabOf(page, 'Loops').getByRole('button', { name: /^Close/ }).click();
  await expect(tabOf(page, 'Loops')).toHaveCount(0);
  await openTool(page, TOOLS[8]);
  await shows(page, TOOLS[8]);
});

test('a browser tab shows a page by its address, follows it, and the Ports panel opens one', async ({ page, hub }, info) => {
  test.slow();
  await codeView(page, hub, 'Shop browser');
  const browser = TOOLS[7];
  await openTool(page, browser);
  let tool = await shows(page, browser);
  await expect(tool.getByText('Open a dev server')).toBeVisible();

  // an address typed: the dev server's page through the hub, the tab named after it
  const address = tool.getByRole('textbox', { name: 'Address' });
  await address.fill(`127.0.0.1:${port}/cart`);
  await address.press('Enter');
  const title = `localhost:${port}`;
  await expect(tabOf(page, title)).toHaveAttribute('aria-selected', 'true');
  await expect(tabOf(page, title)).toHaveAttribute('title', `http://localhost:${port}/cart`);
  const frame = tool.frameLocator('iframe');
  await expect(frame.getByRole('heading', { name: 'Shop · Cart' })).toBeVisible({ timeout: 20_000 });
  await expect(tool.locator('.web-state')).toContainText('Live');
  await shot(page, info, 'desktop-browser-page');

  // a link in the page: the address follows
  await frame.getByRole('link', { name: 'Checkout' }).click();
  await expect(frame.getByRole('heading', { name: 'Checkout' })).toBeVisible();
  await expect(address).toHaveValue(`http://localhost:${port}/checkout`);

  // out of sight behind another tool, the page stays as it was, nothing loaded again
  const loads: string[] = [];
  const count = (r: { url: () => string }) => r.url().includes('/__nb/launch') && loads.push(r.url());
  page.on('request', count);
  await openTool(page, TOOLS[1]);
  await expect(page.locator('.tool-tab[data-tool="browser"]')).toBeHidden();
  await tabOf(page, title).click();
  await expect(frame.getByRole('heading', { name: 'Checkout' })).toBeVisible();
  expect(loads).toEqual([]);
  page.off('request', count);

  // kept with the editor's tabs (saved a moment after a change): a reload shows the same page
  await expect
    .poll(() => page.evaluate((u) => Object.keys(localStorage).some((k) => k.startsWith('nb.wb.') && (localStorage.getItem(k) ?? '').includes(u)), `localhost:${port}/checkout`))
    .toBe(true);
  await page.reload();
  await expect(page.locator('.cv-bar')).toBeVisible({ timeout: 20_000 });
  tool = toolTab(page, 'browser');
  await expect(tool.frameLocator('iframe').getByRole('heading', { name: 'Checkout' })).toBeVisible({ timeout: 20_000 });
  await expect(tool.getByRole('textbox', { name: 'Address' })).toHaveValue(`http://localhost:${port}/checkout`);

  // the editor's openWeb: the Ports panel opens the port as a browser tab, and again focuses it
  await page.locator('.cv-bar').getByRole('button', { name: /^Toggle panel/ }).click();
  await page.getByRole('tablist', { name: 'Panel' }).getByRole('tab', { name: 'Ports' }).click();
  const row = page.locator('.wb-ports tr').filter({ has: page.getByRole('cell', { name: String(port), exact: true }) });
  await row.getByRole('button', { name: 'Open in a tab' }).click({ timeout: 20_000 });
  await expect(tabOf(page, title)).toHaveCount(2);
  const opened = page.locator('.wb-tab[aria-selected="true"]');
  await expect(opened).toHaveAttribute('title', `http://localhost:${port}/`);
  await expect(toolTab(page, 'browser').frameLocator('iframe').getByRole('heading', { name: 'Shop', exact: true })).toBeVisible({ timeout: 20_000 });
  await tabOf(page, title).first().click();
  await row.getByRole('button', { name: 'Open in a tab' }).click();
  await expect(tabOf(page, title)).toHaveCount(2);
  await expect(page.locator('.wb-tab[aria-selected="true"]')).toHaveAttribute('title', `http://localhost:${port}/`);
});

test('the vault in a tab: locked, unlocked, an item, locked again', async ({ page, hub }, info) => {
  await codeView(page, hub, 'Harbor vault');
  const vault = TOOLS[6];
  await openTool(page, vault);
  const tool = await shows(page, vault);
  // a page load locks the vault: its gate, and no item actions
  await expect(tool.getByRole('button', { name: 'Lock the vault' })).toHaveCount(0);
  await openVault(page);
  await expect(tool.locator('.tool-head').getByText('Unlocked', { exact: true })).toBeVisible();

  const name = `Payment gateway ${info.project.name} ${Date.now().toString(36)}`;
  await tool.locator('.tool-head').getByRole('button', { name: 'New item' }).click();
  const form = page.locator('#vault-item-form');
  await form.getByLabel('Name', { exact: true }).fill(name);
  await form.getByLabel('Password', { exact: true }).fill(`staging-${Date.now()}`);
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(form).toBeHidden();
  await expect(tool.getByText(name, { exact: true }).first()).toBeVisible();
  await expect.poll(async () => ((await hub.call('GET', '/api/vault')).items ?? []).length).toBeGreaterThan(0);
  await shot(page, info, 'desktop-vault-unlocked');

  // locked from the tab: the gate is back and the item is gone from the page
  await tool.getByRole('button', { name: 'Lock the vault' }).click();
  await expect(tool.locator('.tool-head').getByText('Locked', { exact: true })).toBeVisible();
  await expect(tool.getByText(name, { exact: true })).toHaveCount(0);
  await shot(page, info, 'desktop-vault-locked');
  await openVault(page);
  await expect(tool.getByText(name, { exact: true }).first()).toBeVisible();
});

test('Mission Map draws the board’s consoles without the board, and opens one as a tab', async ({ page, hub }, info) => {
  test.slow();
  const p = await hub.project('Lumen mission');
  const c = await hub.console(p.wid);
  await page.goto(`/p/${p.id}/code`);
  await expect(page.locator('.cv-bar')).toBeVisible({ timeout: 20_000 });
  const mission = TOOLS[9];
  await openTool(page, mission);
  const tool = await shows(page, mission);
  await expect(page.locator('.board-canvas')).toHaveCount(0);
  const row = tool.locator(`.mm-console[data-mm="c:${c.node}"]`);
  await expect(row).toBeVisible({ timeout: 20_000 });
  await row.click();
  await shot(page, info, 'desktop-mission-console');
  await tool.getByRole('button', { name: 'Open as tab' }).click();
  // the console itself, not a copy: its terminal in a tab of the editor
  await expect(page.locator(`.wb-group .wb-terminal[data-session="${c.id}"]`)).toBeVisible({ timeout: 20_000 });
  await expect(tabOf(page, 'Mission Map')).toHaveAttribute('aria-selected', 'false');
  // again from the map: the same tab
  await tabOf(page, 'Mission Map').click();
  await toolTab(page, 'mission').locator(`.mm-console[data-mm="c:${c.node}"]`).dblclick();
  await expect(page.locator(`.wb-group .wb-terminal[data-session="${c.id}"]`)).toHaveCount(1);
});

// a diagram run of the Diagrams tab, answered here so no CLI runs: the hub only stores what lands
const CHECKOUT = {
  schema: 'nb.diagram/v1',
  title: 'Checkout flow',
  direction: 'LR',
  nodes: [
    { id: 'cart', label: 'Cart page', shape: 'round' },
    { id: 'pay', label: 'Payment API', shape: 'box' },
    { id: 'done', label: 'Order saved', shape: 'box' },
  ],
  edges: [
    { from: 'cart', to: 'pay', label: 'POST /pay' },
    { from: 'pay', to: 'done' },
  ],
  groups: [],
};

test('a diagram drawn from its tab lands on the board after the tab is closed', async ({ page, hub }) => {
  const p = await codeView(page, hub, 'Harbor diagrams');
  const before = (await hub.call('GET', `/api/workspaces/${p.wid}/board`)).nodes.length as number;
  const id = `${p.id.slice(2)}x`;
  const job = { id: `jb_${id}`, kind: 'diagram', state: 'running', projectId: p.id };
  let done = false;
  await page.route('**/api/projects/*/diagrams', (route) =>
    route.request().method() === 'POST' ? route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ diagramId: `dg_${id}`, job }) }) : route.continue(),
  );
  await page.route(`**/api/jobs/${job.id}`, (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ ...job, state: done ? 'done' : 'running' }) }));
  await page.route(`**/api/diagrams/dg_${id}`, (route) =>
    route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ id: `dg_${id}`, projectId: p.id, subject: 'architecture', effort: 'balanced', style: 'ink', agent: 'claude', title: 'Checkout flow', createdAt: new Date().toISOString(), state: 'done', jobId: job.id, diagram: CHECKOUT }),
    }),
  );
  await openTool(page, TOOLS[3]);
  await toolTab(page, 'diagrams').getByRole('button', { name: 'Draw diagram' }).click();
  await expect(page.locator('.drawing-cards')).toBeVisible();
  // the tab goes before the run ends; its Drawing card stays, and the diagram still lands
  await tabOf(page, 'Diagrams').getByRole('button', { name: /^Close/ }).click();
  await expect(tabOf(page, 'Diagrams')).toHaveCount(0);
  await expect(page.locator('.drawing-cards')).toBeVisible();
  done = true;
  await expect.poll(async () => (await hub.call('GET', `/api/workspaces/${p.wid}/board`)).nodes.length, { timeout: 20_000 }).toBeGreaterThanOrEqual(before + 3);
  await expect(page.locator('.drawing-cards')).toHaveCount(0);
});

/** Every tool on a smaller screen, by finger: it opens, shows, and fits. */
async function everyToolFits(page: Page, hub: Hub, info: TestInfo, device: string) {
  test.slow();
  await codeView(page, hub, `Tools ${device}`);
  // a side bar lying over the editor (a tablet) is closed first, as a finger would
  for (const scrim of await page.locator('.wb-scrim').all()) if (await scrim.isVisible()) await scrim.click({ position: { x: 8, y: 8 } });
  for (const t of TOOLS) {
    await openTool(page, t);
    await shows(page, t);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await shot(page, info, `${device}-${t.kind}`);
  }
}

test.describe('on a phone', () => {
  test.use({ viewport: { width: 393, height: 873 }, deviceScaleFactor: 2.75, isMobile: true, hasTouch: true });
  test('every tool opens from ⋯ Tools and fits the screen', async ({ page, hub }, info) => {
    await everyToolFits(page, hub, info, 'phone');
  });
});

test.describe('on a tablet held upright', () => {
  test.use({ viewport: { width: 800, height: 1280 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  test('every tool opens from the activity bar and fits the screen', async ({ page, hub }, info) => {
    await everyToolFits(page, hub, info, 'tablet');
  });
});
