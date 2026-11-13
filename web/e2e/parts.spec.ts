import { mkdirSync } from 'node:fs';
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
