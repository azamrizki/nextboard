import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';

// Pinned and locked tabs of the Code editor (docs/API-studio.md §7.2): a pinned tab leads its
// strip, compact, and bulk closes leave it; nothing closes a locked one, file or agent, until it
// is unlocked; both stay after a reload. On a touch screen the tab's menu comes by long press.

const touch = (name: string) => ['phone', 'tablet-portrait', 'tablet-landscape'].includes(name);

async function editor(page: Page, hub: Hub, name: string, files: Record<string, string>) {
  const p = await hub.project(name);
  for (const [f, text] of Object.entries(files)) writeFileSync(join(p.path, f), text);
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_edit000001', kind: 'editor', x: 0, y: 0, w: 1200, h: 720, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_edit000001`);
  await expect(page.locator('.wb')).toBeVisible();
  return p;
}

/** The first group's tab strip, and its tabs by name. */
const strip = (page: Page) => page.locator('.wb-group').first().locator('.wb-tabs');
const tabOf = (page: Page, name: string) => strip(page).locator('.wb-tab', { hasText: name });
const names = (page: Page) => strip(page).locator('.wb-tab .wb-tab-name');

/** A menu item by its label, with or without the shortcut it shows ("Pin Ctrl+K Shift+Enter"). */
const item = (label: string) => ({ name: new RegExp(`^${label}( (Alt|Ctrl)\\+.*)?$`) });

async function tabMenu(page: Page, tab: Locator, label: string) {
  await tab.click({ button: 'right' });
  await page.getByRole('menu', { name: 'Tab actions' }).getByRole('menuitem', item(label)).click();
}

test('a pinned tab leads the strip, a locked one outlives every close, and both come back after a reload', async ({ page, hub }, info) => {
  test.skip(touch(info.project.name), 'desktop');
  const p = await editor(page, hub, 'Pinned tabs', { 'alpha.ts': 'export const alpha = 1;\n', 'beta.ts': 'export const beta = 2;\n', 'gamma.ts': 'export const gamma = 3;\n' });
  const side = page.locator('.wb-side');
  for (const f of ['alpha.ts', 'beta.ts', 'gamma.ts']) await side.getByRole('treeitem', { name: f }).dblclick({ timeout: 20_000 });
  await expect(names(page)).toHaveText(['alpha.ts', 'beta.ts', 'gamma.ts']);

  // pinned: first in the strip, a pin where the close button was, compact
  await tabMenu(page, tabOf(page, 'gamma.ts'), 'Pin');
  await expect(names(page)).toHaveText(['gamma.ts', 'alpha.ts', 'beta.ts']);
  const gamma = tabOf(page, 'gamma.ts');
  await expect(gamma).toHaveClass(/is-pinned/);
  await expect(gamma.getByRole('button', { name: 'Unpin gamma.ts' })).toBeVisible();
  expect((await gamma.boundingBox())!.width).toBeLessThanOrEqual(133);

  // locked: a lock that is no button, with the reason as its tooltip
  await tabMenu(page, tabOf(page, 'alpha.ts'), 'Lock');
  const alpha = tabOf(page, 'alpha.ts');
  await expect(alpha).toHaveClass(/is-locked/);
  await expect(alpha.locator('.wb-tab-lock')).toHaveAttribute('title', 'Locked: unlock it to close');
  await expect(alpha.getByRole('button')).toHaveCount(0);
  await expect(names(page)).toHaveText(['gamma.ts', 'alpha.ts', 'beta.ts']);

  // Close All leaves the pinned and the locked tab
  await tabMenu(page, tabOf(page, 'beta.ts'), 'Close All');
  await expect(names(page)).toHaveText(['gamma.ts', 'alpha.ts']);

  // the card's close key (browsers keep Ctrl+W) and a middle click: it stays, and says why
  await alpha.click();
  await page.locator('.monaco-editor .view-line', { hasText: 'alpha' }).first().click();
  await page.keyboard.press('Alt+w');
  await expect(page.locator('.toast', { hasText: 'Locked: unlock it to close.' })).toBeVisible();
  await alpha.click({ button: 'middle' });
  await tabMenu(page, alpha, 'Close');
  await expect(names(page)).toHaveText(['gamma.ts', 'alpha.ts']);

  // a reload: both flags come back with the tabs, saved with them in this browser
  const savedTabs = () => page.evaluate((k) => JSON.stringify(JSON.parse(localStorage.getItem(k) ?? '{}').groups?.[0]?.tabs ?? null), `nb.wb.${p.id}`);
  await expect.poll(savedTabs).toBe(JSON.stringify([{ path: join(p.path, 'gamma.ts'), preview: false, pinned: true }, { path: join(p.path, 'alpha.ts'), preview: false, locked: true }]));
  await page.reload();
  await expect(names(page)).toHaveText(['gamma.ts', 'alpha.ts'], { timeout: 20_000 });
  await expect(tabOf(page, 'gamma.ts')).toHaveClass(/is-pinned/);
  await expect(tabOf(page, 'alpha.ts')).toHaveClass(/is-locked/);

  // an explicit close takes a pinned tab
  await tabMenu(page, tabOf(page, 'gamma.ts'), 'Close');
  await expect(names(page)).toHaveText(['alpha.ts']);

  // the palette unlocks the active tab; VS Code's chord in the editor pins it
  await tabOf(page, 'alpha.ts').click();
  await page.keyboard.press('Control+Shift+P');
  const palette = page.locator('.wb-palette').getByRole('combobox');
  await expect(palette).toBeFocused();
  await palette.fill('>View: Unlock Editor');
  await expect(page.getByRole('option', { name: /View: Unlock Editor/ })).toBeVisible();
  await page.keyboard.press('Enter');
  await expect(tabOf(page, 'alpha.ts')).not.toHaveClass(/is-locked/);
  await expect(tabOf(page, 'alpha.ts').getByRole('button', { name: 'Close alpha.ts' })).toBeAttached();
  await page.locator('.monaco-editor .view-line', { hasText: 'alpha' }).first().click();
  await page.keyboard.press('Control+k');
  await page.keyboard.press('Shift+Enter');
  await expect(tabOf(page, 'alpha.ts')).toHaveClass(/is-pinned/);
  // the file itself is untouched by the chord
  await expect(page.locator('.monaco-editor .view-line', { hasText: 'alpha' })).toHaveCount(1);
  await expect(tabOf(page, 'alpha.ts')).not.toHaveClass(/is-dirty/);
});

