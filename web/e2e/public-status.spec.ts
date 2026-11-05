import { expect, test } from './hub';

// What must never reach a stranger, planted in the fields it could come from.
const SECRETS = ['CANARY-HOST', 'CANARY-PATH', 'CANARY-TOKEN', 'CANARY-HEADER', 'CANARY-KEYWORD', 'CANARY-NAME', 'CANARY-HIDDEN', 'CANARY-PROJECT', '127.0.0.1', 'nextboard', 'NextBoard', 'devbox'];

test('a stranger sees the public page, and nothing else', async ({ page, hub, browser, baseURL }) => {
  const p = await hub.project('CANARY-PROJECT');
  await hub.call('POST', `/api/projects/${p.id}/uptime`, {
    name: 'CANARY-NAME',
    type: 'http',
    url: 'http://127.0.0.1:9/CANARY-PATH?t=CANARY-TOKEN',
    keyword: 'CANARY-KEYWORD',
    headers: [{ name: 'X-Key', value: 'CANARY-HEADER' }],
    public: true,
    publicName: 'Website',
  });
  const job = await hub.call('POST', `/api/projects/${p.id}/uptime`, { name: 'CANARY-NAME-2', type: 'push', public: true, publicName: 'Nightly job' });
  await hub.call('GET', `/api/push/${job.pushToken}?status=up&msg=CANARY-NAME-3`);
  await hub.call('POST', `/api/projects/${p.id}/uptime`, { name: 'CANARY-HIDDEN', type: 'http', url: 'http://CANARY-HOST/' });
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_upt0000001', kind: 'uptime', x: 40, y: 40, w: 720, h: 520, data: {} } }]);

  // The owner turns it on from the card.
  await page.goto(`/p/${p.id}`);
  await page.getByRole('button', { name: /actions/i }).first().click();
  await page.getByRole('menuitem', { name: 'Public status page…' }).click();
  const dialog = page.locator('.modal-layer');
  await expect(dialog).toContainText('2 monitors are on the page');
  await dialog.getByLabel('Turn on the public page').check();
  await dialog.getByLabel('Title').fill('Acme services');
  await dialog.getByRole('button', { name: 'Save' }).click();
  const address = dialog.getByLabel('Address', { exact: true });
  await expect(address).toHaveValue(/\/status\//);
  const link = await address.inputValue();

  // Someone with no cookie, with only the link.
  const stranger = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const tab = await stranger.newPage();
  const scripts: string[] = [];
  tab.on('request', (r) => {
    if (r.url() !== link) scripts.push(r.url());
  });
  const response = await tab.goto(link);
  expect(response!.status()).toBe(200);
  await expect(tab.locator('h1')).toHaveText('Acme services');
  await expect(tab.locator('.card', { hasText: 'Website' })).toBeVisible();
  await expect(tab.locator('.card', { hasText: 'Nightly job' })).toContainText('Operational');
  await expect(tab.locator('.card', { hasText: 'Nightly job' })).toContainText('100.00% uptime');
  // The check does not fail on what it shows: an unreachable target is a state, not a leak.
  const html = await response!.text();
  for (const secret of SECRETS) expect(html, secret).not.toContain(secret);
  expect(await tab.title()).toBe('Acme services');
  // Nothing else is loaded, nothing runs, and it leaves nothing behind.
  expect(scripts).toEqual([]);
  expect(await tab.locator('script, link, img, iframe, form').count()).toBe(0);
  expect(await stranger.cookies()).toEqual([]);
  expect(response!.headers()['content-security-policy']).toContain("default-src 'none'");
  expect(response!.headers()['set-cookie']).toBeUndefined();

  // The app is not there for this stranger, and a wrong address says nothing.
  const wrong = await tab.goto(`${baseURL}/status/not-the-token`);
  expect(wrong!.status()).toBe(404);
  expect(await wrong!.text()).not.toContain('<html');

  await tab.goto(link);
  if (process.env.NEXTBOARD_E2E_SHOTS) await tab.screenshot({ path: `${process.env.NEXTBOARD_E2E_SHOTS}/public-${test.info().project.name}.png` });

  // A new address: the one given out stops working at once.
  await dialog.getByRole('button', { name: 'New address' }).click();
  await page.getByRole('dialog', { name: 'Make a new address?' }).getByRole('button', { name: 'New address' }).click();
  await expect(address).not.toHaveValue(link);
  expect((await tab.goto(link))!.status()).toBe(404);
  await stranger.close();
});

test('the public name is asked for, and may not be the address', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_upt0000001', kind: 'uptime', x: 40, y: 40, w: 720, h: 520, data: {} } }]);
  const leak = await page.request.post(`/api/projects/${p.id}/uptime`, {
    headers: { 'X-NextBoard': '1' },
    data: { name: 'x', type: 'http', url: 'http://db.internal/', public: true, publicName: 'db.internal' },
  });
  expect(leak.status()).toBe(400);
  const empty = await page.request.post(`/api/projects/${p.id}/uptime`, {
    headers: { 'X-NextBoard': '1' },
    data: { name: 'x', type: 'http', url: 'http://db.internal/', public: true },
  });
  expect(empty.status()).toBe(400);
});
