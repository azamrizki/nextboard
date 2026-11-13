import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';

// A project in several folders, on one machine or more (docs/API-board.md §1, ROADMAP #49).

test('a project gets a part, shows it, and a terminal opens there', async ({ page, hub }) => {
  const p = await hub.project('Shop');
  const other = await hub.project('Blog');
  const web = `${p.path}-web`;
  mkdirSync(web);

  await page.goto('/');
  await page.getByRole('button', { name: `Actions for ${p.name}` }).click();
  await page.getByRole('menuitem', { name: 'Parts…' }).click();
  const dialog = page.getByRole('dialog', { name: `Parts of ${p.name}` });
  const list = dialog.getByRole('list', { name: `Parts of ${p.name}` });
  await expect(list.getByRole('listitem')).toHaveCount(1);

  // another project's folder is refused, and says which project has it
  const add = dialog.getByRole('form', { name: 'Add a part' });
  await add.getByLabel('Name').fill('Blog');
  await add.getByLabel('Folder').fill(other.path);
  await add.getByRole('button', { name: 'Add part' }).click();
  await expect(add.getByText(`already in project "${other.name}"`)).toBeVisible();

  await add.getByLabel('Name').fill('Frontend');
  await add.getByLabel('Folder').fill(web);
  await add.getByRole('button', { name: 'Add part' }).click();
  await expect(list.getByRole('listitem')).toHaveCount(2);
  await expect(add.getByLabel('Name')).toHaveValue('');

  // the first part gets a name of its role; the other becomes read-only for agents
  await list.getByLabel('Name of part Main').fill('Backend');
  await list.getByLabel('Name of part Main').press('Enter');
  await expect(list.getByLabel('Name of part Backend')).toBeVisible();
  await list.getByRole('listitem').nth(1).getByLabel('Read-only').check();
  await expect.poll(async () => (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).parts[1].readOnly).toBe(true);
  await expect(list.getByRole('button', { name: 'Remove part Backend' })).toBeDisabled();
  // only parts on other machines are read only for agents, unless the project gives full access
  await dialog.getByLabel('Agents in parts on other machines').selectOption('full');
  await expect.poll(async () => (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).partAccess).toBe('full');
  await dialog.getByRole('button', { name: 'Done' }).click();

  // the card shows each part with its machine
  const chips = page.getByRole('list', { name: `Parts of ${p.name}` });
  await expect(chips.getByRole('listitem')).toHaveText(['BackendThis PC', 'FrontendThis PC']);

  // a terminal in the part opens in its folder
  const t = await hub.call('POST', '/api/sessions', { preset: 'shell', workspaceId: p.wid, cwd: `${web}` });
  expect(t.partId).toBe((await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).parts[1].id);
  await hub.call('DELETE', `/api/sessions/${t.id}`);

  // the project menu says it spans more than one folder
  await page.goto(`/p/${p.id}`);
  await page.locator('.project-switch:visible').first().click();
  await expect(page.getByRole('menu', { name: 'Switch project' }).getByRole('menuitemcheckbox', { name: new RegExp(`^${p.name}`) })).toContainText('This PC +1');
  await page.keyboard.press('Escape');

  // a part goes; its folder stays
  await page.goto('/');
  await page.getByRole('button', { name: `Actions for ${p.name}` }).click();
  await page.getByRole('menuitem', { name: 'Parts…' }).click();
  await dialog.getByRole('button', { name: 'Remove part Frontend' }).click();
  await expect(list.getByRole('listitem')).toHaveCount(1);
  await dialog.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByRole('list', { name: `Parts of ${p.name}` })).toHaveCount(0);
});

test('a project is added in several parts at once, and a wrong folder makes nothing', async ({ page, hub }) => {
  const taken = await hub.project('Taken');
  const api = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'shop-api-'));
  const web = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'shop-web-'));
  const name = `Shop ${Date.now().toString(36)}`;
  // all tests share one hub: look for this folder, not at how many projects there are
  const made = async () => (await hub.call('GET', '/api/projects')).find((x: any) => x.path === api);

  await page.goto('/');
  await page.getByRole('button', { name: 'Add project' }).first().click();
  const dialog = page.getByRole('dialog', { name: 'Add project' });
  await dialog.getByLabel('Folder').fill(api);
  await dialog.getByRole('button', { name: 'Add a part' }).click();

  // the own folder becomes part 1, with a name; the new part takes focus
  const one = dialog.getByRole('region', { name: 'Part 1' });
  const two = dialog.getByRole('region', { name: 'Part 2' });
  await expect(one.getByLabel('Folder')).toHaveValue(api);
  await expect(two.getByLabel('Part name')).toBeFocused();
  await one.getByLabel('Part name').fill('Api');
  await dialog.getByLabel('Project name').fill(name);

  // checked before anything is sent
  await dialog.getByRole('button', { name: 'Add project' }).click();
  await expect(two.getByText('Name the part')).toBeVisible();
  await expect(two.getByText('Choose a folder.')).toBeVisible();

  // another project's folder: the hub says so at that part, and makes nothing
  await two.getByLabel('Part name').fill('Web');
  await two.getByLabel('Folder').fill(taken.path);
  await dialog.getByRole('button', { name: 'Add project' }).click();
  await expect(two.getByText(`already in project "${taken.name}"`)).toBeVisible();
  expect(await made()).toBeUndefined();

  // a third part can go again
  await dialog.getByRole('button', { name: 'Add a part' }).click();
  await dialog.getByRole('button', { name: 'Remove part 3' }).click();
  await expect(dialog.getByRole('region', { name: 'Part 3' })).toHaveCount(0);

  await two.getByLabel('Folder').fill(web);
  await dialog.getByRole('button', { name: 'Add project' }).click();
  await expect(dialog).toBeHidden();
  const shop = await made();
  expect(shop.name).toBe(name);
  expect(shop.parts.map((pt: any) => [pt.name, pt.path])).toEqual([
    ['Api', api],
    ['Web', web],
  ]);
});

