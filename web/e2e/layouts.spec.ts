import { expect, test } from './hub';

const consoles = async (hub: any, wid: string) => {
  const board = await hub.call('GET', `/api/workspaces/${wid}/board`);
  return board as { nodes: { id: string; kind: string; x: number; y: number; w: number; h: number; data: { members?: string[]; name?: string } }[]; edges: unknown[] };
};

// Choosing how many consoles a group gets, before anything is made: the preview follows, the
// template stays as it was, and what lands is what the preview showed.
test('a layout deploys with the group sizes chosen for it', async ({ page, hub }, info) => {
  const p = await hub.project(`Sizes ${info.project.name}`);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: 'Layouts' }).first().click();
  const panel = page.getByRole('dialog', { name: 'Layouts' });
  await panel.getByRole('textbox', { name: 'Search layouts' }).fill('Build & review');
  await panel.getByRole('option', { name: /Build & review/ }).click();
  const sizes = panel.getByTestId('layout-sizes');
  await expect(sizes).toContainText('3 consoles');

  // nothing is made while choosing
  const before = await consoles(hub, p.wid);
  const previewBefore = await panel.getByTestId('layout-preview').innerHTML();
  await sizes.getByRole('button', { name: 'More consoles in Builders' }).click();
  await expect(sizes).toContainText('4 consoles');
  await expect.poll(() => panel.getByTestId('layout-preview').innerHTML()).not.toBe(previewBefore);
  await expect(panel.getByRole('combobox', { name: 'Agent for lane b3' })).toBeVisible();
  expect((await consoles(hub, p.wid)).nodes.length).toBe(before.nodes.length);

  // back again, then the other way, and one group left out
  await sizes.getByRole('button', { name: 'Back to the template’s sizes' }).click();
  await expect(sizes).toContainText('3 consoles');
  await expect(panel.getByRole('combobox', { name: 'Agent for lane b3' })).toHaveCount(0);
  await sizes.getByRole('button', { name: 'Fewer consoles in Review' }).click();
  await expect(sizes).toContainText('2 consoles');
  await expect(panel.getByRole('combobox', { name: 'Agent for lane r1' })).toHaveCount(0);
  await sizes.getByRole('button', { name: 'More consoles in Builders' }).click();
  await expect(sizes).toContainText('3 consoles');

  await panel.getByRole('button', { name: 'Deploy Build & review' }).click();
  await expect(panel).toHaveCount(0, { timeout: 15_000 });
  const board = await consoles(hub, p.wid);
  const terms = board.nodes.filter((n) => n.kind === 'terminal');
  const groups = board.nodes.filter((n) => n.kind === 'group');
  expect(terms.length).toBe(3);
  expect(groups.map((g) => g.data.name)).toEqual(['Builders']);
  expect(groups[0].data.members?.length).toBe(3);
  // the wire to the group left out went with it, the one to Builders stayed
  expect(board.edges.length).toBe(1);

  // the template is as it was
  const all = await hub.call('GET', '/api/layouts');
  const t = all.find((l: { id: string }) => l.id === 'builtin:build-review');
  expect(t.lanes.length).toBe(3);
  expect(t.groups.length).toBe(2);
  for (const s of await hub.call('GET', '/api/sessions')) if (s.workspaceId === p.wid) await hub.call('DELETE', `/api/sessions/${s.id}`);
});

test('the limits show before the hub has to refuse', async ({ page, hub }, info) => {
  const p = await hub.project(`Limits ${info.project.name}`);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: 'Layouts' }).first().click();
  const panel = page.getByRole('dialog', { name: 'Layouts' });
  await panel.getByRole('textbox', { name: 'Search layouts' }).fill('Duo');
  await panel.getByRole('option', { name: /^Duo/ }).click();
  const sizes = panel.getByTestId('layout-sizes');
  // the only group of a layout stays
  await sizes.getByRole('button', { name: 'Fewer consoles in Duo' }).click();
  await expect(sizes).toContainText('1 console');
  await expect(sizes.getByRole('button', { name: 'Fewer consoles in Duo' })).toBeDisabled();
  // up to the most a deploy may make
  for (let i = 1; i < 12; i++) await sizes.getByRole('button', { name: 'More consoles in Duo' }).click();
  await expect(sizes).toContainText('12 consoles');
  await expect(sizes.getByRole('button', { name: 'More consoles in Duo' })).toBeDisabled();
  await page.keyboard.press('Escape');
});

// The library holds more now, so its search has to find them by what they are for.
test('the layout library finds templates by what they are for', async ({ page, hub }, info) => {
  const p = await hub.project(`Library ${info.project.name}`);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: 'Layouts' }).first().click();
  const panel = page.getByRole('dialog', { name: 'Layouts' });
  const library = panel.getByRole('listbox', { name: 'Layouts' });
  await expect(library.getByRole('option')).toHaveCount(44, { timeout: 10_000 });
  await panel.getByRole('textbox', { name: 'Search layouts' }).fill('laravel');
  await expect(library.getByRole('option')).toHaveCount(1);
  await library.getByRole('option', { name: /Laravel crew/ }).click();
  await expect(panel.getByText('Blade', { exact: false }).first()).toBeVisible();
  await panel.getByRole('textbox', { name: 'Search layouts' }).fill('timeline');
  await expect(library.getByRole('option', { name: /Incident room/ })).toHaveCount(1);
  await page.keyboard.press('Escape');
});

// The grey box of a preview is the board's Code card: deployed on a board that has one, that one
// moves into the layout's place (left where it was, the layout looked as if it had none).
test('a layout with the Code card moves the one the board has into place', async ({ page, hub }, info) => {
  const p = await hub.project(`Editor ${info.project.name}`);
  await hub.call('POST', `/api/workspaces/${p.wid}/board/ops`, {
    clientId: 'c_e2e',
    baseRev: 0,
    ops: [{ op: 'put', node: { id: 'n_editore2e01', kind: 'editor', x: 0, y: 0, w: 640, h: 400, data: {} } }],
  });
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: 'Layouts' }).first().click();
  const panel = page.getByRole('dialog', { name: 'Layouts' });
  await panel.getByRole('textbox', { name: 'Search layouts' }).fill('Code & pair');
  const option = panel.getByRole('option', { name: /Code & pair/ });
  await expect(option).toContainText('2 consoles · Code');
  await expect(option.locator('.pv-label')).toHaveText('Code');
  await option.click();
  await panel.getByRole('button', { name: 'Deploy Code & pair' }).click();
  await expect(panel).toHaveCount(0, { timeout: 15_000 });
  const board = await consoles(hub, p.wid);
  const editors = board.nodes.filter((n) => n.kind === 'editor');
  const terms = board.nodes.filter((n) => n.kind === 'terminal');
  expect(editors.length).toBe(1);
  expect(terms.length).toBe(2);
  // the editor sits where the preview drew it: left of the pair, as large as the layout says
  expect(editors[0].x).toBeGreaterThan(0);
  expect(editors[0].w).toBeGreaterThan(640);
  for (const t of terms) expect(t.x).toBeGreaterThan(editors[0].x + editors[0].w);
  for (const s of await hub.call('GET', '/api/sessions')) if (s.workspaceId === p.wid) await hub.call('DELETE', `/api/sessions/${s.id}`);
});
