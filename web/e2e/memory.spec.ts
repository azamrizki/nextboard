import type { Page } from '@playwright/test';
import { expect, test } from './hub';

const openBoard = async (page: Page, pid: string) => {
  await page.goto(`/p/${pid}`);
  await expect(page.locator('.memory-stack').first()).toBeVisible();
};
const bricks = (page: Page) => page.locator('.memory-stack').first().locator('.memory-brick-name');

test('a tile is a stack on the memory of the project', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  const other = await hub.project('Acme');
  await hub.memory(p.id, '## Commands\n- make test runs everything\n');
  await hub.page(other.id, 'design', 'Design system', '- buttons are square\n');
  await hub.tile(p.wid, [{ projectId: other.id, name: 'design' }, { name: 'gone-page' }]);
  await openBoard(page, p.id);

  const base = page.locator('.memory-brick.is-base');
  await expect(base).toContainText(p.name);
  // It is always on: nothing on it removes it.
  await expect(base.getByRole('button')).toHaveCount(0);
  await expect(bricks(page).nth(1)).toContainText(`Design system · ${other.name}`);
  await expect(page.locator('.memory-brick.is-missing')).toContainText('gone');
  await expect(page.locator('.memory-push')).toContainText('Connect it to agents');

  // An icon without a size once took the whole tile.
  const icon = await base.locator('svg').boundingBox();
  const brick = await base.boundingBox();
  expect(icon!.width).toBeLessThan(20);
  expect(brick!.height).toBeLessThan(60);
});

test('a brick comes from another project and goes to who is connected', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  const other = await hub.project('Acme');
  await hub.memory(p.id, '- the project fact\n');
  await hub.page(other.id, 'deploy', 'Deployment', '- deploys happen on Friday\n', 'How releases reach production.');
  const c = await hub.console(p.wid);
  await hub.tile(p.wid, [], [c.node]);
  await openBoard(page, p.id);

  await page.locator('.memory-brick.is-add').first().click();
  const dialog = page.locator('.modal-layer');
  await expect(dialog.locator('.memory-picker-list')).toBeVisible();
  // Critical notes are never offered, and neither is what is always on.
  await expect(dialog.locator('.memory-pick-row', { hasText: /critical/i })).toHaveCount(0);
  await dialog.locator('summary', { hasText: other.name }).click();
  await dialog.locator('details', { hasText: other.name }).locator('.memory-pick-row', { hasText: 'Deployment' }).click();
  await dialog.getByRole('button', { name: 'Add 1 and send' }).click();

  await expect(bricks(page).nth(1)).toContainText(`Deployment · ${other.name}`);
  // another test's uptime alert may be showing too: this one's toast, not the first
  await expect(page.locator('.toast', { hasText: /Sent to 1 console/ })).toBeVisible();
  expect(await hub.blocks(p.wid)).toEqual([{ projectId: other.id, name: 'deploy' }]);
});

test('bricks are reordered by drag and by keyboard, and the hub keeps the order', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  for (const [name, title] of [['alpha', 'Alpha page'], ['bravo', 'Bravo page'], ['charlie', 'Charlie page']]) {
    await hub.page(p.id, name, title, `- a fact of ${name}\n`);
  }
  await hub.tile(p.wid, [{ projectId: p.id, name: 'alpha' }, { projectId: p.id, name: 'bravo' }, { projectId: p.id, name: 'charlie' }]);
  await openBoard(page, p.id);
  const stack = page.locator('.memory-stack').first();

  await stack.locator('.memory-brick', { hasText: 'Charlie page' }).dragTo(stack.locator('.memory-brick', { hasText: 'Alpha page' }));
  await expect(bricks(page).nth(1)).toContainText('Charlie page');
  await expect(bricks(page).nth(2)).toContainText('Alpha page');
  // It moved the brick, not the tile.
  await expect.poll(async () => (await hub.blocks(p.wid)).map((b) => b.name)).toEqual(['charlie', 'alpha', 'bravo']);

  await stack.locator('.memory-brick', { hasText: 'Bravo page' }).focus();
  await page.keyboard.press('Alt+ArrowUp');
  await expect(bricks(page).nth(2)).toContainText('Bravo page');
  await expect.poll(async () => (await hub.blocks(p.wid)).map((b) => b.name)).toEqual(['charlie', 'bravo', 'alpha']);
});

