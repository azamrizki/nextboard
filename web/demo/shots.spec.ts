import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import { OUT } from './playwright.config';
import { assertPrivate, call, consoles, docsBoard, openShop, platformBoard, projects, releaseBoard, seedVault, shopBoard } from './demo';

// The README's screenshots, from the demo hub, all in the dark theme, one feature each: the board
// and its agents' states, the projects with their covers, select mode, the Archived view and the
// remove dialog, an order and its report, the Mission Map, a project in parts on three machines,
// where a console works (Root or a part), consoles at the root and in parts, the editor at the
// root, every part's git, review, Local History and git in the code card, the web preview,
// layouts placed in parts, memory, the vault, API providers, a sleeping agent, a phone, uptime and
// its public page.
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
  const platform = await platformBoard(request, shop);
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

  // ---- the projects, at 1600 × 900: covers and icons, and acme-shop's one row of parts
  await page.goto('/');
  const covers = page.locator('.project-cover img');
  await expect.poll(() => covers.count(), { timeout: 15_000 }).toBeGreaterThanOrEqual(6);
  await expect.poll(async () => (await covers.count()) === (await page.locator('.project-cover img.is-loaded').count()), { timeout: 15_000 }).toBe(true);
  await expect(page.locator('.partsline-sum').first()).toBeVisible();
  await settle(page, 1200);
  await shot(page, 'projects');

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

  // ---- where new consoles work: at the root, which reaches every part, or in one part
  await p.getByRole('button', { name: 'Quick actions' }).click();
  await p.getByRole('menu', { name: 'Quick actions' }).getByRole('menuitem', { name: /^Spawn \d/ }).click();
  const spawn = p.getByRole('dialog', { name: 'Spawn consoles' });
  await spawn.getByRole('radiogroup', { name: 'Place in' }).getByRole('radio', { name: 'Root' }).click();
  await spawn.getByRole('button', { name: 'Every slot runs Builder' }).click();
  await spawn.getByRole('button', { name: 'One console fewer' }).click();
  await blur(p);
  await settle(p, 600);
  await shot(p, 'place');
  await p.keyboard.press('Escape');
  await expect(spawn).toBeHidden();

  // ---- the layout library: each group in a part of its own
  await p.getByRole('button', { name: 'Quick actions' }).click();
  await p.getByRole('menu', { name: 'Quick actions' }).getByRole('menuitem', { name: 'Layouts' }).click();
  const layouts = p.getByRole('dialog', { name: 'Layouts' });
  await layouts.getByRole('option', { name: /Front & back/ }).click();
  const placeFront = layouts.getByLabel('Place Frontend in');
  if (await placeFront.count()) {
    await placeFront.selectOption({ label: 'Storefront' });
    await layouts.getByLabel('Place Backend in').selectOption({ label: 'API' });
  }
  await layouts.getByRole('button', { name: /^More consoles in / }).first().click();
  await blur(p);
  await settle(p);
  await shot(p, 'layouts');
  await p.keyboard.press('Escape');

  // ---- a console at the root beside consoles in the two server parts, at 1600 × 900 (three
  // tiles in a row, the minimap below them)
  await page.goto(`/p/${shop.id}/w/${platform}`);
  const roots = page.locator('.react-flow__node');
  await expect(roots.filter({ hasText: 'One plan for three parts' })).toHaveCount(1, { timeout: 20_000 });
  await expect(roots.filter({ hasText: 'Codes are checked on api-server' })).toHaveCount(1, { timeout: 20_000 });
  await expect(roots.filter({ hasText: 'Receipts print the coupon' })).toHaveCount(1, { timeout: 20_000 });
  await page.getByRole('button', { name: 'Fit to content' }).click();
  await settle(page, 1500);
  await shot(page, 'root-console');

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
  // by host, a project of several parts is under each machine it is on: the first is This PC's
  await p.getByRole('button', { name: 'Actions for acme-shop' }).first().click();
  await p.getByRole('menuitem', { name: 'Parts…' }).click();
  const parts = p.getByRole('dialog', { name: 'Parts of acme-shop' });
  await expect(parts.getByRole('list', { name: 'Parts of acme-shop' }).getByRole('listitem')).toHaveCount(3);
  await blur(p);
  await settle(p, 600);
  await shot(p, 'parts');
  await p.keyboard.press('Escape');

  // ---- select mode: two picked, then the remove dialog with Delete permanently chosen
  const selectBar = p.getByRole('toolbar', { name: /^Selected / });
  await p.getByRole('button', { name: 'Select', exact: true }).click();
  await p.getByRole('checkbox', { name: 'analytics', exact: true }).click();
  await p.getByRole('checkbox', { name: 'infra', exact: true }).click();
  await expect(selectBar).toContainText('2 selected');
  await selectBar.getByRole('button', { name: 'Archive' }).click();
  const remove = p.getByRole('dialog', { name: 'Remove 2 projects?' });
  await remove.getByRole('radio', { name: /^Delete permanently/ }).click();
  await expect(remove).toContainText('What is deleted');
  await blur(p);
  await settle(p, 600);
  await shot(p, 'archive');
  await remove.getByRole('button', { name: 'Cancel' }).click();
  await selectBar.getByRole('button', { name: 'Cancel' }).click();
  await expect(selectBar).toBeHidden();

  // ---- the Archived view, in select mode, with its bar
  await p.getByRole('radio', { name: /^Archived/ }).click();
  const archived = p.getByRole('list', { name: 'Archived projects' });
  await expect(archived.getByRole('article')).toHaveCount(3);
  await p.getByRole('button', { name: 'Select', exact: true }).click();
  for (const name of ['spring-sale', 'holiday-promo']) await archived.getByRole('checkbox', { name: `Select ${name}` }).click();
  await expect(selectBar).toContainText('2 selected');
  await blur(p);
  await settle(p, 600);
  await shot(p, 'select');
  await selectBar.getByRole('button', { name: 'Cancel' }).click();
  await p.getByRole('radio', { name: /^Projects/ }).click();

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

  // ---- the code card, on its own: the tester's change in Review, Local History, git. It is
  // placed in the storefront part (no data.part would be the Root card of all three parts);
  // without the servers the shop is that one part
  const shopParts: { id: string; name: string }[] = (await call(request, 'GET', '/api/projects')).find((x: { id: string }) => x.id === shop.id).parts;
  const storefront = (shopParts.find((pt) => pt.name === 'Storefront') ?? shopParts[0]).id;
  await call(request, 'POST', `/api/workspaces/${shop.wid}/board/ops`, {
    clientId: 'c_demo',
    baseRev: 0,
    ops: [{ op: 'put', node: { id: 'n_demoeditor', kind: 'editor', x: 0, y: 1000, w: 1300, h: 800, data: { part: storefront } } }],
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

  // ---- the editor at the root: every part a folder, one search across them, grouped by part
  await call(request, 'POST', `/api/workspaces/${shop.wid}/board/ops`, {
    clientId: 'c_demo',
    baseRev: 0,
    ops: [{ op: 'put', node: { id: 'n_demoroot01', kind: 'editor', x: 1400, y: 1000, w: 1300, h: 800, data: {} } }],
  });
  await p.goto(`/p/${shop.id}/w/${shop.wid}?card=n_demoroot01`);
  const rootCard = p.getByRole('group', { name: 'Code editor · All parts' });
  const rootSide = rootCard.locator('.wb-side');
  // the side bar opens on the view the last card used (Source Control): Search is one click
  await rootCard.getByRole('button', { name: 'Search', exact: true }).click({ timeout: 20_000 });
  await rootSide.getByRole('textbox', { name: 'Search', exact: true }).fill('coupon');
  const byPart = rootSide.locator('.wb-result-part');
  await expect(byPart).toHaveCount(shopParts.length, { timeout: 20_000 });
  // every file folded but the API's coupons.go, so that the three parts show at once
  const unfolded = rootSide.locator('.wb-result-head[aria-expanded="true"]').filter({ hasNotText: 'coupons.go' });
  while (await unfolded.count()) await unfolded.first().click();
  const apiPart = byPart.filter({ has: p.locator('.wb-part-head', { hasText: 'API' }) });
  if (await apiPart.count()) await apiPart.locator('.wb-result-file', { hasText: 'coupons.go' }).locator('.wb-hit').first().click();
  await expect(rootCard.locator('.wb-tab.is-active')).toBeVisible({ timeout: 15_000 });
  await rootSide.locator('.wb-results').evaluate((el) => (el.scrollTop = 0));
  await blur(p);
  await settle(p, 2500);
  await shot(p, 'editor-root');

  // ---- the board's Git panel, at 1600 × 900 (it is tall): every part's repository, the last
  // one (the worker's) open on its history graph, so that every part's row stays in view
  await call(request, 'POST', `/api/workspaces/${shop.wid}/board/ops`, { clientId: 'c_demo', baseRev: 0, ops: [{ op: 'del', id: 'n_demoroot01' }, { op: 'del', id: 'n_demoeditor' }] });
  await page.goto(`/p/${shop.id}/w/${shop.wid}`);
  await expect(page.locator('.react-flow__node').first()).toBeVisible({ timeout: 15_000 });
  await page.getByRole('button', { name: 'Fit to content' }).click();
  await page.getByRole('button', { name: /^Git \(/ }).click();
  const gitPanel = page.getByRole('complementary', { name: 'Git' });
  await expect(gitPanel.getByRole('region', { name: /^Part / }).first()).toBeVisible({ timeout: 15_000 });
  const workerGit = gitPanel.getByRole('region', { name: 'Part Worker' });
  if (await workerGit.count()) {
    await workerGit.getByRole('button', { name: /^Worker/ }).first().click();
    await workerGit.getByRole('button', { name: 'History', exact: true }).click();
    await expect(workerGit.locator('.wb-graph-row').first()).toBeVisible({ timeout: 15_000 });
  }
  await blur(page);
  await settle(page, 1500);
  await shot(page, 'git-parts');

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
