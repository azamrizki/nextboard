import { mkdtempSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';
import { twoParts } from './two-parts';

// Parts changed while a board or the parts dialog is open (v0.9.1): from this tab, another tab
// or another device, the open cards show the change at once, without a reload.

test('the editor cards follow a part renamed, added and removed', async ({ page, hub }) => {
  test.slow();
  const p = await twoParts(hub, 'Live');
  await hub.ops(p.wid, [
    { op: 'put', node: { id: 'n_rootcard01', kind: 'editor', x: 0, y: 0, w: 900, h: 600, data: {} } },
    { op: 'put', node: { id: 'n_partcard01', kind: 'editor', x: 950, y: 0, w: 900, h: 600, data: { part: p.front.id } } },
  ]);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const root = page.getByRole('group', { name: 'Code editor · All parts' });
  const top = root.locator('.wb-side [role="treeitem"][aria-level="1"]');
  await expect(top).toHaveCount(2, { timeout: 20_000 });
  await expect(top.nth(1)).toHaveText(/^frontend/);
  await expect(page.getByRole('group', { name: 'Code editor · frontend' }).locator('.wb-view-head h2')).toHaveText(/· frontend$/);

  // renamed elsewhere: the tree, the card's name and its explorer all say the new name
  await hub.call('PATCH', `/api/projects/${p.id}/parts/${p.front.id}`, { name: 'web' });
  await expect(top.nth(1)).toHaveText(/^web/);
  const part = page.getByRole('group', { name: 'Code editor · web' });
  await expect(part.locator('.wb-view-head h2')).toHaveText(/· web$/);

  // a part added shows up as a folder of the root card, and goes again when removed
  const dir = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'docs-'));
  writeFileSync(join(dir, 'README.md'), '# docs\n');
  const made = await hub.call('POST', `/api/projects/${p.id}/parts`, { name: 'docs', machineId: 'local', path: dir });
  await expect(top).toHaveCount(3);
  await expect(top.nth(2)).toHaveText(/^docs/);
  await expect(root.locator('.wb-side').getByRole('treeitem', { name: 'README.md' })).toBeVisible();
  await hub.call('DELETE', `/api/projects/${p.id}/parts/${made.parts.find((x: any) => x.name === 'docs').id}`);
  await expect(top).toHaveCount(2);
  await expect(top.nth(1)).toHaveText(/^web/);
});

test('an editor of a one-part project becomes the root card when a part is added, and back', async ({ page, hub }) => {
  const p = await hub.project('Grow');
  writeFileSync(join(p.path, 'main.go'), 'package main\n');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_growcard1', kind: 'editor', x: 0, y: 0, w: 900, h: 600, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await expect(page.getByRole('group', { name: 'Code editor' })).toBeVisible({ timeout: 20_000 });
  await expect(page.getByRole('group', { name: 'Code editor' }).getByRole('treeitem', { name: 'main.go' })).toBeVisible();

  const dir = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'api-'));
  writeFileSync(join(dir, 'api.ts'), 'export {};\n');
  const made = await hub.call('POST', `/api/projects/${p.id}/parts`, { name: 'api', machineId: 'local', path: dir });
  const root = page.getByRole('group', { name: 'Code editor · All parts' });
  const top = root.locator('.wb-side [role="treeitem"][aria-level="1"]');
  await expect(top).toHaveCount(2);
  await expect(top.nth(1)).toHaveText(/^api/);
  await expect(root.locator('.wb-side').getByRole('treeitem', { name: 'api.ts' })).toBeVisible();

  await hub.call('DELETE', `/api/projects/${p.id}/parts/${made.parts.find((x: any) => x.name === 'api').id}`);
  const one = page.getByRole('group', { name: 'Code editor' });
  await expect(one.getByRole('treeitem', { name: 'main.go' })).toBeVisible();
  await expect(one.locator('.wb-side [role="treeitem"][aria-level="1"]', { hasText: /^api/ })).toHaveCount(0);
});

test('the parts dialog shows a rename from elsewhere, and leaving the field keeps it', async ({ page, hub }) => {
  const p = await twoParts(hub, 'Dialog');
  await page.goto('/');
  const card = page.locator('.project-card', { hasText: p.name });
  await card.getByRole('button', { name: /Show the parts/ }).click();
  await page.getByRole('button', { name: 'Edit parts…' }).click();
  const dialog = page.getByRole('dialog', { name: `Parts of ${p.name}` });
  const field = dialog.getByRole('textbox', { name: 'Name of part frontend' });
  await expect(field).toHaveValue('frontend');

  // focused but not typed in: a rename from another tab still lands in the field
  await field.focus();
  await hub.call('PATCH', `/api/projects/${p.id}/parts/${p.front.id}`, { name: 'shop' });
  const renamed = dialog.getByRole('textbox', { name: 'Name of part shop' });
  await expect(renamed).toHaveValue('shop');
  await renamed.blur();
  await page.waitForTimeout(500);
  const now = (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id);
  expect(now.parts.map((x: any) => x.name)).toEqual(['admin', 'shop']);

  // typed in: the typed name wins
  await renamed.fill('storefront');
  await renamed.press('Enter');
  await expect.poll(async () => (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).parts[1].name).toBe('storefront');
  await expect(dialog.getByRole('textbox', { name: 'Name of part storefront' })).toHaveValue('storefront');
});
