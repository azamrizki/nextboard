import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { expect, test } from './hub';

// The Code view's Browser tab shows a page from the preview's own port. When this browser cannot
// reach that port (NextBoard behind a proxy or a tunnel that forwards only its own port), the tab
// says so instead of staying white, with ways out.

let site: Server;
let port = 0;
test.beforeAll(async () => {
  site = createServer((_req, res) => {
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end('<!doctype html><title>Shop</title><h1>Shop</h1>');
  });
  await new Promise<void>((done) => site.listen(0, '127.0.0.1', done));
  port = (site.address() as AddressInfo).port;
});
test.afterAll(() => site?.close());

test('a preview this browser cannot reach says so instead of a white page', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  test.setTimeout(60_000);
  const p = await hub.project('Unreached');
  // the hub's preview ports (12320–12339) are out of reach, as through a tunnel
  await page.route(/^https?:\/\/[^/]+:123[23]\d\//, (r) => r.abort('connectionrefused'));
  await page.goto(`/p/${p.id}/code`);
  await expect(page.locator('.cv-bar')).toBeVisible({ timeout: 20_000 });
  await page.getByRole('navigation', { name: 'Views' }).getByRole('button', { name: 'Browser', exact: true }).click();
  const addr = page.getByRole('textbox', { name: 'Address' });
  await addr.fill(`http://localhost:${port}/`);
  await addr.press('Enter');
  const hint = page.locator('.tool-browser .web-hint');
  await expect(hint).toContainText('Blank page? This browser may not reach port', { timeout: 20_000 });
  await expect(hint).toContainText('forwards only its own port');
  await expect(hint.getByRole('button', { name: 'Open it in a new tab once' })).toBeVisible();
  await hint.getByRole('button', { name: 'Dismiss' }).click();
  await expect(hint).toHaveCount(0);

  // reachable again: the page answers and no hint shows
  await page.unroute(/^https?:\/\/[^/]+:123[23]\d\//);
  await page.locator('.tool-browser').getByRole('button', { name: /^Reload/ }).first().click();
  await expect(page.locator('.tool-browser iframe').contentFrame().getByRole('heading', { name: 'Shop' })).toBeVisible({ timeout: 20_000 });
  await page.waitForTimeout(7000);
  await expect(hint).toHaveCount(0);
});
