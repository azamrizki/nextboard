import { cpSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test, type Page } from '@playwright/test';
import { OUT, ROOT } from './playwright.config';
import { call, consoles, openShop, projects, seedVault, shopBoard } from './demo';

// The README's screenshots, from the demo hub: the board in both themes, the code card with the
// tester's change, the layout library, memory, the vault, uptime and its public page, a phone.

const DIR = join(OUT, 'shots');

test.use({ viewport: { width: 1600, height: 900 }, colorScheme: 'light' });

async function settle(page: Page, ms = 900) {
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(ms);
}

test('screenshots', async ({ page, request, browser }) => {
  test.setTimeout(600_000);
  mkdirSync(DIR, { recursive: true });
  const shot = (name: string, p: Page = page) => p.screenshot({ path: join(DIR, `${name}.png`) });
  const shop = projects()['acme-shop'];
  await shopBoard(request, shop);
  const nodes = await consoles(request, shop.wid);
  const tile = (id: string) => page.locator(`.react-flow__node[data-id="${id}"]`);
  // the tester's change, as its work in the video leaves it
  cpSync(join(ROOT, '.local', 'share', 'demo-patch'), shop.path, { recursive: true });

  await page.goto(`/p/${shop.id}/w/${shop.wid}`);
  await openShop(page);
  for (const id of Object.values(nodes)) await expect(tile(id)).toContainText('demo agent', { timeout: 15_000 });
  await page.getByRole('button', { name: /^Credentials/ }).first().click();
  await seedVault(page);
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: 'Fit to content' }).click();
  await settle(page, 2500);
  await shot('board-light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await settle(page, 1500);
  await shot('board-dark');
  await page.emulateMedia({ colorScheme: 'light' });

  // a console close up
  await tile(nodes.reviewer).locator('.nb-node-header').dblclick({ position: { x: 200, y: 10 } });
  await settle(page, 1200);
  await shot('console');

  // the code card on the change
  await page.getByRole('button', { name: 'Code editor' }).click();
  await page.getByRole('button', { name: 'Source Control', exact: true }).click({ timeout: 20_000 });
  await page.locator('.wb-side').getByText('checkout.ts', { exact: true }).first().click({ timeout: 15_000 });
  await settle(page, 2500);
  await shot('code');
  const board = await call(request, 'GET', `/api/workspaces/${shop.wid}/board`);
  const editors = board.nodes.filter((n: { kind: string }) => n.kind === 'editor').map((n: { id: string }) => ({ op: 'del', id: n.id }));
  await call(request, 'POST', `/api/workspaces/${shop.wid}/board/ops`, { clientId: 'c_demo', baseRev: 0, ops: editors });

  // the layout library
  await page.getByRole('button', { name: 'Fit to content' }).click();
  await page.getByRole('button', { name: 'Layouts' }).first().click();
  const layouts = page.getByRole('dialog', { name: 'Layouts' });
  await layouts.getByRole('option', { name: /Front & back/ }).click();
  await layouts.getByRole('button', { name: /^More consoles in / }).first().click();
  await settle(page);
  await shot('layouts');
  await page.keyboard.press('Escape');

  // the vault, in dark
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.getByRole('button', { name: /^Credentials/ }).first().click();
  await page.getByText('Payments sandbox').first().click();
  await settle(page);
  await shot('vault');
  await page.keyboard.press('Escape');

  // memory
  await page.getByRole('link', { name: 'Memory' }).first().click();
  await settle(page, 3500);
  await shot('memory');
  await page.emulateMedia({ colorScheme: 'light' });

  // the projects
  await page.getByRole('link', { name: 'Projects' }).first().click();
  await settle(page, 1500);
  await shot('projects');

  // a phone: the board, and a console on its own
  const phone = await browser.newContext({
    storageState: (await page.context().storageState()) as never,
    viewport: { width: 393, height: 873 },
    deviceScaleFactor: 2,
    isMobile: true,
    hasTouch: true,
    colorScheme: 'dark',
  });
  const mobile = await phone.newPage();
  await mobile.goto(`/p/${shop.id}/w/${shop.wid}`);
  await settle(mobile, 3000);
  await shot('phone-board', mobile);
  await mobile.goto(`/p/${shop.id}/w/${shop.wid}?card=${nodes.reviewer}`);
  await expect(mobile.locator('.react-flow__node.is-focused .xterm')).toBeVisible({ timeout: 15_000 });
  await settle(mobile, 2000);
  await shot('phone-console', mobile);
  await phone.close();

  // uptime, once the monitors have a few checks behind them, and its public page
  await page.waitForTimeout(70_000);
  await call(request, 'POST', `/api/workspaces/${shop.wid}/board/ops`, {
    clientId: 'c_demo',
    baseRev: 0,
    ops: [{ op: 'put', node: { id: 'n_demouptime', kind: 'uptime', x: 0, y: 1000, w: 1000, h: 640, data: { title: 'Production' } } }],
  });
  await page.goto(`/p/${shop.id}/w/${shop.wid}?card=n_demouptime`);
  const card = page.locator('.react-flow__node.is-focused');
  await expect(card).toBeVisible();
  await card.getByRole('radio', { name: '1 h', exact: true }).or(card.getByRole('button', { name: '1 h', exact: true })).first().click({ timeout: 10_000 });
  await settle(page, 2500);
  await shot('uptime');
  const pub = await call(request, 'PUT', `/api/projects/${shop.id}/uptime/public`, { enabled: true, title: 'Acme Shop status' });
  await page.goto(`/status/${pub.token}`);
  await settle(page, 800);
  await shot('status-page');
});
