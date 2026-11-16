import type { Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';
import { twoParts, type TwoParts } from './two-parts';

// Part memories (docs/API-orchestration.md §12): a project of several parts has the memory of the
// whole project and one memory per part. The memory page and the board's Memory panel show both,
// Scan and Init sort what they found under the whole project and each part, and accepting puts
// every fact where it belongs. A project of one part looks as it always did.

const slug = (partId: string) => `part-${partId.replace(/^pt_/, '')}`;
const docOf = (hub: Hub, pid: string, name: string) => hub.call('GET', `/api/memory/docs/${name}?projectId=${pid}`);

/** A finished run, as the hub would hand it over: the browser never waits on a real CLI. */
async function finishedRun(page: Page, url: string, id: string, purpose: string, p: TwoParts, result: unknown) {
  const job = { id, kind: 'runner', purpose, projectId: p.id, state: 'done', progress: null, createdAt: new Date().toISOString(), startedAt: new Date().toISOString(), finishedAt: new Date().toISOString(), result };
  await page.route(url, (route) => route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify(job) }));
  await page.route(`**/api/jobs/${id}`, (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(job) }));
}

/** The board's Memory panel, from the right rail. */
async function openPanel(page: Page) {
  await page.getByRole('button', { name: 'Memory (M)', exact: true }).click();
  await expect(page.locator('.memory-panel')).toBeVisible();
}

const vault = (page: Page) => page.getByRole('navigation', { name: 'Memories' });
const partRow = (page: Page, name: string) => vault(page).getByRole('region', { name: 'Parts' }).locator('.mem-row-part', { hasText: name });

test('the whole project and each part have a memory, to read, edit and start', async ({ page, hub }) => {
  const p = await twoParts(hub, 'Shop');
  await hub.memory(p.id, '## Commands\n\n- make test runs every part\n');
  await hub.page(p.id, 'design', 'Design system', '- buttons are square\n');
  // what a client says a part page is called does not count: it is the part's name
  await hub.page(p.id, slug(p.admin.id), 'Some other title', '## Gotchas\n\n- admin needs php 8.3\n');
  const got = await docOf(hub, p.id, slug(p.admin.id));
  expect(got.part).toBe(p.admin.id);
  expect(got.title).toBe('admin');

  await page.goto(`/memory/${p.id}`);
  await expect(vault(page).locator('.mem-group', { hasText: 'Whole project' })).toBeVisible();
  await expect(partRow(page, 'admin')).toContainText('This PC');
  await expect(partRow(page, 'frontend')).toContainText('No memory for frontend yet');
  // a part's page is under Parts, not again among the pages
  await expect(vault(page).locator('.mem-row', { hasText: 'Design system' })).toHaveCount(1);
  await expect(vault(page).locator('.mem-row', { hasText: 'admin' })).toHaveCount(1);

  // open and edit the part's memory: no title to change, the hub names it
  await partRow(page, 'admin').click();
  const panel = page.locator('.mem-doc');
  await expect(panel.locator('.mem-doc-title h2')).toHaveText('admin');
  await expect(panel.locator('.badge')).toHaveText('Part memory');
  await expect(panel).toContainText('What holds for the admin part only');
  await expect(panel).toContainText('admin needs php 8.3');
  await panel.getByRole('button', { name: 'Edit' }).click();
  await expect(panel.getByLabel('Title')).toHaveCount(0);
  await panel.locator('textarea').fill('## Gotchas\n\n- admin needs php 8.3\n- admin queues run on redis\n');
  await panel.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('.toast', { hasText: 'Memory of admin saved.' })).toBeVisible();
  await expect.poll(async () => (await docOf(hub, p.id, slug(p.admin.id))).content).toContain('admin queues run on redis');

  // a part without one: it opens empty, and the first save makes it
  await partRow(page, 'frontend').click();
  await expect(panel.locator('.mem-doc-title h2')).toHaveText('frontend');
  const text = panel.locator('textarea');
  await expect(text).toBeVisible();
  await expect(text).toHaveValue('');
  await text.fill('## Commands\n\n- npm run dev serves the shop\n');
  await panel.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('.toast', { hasText: 'Memory of frontend saved.' })).toBeVisible();
  const made = await docOf(hub, p.id, slug(p.front.id));
  expect(made.part).toBe(p.front.id);
  expect(made.content).toContain('npm run dev serves the shop');
  await expect(partRow(page, 'frontend')).not.toContainText('No memory');

  // a rename shows at once
  await hub.call('PATCH', `/api/projects/${p.id}/parts/${p.front.id}`, { name: 'storefront' });
  await page.reload();
  await expect(partRow(page, 'storefront')).toBeVisible();
});