test('a brick can be left to look up, or taken off', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  await hub.page(p.id, 'alpha', 'Alpha page', '- a fact\n');
  await hub.tile(p.wid, [{ projectId: p.id, name: 'alpha' }]);
  await openBoard(page, p.id);
  const brick = page.locator('.memory-brick', { hasText: 'Alpha page' });

  await brick.getByRole('button', { name: 'Leave Alpha page to look up' }).click();
  await expect(brick).toContainText('look up');
  await expect.poll(async () => (await hub.blocks(p.wid))[0].lookup).toBe(true);

  await brick.getByRole('button', { name: 'Remove Alpha page' }).click();
  await expect(brick).toHaveCount(0);
  await expect.poll(() => hub.blocks(p.wid)).toEqual([]);
});

test('send to agents: which memory, which agents, and nothing for who has it', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  await hub.memory(p.id, '- the project fact\n');
  await hub.page(p.id, 'writing', 'Writing style', '- short sentences\n');
  await hub.console(p.wid);
  await hub.console(p.wid);
  await hub.tile(p.wid, []);
  await openBoard(page, p.id);
  await page.mouse.click(760, 760);
  await page.keyboard.press('m');
  await page.locator('.memory-panel').getByRole('button', { name: 'Send to agents…' }).click();
  const dialog = page.locator('.modal-layer');

  // The launch gave them the project's memory: nothing to send, no turn used.
  await expect(dialog.locator('.memory-targets .memory-pick-row')).toHaveCount(2);
  await expect(dialog.locator('.memory-targets')).toContainText('Has it already');
  await expect(dialog.getByRole('button', { name: 'Send to 0 agents' })).toBeDisabled();

  await dialog.getByRole('region', { name: `${p.name} (this project)` }).locator('.memory-pick-row', { hasText: 'Writing style' }).click();
  await expect(dialog.getByRole('button', { name: 'Send to 2 agents' })).toBeEnabled();
  await expect(dialog.locator('.memory-send')).toContainText(/tokens in total/);

  await dialog.getByRole('radio', { name: 'Choose' }).click();
  await expect(dialog.getByRole('button', { name: 'Send to 0 agents' })).toBeDisabled();
  await dialog.locator('.memory-targets input[type=checkbox]').first().check();
  await dialog.getByRole('button', { name: 'Send to 1 agent' }).click();
  await expect(page.locator('.toast', { hasText: '1 agent' })).toBeVisible();
  await expect(dialog).toHaveCount(0);
});

test('the owner keeps the list of blocked commands', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  await page.goto(`/memory/${p.id}`);
  await page.locator('.mem-row.is-critical').first().click();
  const section = page.locator('.mem-blocked');
  await expect(section).toContainText('Also from all projects');
  await expect(section).toContainText('git reset --hard');

  const area = section.locator('textarea');
  await area.fill('npm run deploy\n\n   *wipe-all*   ');
  await section.getByRole('button', { name: 'Save commands' }).click();
  await expect(area).toHaveValue('npm run deploy\n*wipe-all*');
  await expect.poll(async () => (await hub.call('GET', `/api/memory/blocked?projectId=${p.id}`)).commands).toEqual(['npm run deploy', '*wipe-all*']);

  // A pattern that would stop every command is refused, and the old list stays.
  await area.fill('*');
  await section.getByRole('button', { name: 'Save commands' }).click();
  await expect(section.locator('.field-error')).toBeVisible();
  expect((await hub.call('GET', `/api/memory/blocked?projectId=${p.id}`)).commands).toEqual(['npm run deploy', '*wipe-all*']);
});

test('nothing runs off a phone screen', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  await hub.memory(p.id, '- the project fact\n');
  await hub.console(p.wid);
  await hub.tile(p.wid, []);
  await page.setViewportSize({ width: 390, height: 780 });
  await page.goto(`/memory/${p.id}`);
  // The list is behind a button at this width.
  await page.getByRole('button', { name: 'Memories' }).click();
  await page.locator('.mem-row.is-critical').first().click();
  await expect(page.locator('.mem-blocked')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
});
