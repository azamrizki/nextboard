import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';
import { SSHD } from './sshd';

// The code card of a project on a server over SSH, as the hub meets one: a real sshd, SFTP, the
// default MaxSessions of 10. The card sends many commands at once when it opens; none of them may
// fail. Then the work of a day: the tree with what git ignores dimmed, an edit saved, a search,
// a commit, a terminal on the server, an agent in a tab.

let sshd: SSHD;
test.beforeAll(async () => {
  test.skip(!SSHD.available(), 'no /usr/sbin/sshd');
  sshd = await SSHD.start();
});
test.afterAll(() => sshd?.stop());

function git(dir: string, ...args: string[]) {
  return execFileSync('git', ['-C', dir, ...args], { encoding: 'utf8' });
}

test('the code card works on a project over SSH', async ({ page, hub }, info) => {
  test.slow();
  const mid = await sshd.machine(hub, `server-${info.project.name}`);
  const dir = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'remote-'));
  mkdirSync(join(dir, 'src'));
  mkdirSync(join(dir, 'build'));
  writeFileSync(join(dir, 'README.md'), '# Remote\n');
  writeFileSync(join(dir, 'src', 'main.ts'), 'export const greeting = "hello";\n');
  writeFileSync(join(dir, 'build', 'out.js'), 'compiled\n');
  writeFileSync(join(dir, '.gitignore'), 'build/\n');
  git(dir, 'init', '-q', '-b', 'main');
  git(dir, 'config', 'user.name', 'E2E');
  git(dir, 'config', 'user.email', 'e2e@example.com');
  git(dir, 'add', '-A');
  git(dir, 'commit', '-q', '-m', 'first');

  const p = await hub.call('POST', '/api/projects', { machineId: mid, path: dir, name: `Remote ${info.project.name}` });
  const wid = p.workspaces[0].id;
  await hub.ops(wid, [{ op: 'put', node: { id: 'n_edit000001', kind: 'editor', x: 0, y: 0, w: 1200, h: 760, data: {} } }]);

  // first with agents on servers turned off (on by default since they left preview)
  await hub.call('PATCH', '/api/settings', { agents: { remoteTools: false } });
  const failed: string[] = [];
  page.on('response', (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith('/api/') && r.status() >= 400) failed.push(`${r.status()} ${r.request().method()} ${u.pathname}`);
  });

  await page.goto(`/p/${p.id}/w/${wid}?card=n_edit000001`);
  await expect(page.locator('.wb-tab', { hasText: 'README.md' })).toBeVisible({ timeout: 20_000 });
  const side = page.locator('.wb-side');

  // the tree, with what git ignores dimmed
  await expect(side.getByRole('treeitem', { name: 'build' })).toHaveClass(/is-dim/);
  await expect(side.getByRole('treeitem', { name: 'src' })).not.toHaveClass(/is-dim/);

  // an edit, saved on the server
  await side.getByRole('treeitem', { name: 'src' }).click();
  await side.getByRole('treeitem', { name: 'main.ts' }).click();
  await expect(page.locator('.wb-tab', { hasText: 'main.ts' })).toBeVisible();
  await page.locator('.monaco-editor .view-line', { hasText: 'greeting' }).click();
  await page.keyboard.press('End');
  await page.keyboard.type(' // over ssh');
  await page.keyboard.press('Control+s');
  await expect.poll(() => readFileSync(join(dir, 'src', 'main.ts'), 'utf8')).toContain('// over ssh');

  // a search across the project
  await page.getByRole('button', { name: 'Search', exact: true }).click();
  await side.getByRole('textbox', { name: 'Search', exact: true }).fill('greeting');
  await expect(side.locator('.wb-result-head', { hasText: 'main.ts' })).toBeVisible({ timeout: 15_000 });

  // the change, committed
  await page.getByRole('button', { name: 'Source Control' }).click();
  await expect(side.getByText('main.ts')).toBeVisible({ timeout: 15_000 });
  await side.getByRole('textbox', { name: 'Commit message' }).fill('edit over ssh');
  await side.getByRole('button', { name: /^Commit/ }).click();
  await expect.poll(() => git(dir, 'log', '-1', '--format=%s').trim(), { timeout: 30_000 }).toBe('edit over ssh');
  await expect(side.getByText('No changes in the working tree.')).toBeVisible({ timeout: 15_000 });

  // a terminal on the server, in the project's folder
  await page.getByRole('button', { name: /^Toggle panel/ }).click();
  const term = page.locator('.wb-terminal .xterm');
  await expect(term).toBeVisible({ timeout: 15_000 });
  await term.click();
  await page.keyboard.type('echo "nb-$((6*7))-$(basename "$PWD")"\n');
  await expect(term.locator('.xterm-rows')).toContainText(`nb-42-${dir.split('/').pop()}`, { timeout: 15_000 });

  // agents on servers are off: the menu says where they are turned on, rather than nothing
  await page.getByRole('button', { name: 'Open an agent in a new tab' }).click();
  await expect(page.getByRole('menuitem', { name: 'Fake CLI' })).toHaveCount(0);
  await expect(page.getByRole('menuitem', { name: 'Turn on agents on servers…' })).toBeVisible();
  await page.keyboard.press('Escape');

  // on: an agent of This PC works in a tab of the card, on the server's folder
  await hub.call('PATCH', '/api/settings', { agents: { remoteTools: true } });
  try {
    await page.reload();
    await expect(page.locator('.wb-tab', { hasText: 'main.ts' })).toBeVisible({ timeout: 20_000 });
    await page.getByRole('button', { name: 'Open an agent in a new tab' }).click();
    await page.getByRole('menuitem', { name: 'Fake CLI' }).click();
    await expect(page.locator('.wb-pane-tab .xterm-rows')).toContainText('>', { timeout: 20_000 });
  } finally {
    await hub.call('PATCH', '/api/settings', { agents: { remoteTools: true } });
  }

  expect(failed).toEqual([]);
});

