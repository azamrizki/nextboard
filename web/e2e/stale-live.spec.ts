import { expect, test } from './hub';

// What one tab took from the hub once and then showed or saved back after another tab, another
// device or the hub changed it (v0.9.1): a dialog open meanwhile shows the change, and saving
// from it never puts the older copy back.

test('a maintenance window added in another tab survives a change made in this one', async ({ page, hub }) => {
  const p = await hub.project('Stale maint');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_uptstale01', kind: 'uptime', x: 0, y: 0, w: 1000, h: 640, data: {} } }]);
  await hub.call('POST', `/api/projects/${p.id}/uptime`, { name: 'Shop cron', type: 'push', intervalSec: 20 });
  await hub.call('PUT', `/api/projects/${p.id}/uptime/maintenance`, { windows: [{ title: 'Nightly', mode: 'manual', active: false }] });
  const open = async (tab: typeof page) => {
    await tab.goto(`/p/${p.id}/w/${p.wid}?card=n_uptstale01`);
    await tab.locator('.react-flow__node[data-id="n_uptstale01"]').getByRole('button', { name: /^Actions for/ }).click();
    await tab.getByRole('menuitem', { name: /^Maintenance…/ }).click();
    const dialog = tab.getByRole('dialog', { name: 'Maintenance' });
    await expect(dialog.locator('.up-maint-row', { hasText: 'Nightly' })).toBeVisible();
    return dialog;
  };

  // this tab opens the dialog first; another tab of the same browser then adds a window
  const here = await open(page);
  const other = await page.context().newPage();
  const there = await open(other);
  await there.getByRole('button', { name: 'Add window' }).click();
  await there.getByRole('textbox', { name: 'Title' }).fill('Backup');
  await there.getByRole('button', { name: 'Save window' }).click();
  await expect(there.locator('.up-maint-row', { hasText: 'Backup' })).toBeVisible();
  await other.close();

  // the open dialog here shows it, and turning another window on keeps it
  await expect(here.locator('.up-maint-row', { hasText: 'Backup' })).toBeVisible();
  await here.locator('.up-maint-row', { hasText: 'Nightly' }).getByRole('button', { name: 'Turn on' }).click();
  await expect(here.locator('.up-maint-row', { hasText: 'Nightly' }).getByRole('button', { name: 'Turn off' })).toBeVisible();
  const saved = (await hub.call('GET', `/api/projects/${p.id}/uptime`)).settings.maintenance;
  expect(saved.map((w: { title: string; active?: boolean }) => [w.title, !!w.active])).toEqual([
    ['Nightly', true],
    ['Backup', true],
  ]);

  // deleted elsewhere while this dialog is open: it goes here too, and a delete here takes only its own
  await hub.call('PUT', `/api/projects/${p.id}/uptime/maintenance`, { windows: [...saved, { title: 'Late', mode: 'manual', active: false }] });
  await expect(here.locator('.up-maint-row', { hasText: 'Late' })).toBeVisible();
  await here.getByRole('button', { name: 'Delete Backup' }).click();
  await expect(here.locator('.up-maint-row')).toHaveCount(2);
  const left = (await hub.call('GET', `/api/projects/${p.id}/uptime`)).settings.maintenance;
  expect(left.map((w: { title: string }) => w.title)).toEqual(['Nightly', 'Late']);
});

test('a board restore offers its own project after a rename, not one that took the old name', async ({ page, hub }) => {
  const p = await hub.project('Restore');
  const w = await hub.call('POST', `/api/projects/${p.id}/workspaces`, { name: 'Kept board' });
  await hub.ops(w.id, [{ op: 'put', node: { id: 'n_keptnote1', kind: 'note', x: 0, y: 0, w: 220, h: 200, data: { text: 'Keep me' } } }]);
  await hub.call('DELETE', `/api/workspaces/${w.id}`);
  const archived = (await hub.call('GET', '/api/archive')).find((a: { projectId?: string; workspaceName: string }) => a.projectId === p.id && a.workspaceName === 'Kept board');
  expect(archived?.projectName).toBe(p.name);

  // renamed since, and another project now has its old name
  await hub.call('PATCH', `/api/projects/${p.id}`, { name: `${p.name} (old)` });
  const q = await hub.project('Taker');
  await hub.call('PATCH', `/api/projects/${q.id}`, { name: p.name });

  await page.goto('/');
  await page.getByRole('button', { name: 'Board archive' }).click();
  const dialog = page.getByRole('dialog', { name: 'Board archive' });
  const row = dialog.locator('.archive-item', { hasText: `Kept board · ${p.name}` });
  await expect(row).toHaveCount(1);
  await expect(row.getByRole('combobox', { name: 'Project to restore “Kept board” into' })).toHaveValue(p.id);
  await row.getByRole('button', { name: 'Restore' }).click();
  await expect(page).toHaveURL(new RegExp(`/p/${p.id}/w/`));
});

test('a console names its machine as it is called now', async ({ page, hub }) => {
  const machines = await hub.call('GET', '/api/machines');
  const before = machines.find((m: { id: string }) => m.id === 'local').name;
  const p = await hub.project('Bench move');
  const c = await hub.console(p.wid);
  const s = await hub.call('GET', `/api/sessions/${c.id}`);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const tile = page.locator(`[data-session="${c.id}"]`);
  await expect(tile).toBeVisible();
  try {
    // renamed elsewhere: the console's own copy of the name is not refreshed by the hub
    await hub.call('PATCH', '/api/machines/local', { name: 'Bench PC' });
    await expect(tile).toContainText('@ Bench PC');
    await tile.getByRole('button', { name: `Actions for @${s.handle}` }).click();
    await page.getByRole('menuitem', { name: 'Close terminal…' }).click();
    const dialog = page.getByRole('dialog', { name: 'Close this terminal?' });
    await expect(dialog).toContainText('on Bench PC will be killed');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
  } finally {
    await hub.call('PATCH', '/api/machines/local', { name: before });
  }
});

test('a workspace tab renamed elsewhere while its field is open keeps that name', async ({ page, hub }) => {
  const p = await hub.project('Tabs');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const tabs = page.getByRole('tablist', { name: `Workspaces of ${p.name}` });
  const field = page.getByRole('textbox', { name: 'Workspace name' });
  const sent: string[] = [];
  page.on('request', (r) => {
    if (r.method() === 'PATCH' && r.url().includes(`/api/workspaces/${p.wid}`)) sent.push(r.postData() ?? '');
  });
  const name = async () => (await hub.call('GET', '/api/projects')).find((x: { id: string }) => x.id === p.id).workspaces[0].name;

  // F2 on "Main", renamed to "API" in another tab, Enter here without typing: "API" stays
  await tabs.getByRole('tab').first().focus();
  await page.keyboard.press('F2');
  await expect(field).toHaveValue('Main');
  await hub.call('PATCH', `/api/workspaces/${p.wid}`, { name: 'API' });
  await expect(field).toHaveValue('API');
  await field.press('Enter');
  await expect(tabs.getByRole('tab').first()).toHaveText(/^API/);
  await page.waitForTimeout(400);
  expect(sent).toEqual([]);
  expect(await name()).toBe('API');

  // typed in: the typed name is saved
  await tabs.getByRole('tab').first().focus();
  await page.keyboard.press('F2');
  await field.fill('Backend');
  await field.press('Enter');
  await expect.poll(name).toBe('Backend');
  await expect(tabs.getByRole('tab').first()).toHaveText(/^Backend/);
});
