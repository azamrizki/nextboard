import { existsSync, mkdirSync, mkdtempSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';

// A project in several folders, on one machine or more (docs/API-board.md §1, ROADMAP #49). All
// parts are equal (v0.9.0-beta.2): none is "Main", and any one is renamed, moved or removed while
// one is left. The editor, consoles and layouts by part: root-*.spec.ts and place.spec.ts.

test('parts are equal: any one is renamed, moved first or removed, while one is left', async ({ page, hub }) => {
  const p = await hub.project('Shop');
  const other = await hub.project('Blog');
  const web = `${p.path}-web`;
  mkdirSync(web);
  // the first part is named after its folder, as any part: none is "Main"
  const folder = p.path.split('/').pop()!;
  const parts = async () => (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id);

  await page.goto('/');
  await page.getByRole('button', { name: `Actions for ${p.name}` }).click();
  await page.getByRole('menuitem', { name: 'Parts…' }).click();
  const dialog = page.getByRole('dialog', { name: `Parts of ${p.name}` });
  const list = dialog.getByRole('list', { name: `Parts of ${p.name}` });
  await expect(list.getByRole('listitem')).toHaveCount(1);
  await expect(list.getByLabel(`Name of part ${folder}`)).toHaveValue(folder);
  await expect(dialog).not.toContainText('Main');
  await expect(dialog).not.toContainText('The project’s own folder');
  // a project keeps at least one part
  await expect(list.getByRole('button', { name: `Remove part ${folder}` })).toBeDisabled();
  await expect(list.getByRole('button', { name: `Remove part ${folder}` })).toHaveAttribute('title', 'A project keeps at least one part: add another first');

  // another project's folder is refused, and says which project has it
  const add = dialog.getByRole('form', { name: 'Add a part' });
  await add.getByLabel('Part name').fill('Blog');
  await add.getByLabel('Folder').fill(other.path);
  await add.getByRole('button', { name: 'Add part' }).click();
  await expect(add.getByText(`already in project "${other.name}"`)).toBeVisible();

  await add.getByLabel('Part name').fill('Frontend');
  await add.getByLabel('Folder').fill(web);
  await add.getByRole('button', { name: 'Add part' }).click();
  await expect(list.getByRole('listitem')).toHaveCount(2);
  await expect(add.getByLabel('Part name')).toHaveValue('');

  // a part's name is its folder in the project root: said before the hub is asked
  const first = list.getByLabel(`Name of part ${folder}`);
  await first.fill('api/v2');
  await first.press('Enter');
  await expect(list.getByText('A part’s name is a folder name in the project root: no / or \\.')).toBeVisible();
  expect((await parts()).parts[0].name).toBe(folder);
  await first.fill('Backend');
  await first.press('Enter');
  await expect(list.getByLabel('Name of part Backend')).toBeVisible();
  await expect.poll(async () => (await parts()).parts.map((pt: any) => pt.name)).toEqual(['Backend', 'Frontend']);

  // the other becomes read-only for agents
  await list.getByRole('listitem').nth(1).getByLabel('Read-only').check();
  await expect.poll(async () => (await parts()).parts[1].readOnly).toBe(true);
  await expect(list.getByRole('button', { name: 'Remove part Backend' })).toBeEnabled();
  // only parts on other machines are read only for agents, unless the project gives full access
  await dialog.getByLabel('Agents in parts on other machines').selectOption('full');
  await expect.poll(async () => (await parts()).partAccess).toBe('full');

  // any part moves first: the order is only how parts are shown, and older clients see the first
  await expect(list.getByRole('button', { name: 'Move Backend up' })).toBeDisabled();
  await list.getByRole('button', { name: 'Move Frontend up' }).click();
  await expect.poll(async () => (await parts()).parts.map((pt: any) => pt.name)).toEqual(['Frontend', 'Backend']);
  await expect(list.getByRole('listitem').nth(0).locator('.part-name')).toHaveValue('Frontend');
  await expect(list.getByRole('listitem').nth(1).locator('.part-name')).toHaveValue('Backend');
  expect((await parts()).path).toBe(web);
  await dialog.getByRole('button', { name: 'Done' }).click();

  // the card names each part, in that order, and lists them with their machines
  const card = page.locator('.project-card').filter({ hasText: p.name });
  const line = card.locator('.project-partsline');
  await expect(line).toHaveAccessibleName(/: Frontend, Backend\. Show the parts$/);
  await line.click();
  const rows = page.locator('.parts-pop .parts-pop-row');
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText('Frontend');
  await expect(rows.nth(0)).toContainText('This PC');
  await expect(rows.nth(1)).toContainText('Backend');
  await page.keyboard.press('Escape');
  await expect(rows).toHaveCount(0);

  // a terminal in the part opens in its folder
  const t = await hub.call('POST', '/api/sessions', { preset: 'shell', workspaceId: p.wid, cwd: `${web}` });
  expect(t.partId).toBe((await parts()).parts[0].id);
  await hub.call('DELETE', `/api/sessions/${t.id}`);

  // the project menu says it spans more than one folder
  await page.goto(`/p/${p.id}`);
  await page.locator('.project-switch:visible').first().click();
  await expect(page.getByRole('menu', { name: 'Switch project' }).getByRole('menuitemcheckbox', { name: new RegExp(`^${p.name}`) })).toContainText('This PC +1');
  await page.keyboard.press('Escape');

  // the part shown first goes; the other stays, and so does its folder
  await page.goto('/');
  await page.getByRole('button', { name: `Actions for ${p.name}` }).click();
  await page.getByRole('menuitem', { name: 'Parts…' }).click();
  await dialog.getByRole('button', { name: 'Remove part Frontend' }).click();
  await expect(list.getByRole('listitem')).toHaveCount(1);
  await expect.poll(async () => (await parts()).parts.map((pt: any) => pt.name)).toEqual(['Backend']);
  expect((await parts()).path).toBe(p.path);
  expect(existsSync(web)).toBe(true);
  await expect(list.getByRole('button', { name: 'Remove part Backend' })).toBeDisabled();
  await dialog.getByRole('button', { name: 'Done' }).click();
  await expect(card.locator('.project-partsline')).toHaveCount(0);
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

  // one part or several, the same form: a project in one folder is a list of one
  const one = dialog.getByRole('region', { name: 'Part 1' });
  const two = dialog.getByRole('region', { name: 'Part 2' });
  await expect(one).toBeVisible();
  await expect(one.getByRole('button', { name: 'Remove part 1' })).toHaveCount(0);
  await one.getByLabel('Folder').fill(api);
  await dialog.getByRole('button', { name: 'Add a part' }).click();
  await expect(two.getByLabel('Part name')).toBeFocused();
  await expect(one.getByLabel('Folder')).toHaveValue(api);
  // a new part starts on a machine the others are not on: servers other tests left are no matter
  await two.getByLabel('Machine').selectOption('local');
  await one.getByLabel('Part name').fill('Api');
  await dialog.getByLabel('Project name').fill(name);

  // checked before anything is sent
  await dialog.getByRole('button', { name: 'Add project' }).click();
  await expect(two.getByText('Choose a folder.')).toBeVisible();
  await expect(one.getByText('Choose a folder.')).toHaveCount(0);

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