// Review and Local History of a project on a server, as the owner uses them. The copy on This PC
// takes what the file held, the save goes through the editor, and an older state comes back.
test('local history works on a project over SSH', async ({ page, hub }, info) => {
  test.slow();
  const mid = await sshd.machine(hub, `history-${info.project.name}`);
  const dir = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'remote-'));
  writeFileSync(join(dir, 'app.php'), '<?php // first version\n');
  writeFileSync(join(dir, '.env'), 'APP_KEY=CANARY-in-browser-test\n');
  const p = await hub.call('POST', '/api/projects', { machineId: mid, path: dir, name: `History ${info.project.name}` });
  const wid = p.workspaces[0].id;
  await hub.ops(wid, [{ op: 'put', node: { id: 'n_edit000002', kind: 'editor', x: 0, y: 0, w: 1200, h: 760, data: {} } }]);

  const failed: string[] = [];
  page.on('response', (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith('/api/') && r.status() >= 400) failed.push(`${r.status()} ${r.request().method()} ${u.pathname}`);
  });

  await page.goto(`/p/${p.id}/w/${wid}?card=n_edit000002`);
  const side = page.locator('.wb-side');
  await side.getByRole('treeitem', { name: 'app.php' }).click({ timeout: 20_000 });
  await expect(page.locator('.wb-tab', { hasText: 'app.php' })).toBeVisible();

  // before: "Review works for projects on This PC."
  await page.getByRole('button', { name: 'Review', exact: true }).click();
  await expect(side.getByText('Nothing to review.', { exact: false })).toBeVisible({ timeout: 20_000 });

  // the file as it was goes in before the first save changes it
  await expect.poll(async () => (await hub.call('GET', `/api/projects/${p.id}/history?path=app.php`)).items.length, { timeout: 30_000 }).toBeGreaterThan(0);
  await page.locator('.monaco-editor .view-line', { hasText: 'first version' }).click();
  await page.keyboard.press('End');
  await page.keyboard.type(' edited');
  await page.keyboard.press('Control+s');
  await expect.poll(() => readFileSync(join(dir, 'app.php'), 'utf8')).toContain('edited');

  await page.keyboard.press('Control+Shift+P');
  const palette = page.locator('.wb-palette').getByRole('combobox');
  await expect(palette).toBeFocused();
  await palette.fill('>File: Local History');
  await expect(page.getByRole('option', { name: /File: Local History/ })).toBeVisible();
  await page.keyboard.press('Enter');
  const versions = page.getByRole('list', { name: 'Versions of app.php' });
  await expect(versions.locator('.wb-lh-row').first()).toContainText('Now', { timeout: 30_000 });
  await expect(versions.locator('.wb-lh-row')).toHaveCount(2, { timeout: 30_000 });
  await page.getByRole('button', { name: 'Restore this version' }).click();
  await expect(page.locator('.wb-tab', { hasText: 'app.php' }).first()).toBeVisible();
  await page.locator('.wb-tab', { hasText: /^app\.php/ }).first().click();
  await expect(page.locator('.monaco-editor .view-line', { hasText: 'first version' }).first()).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('.monaco-editor .view-line', { hasText: 'edited' })).toHaveCount(0);

  expect(failed).toEqual([]);
});
