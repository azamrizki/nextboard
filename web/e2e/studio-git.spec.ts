import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';

// Source control in a folder that is no repository yet. The panel says so and asks git for
// nothing else: stashes, history and pull requests only exist in a repository, and asking for
// them only fills the browser's console with errors. That held only once the status was in, so
// a page opened straight on this view asked before it knew. Once the folder is a repository,
// the lists load.
test('source control asks git for its lists only in a repository', async ({ page, hub }) => {
  const p = await hub.project('Plain');
  writeFileSync(join(p.path, 'README.md'), '# Plain\n');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_edit000001', kind: 'editor', x: 0, y: 0, w: 1100, h: 700, data: {} } }]);
  const lists: string[] = [];
  const failed: string[] = [];
  page.on('response', (r) => {
    const u = new URL(r.url());
    if (!u.pathname.startsWith(`/api/projects/${p.id}/`)) return;
    if (/\/git\/(stash|log|pulls?)$/.test(u.pathname)) lists.push(`${r.status()} ${u.pathname}`);
    if (r.status() >= 400) failed.push(`${r.status()} ${r.request().method()} ${u.pathname}`);
  });

  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_edit000001`);
  await expect(page.locator('.wb-tab', { hasText: 'README.md' })).toBeVisible({ timeout: 15_000 });
  await page.getByRole('button', { name: 'Source Control' }).click();
  const plain = page.getByText('This project is not a Git repository yet.');
  await expect(plain).toBeVisible();

  // the view is remembered: the next load opens on it, before the status is in
  await expect.poll(() => page.evaluate((k) => JSON.parse(localStorage.getItem(k) ?? '{}').side, `nb.wb.${p.id}`)).toBe('scm');
  await page.reload();
  await expect(plain).toBeVisible({ timeout: 15_000 });
  await page.waitForTimeout(800);
  expect(lists).toEqual([]);
  expect(failed).toEqual([]);

  await page.getByRole('button', { name: 'Create repository' }).click();
  await expect(page.getByText('No commits yet.')).toBeVisible({ timeout: 15_000 });
  await expect.poll(() => lists.some((l) => l.endsWith('/git/stash'))).toBe(true);
  expect(lists.filter((l) => !l.startsWith('200 '))).toEqual([]);
  expect(failed).toEqual([]);
});
