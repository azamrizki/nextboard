import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';

// The board's Git panel for a project in several parts (ROADMAP #49, docs/API-studio.md §3.8):
// every part's repository at once, each opened on its own, with the editor's history graph; the
// git pill adds the parts up, and a commit opens in the editor card of its part (v0.9.0-beta.2).

const git = (cwd: string, ...args: string[]) => execFileSync('git', args, { cwd, encoding: 'utf8', env: { ...process.env, GIT_CONFIG_NOSYSTEM: '1' } }).trim();

/** A repository with its own author: the hub's home has no git identity. */
function repo(dir: string, files: Record<string, string>, subject: string) {
  git(dir, 'init', '-q', '-b', 'main');
  git(dir, 'config', 'user.name', 'Test Person');
  git(dir, 'config', 'user.email', 'test@example.com');
  for (const [name, text] of Object.entries(files)) writeFileSync(join(dir, name), text);
  git(dir, 'add', '-A');
  git(dir, 'commit', '-qm', subject);
}

test('the Git panel shows every part at once, commits in one, and draws its history', async ({ page, hub }) => {
  test.slow();
  const p = await hub.project('Shop');
  const web = `${p.path}-web`;
  mkdirSync(web);
  repo(p.path, { 'server.go': 'package main\n' }, 'api base');
  writeFileSync(join(p.path, 'server.go'), 'package main\n\nfunc main() {}\n');
  repo(web, { 'index.html': '<p>shop</p>\n' }, 'web base');
  const bare = `${web}.remote.git`;
  execFileSync('git', ['init', '-q', '--bare', '-b', 'main', bare]);
  git(web, 'remote', 'add', 'origin', bare);
  git(web, 'push', '-q', '-u', 'origin', 'main');
  writeFileSync(join(web, 'style.css'), 'p { color: red }\n');
  git(web, 'add', '-A');
  git(web, 'commit', '-qm', 'web style');
  writeFileSync(join(web, 'app.js'), 'console.log("shop")\n');
  const first = (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).parts[0];
  await hub.call('PATCH', `/api/projects/${p.id}/parts/${first.id}`, { name: 'Api' });
  await hub.call('POST', `/api/projects/${p.id}/parts`, { name: 'Web', machineId: 'local', path: web });

  await page.goto(`/p/${p.id}/w/${p.wid}`);
  // the pill adds every part up: no part's repository is the project's
  const pill = page.locator('.git-pill');
  await expect(pill).toHaveAccessibleName(/^2 repositories in 2 parts, branch main, 2 changed files, 1 to push\. Open the Git panel/, { timeout: 20_000 });
  await expect(pill).toContainText('main');
  await page.getByRole('button', { name: /^Git \(/ }).click();
  const panel = page.getByRole('complementary', { name: 'Git' });
  const api = panel.getByRole('region', { name: 'Part Api' });
  const front = panel.getByRole('region', { name: 'Part Web' });
  const apiHead = api.getByRole('button', { name: /^Api/ });
  const webHead = front.getByRole('button', { name: /^Web/ });

  // both parts, closed: each with its branch, its changes and what waits to be pushed
  await expect(apiHead).toHaveAccessibleName(/Branch main, 1 changed file$/, { timeout: 15_000 });
  await expect(webHead).toHaveAccessibleName(/Branch main, 1 changed file, 1 to push$/);
  await expect(apiHead).toHaveAttribute('aria-expanded', 'false');
  await expect(panel.getByRole('list', { name: 'Changed files' })).toHaveCount(0);
  // the parts are named as they are: none of them is "Main"
  await expect(panel).not.toContainText('Main');

  // both open at once, each with its own files and its own message box
  await apiHead.click();
  await webHead.click();
  await expect(api.getByRole('list', { name: 'Changed files' })).toContainText('server.go');
  await expect(front.getByRole('list', { name: 'Changed files' })).toContainText('app.js');
  await expect(panel.getByRole('textbox', { name: 'Commit message' })).toHaveCount(2);

  // a commit in the second part goes there only
  await front.getByRole('textbox', { name: 'Commit message' }).fill('web script');
  await front.getByRole('button', { name: 'Commit only' }).click();
  await expect.poll(() => git(web, 'log', '-1', '--format=%s')).toBe('web script');
  await expect(webHead).toHaveAccessibleName(/Branch main, 0 changed files, 2 to push$/, { timeout: 15_000 });
  expect(git(p.path, 'log', '--format=%s')).toBe('api base');
  expect(git(p.path, 'status', '--porcelain')).toContain('server.go');
  await expect(apiHead).toHaveAccessibleName(/Branch main, 1 changed file$/);
  await expect(pill).toHaveAccessibleName(/^2 repositories in 2 parts, branch main, 1 changed file, 2 to push\./, { timeout: 20_000 });

  // the history graph of that part: its three commits, the one on the remote marked
  await front.getByRole('button', { name: 'History', exact: true }).click();
  const rows = front.locator('.wb-graph-row');
  await expect(rows).toHaveCount(3, { timeout: 15_000 });
  await expect(rows.nth(0)).toHaveAccessibleName(/^web script, /);
  await expect(rows.nth(2)).toContainText('origin/main');
  await expect(api.locator('.wb-graph-row')).toHaveCount(0);

  // a commit shows what it changed; its diffs open in the editor
  await rows.nth(1).click();
  const peek = front.getByRole('region', { name: /^Commit [0-9a-f]{7}/ });
  await expect(peek.getByRole('list', { name: /^Files of / })).toContainText('style.css');
  await expect(peek).toContainText('+1');
  await peek.getByRole('button', { name: 'Open in editor' }).click();
  // in the editor card of that part: the board had none, so one is added there
  const card = page.getByRole('group', { name: 'Code editor · Web' });
  await expect(card.locator('.wb-tab', { hasText: 'web style' })).toBeVisible({ timeout: 30_000 });
  const webPart = (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).parts[1];
  const editors = (await hub.call('GET', `/api/workspaces/${p.wid}/board`)).nodes.filter((n: any) => n.kind === 'editor');
  expect(editors.map((n: any) => n.data.part)).toEqual([webPart.id]);

  // the panel remembers which parts were open, and one Escape closes it
  await page.reload();
  await page.getByRole('button', { name: /^Git \(/ }).click();
  await expect(panel.getByRole('region', { name: 'Part Web' }).getByRole('button', { name: /^Web/ })).toHaveAttribute('aria-expanded', 'true', { timeout: 15_000 });
  await page.keyboard.press('Escape');
  await expect(panel).toHaveCount(0);
});

test('a project in one folder keeps its Git panel, with the history graph and the stash', async ({ page, hub }) => {
  const p = await hub.project('Blog');
  repo(p.path, { 'post.md': '# First\n' }, 'first post');
  writeFileSync(join(p.path, 'post.md'), '# First\n\nMore.\n');

  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: /^Git \(/ }).click();
  const panel = page.getByRole('complementary', { name: 'Git' });
  await expect(panel.getByRole('list', { name: 'Changed files' })).toContainText('post.md', { timeout: 15_000 });
  await expect(panel.getByRole('region', { name: /^Part / })).toHaveCount(0);
  await expect(panel.getByRole('heading', { name: 'Git', exact: true })).toBeVisible();

  await panel.getByRole('button', { name: 'Stash', exact: true }).click();
  await panel.getByRole('button', { name: 'Stash changes' }).click();
  await page.getByRole('dialog', { name: 'Stash changes' }).getByRole('button', { name: 'Stash', exact: true }).click();
  await expect(panel.getByText('Nothing to commit — the working tree is clean.')).toBeVisible({ timeout: 15_000 });
  await expect(panel.locator('.wb-stash-row')).toHaveCount(1);

  await panel.getByRole('button', { name: 'History', exact: true }).click();
  await expect(panel.locator('.wb-graph-row')).toHaveCount(1, { timeout: 15_000 });
  await expect(panel.locator('.wb-graph-row').first()).toHaveAccessibleName(/^first post, /);
  // nothing in the panel runs past its edge on a phone
  await page.setViewportSize({ width: 393, height: 873 });
  await expect.poll(() => panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
});
