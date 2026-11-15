import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import { OUT } from './playwright.config';
import { assertPrivate, call, consoles, docsBoard, openShop, projects, releaseBoard, seedVault, shopBoard } from './demo';

// The README's screenshots, from the demo hub, all in the dark theme, one feature each: the board
// and its agents' states, an order and its report, the Mission Map, a project in parts on three
// machines, review, Local History and git in the code card, the web preview, layouts, memory, the
// vault, API providers, a sleeping agent, a phone, uptime and its public page.
//
// The board and the memory galaxy are taken at 1600 × 900; the rest at 1280 × 720 and a device
// scale of 1.25, so that panels and dialogs come out at the same 1600 px, larger and readable.

const DIR = join(OUT, 'shots');

test.use({ viewport: { width: 1600, height: 900 }, colorScheme: 'dark', actionTimeout: 20_000 });

async function settle(page: Page, ms = 900) {
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(ms);
}

async function shot(page: Page, name: string) {
  await assertPrivate(page);
  await page.screenshot({ path: join(DIR, `${name}.png`) });
}

/** No focus ring on a control a dialog focused by itself. */
const blur = (page: Page) => page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());

/** Scrolls whatever scrolls inside a locator back to its top. */
const toTop = (page: Page, sel: string) =>
  page.evaluate((sel) => {
    for (const el of document.querySelectorAll<HTMLElement>(`${sel}, ${sel} *`)) if (el.scrollHeight > el.clientHeight) el.scrollTop = 0;
  }, sel);

const session = (request: APIRequestContext, id: string) => call(request, 'GET', `/api/sessions/${id}`);

