// The Code view and the Board view are one board shown two ways (DESIGN.md §3.19): an agent started
// in the Code view is a console on the board, groups and orchestrator links made in its Agents
// list are the board's, and the board's cards (Kanban, the orchestrator…) open as its tabs.
import { expect, test } from './hub';

type Doc = { nodes: { id: string; kind: string; data: Record<string, unknown> }[]; edges: { kind: string; source: string; target: string }[] };

test('agents, groups, links and cards made in the Code view are the board’s', async ({ page, hub }) => {
  test.setTimeout(120_000);
  const p = await hub.project('Parity');
  const board = () => hub.call<Doc>('GET', `/api/workspaces/${p.wid}/board`);
  const count = async (kind: string) => (await board()).nodes.filter((n) => n.kind === kind).length;
  await page.goto(`/p/${p.id}/code`);

  // two agents from the Code view: two consoles on the board, each as a tab here
  const agents = page.getByRole('tab', { name: /^Agents/ });
  for (const want of [1, 2]) {
    await agents.click();
    await page.getByRole('button', { name: 'New agent' }).click();
    await page.getByRole('menuitem', { name: 'Fake Claude' }).click();
    await expect.poll(() => count('terminal'), { timeout: 15_000 }).toBe(want);
  }

  // a group of both, from the Agents list
  await agents.click();
  await page.getByRole('button', { name: 'New group' }).click();
  const dlg = page.getByRole('dialog', { name: 'New group' });
  await dlg.getByLabel('Name').fill('Builders');
  for (const c of await dlg.getByRole('checkbox').all()) await c.check();
  await dlg.getByRole('button', { name: 'Make group' }).click();
  const group = page.getByRole('group', { name: 'Group Builders' });
  await expect(group.getByRole('listitem')).toHaveCount(2);
  await expect
    .poll(async () => (await board()).nodes.find((n) => n.kind === 'group')?.data.members)
    .toEqual((await board()).nodes.filter((n) => n.kind === 'terminal').map((n) => n.id));

  // an orchestrator card and a Kanban from the Cards list, each opening as a tab
  for (const kind of ['Orchestrator', 'Kanban']) {
    await page.getByRole('tab', { name: 'Cards' }).click();
    await page.getByRole('button', { name: 'New card' }).click();
    await page.getByRole('menuitem', { name: kind }).click();
    await expect(page.getByRole('tab', { name: kind, exact: true }).or(page.locator('.wb-tab', { hasText: kind })).first()).toBeVisible();
  }
  await expect.poll(() => count('orchestrator')).toBe(1);
  await expect.poll(() => count('kanban')).toBe(1);

  // the Kanban in its tab is the board's: a card added here is in the board's document
  const kanban = page.locator('.card-tab[data-card]').filter({ has: page.getByText('Backlog') });
  await kanban.getByRole('button', { name: 'Add card to Backlog' }).click();
  await kanban.getByLabel('New card in Backlog').fill('Ship the parity');
  await kanban.getByLabel('New card in Backlog').press('Enter');
  await expect.poll(async () => JSON.stringify((await board()).nodes.find((n) => n.kind === 'kanban')?.data)).toContain('Ship the parity');

  // the group takes the orchestrator's orders: a wire from the card to the group
  await agents.click();
  await group.getByRole('button', { name: 'Actions for group Builders' }).click();
  await page.getByRole('menuitemcheckbox', { name: 'Orchestrator' }).or(page.getByRole('menuitem', { name: 'Orchestrator' })).first().click();
  await expect(group.getByText('Linked to Orchestrator')).toBeVisible();
  await expect
    .poll(async () => {
      const doc = await board();
      const g = doc.nodes.find((n) => n.kind === 'group')!;
      const o = doc.nodes.find((n) => n.kind === 'orchestrator')!;
      return doc.edges.some((e) => e.kind === 'wire' && e.source === o.id && e.target === g.id);
    })
    .toBe(true);

  // a console's tab shows its group and who directs it, and has the tile's menu
  await group.getByRole('button', { name: /^Show|^Open .* as a tab/ }).first().click();
  const bar = page.locator('.wb-console-bar').filter({ visible: true });
  await expect(bar.getByText('Builders')).toBeVisible();
  await expect(bar.getByText('Directed by Orchestrator')).toBeVisible();
  await bar.getByRole('button', { name: /^Console menu of / }).click();
  for (const entry of ['Hand off…', 'Restart console', 'Remove from Builders']) await expect(page.getByRole('menuitem', { name: entry })).toBeVisible();
  // the console appearing does not take the keyboard from its open menu
  await page.keyboard.press('Escape');
  await expect(page.getByRole('menuitem', { name: 'Hand off…' })).toHaveCount(0);

  // the same board in the Board view: the group frame, the wire and the cards
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await expect(page.locator('.react-flow__node-nbgroup')).toHaveCount(1);
  await expect(page.locator('.react-flow__node-terminal')).toHaveCount(2);
  await expect(page.locator('.react-flow__node-kanban').getByText('Ship the parity')).toBeVisible();
});

test('the Code view shows the board picked in its top bar', async ({ page, hub }) => {
  const p = await hub.project('Two boards');
  const second = await hub.call<{ id: string }>('POST', `/api/projects/${p.id}/workspaces`, { name: 'Ops' });
  const { rev } = await hub.call<{ rev: number }>('GET', `/api/workspaces/${second.id}/board`);
  await hub.call('POST', `/api/workspaces/${second.id}/board/ops`, {
    clientId: 'c_e2e',
    baseRev: rev,
    ops: [{ op: 'put', node: { id: 'n_opstasks1', kind: 'kanban', x: 0, y: 0, w: 900, h: 500, z: 0, data: { title: 'Ops tasks' } } }],
  });
  await page.goto(`/p/${p.id}/code`);
  const picker = page.locator('header .cv-board');
  await expect(picker).toHaveText('Main');
  await picker.click();
  await page.getByRole('menuitemcheckbox', { name: 'Ops' }).or(page.getByRole('menuitem', { name: 'Ops' })).first().click();
  await expect(picker).toHaveText('Ops');
  // its cards are the other board's
  await page.getByRole('tab', { name: 'Cards' }).click();
  await page.getByRole('button', { name: 'Open Ops tasks as a tab' }).click();
  await expect(page.locator('.card-tab[data-card="n_opstasks1"]')).toBeVisible();
});