test('scan on the board: facts under the whole project and each part, each added where it belongs', async ({ page, hub }) => {
  const p = await twoParts(hub, 'Shop');
  await hub.memory(p.id, '- the project fact\n');
  await finishedRun(page, `**/api/projects/${p.id}/memory/scan`, 'jb_e2epartscan1', 'memory-scan', p, {
    facts: [
      { kind: 'commands', text: 'make test runs every part', part: '' },
      { kind: 'commands', text: 'npm run dev serves the shop', part: p.front.id },
      { kind: 'gotchas', text: 'admin needs php 8.3', part: p.admin.id },
    ],
  });
  await page.goto(`/p/${p.id}`);
  await openPanel(page);
  const panel = page.locator('.memory-panel');
  await expect(panel.getByLabel('Which memory')).toHaveValue('');
  await expect(panel.locator('.field-label', { hasText: 'Whole project memory' })).toBeVisible();

  await panel.getByRole('button', { name: 'Scan project' }).click();
  const found = panel.getByRole('group', { name: 'Proposed facts' });
  await expect(found.getByRole('group', { name: 'Facts for the whole project' })).toContainText('make test runs every part');
  await expect(found.getByRole('group', { name: 'Facts for frontend' })).toContainText('npm run dev serves the shop');
  await expect(found.getByRole('group', { name: 'Facts for admin' })).toContainText('admin needs php 8.3');
  // the parts in their order, after the whole project
  await expect(found.locator('.fact-group-head')).toHaveText(['Whole project', 'admin', 'frontend']);

  await panel.getByRole('button', { name: 'Add 3 to memory' }).click();
  await expect(page.locator('.toast', { hasText: 'Added 3 facts: 1 to the whole project, 1 to admin, 1 to frontend.' })).toBeVisible();
  const main = await hub.call('GET', `/api/projects/${p.id}/memory`);
  expect(main.project.content).toContain('make test runs every part');
  expect(main.project.content).not.toContain('npm run dev');
  expect((await docOf(hub, p.id, slug(p.front.id))).content).toContain('npm run dev serves the shop');
  expect((await docOf(hub, p.id, slug(p.admin.id))).content).toContain('admin needs php 8.3');

  // and each part's memory is edited right there
  await panel.getByLabel('Which memory').selectOption(p.front.id);
  await expect(panel.locator('textarea')).toHaveValue(/npm run dev serves the shop/);
  await panel.locator('textarea').fill('- npm run dev serves the shop\n- the shop builds with vite\n');
  await panel.getByRole('button', { name: 'Save memory' }).click();
  await expect(page.locator('.toast', { hasText: 'Memory of frontend saved.' })).toBeVisible();
  expect((await docOf(hub, p.id, slug(p.front.id))).content).toContain('the shop builds with vite');
});

test('init review: facts under the whole project and each part, saved where they belong', async ({ page, hub }) => {
  const p = await twoParts(hub, 'Shop');
  await finishedRun(page, `**/api/projects/${p.id}/memory/init`, 'jb_e2epartinit1', 'memory-init', p, {
    facts: [
      { kind: 'architecture', text: 'admin serves the API the frontend calls', part: '' },
      { kind: 'commands', text: 'php artisan test runs the admin tests', part: p.admin.id },
    ],
    subs: [],
    critical: [],
  });
  await page.goto(`/memory/${p.id}`);
  await vault(page).getByRole('button', { name: 'Init memory' }).click();
  const dialog = page.locator('.modal-layer');
  await dialog.getByRole('button', { name: 'Read the project' }).click();
  await expect(dialog.getByRole('region', { name: 'Facts for the whole project' })).toContainText('admin serves the API');
  await expect(dialog.getByRole('region', { name: 'Facts for admin' })).toContainText('php artisan test');
  await expect(dialog.getByRole('region', { name: 'Facts for frontend' })).toHaveCount(0);
  await dialog.getByRole('button', { name: 'Save 2 items' }).click();
  await expect(dialog).toHaveCount(0);
  const main = await hub.call('GET', `/api/projects/${p.id}/memory`);
  expect(main.project.content).toContain('admin serves the API the frontend calls');
  expect(main.project.content).not.toContain('php artisan');
  expect((await docOf(hub, p.id, slug(p.admin.id))).content).toContain('php artisan test runs the admin tests');
  await expect(partRow(page, 'admin')).not.toContainText('No memory');
});

test('a project of one part looks as it always did', async ({ page, hub }) => {
  const p = await hub.project('Solo');
  await hub.memory(p.id, '- the project fact\n');
  await hub.page(p.id, 'design', 'Design system', '- buttons are square\n');
  await page.goto(`/memory/${p.id}`);
  await expect(vault(page).locator('.mem-row', { hasText: 'Design system' })).toBeVisible();
  await expect(vault(page).locator('.mem-group', { hasText: 'Sub-memories' })).toBeVisible();
  await expect(vault(page).locator('.mem-group', { hasText: /Whole project|Parts/ })).toHaveCount(0);

  await page.goto(`/p/${p.id}`);
  await openPanel(page);
  const panel = page.locator('.memory-panel');
  await expect(panel.locator('.field-label', { hasText: `${p.name} memory` })).toBeVisible();
  await expect(panel.getByLabel('Which memory')).toHaveCount(0);
});

test('part memories on a phone: nothing runs off the screen', async ({ page, hub }) => {
  const p = await twoParts(hub, 'Shop', { front: 'a-rather-long-frontend' });
  await hub.page(p.id, slug(p.admin.id), '', '- admin needs php 8.3\n');
  await page.setViewportSize({ width: 390, height: 780 });
  await page.goto(`/memory/${p.id}`);
  await page.getByRole('button', { name: 'Memories' }).click();
  await expect(partRow(page, 'a-rather-long-frontend')).toContainText('No memory for a-rather-long-frontend yet');
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
  await partRow(page, 'admin').click();
  await expect(page.locator('.mem-doc')).toContainText('admin needs php 8.3');
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
});