test('screenshots', async ({ page, request, browser }) => {
  test.setTimeout(900_000);
  mkdirSync(DIR, { recursive: true });
  const shop = projects()['acme-shop'];
  const docs = projects()['docs-site'];
  await shopBoard(request, shop);
  const release = await releaseBoard(request, shop);
  const writer = await docsBoard(request, docs);
  const nodes = await consoles(request, shop.wid);
  const tile = (p: Page, id: string) => p.locator(`.react-flow__node[data-id="${id}"]`);
  const sessions: { id: string; agent: string; handle: string }[] = await call(request, 'GET', '/api/sessions');
  const of = (agent: string) => sessions.find((s) => s.agent === agent)!;

  // ---- the board, at 1600 × 900: agents working, waiting for an answer, idle
  await page.goto(`/p/${shop.id}/w/${shop.wid}`);
  await openShop(page);
  for (const id of Object.values(nodes)) await expect(tile(page, id)).toContainText('demo agent', { timeout: 15_000 });
  await expect(tile(page, nodes.builder)).toContainText(/working/i, { timeout: 15_000 });
  await expect(tile(page, nodes.reviewer)).toContainText(/needs you/i, { timeout: 15_000 });
  await page.getByRole('button', { name: 'Fit to content' }).click();
  await settle(page, 2500);
  await shot(page, 'board');

  // the rest at 125 %
  const big = await browser.newContext({
    storageState: (await page.context().storageState()) as never,
    viewport: { width: 1280, height: 720 },
    deviceScaleFactor: 1.25,
    colorScheme: 'dark',
  });
  big.setDefaultTimeout(20_000);
  const p = await big.newPage();

  // ---- Mission Map of the release board: the command bar and a lead card direct two groups
  await p.goto(`/p/${shop.id}/w/${release}`);
  await expect(p.locator('.react-flow__node').first()).toBeVisible({ timeout: 15_000 });
  await settle(p, 1500);
  await p.keyboard.press('x');
  const map = p.getByRole('dialog', { name: 'Mission Map' });
  await expect(map).toBeVisible();
  await map.getByText(`@${of('qa').handle}`, { exact: true }).first().click();
  await settle(p, 1200);
  await shot(p, 'mission');
  await p.keyboard.press('Escape');

  // ---- the storefront in its web tile, on its own
  await p.goto(`/p/${shop.id}/w/${shop.wid}?card=n_demoweb001`);
  await expect(p.frameLocator('.react-flow__node.is-focused iframe').first().getByText('Order summary')).toBeVisible({ timeout: 20_000 });
  await settle(p, 1500);
  await shot(p, 'preview');

  // ---- the vault, with four made-up logins
  await p.goto(`/p/${shop.id}/w/${shop.wid}`);
  await p.getByRole('button', { name: /^Credentials/ }).first().click();
  await seedVault(p);
  await p.getByText('Payments sandbox').first().click();
  await settle(p);
  await shot(p, 'vault');
  await p.keyboard.press('Escape');

  // ---- the layout library
  await p.getByRole('button', { name: 'Layouts' }).first().click();
  const layouts = p.getByRole('dialog', { name: 'Layouts' });
  await layouts.getByRole('option', { name: /Front & back/ }).click();
  await layouts.getByRole('button', { name: /^More consoles in / }).first().click();
  await settle(p);
  await shot(p, 'layouts');
  await p.keyboard.press('Escape');

  // ---- an API provider: the stand-in on loopback answers with its models
  await p.goto('/settings/providers');
  await p.getByRole('button', { name: 'Edit Team router' }).click();
  const provider = p.getByRole('dialog');
  await provider.getByRole('button', { name: 'Test the connection' }).click();
  await expect(provider.getByText('It answers, with 3 models.')).toBeVisible({ timeout: 15_000 });
  await blur(p);
  await toTop(p, '[role="dialog"]');
  await settle(p, 400);
  await shot(p, 'providers');
  await p.keyboard.press('Escape');

  // ---- acme-shop in three parts: the storefront here, its API and worker on two servers
  await p.goto('/');
  await p.getByRole('button', { name: 'Actions for acme-shop' }).click();
  await p.getByRole('menuitem', { name: 'Parts…' }).click();
  const parts = p.getByRole('dialog', { name: 'Parts of acme-shop' });
  await expect(parts.getByRole('list', { name: 'Parts of acme-shop' }).getByRole('listitem')).toHaveCount(3);
  await blur(p);
  await settle(p, 600);
  await shot(p, 'parts');
  await p.keyboard.press('Escape');

  // ---- a sleeping agent: its process ended to free memory, its conversation kept
  await p.goto(`/p/${docs.id}/w/${docs.wid}`);
  const sleeper = p.locator(`[data-session="${writer}"]`);
  await expect(sleeper).toContainText('demo agent', { timeout: 15_000 });
  await expect.poll(async () => (await session(request, writer)).agentState, { timeout: 20_000 }).toBe('ready');
  await sleeper.getByRole('button', { name: /^Actions for @/ }).click();
  await p.getByRole('menuitem', { name: 'Sleep now' }).click();
  await expect(sleeper.getByLabel('Status: Asleep')).toBeVisible();
  await p.mouse.click(960, 430); // the canvas, beside the tile: nothing selected
  await settle(p, 1000);
  await shot(p, 'sleep');

  // ---- memory, as a galaxy, at 1600 × 900
  await page.getByRole('link', { name: 'Memory' }).first().click();
  await settle(page, 3500);
  await shot(page, 'memory');

  // ---- a phone: the Cards list, and a console with its key bar
  const phone = await browser.newContext({
    storageState: (await page.context().storageState()) as never,
    viewport: { width: 393, height: 873 },
    deviceScaleFactor: 2,
    isMobile: true,
    hasTouch: true,
    colorScheme: 'dark',
  });
  phone.setDefaultTimeout(20_000);
  const mobile = await phone.newPage();
  await mobile.goto(`/p/${shop.id}/w/${shop.wid}`);
  await expect(mobile.getByText(`@${of('reviewer').handle}`).first()).toBeVisible({ timeout: 15_000 });
  await settle(mobile, 2500);
  await shot(mobile, 'phone-board');
  await mobile.goto(`/p/${shop.id}/w/${shop.wid}?card=${nodes.reviewer}`);
  await expect(mobile.locator('.react-flow__node.is-focused .xterm')).toBeVisible({ timeout: 15_000 });
  await settle(mobile, 2000);
  await shot(mobile, 'phone-console');
  await phone.close();

  // ---- an order and its report. The builder and QA finish their own work first, and the
  // reviewer gets its answer, so that only the tester is at work when files change.
  await p.goto(`/p/${shop.id}/w/${shop.wid}`);
  const reviewer = tile(p, nodes.reviewer);
  await expect(reviewer).toContainText(/needs you/i, { timeout: 15_000 });
  await reviewer.locator('.xterm-screen').click();
  await p.keyboard.press('1');
  await expect(reviewer).toContainText('Applied.');
  for (const agent of ['builder', 'qa']) {
    await expect.poll(async () => (await session(request, of(agent).id)).agentState, { timeout: 150_000, intervals: [2000] }).toBe('ready');
  }
  await p.getByRole('button', { name: 'Fit to content' }).click();
  const bar = p.getByRole('combobox', { name: 'Order for your agents' });
  await bar.click();
  await p.keyboard.type(`@${of('tester').handle.slice(0, 2)}`, { delay: 40 });
  await expect(p.getByRole('listbox', { name: 'Direct to' })).toBeVisible();
  await p.keyboard.press('Tab');
  await p.keyboard.type('add coupon codes to checkout, with a test for an expired code', { delay: 10 });
  await p.keyboard.press('Enter');
  const thread = p.getByRole('region', { name: 'Thread' });
  await expect(thread.getByText(`Sent to @${of('tester').handle}`, { exact: false }).first()).toBeVisible();
  await expect(thread.getByRole('button', { name: /^See output/ })).toHaveCount(1, { timeout: 60_000 });
  await blur(p);
  await settle(p, 1500);
  await shot(p, 'command');
  await p.getByRole('button', { name: 'Close the thread' }).first().click();

  // ---- the code card, on its own: the tester's change in Review, Local History, git
  await call(request, 'POST', `/api/workspaces/${shop.wid}/board/ops`, {
    clientId: 'c_demo',
    baseRev: 0,
    ops: [{ op: 'put', node: { id: 'n_demoeditor', kind: 'editor', x: 0, y: 1000, w: 1300, h: 800, data: {} } }],
  });
  await p.goto(`/p/${shop.id}/w/${shop.wid}?card=n_demoeditor`);
  await p.getByRole('button', { name: 'Review', exact: true }).click({ timeout: 20_000 });
  const side = p.locator('.wb-side');
  await expect(side.getByText(`@${of('tester').handle}`, { exact: false }).first()).toBeVisible({ timeout: 15_000 });
  await side.getByText('checkout.ts', { exact: true }).first().click();
  await expect(p.getByRole('button', { name: 'Keep file' })).toBeVisible({ timeout: 15_000 });
  await settle(p, 2000);
  await shot(p, 'review');

  await p.getByRole('button', { name: 'Explorer', exact: true }).click();
  await side.getByRole('treeitem', { name: 'src' }).click();
  await side.getByRole('treeitem', { name: 'checkout.ts' }).dblclick();
  await p.locator('.wb-tab.is-active').click({ button: 'right' });
  await p.getByRole('menuitem', { name: 'Local History' }).click();
  await expect(p.getByText('Restore this version')).toBeVisible({ timeout: 15_000 });
  await settle(p, 2000);
  await shot(p, 'history');

  await p.getByRole('button', { name: 'Source Control', exact: true }).click();
  const message = side.locator('textarea').first();
  await expect(message).toBeVisible({ timeout: 15_000 });
  await message.fill('feat: coupon codes at checkout');
  await side.getByText('coupons.ts', { exact: true }).first().click();
  await blur(p);
  await settle(p, 2000);
  await shot(p, 'git');

  // ---- uptime, once the monitors have a few checks behind them, and its public page
  await call(request, 'POST', `/api/workspaces/${shop.wid}/board/ops`, {
    clientId: 'c_demo',
    baseRev: 0,
    ops: [{ op: 'put', node: { id: 'n_demouptime', kind: 'uptime', x: 0, y: 1900, w: 1000, h: 640, data: { title: 'Production' } } }],
  });
  await p.goto(`/p/${shop.id}/w/${shop.wid}?card=n_demouptime`);
  const card = p.locator('.react-flow__node.is-focused');
  await expect(card).toBeVisible();
  await card.getByRole('radio', { name: '1 h', exact: true }).or(card.getByRole('button', { name: '1 h', exact: true })).first().click({ timeout: 10_000 });
  await settle(p, 2500);
  await shot(p, 'uptime');
  const pub = await call(request, 'PUT', `/api/projects/${shop.id}/uptime/public`, { enabled: true, title: 'Acme Shop status' });
  await p.goto(`/status/${pub.token}`);
  await settle(p, 1000);
  await shot(p, 'status-page');
  await big.close();
});