test('the editor shows one part at a time', async ({ page, hub }) => {
  test.slow();
  const p = await hub.project('Shop');
  const web = `${p.path}-web`;
  mkdirSync(web);
  writeFileSync(`${p.path}/server.go`, 'package main\n');
  writeFileSync(`${web}/index.html`, '<p>shop</p>\n');
  await hub.call('POST', `/api/projects/${p.id}/parts`, { name: 'Frontend', machineId: 'local', path: web });
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_partsed01', kind: 'editor', x: 0, y: 0, w: 1300, h: 800, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_partsed01`);
  const side = page.locator('.wb-side');
  await expect(side.getByRole('treeitem', { name: 'server.go' })).toBeVisible({ timeout: 15_000 });
  const pick = page.getByRole('combobox', { name: 'Part of the project' });
  await pick.selectOption({ label: 'Frontend' });
  await expect(side.getByRole('treeitem', { name: 'index.html' })).toBeVisible({ timeout: 15_000 });
  await expect(side.getByRole('treeitem', { name: 'server.go' })).toHaveCount(0);
  // remembered in this browser
  await page.reload();
  await expect(page.getByRole('combobox', { name: 'Part of the project' })).toHaveValue(/pt_/);
  await expect(page.locator('.wb-side').getByRole('treeitem', { name: 'index.html' })).toBeVisible({ timeout: 15_000 });
});

test('a project merges into another, its old link opens there, and a part splits off', async ({ page, hub }) => {
  const a = await hub.project('Shop');
  const b = await hub.project('Shop Web');
  await hub.call('PUT', `/api/memory/docs/critical?projectId=${b.id}`, { content: '- never deploy on friday\n' });

  await page.goto('/');
  await page.getByRole('button', { name: `Actions for ${a.name}` }).click();
  await page.getByRole('menuitem', { name: 'Parts…' }).click();
  await page.getByRole('button', { name: 'Merge another project into this one…' }).click();
  const dialog = page.getByRole('dialog', { name: `Merge into ${a.name}` });
  await dialog.getByLabel('Project to merge in').selectOption({ label: b.name });
  await expect(dialog.getByText('become tabs here')).toBeVisible();
  await expect(dialog.locator('.merge-parts')).toContainText(b.path.split('/').pop()!);
  await dialog.getByLabel(`Critical notes of ${b.name}`).selectOption('append');
  await dialog.getByRole('button', { name: 'Merge', exact: true }).click();
  await expect(dialog).toBeHidden();
  const parts = page.getByRole('dialog', { name: `Parts of ${a.name}` }).getByRole('list', { name: `Parts of ${a.name}` });
  await expect(parts.getByRole('listitem')).toHaveCount(2);
  await expect(page.getByRole('button', { name: `Actions for ${b.name}` })).toHaveCount(0);

  // its old link opens its board in the project it is part of now
  await page.goto(`/p/${b.id}/w/${b.wid}`);
  await expect(page).toHaveURL(new RegExp(`/p/${a.id}/w/${b.wid}`));

  // and it splits off again, as a project of its own
  await page.goto('/');
  await page.getByRole('button', { name: `Actions for ${a.name}` }).click();
  await page.getByRole('menuitem', { name: 'Parts…' }).click();
  await page.getByRole('button', { name: `Split ${b.name} off` }).click();
  await page.getByRole('dialog', { name: `Make ${b.name} a project of its own?` }).getByRole('button', { name: 'Split it off' }).click();
  await expect(parts.getByRole('listitem')).toHaveCount(1);
  await page.getByRole('dialog', { name: `Parts of ${a.name}` }).getByRole('button', { name: 'Done' }).click();
  await expect(page.getByRole('button', { name: `Actions for ${b.name}` })).toBeVisible();
});
