import type { Locator, Page } from '@playwright/test';
import { expect, test } from './hub';

// Uptime beyond the basics: monitors under groups and filtered by tag, a maintenance window that
// stops checks and shows on the monitors, and the form for the newer kinds of check. Run on
// phones and tablets too: the dialogs must fit the screen.

async function fits(page: Page, dialog: Locator, what: string) {
  const b = (await dialog.boundingBox())!;
  const vw = page.viewportSize()!.width;
  expect(b.x, `${what}: left edge`).toBeGreaterThanOrEqual(-1);
  expect(b.x + b.width, `${what}: right edge`).toBeLessThanOrEqual(vw + 1);
  const wide = await dialog.evaluate((el) => [...el.querySelectorAll<HTMLElement>('*')].filter((e) => e.scrollWidth > e.clientWidth + 1 && getComputedStyle(e).overflowX === 'visible' && e.clientWidth > 0).length);
  expect(wide, `${what}: content wider than its box`).toBe(0);
}

test('groups, tags and a maintenance window on the uptime card', async ({ page, hub }, info) => {
  const touch = !!info.project.use.hasTouch;
  const p = await hub.project('Uptime more');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_upt0000001', kind: 'uptime', x: 0, y: 0, w: 1000, h: 640, data: {} } }]);
  const mk = (body: Record<string, unknown>) => hub.call('POST', `/api/projects/${p.id}/uptime`, { intervalSec: 20, ...body });
  const shop = await mk({ name: 'Shop cron', type: 'push', group: 'Shop', tags: ['prod'] });
  await mk({ name: 'API health', type: 'tcp', host: '127.0.0.1', port: 9, group: 'API', tags: ['staging'] });
  await mk({ name: 'Loose', type: 'push' });

  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_upt0000001`);
  const card = page.locator('.react-flow__node[data-id="n_upt0000001"]');
  const list = card.getByRole('list', { name: 'Monitors' });
  await expect(list.locator('.up-group-head')).toHaveText(['API', 'Shop', 'Other']);

  // a tag shows only its monitors, and again all of them
  const prod = card.getByRole('button', { name: 'prod', exact: true });
  await (touch ? prod.tap() : prod.click());
  await expect(list.locator('.up-row-name')).toHaveText(['Shop cron']);
  await (touch ? prod.tap() : prod.click());
  await expect(list.locator('.up-row-name')).toHaveCount(3);

  // a maintenance window for the shop
  await card.getByRole('button', { name: /^Actions for/ }).click();
  await page.getByRole('menuitem', { name: /^Maintenance…/ }).click();
  const dialog = page.getByRole('dialog', { name: 'Maintenance' });
  await expect(dialog).toBeVisible();
  await fits(page, dialog, 'maintenance');
  await dialog.getByRole('button', { name: 'Add window' }).click();
  await dialog.getByRole('textbox', { name: 'Title' }).fill('Deploy');
  await dialog.getByRole('checkbox', { name: 'All monitors of this project' }).uncheck();
  await dialog.getByRole('checkbox', { name: 'Shop cron' }).check();
  await dialog.getByRole('checkbox', { name: 'API health' }).uncheck();
  await fits(page, dialog, 'maintenance window');
  await dialog.getByRole('button', { name: 'Save window' }).click();
  await expect(dialog.locator('.up-maint-row')).toContainText('On now · Shop cron');
  await page.keyboard.press('Escape');

  // the hub stops checking it and says so; the others go on
  await expect.poll(async () => (await hub.call('GET', `/api/projects/${p.id}/uptime`)).items.find((m: { id: string }) => m.id === shop.id).status, { timeout: 15_000 }).toBe('maintenance');
  await list.getByRole('button', { name: /Shop cron/ }).click();
  await expect(card.locator('.up-now')).toContainText('Maintenance');
  await expect(card.locator('.up-now')).toContainText('Deploy');

  // turned off: back to waiting for its push
  await card.getByRole('button', { name: /^Actions for/ }).click();
  await page.getByRole('menuitem', { name: /^Maintenance…/ }).click();
  await dialog.getByRole('button', { name: 'Turn off' }).click();
  await expect(dialog.getByRole('button', { name: 'Turn on' })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect.poll(async () => (await hub.call('GET', `/api/projects/${p.id}/uptime`)).items.find((m: { id: string }) => m.id === shop.id).status, { timeout: 15_000 }).not.toBe('maintenance');
});

test('the monitor form asks what each newer check needs', async ({ page, hub }) => {
  const p = await hub.project('Uptime form');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_upt0000002', kind: 'uptime', x: 0, y: 0, w: 1000, h: 640, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_upt0000002`);
  const card = page.locator('.react-flow__node[data-id="n_upt0000002"]');
  await card.getByRole('button', { name: 'Add monitor' }).first().click();
  const form = page.getByRole('dialog', { name: 'Add monitor' });
  const kind = form.getByRole('combobox', { name: 'Check', exact: true });

  await kind.selectOption('docker');
  await expect(form.getByRole('textbox', { name: 'Container' })).toBeVisible();
  await fits(page, form, 'docker form');
  await kind.selectOption('steam');
  await expect(form.getByRole('textbox', { name: 'Query port' })).toBeVisible();
  await kind.selectOption('http');
  await form.getByRole('textbox', { name: 'JSON value' }).fill('data.status');
  await expect(form.getByRole('combobox', { name: 'Is' })).toBeVisible();
  await fits(page, form, 'http form');

  // the hub says what is wrong, in the form
  await kind.selectOption('websocket');
  await form.getByRole('textbox', { name: 'URL' }).fill('https://example.com/socket');
  await form.getByRole('button', { name: 'Add monitor' }).click();
  await expect(form.getByRole('alert')).toContainText('ws:// or wss://');
  await form.getByRole('textbox', { name: 'URL' }).fill('ws://127.0.0.1:9/socket');
  await form.getByRole('textbox', { name: 'Tags' }).fill('edge, prod');
  await form.getByRole('button', { name: 'Add monitor' }).click();
  await expect(form).toHaveCount(0);
  const items = (await hub.call('GET', `/api/projects/${p.id}/uptime`)).items;
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ type: 'websocket', url: 'ws://127.0.0.1:9/socket', tags: ['edge', 'prod'] });
});
