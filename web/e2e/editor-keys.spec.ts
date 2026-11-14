import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';

// The editor's keys are registered once and go to the editor that has focus
// (studio/code/editorKeys.ts): with two files side by side, Ctrl+S saves the one it was pressed in.
test('Ctrl+S saves the file of the editor it was pressed in, also side by side', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light', 'one board is enough');
  const p = await hub.project('Keys');
  writeFileSync(join(p.path, 'left.txt'), 'left\n');
  writeFileSync(join(p.path, 'right.txt'), 'right\n');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_edit000001', kind: 'editor', x: 0, y: 0, w: 1200, h: 720, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_edit000001`);
  const side = page.locator('.wb-side');
  await side.getByRole('treeitem', { name: 'left.txt' }).click({ timeout: 20_000 });
  await expect(page.locator('.wb-tab', { hasText: 'left.txt' })).toBeVisible();

  // split, and open the other file on the right
  await page.locator('.monaco-editor .view-line', { hasText: 'left' }).first().click();
  await page.keyboard.press('Control+\\');
  await expect(page.locator('.wb-group')).toHaveCount(2);
  await side.getByRole('treeitem', { name: 'right.txt' }).click();
  const groups = page.locator('.wb-group');
  await expect(groups.nth(1).locator('.wb-tab', { hasText: 'right.txt' })).toBeVisible();

  // typed and saved in the left one: only left.txt changes
  await groups.nth(0).locator('.monaco-editor .view-line', { hasText: 'left' }).click();
  await page.keyboard.press('End');
  await page.keyboard.type(' saved here');
  await page.keyboard.press('Control+s');
  await expect.poll(() => readFileSync(join(p.path, 'left.txt'), 'utf8')).toContain('left saved here');
  expect(readFileSync(join(p.path, 'right.txt'), 'utf8')).toBe('right\n');

  // and in the right one: only right.txt
  await groups.nth(1).locator('.monaco-editor .view-line', { hasText: 'right' }).click();
  await page.keyboard.press('End');
  await page.keyboard.type(' too');
  await page.keyboard.press('Control+s');
  await expect.poll(() => readFileSync(join(p.path, 'right.txt'), 'utf8')).toContain('right too');
  expect(readFileSync(join(p.path, 'left.txt'), 'utf8')).toBe('left saved here\n');
});
