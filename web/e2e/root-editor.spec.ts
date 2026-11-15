import { readFileSync, utimesSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { expect, test } from './hub';
import { SSHD } from './sshd';
import { git, twoParts } from './two-parts';

// The Code editor of a project in several parts (v0.9.0-beta.2, docs/API-studio.md §7.7): a card
// at the Root shows one tree with every part as a top-level folder, a part's card only that part.
// Files stay on their machines: here admin is on This PC and frontend on a server over SSH.

let sshd: SSHD;
test.beforeAll(async () => {
  test.skip(!SSHD.available(), 'no /usr/sbin/sshd');
  sshd = await SSHD.start();
});
test.afterAll(() => sshd?.stop());

/** A line of the open file, clicked, then text typed at its end. */
async function typeAtEnd(page: Page, card: Locator, line: string, text: string) {
  await card.locator('.monaco-editor .view-line', { hasText: line }).first().click();
  await page.keyboard.press('End');
  await page.keyboard.type(text);
}

test('the editor at the root shows both parts as folders, and works in each', async ({ page, hub }) => {
  test.slow();
  const p = await twoParts(hub, 'Shop', { sshd, git: true });
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_rooted0001', kind: 'editor', x: 0, y: 0, w: 1300, h: 800, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_rooted0001`);
  const card = page.getByRole('group', { name: 'Code editor · All parts' });
  const side = card.locator('.wb-side');

  // one tree: each part a folder at the top, named as the part, the server one with its machine
  const top = side.locator('[role="treeitem"][aria-level="1"]');
  await expect(top).toHaveCount(2, { timeout: 20_000 });
  await expect(top.nth(0)).toHaveText(/^admin/);
  await expect(top.nth(1)).toHaveText(/^frontend/);
  await expect(top.nth(1).locator('.wb-part-machine')).toHaveText(p.server!);
  await expect(top.nth(0)).toHaveAttribute('title', `admin: ${p.admin.path} on This PC`);
  await expect(top.nth(1)).toHaveAttribute('title', `frontend: ${p.front.path} on ${p.server}`);
  // nothing is made at the root itself: it is no folder
  await expect(card.getByRole('button', { name: 'New file' })).toBeDisabled();

  // both parts open at first; a file of each, opened, edited and saved where it is
  await expect(top.nth(0)).toHaveAttribute('aria-expanded', 'true');
  await expect(top.nth(1)).toHaveAttribute('aria-expanded', 'true');
  await side.getByRole('treeitem', { name: 'server.go' }).click({ timeout: 20_000 });
  await expect(card.locator('.wb-tab', { hasText: 'server.go' })).toBeVisible();
  await typeAtEnd(page, card, 'greeting from admin', ' and the root');
  await page.keyboard.press('Control+s');
  await expect.poll(() => readFileSync(join(p.admin.path, 'server.go'), 'utf8')).toContain('// greeting from admin and the root');
  await expect(side.getByRole('treeitem', { name: 'src' })).toBeVisible({ timeout: 20_000 });
  await side.getByRole('treeitem', { name: 'index.html' }).click();
  await expect(card.locator('.wb-tab', { hasText: 'index.html' })).toBeVisible();
  await typeAtEnd(page, card, 'greeting from frontend', '<!-- over ssh -->');
  await page.keyboard.press('Control+s');
  await expect.poll(() => readFileSync(join(p.front.path, 'index.html'), 'utf8')).toContain('<!-- over ssh -->');

  // one search across both parts, each part's results under its name
  await card.getByRole('button', { name: 'Search', exact: true }).click();
  await side.getByRole('textbox', { name: 'Search', exact: true }).fill('greeting');
  const heads = side.locator('.wb-result-part .wb-part-head');
  await expect(heads).toHaveCount(2, { timeout: 20_000 });
  await expect(heads.nth(0)).toContainText('admin');
  await expect(heads.nth(1)).toContainText('frontend');
  await expect(side.locator('.wb-result-head', { hasText: 'server.go' })).toBeVisible();
  await expect(side.locator('.wb-result-head', { hasText: 'index.html' })).toBeVisible();
  await expect(side.getByRole('status')).toContainText('2 results in 2 files');
  // a folder of the tree as the files to include: that part only
  await side.getByRole('button', { name: 'Files to include and exclude' }).click();
  await side.getByRole('textbox', { name: 'Files to include' }).fill('frontend/**');
  await expect(side.locator('.wb-result-head', { hasText: 'server.go' })).toHaveCount(0, { timeout: 20_000 });
  await expect(side.locator('.wb-result-head', { hasText: 'index.html' })).toBeVisible();

  // source control: a group per part, each its own repository; a commit in one goes there only
  await card.getByRole('button', { name: 'Source Control' }).click();
  const scmAdmin = side.getByRole('region', { name: 'Source control of admin' });
  const scmFront = side.getByRole('region', { name: 'Source control of frontend' });
  await expect(scmAdmin).toBeVisible({ timeout: 20_000 });
  await expect(scmFront).toBeVisible();
  // the part in use (frontend: its file is the active tab) is open
  await expect(scmFront.locator('.wb-part-head')).toHaveAttribute('aria-expanded', 'true');
  await expect(scmAdmin.locator('.wb-part-head')).toHaveAttribute('aria-expanded', 'false');
  await expect(scmFront).toContainText('index.html', { timeout: 20_000 });
  await scmAdmin.locator('.wb-part-head').click();
  await expect(scmAdmin).toContainText('server.go', { timeout: 20_000 });
  await scmFront.getByRole('textbox', { name: 'Commit message' }).fill('frontend over ssh');
  await scmFront.getByRole('button', { name: /^Commit/ }).first().click();
  await expect.poll(() => git(p.front.path, 'log', '-1', '--format=%s'), { timeout: 30_000 }).toBe('frontend over ssh');
  expect(git(p.admin.path, 'log', '-1', '--format=%s')).toBe('admin base');
  expect(git(p.admin.path, 'status', '--porcelain')).toContain('server.go');
  await expect(scmFront.getByText('No changes in the working tree.')).toBeVisible({ timeout: 20_000 });
  await expect(scmAdmin).toContainText('server.go');
});

test('a part card shows only its part, and two cards on one file share its edits', async ({ page, hub }) => {
  test.slow();
  const p = await twoParts(hub, 'Shop');
  await hub.ops(p.wid, [
    { op: 'put', node: { id: 'n_rooted0002', kind: 'editor', x: 0, y: 0, w: 1300, h: 800, data: {} } },
    { op: 'put', node: { id: 'n_admined002', kind: 'editor', x: 1400, y: 0, w: 1300, h: 800, data: { part: p.admin.id } } },
  ]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_rooted0002`);
  const root = page.getByRole('group', { name: 'Code editor · All parts' });
  const admin = page.getByRole('group', { name: 'Code editor · admin' });

  // an edit at the root, not saved
  await root.locator('.wb-side').getByRole('treeitem', { name: 'server.go' }).click({ timeout: 20_000 });
  await typeAtEnd(page, root, 'greeting from admin', ' (shared)');
  await expect(root.locator('.wb-tab.is-dirty', { hasText: 'server.go' })).toBeVisible();

  // the card of the part: its folder is the tree's root, the other part is not there
  const bar = page.getByRole('toolbar', { name: 'Focused card' });
  await expect(bar.getByText('Code editor · All parts', { exact: true })).toBeVisible();
  await bar.getByRole('button', { name: 'Next card: Code editor · admin' }).click();
  await expect(page).toHaveURL(/[?&]card=n_admined002/);
  const side = admin.locator('.wb-side');
  await expect(side.locator('[role="treeitem"][aria-level="1"]', { hasText: 'server.go' })).toBeVisible({ timeout: 20_000 });
  await expect(side.getByRole('treeitem', { name: /^frontend/ })).toHaveCount(0);
  await expect(side.getByRole('treeitem', { name: 'index.html' })).toHaveCount(0);

  // the same file there: the same unsaved text, and one save for both
  await side.getByRole('treeitem', { name: 'server.go' }).click();
  await expect(admin.locator('.wb-tab.is-dirty', { hasText: 'server.go' })).toBeVisible();
  await expect(admin.locator('.monaco-editor .view-line', { hasText: '(shared)' })).toBeVisible();
  await typeAtEnd(page, admin, '(shared)', ' and saved');
  await page.keyboard.press('Control+s');
  await expect.poll(() => readFileSync(join(p.admin.path, 'server.go'), 'utf8')).toContain('// greeting from admin (shared) and saved');
  await expect(admin.locator('.wb-tab.is-dirty')).toHaveCount(0);
  await bar.getByRole('button', { name: 'Previous card: Code editor · All parts' }).click();
  await expect(page).toHaveURL(/[?&]card=n_rooted0002/);
  await expect(root.locator('.wb-tab', { hasText: 'server.go' })).toBeVisible();
  await expect(root.locator('.wb-tab.is-dirty')).toHaveCount(0);
  await expect(root.locator('.monaco-editor .view-line', { hasText: 'and saved' })).toBeVisible();
  // changed on disk right after the switch: the card's watch is made again for the card shown,
  // and its first look catches up with a change no watch saw
  writeFileSync(join(p.admin.path, 'server.go'), 'package main\n\n// changed on disk\nfunc main() {}\n');
  // a later time than the save's, which may have been within the same second
  utimesSync(join(p.admin.path, 'server.go'), new Date(), new Date(Date.now() + 5000));
  await expect(root.locator('.monaco-editor .view-line', { hasText: 'changed on disk' })).toBeVisible({ timeout: 20_000 });

  // the rail's Code editor asks which one, and adds a card only for a placement without one
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: 'Code editor', exact: true }).first().click();
  const ask = page.getByRole('dialog', { name: 'Open the code editor' });
  const show = ask.getByRole('radiogroup', { name: 'Show' });
  await expect(show.getByRole('radio')).toHaveText(['Root', 'admin', 'frontend']);
  await show.getByRole('radio', { name: 'frontend' }).click();
  await expect(ask).toContainText('Only this part’s folder, as its own tree.');
  await ask.getByRole('button', { name: 'Add the editor' }).click();
  await expect(ask).toBeHidden();
  const editors = async () => ((await hub.call('GET', `/api/workspaces/${p.wid}/board`)).nodes as { kind: string; data: { part?: string } }[]).filter((n) => n.kind === 'editor');
  await expect.poll(async () => (await editors()).map((n) => n.data.part ?? '')).toEqual(['', p.admin.id, p.front.id]);
  const front = page.getByRole('group', { name: 'Code editor · frontend' });
  await expect(front.locator('.wb-side').getByRole('treeitem', { name: 'index.html' })).toBeVisible({ timeout: 20_000 });
  // the last one picked is the default the next time; a placement with a card shows it
  await page.getByRole('button', { name: 'Code editor', exact: true }).first().click();
  await expect(show.getByRole('radio', { name: 'frontend' })).toHaveAttribute('aria-checked', 'true');
  await show.getByRole('radio', { name: 'admin' }).click();
  await ask.getByRole('button', { name: 'Show the editor' }).click();
  await expect(ask).toBeHidden();
  expect((await editors()).length).toBe(3);
});

test('a project in one folder keeps its one editor, with no question', async ({ page, hub }) => {
  const p = await hub.project('Blog');
  writeFileSync(join(p.path, 'post.md'), '# First\n');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: 'Code editor', exact: true }).first().click();
  await expect(page.getByRole('dialog', { name: 'Open the code editor' })).toHaveCount(0);
  const card = page.getByRole('group', { name: 'Code editor', exact: true });
  // its folder is the tree's root, as before
  await expect(card.locator('.wb-side [role="treeitem"][aria-level="1"]', { hasText: 'post.md' })).toBeVisible({ timeout: 20_000 });
  const board = await hub.call('GET', `/api/workspaces/${p.wid}/board`);
  const editors = board.nodes.filter((n: { kind: string }) => n.kind === 'editor');
  expect(editors.length).toBe(1);
  expect(editors[0].data.part).toBeUndefined();
  // asked again, the same card
  await page.getByRole('button', { name: 'Code editor', exact: true }).first().click();
  expect((await hub.call('GET', `/api/workspaces/${p.wid}/board`)).nodes.filter((n: { kind: string }) => n.kind === 'editor').length).toBe(1);
});