test('a locked agent tab outlives Close All and its close, then closes once unlocked', async ({ page, hub }, info) => {
  test.skip(touch(info.project.name), 'desktop');
  await editor(page, hub, 'Locked agent', { 'README.md': '# Agents\n' });
  await expect(tabOf(page, 'README.md')).toBeVisible({ timeout: 15_000 });
  await tabMenu(page, tabOf(page, 'README.md'), 'Pin');
  await page.getByRole('button', { name: 'Open an agent in a new tab' }).click();
  await page.getByRole('menuitem', { name: 'Fake CLI' }).click();
  await expect(page.locator('.wb-pane-tab .xterm-rows')).toContainText('>', { timeout: 20_000 });
  const agent = strip(page).locator('.wb-tab:not(:has-text("README.md"))');
  await expect(agent).toHaveCount(1);
  await expect(agent.getByRole('button', { name: 'Close this agent' })).toBeVisible();

  await tabMenu(page, agent, 'Lock');
  await expect(agent).toHaveClass(/is-locked/);
  await expect(agent.getByRole('button', { name: 'Close this agent' })).toHaveCount(0);
  // the side bar has no lock: the tab stays where it is
  await agent.click({ button: 'right' });
  await expect(page.getByRole('menu', { name: 'Tab actions' }).getByRole('menuitem', { name: 'Move to Side Bar' })).toBeDisabled();
  await page.keyboard.press('Escape');

  // Close All (from the pinned README) leaves both; Close and a middle click ask nothing
  await tabMenu(page, tabOf(page, 'README.md'), 'Close All');
  await expect(strip(page).locator('.wb-tab')).toHaveCount(2);
  await tabMenu(page, agent, 'Close');
  await expect(page.locator('.toast', { hasText: 'Locked: unlock it to close.' })).toBeVisible();
  await agent.click({ button: 'middle' });
  await expect(page.getByRole('dialog', { name: /^Stop / })).toHaveCount(0);
  await expect(agent).toHaveCount(1);
  await expect(page.locator('.wb-pane-tab .xterm-rows')).toContainText('>');

  // unlocked, its close button asks before the agent stops
  await tabMenu(page, agent, 'Unlock');
  await agent.getByRole('button', { name: 'Close this agent' }).click();
  const stop = page.getByRole('dialog', { name: /^Stop / });
  await expect(stop).toBeVisible();
  await stop.getByRole('button', { name: 'Stop and close' }).click();
  await expect(agent).toHaveCount(0);
  await expect(strip(page).locator('.wb-tab')).toHaveCount(1);
});

test('on a touch screen a long press on a tab opens its menu, and a pinned tab fits the strip', async ({ page, hub }, info) => {
  test.skip(!touch(info.project.name), 'touch screens');
  await editor(page, hub, 'Touch tabs', { 'README.md': '# Touch\n\nA small project.\n' });
  await expect(tabOf(page, 'README.md')).toBeVisible({ timeout: 15_000 });
  // on a phone the side bar may still lie over the editor
  const scrim = page.getByRole('button', { name: 'Close the side bar' });
  if (await scrim.isVisible()) await scrim.dispatchEvent('click');
  await expect(scrim).toHaveCount(0);

  const cdp = await page.context().newCDPSession(page);
  const b = (await tabOf(page, 'README.md').boundingBox())!;
  const at = { x: b.x + Math.min(30, b.width / 2), y: b.y + b.height / 2 };
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [at] });
  await page.waitForTimeout(700);
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  const menu = page.getByRole('menu', { name: 'Tab actions' });
  await expect(menu).toBeVisible();
  await expect(menu.getByRole('menuitem', item('Lock'))).toBeVisible();
  await menu.getByRole('menuitem', item('Pin')).tap();
  await expect(tabOf(page, 'README.md')).toHaveClass(/is-pinned/);
  await expect(tabOf(page, 'README.md').getByRole('button', { name: 'Unpin README.md' })).toBeVisible();

  // the strip stays inside the screen: the page does not scroll sideways
  const s = (await strip(page).boundingBox())!;
  expect(s.x).toBeGreaterThanOrEqual(-1);
  expect(s.x + s.width).toBeLessThanOrEqual(page.viewportSize()!.width + 1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
});
