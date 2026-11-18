import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';

// A part that holds several repositories (docs/API-studio.md §3.6, DESIGN.md §3.5 Git): its
// repositories are rows, as inside a part's section of a project in several parts, in the
// board's Git panel, the editor's Source control and the Branches card. No dropdown.

const git = (cwd: string, ...args: string[]) => execFileSync('git', args, { cwd, encoding: 'utf8', env: { ...process.env, GIT_CONFIG_NOSYSTEM: '1' } }).trim();

/** A repository with its own author: the hub's home has no git identity. */
function repo(dir: string, files: Record<string, string>, subject: string) {
  mkdirSync(dir, { recursive: true });
  git(dir, 'init', '-q', '-b', 'main');
  git(dir, 'config', 'user.name', 'Test Person');
  git(dir, 'config', 'user.email', 'test@example.com');
  for (const [name, text] of Object.entries(files)) writeFileSync(join(dir, name), text);
  git(dir, 'add', '-A');
  git(dir, 'commit', '-qm', subject);
}

/** A project of one folder holding two repositories, each with one change of its own. */
async function twoRepos(hub: Parameters<Parameters<typeof test>[1]>[0]['hub'], name: string) {
  const p = await hub.project(name);
  repo(join(p.path, 'engine'), { 'engine.go': 'package engine\n' }, 'engine base');
  repo(join(p.path, 'site'), { 'index.html': '<p>site</p>\n' }, 'site base');
  git(join(p.path, 'site'), 'checkout', '-q', '-b', 'redesign');
  writeFileSync(join(p.path, 'engine', 'engine.go'), 'package engine\n\nconst Version = 2\n');
  writeFileSync(join(p.path, 'site', 'style.css'), 'p { color: teal }\n');
  return p;
}

test('a part of several repositories lists them as rows in the Git panel', async ({ page, hub }) => {
  const p = await twoRepos(hub, 'Nested repos');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: /^Git \(/ }).click();
  const panel = page.getByRole('complementary', { name: 'Git' });
  const rows = panel.getByRole('list', { name: `Repositories of ${p.name}` });
  await expect(rows.getByRole('button')).toHaveCount(2, { timeout: 20_000 });
  await expect(panel.getByRole('combobox', { name: 'Repository' })).toHaveCount(0);
  const engine = rows.getByRole('button', { name: /^engine/ });
  const site = rows.getByRole('button', { name: /^site/ });
  await expect(engine).toContainText('main');
  await expect(engine).toContainText('1 changed');
  await expect(site).toContainText('redesign');

  // picking a row is the repository the panel works on
  await site.click();
  await expect(site).toHaveAttribute('aria-current', 'true');
  await expect(engine).not.toHaveAttribute('aria-current', 'true');
  await expect(panel.getByRole('list', { name: 'Changed files' })).toContainText('style.css', { timeout: 15_000 });
  await engine.click();
  await expect(panel.getByRole('list', { name: 'Changed files' })).toContainText('engine.go', { timeout: 15_000 });
});

test('the editor’s Source control lists a part’s repositories as rows', async ({ page, hub }) => {
  const p = await twoRepos(hub, 'Nested in editor');
  writeFileSync(join(p.path, 'README.md'), '# nested\n');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_edit00rep01', kind: 'editor', x: 0, y: 0, w: 1100, h: 700, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_edit00rep01`);
  await page.getByRole('button', { name: 'Source Control' }).click({ timeout: 20_000 });
  const rows = page.getByRole('list', { name: `Repositories of ${p.name}` });
  await expect(rows.getByRole('button')).toHaveCount(2, { timeout: 20_000 });
  await expect(page.getByRole('combobox', { name: 'Repository' })).toHaveCount(0);
  const site = rows.getByRole('button', { name: /^site/ });
  await site.click();
  await expect(site).toHaveAttribute('aria-current', 'true');
  await expect(page.locator('.wb-view')).toContainText('style.css', { timeout: 15_000 });
});

test('the Branches card picks its part and repository from rows', async ({ page, hub }) => {
  const p = await hub.project('Branch rows');
  repo(p.path, { 'api.go': 'package api\n' }, 'api base');
  const front = `${p.path}-front`;
  repo(join(front, 'app'), { 'app.ts': 'export {}\n' }, 'app base');
  repo(join(front, 'docs'), { 'index.md': '# docs\n' }, 'docs base');
  git(join(front, 'docs'), 'branch', 'drafts');
  const first = (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).parts[0];
  await hub.call('PATCH', `/api/projects/${p.id}/parts/${first.id}`, { name: 'Api' });
  await hub.call('POST', `/api/projects/${p.id}/parts`, { name: 'Front', machineId: 'local', path: front });
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_brch00rep01', kind: 'branches', x: 0, y: 0, w: 360, h: 520, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const card = page.locator('.react-flow__node[data-id="n_brch00rep01"]');
  const parts = card.getByRole('list', { name: 'Parts' });
  await expect(parts.getByRole('button', { name: /^Api/ })).toBeVisible({ timeout: 20_000 });
  await expect(card.getByRole('combobox')).toHaveCount(0);
  await expect(card).toContainText('Pick the part whose branches this card shows.');
  // a part of one repository: its branch, no rows under it
  await expect(parts.getByRole('button', { name: /^Front/ })).toContainText('2 repositories');
  await parts.getByRole('button', { name: /^Api/ }).click();
  await expect(card.getByRole('list', { name: 'Local branches' })).toContainText('main', { timeout: 15_000 });
  await expect(card.getByRole('list', { name: 'Repositories of Api' })).toHaveCount(0);
  // a part of two: its repositories under it, and the branches of the one picked
  await parts.getByRole('button', { name: /^Front/ }).click();
  const repos = card.getByRole('list', { name: 'Repositories of Front' });
  await expect(repos.getByRole('button')).toHaveCount(2, { timeout: 15_000 });
  await repos.getByRole('button', { name: /^docs/ }).click();
  await expect(card.getByRole('list', { name: 'Local branches' })).toContainText('drafts', { timeout: 15_000 });
});
