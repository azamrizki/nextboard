import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';

// Sleeping agents (ROADMAP #50, docs/API-agents.md §28): an idle agent's process is ended to free
// memory, its tile says so, and it resumes when typed into.

const logOf = (dir: string) => {
  try {
    return readFileSync(join(dir, '.fake-claude.log'), 'utf8');
  } catch {
    return '';
  }
};

test('an agent sleeps, says so, and wakes when typed into', async ({ page, hub }) => {
  const p = await hub.project('Sleepy');
  const s = await hub.call('POST', '/api/sessions', { agent: 'fakeclaude', workspaceId: p.wid });
  const info = async () => hub.call('GET', `/api/sessions/${s.id}`);
  await expect.poll(async () => (await info()).agentState, { timeout: 20_000 }).toBe('ready');

  await page.goto(`/p/${p.id}`);
  const tile = page.locator(`[data-session="${s.id}"]`);
  const menu = tile.getByRole('button', { name: `Actions for @${s.handle}` });
  await expect(tile).toBeVisible();

  // Sleep now: the process ends, the tile and the header say asleep
  await menu.click();
  await page.getByRole('menuitem', { name: 'Sleep now' }).click();
  await expect(tile.getByLabel('Status: Asleep')).toBeVisible();
  await expect.poll(async () => (await info()).status).toBe('asleep');
  await expect(page.getByRole('button', { name: /active console/ }).first()).toContainText('asleep');

  // typing wakes it: the keys reach its prompt once it is back
  await tile.locator('.nb-term').click();
  await page.keyboard.type('after the nap');
  await page.keyboard.press('Enter');
  await expect.poll(() => logOf(p.path), { timeout: 20_000 }).toContain('after the nap');
  await expect(tile.getByLabel('Status: Asleep')).toHaveCount(0);
  expect((await info()).asleep).toBeFalsy();

  // Keep awake: never put to sleep, and Sleep now is off
  await expect.poll(async () => (await info()).agentState, { timeout: 20_000 }).toBe('ready');
  await menu.click();
  await page.getByRole('menuitemcheckbox', { name: 'Keep awake' }).click();
  await expect.poll(async () => (await info()).keepAwake).toBe(true);
  await menu.click();
  await expect(page.getByRole('menuitemcheckbox', { name: 'Keep awake' })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('menuitem', { name: 'Sleep now' })).toBeDisabled();
  await page.keyboard.press('Escape');
  await hub.call('POST', `/api/sessions/${s.id}/kill`);
});

test('settings say when idle agents sleep and whether they start when needed', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light', 'one project at a time: all tests share the hub settings');
  await page.goto('/settings/agents');
  const group = page.getByRole('radiogroup', { name: 'Idle agents sleep after' });
  await expect(group.getByRole('radio', { name: '30 min' })).toBeChecked();
  await group.getByRole('radio', { name: '1 h' }).click();
  await expect.poll(async () => (await hub.call('GET', '/api/settings')).agents.sleepAfterMin).toBe(60);
  const lazy = page.getByRole('switch', { name: 'Start agents when they are needed' });
  await expect(lazy).toBeChecked();
  await lazy.click();
  await expect.poll(async () => (await hub.call('GET', '/api/settings')).agents.lazyStart).toBe(false);
  // back as they were: all tests share one hub
  await lazy.click();
  await group.getByRole('radio', { name: '30 min' }).click();
  await expect.poll(async () => (await hub.call('GET', '/api/settings')).agents).toMatchObject({ sleepAfterMin: 30, lazyStart: true });
});
