import type { Page } from '@playwright/test';
import { expect, test } from './hub';

// A console on a touch screen (DESIGN.md §3.9): the key bar sends what a phone's keyboard has no
// key for, copies the console's text, and arms Ctrl for the next key typed.

const touch = (name: string) => ['phone', 'tablet-portrait', 'tablet-landscape'].includes(name);

async function openConsole(page: Page, name: string) {
  await expect(page.locator('.app-header')).toBeVisible();
  if ((await page.locator('html').getAttribute('data-layout')) === 'phone') {
    await page.locator('.phone-tabs').getByRole('button', { name: 'Cards' }).click();
    await page.locator('.card-row').first().click();
  } else {
    await page.locator('.react-flow__node').filter({ has: page.locator('.xterm') }).getByRole('button', { name: /^Focus/ }).click();
  }
  await expect(page).toHaveURL(/[?&]card=/);
  await expect(page.getByRole('toolbar', { name: /^Keys for @/ })).toBeVisible();
  void name;
}

test('the key bar types what a phone keyboard cannot, and the text can be copied', async ({ page, hub }, info) => {
  test.skip(!touch(info.project.name), 'touch screens');
  const p = await hub.project('Keys');
  const c = await hub.console(p.wid);
  await page.goto(`/p/${p.id}`);
  await openConsole(page, info.project.name);
  const bar = page.getByRole('toolbar', { name: /^Keys for @/ });
  // the card ends above the bar
  const card = await page.locator('.react-flow__node.is-focused').boundingBox();
  expect(card!.y + card!.height).toBeLessThanOrEqual((await bar.boundingBox())!.y + 1);

  // the console echoes what it gets: a slash, then the up arrow as ^[[A
  await expect.poll(async () => (await hub.call('GET', `/api/sessions/${c.id}`)).status).toBe('running');
  await page.waitForTimeout(500);
  await bar.getByRole('button', { name: '/' }).click();
  await bar.getByRole('button', { name: 'Up' }).click();
  await bar.getByRole('button', { name: 'Copy text' }).click();
  const dialog = page.getByRole('dialog', { name: /^Copy text from @/ });
  await expect(dialog.locator('.copy-text')).toContainText('> /^[[A');
  await expect(dialog.getByRole('button', { name: 'Copy all' })).toBeEnabled();
  await dialog.getByRole('button', { name: 'Close dialog' }).click();

  // Ctrl, then c typed on the keyboard: the console gets ^C, not c, and Ctrl lets go. ^C stops the
  // fake agent, and the console drops to a shell.
  const state = async () => {
    const s = await hub.call('GET', `/api/sessions/${c.id}`);
    return `${s.status}/${s.agentState}/${s.agent}`;
  };
  const before = await state();
  const ctrl = bar.getByRole('button', { name: /^Ctrl: / });
  await ctrl.click();
  await expect(ctrl).toHaveAttribute('aria-pressed', 'true');
  await page.locator('.react-flow__node.is-focused .xterm-helper-textarea').focus();
  await page.keyboard.type('c');
  await expect(ctrl).toHaveAttribute('aria-pressed', 'false');
  await expect.poll(state, { timeout: 10_000 }).not.toBe(before);
  await bar.getByRole('button', { name: 'Copy text' }).click();
  await expect(dialog.locator('.copy-text')).not.toContainText('[Ac');
});

test('an order can carry a photo picked from the device', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'tablet-landscape' && info.project.name !== 'light', 'one touch screen and the desktop');
  const p = await hub.project('Attach');
  await page.goto(`/p/${p.id}`);
  const chooser = page.waitForEvent('filechooser');
  await page.getByRole('button', { name: 'Attach a file or photo' }).click();
  await (await chooser).setFiles({ name: 'shot.png', mimeType: 'image/png', buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64') });
  await expect(page.locator('.cmd-panel')).toContainText('shot.png');
});
