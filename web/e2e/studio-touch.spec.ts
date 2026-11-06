import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';

// The Code workbench and the studios on a phone and a tablet (DESIGN.md §3.5, §3.6): the layout
// follows the card's width, side bars lie over the editor where it is narrow, and a key bar comes
// with the keyboard. A studio on a phone shows its list or what was picked from it.

const touch = (name: string) => ['phone', 'tablet-portrait', 'tablet-landscape'].includes(name);

async function project(hub: Hub, name: string) {
  const p = await hub.project(name);
  mkdirSync(join(p.path, 'src'), { recursive: true });
  writeFileSync(join(p.path, 'README.md'), '# Demo\n\nA small project.\n');
  writeFileSync(join(p.path, 'src', 'main.ts'), Array.from({ length: 30 }, (_, i) => `export const value${i} = ${i};`).join('\n') + '\n');
  return p;
}

/** The editor card filling the screen (focus mode), with its first file open. */
async function editor(page: Page, hub: Hub, name: string, w = 1000) {
  const p = await project(hub, name);
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_edit000001', kind: 'editor', x: 0, y: 0, w, h: 640, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_edit000001`);
  await expect(page.locator('.wb')).toBeVisible();
  // the first visit opens the README
  await expect(page.locator('.wb-tab', { hasText: 'README.md' })).toBeVisible({ timeout: 15_000 });
  return p;
}

const inView = async (page: Page, selector: string) => {
  const b = (await page.locator(selector).first().boundingBox())!;
  return b.x >= -1 && b.x + b.width <= page.viewportSize()!.width + 1;
};

test('on a phone the side bar lies over the editor, a file closes it, and the keyboard brings keys', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'phone', 'phones');
  const p = await editor(page, hub, 'Pocket code');
  const wb = page.locator('.wb');
  await expect(wb).toHaveClass(/is-narrow/);
  // opening the README closed the side bar: the editor has the screen
  await expect(page.locator('.wb-side')).toHaveCount(0);

  await page.getByRole('button', { name: 'Explorer' }).tap();
  await expect(page.locator('.wb-side')).toBeVisible();
  await expect(page.locator('.wb-scrim-side')).toBeVisible();
  expect(await inView(page, '.wb-side')).toBe(true);
  await page.locator('.wb-side').getByText('src', { exact: true }).tap();
  await page.locator('.wb-side').getByText('main.ts', { exact: true }).tap();
  await expect(page.locator('.wb-side')).toHaveCount(0);
  await expect(page.locator('.monaco-editor .view-line').first()).toBeVisible();
  expect(await inView(page, '.monaco-editor')).toBe(true);

  // a tap at the start of the first line: the keys come up with the keyboard
  const lines = (await page.locator('.monaco-editor .view-lines').first().boundingBox())!;
  await page.touchscreen.tap(lines.x + 2, lines.y + 8);
  const keys = page.getByRole('toolbar', { name: 'Keys for the editor' });
  await expect(keys).toBeVisible();
  // Tab indents, Select and three Rights select "exp", typing replaces it, Save writes it
  await keys.getByRole('button', { name: 'Tab', exact: true }).tap();
  await keys.getByRole('button', { name: /^Select/ }).tap();
  for (let i = 0; i < 3; i++) await keys.getByRole('button', { name: 'Right' }).tap();
  await page.keyboard.type('X');
  await keys.getByRole('button', { name: 'Save' }).tap();
  await expect.poll(() => readFileSync(join(p.path, 'src', 'main.ts'), 'utf8').split('\n')[0]).toMatch(/^\s+Xort const value0 = 0;$/);

  // Find, then Esc closes it: the key goes to the editor as a key press
  await keys.getByRole('button', { name: 'Find' }).tap();
  await expect(page.locator('.find-widget.visible')).toBeVisible();
  await keys.getByRole('button', { name: 'Esc' }).tap();
  await expect(page.locator('.find-widget.visible')).toHaveCount(0);

  // the agents side bar takes the card's width, over the editor
  await page.getByRole('button', { name: /^Toggle agents side bar/ }).tap();
  const right = (await page.locator('.wb-right').boundingBox())!;
  const body = (await page.locator('.wb-body').boundingBox())!;
  expect(Math.abs(right.width - body.width)).toBeLessThan(2);
});

test('on a tablet held upright the agents side bar lies over the editor', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'tablet-portrait', 'tablets held upright');
  await editor(page, hub, 'Upright code');
  await expect(page.locator('.wb')).toHaveClass(/is-medium/);
  const before = (await page.locator('.wb-center').boundingBox())!.width;
  await page.getByRole('button', { name: /^Toggle agents side bar/ }).tap();
  await expect(page.locator('.wb-right')).toBeVisible();
  expect((await page.locator('.wb-center').boundingBox())!.width).toBeCloseTo(before, 0);
  // a tap beside it closes it
  const scrim = (await page.locator('.wb-body > .wb-scrim').boundingBox())!;
  await page.touchscreen.tap(scrim.x + 20, scrim.y + scrim.height / 2);
  await expect(page.locator('.wb-right')).toHaveCount(0);
});

test('a terminal of the editor gets the key bar too', async ({ page, hub }, info) => {
  test.skip(!touch(info.project.name), 'touch screens');
  await editor(page, hub, 'Panel keys');
  await page.getByRole('button', { name: /^Toggle panel/ }).tap();
  const term = page.locator('.wb-terminal .xterm');
  await expect(term).toBeVisible({ timeout: 15_000 });
  await term.tap();
  await expect(page.getByRole('toolbar', { name: 'Keys for the terminal' })).toBeVisible();
  // the editor's bar is not there at the same time
  await expect(page.getByRole('toolbar', { name: 'Keys for the editor' })).toHaveCount(0);
});

test('a wide editor keeps its side bars beside the editor', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light', 'desktop');
  await editor(page, hub, 'Wide code', 1300);
  await expect(page.locator('.wb')).not.toHaveClass(/is-(narrow|medium)/);
  const before = (await page.locator('.wb-center').boundingBox())!.width;
  await page.getByRole('button', { name: /^Toggle agents side bar/ }).click();
  await expect(page.locator('.wb-right')).toBeVisible();
  expect((await page.locator('.wb-center').boundingBox())!.width).toBeLessThan(before - 100);
});

test('a studio on a phone shows its list, then what was picked, and Back returns', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'phone', 'phones');
  const p = await project(hub, 'Notes');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.locator('.phone-tabs').getByRole('button', { name: 'Board' }).tap();
  await page.locator('.rail-left').getByRole('button', { name: 'Insert' }).tap();
  await page.locator('.rail-flyout-insert').getByRole('button', { name: 'Studios' }).tap();
  // a phone has no keys: menus leave the shortcuts out
  await expect(page.locator('.menu-kbd').first()).toBeHidden();
  await page.getByRole('menuitem', { name: 'Markdown' }).tap();
  const sheet = page.getByRole('dialog', { name: 'Markdown' });
  const list = sheet.getByRole('navigation', { name: 'Notes' });
  await expect(list).toBeVisible();
  await expect(sheet.locator('.studio-main')).toBeHidden();
  await list.getByText('Demo').tap();
  await expect(list).toBeHidden();
  await expect(sheet.locator('.studio-main')).toContainText('A small project.');
  await sheet.getByRole('button', { name: 'Notes' }).tap();
  await expect(list).toBeVisible();
});
