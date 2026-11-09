import { execFileSync } from 'node:child_process';
import { closeSync, ftruncateSync, openSync, readFileSync, statSync, writeFileSync } from 'node:fs';
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

// A file over 100 MB that went into a commit (§3.7). The panel finds it before the push
// fails, rewrites only the commits no remote has, keeps the file and an open edit, and can
// put everything back.
test('a big file in an unpushed commit comes out of the commits', async ({ page, hub }) => {
  const p = await hub.project('Big');
  const run = (...args: string[]) => execFileSync('git', args, { cwd: p.path, encoding: 'utf8', env: { ...process.env, GIT_CONFIG_NOSYSTEM: '1' } }).trim();
  const bare = `${p.path}.remote.git`;
  execFileSync('git', ['init', '-q', '--bare', '-b', 'main', bare]);
  run('init', '-q', '-b', 'main');
  run('config', 'user.name', 'Test');
  run('config', 'user.email', 'test@example.com');
  writeFileSync(join(p.path, 'README.md'), '# Big\n');
  run('add', '-A');
  run('commit', '-qm', 'base');
  run('remote', 'add', 'origin', bare);
  run('push', '-q', '-u', 'origin', 'main');
  const fd = openSync(join(p.path, 'dump.bin'), 'w');
  ftruncateSync(fd, 101 * 1024 * 1024);
  closeSync(fd);
  run('add', '-A');
  run('commit', '-qm', 'add the dump');
  writeFileSync(join(p.path, 'work.txt'), 'work\n');
  run('add', '-A');
  run('commit', '-qm', 'real work');
  writeFileSync(join(p.path, 'README.md'), '# Big, being edited\n');
  const before = run('rev-parse', 'HEAD');

  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_edit000002', kind: 'editor', x: 0, y: 0, w: 1100, h: 700, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_edit000002`);
  await page.getByRole('button', { name: 'Source Control' }).click();
  const warning = page.getByTestId('git-large-history');
  await expect(warning).toContainText('dump.bin', { timeout: 15_000 });
  await warning.getByRole('button', { name: 'Remove from commits' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText('Rewrite 2 commits');
  await dialog.getByRole('button', { name: 'Remove from commits' }).click();

  const done = page.getByTestId('git-large-removed');
  await expect(done).toContainText('refs/nextboard/backup/', { timeout: 15_000 });
  await expect(warning).toHaveCount(0);
  expect(run('log', '--format=%s', 'origin/main..HEAD')).toBe('real work');
  expect(statSync(join(p.path, 'dump.bin')).size).toBe(101 * 1024 * 1024);
  expect(readFileSync(join(p.path, 'README.md'), 'utf8')).toBe('# Big, being edited\n');
  expect(readFileSync(join(p.path, '.gitignore'), 'utf8')).toContain('dump.bin');
  run('push', '-q', 'origin', 'main');
  expect(execFileSync('git', ['log', '--format=%h', 'main', '--', 'dump.bin'], { cwd: bare, encoding: 'utf8' }).trim()).toBe('');

  // Pushed now, and the backup still sits there: undo puts the local branch back.
  await done.getByRole('button', { name: 'Undo' }).click();
  await expect(done).toHaveCount(0, { timeout: 15_000 });
  expect(run('rev-parse', 'HEAD')).toBe(before);
  await expect(warning).toContainText('dump.bin', { timeout: 15_000 });
});
