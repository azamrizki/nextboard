import { expect, test } from './hub';

// An agent draws a diagram on the board with the hub's board_diagram tool (docs/API-studio.md §6):
// a mistake comes back in words, a good one lands beside its console as shapes and shows in the
// Diagrams list at once, by the console's handle, with nothing reloaded.

const checkout = {
  title: 'Checkout flow',
  direction: 'LR',
  nodes: [
    { id: 'cart', label: 'Cart page', shape: 'round' },
    { id: 'pay', label: 'Payment API', note: 'card and bank', shape: 'box' },
    { id: 'ok', label: 'Paid?', shape: 'diamond' },
    { id: 'done', label: 'Order saved', shape: 'box' },
  ],
  edges: [
    { from: 'cart', to: 'pay', label: 'POST /pay' },
    { from: 'pay', to: 'ok' },
    { from: 'ok', to: 'done', label: 'yes' },
    { from: 'ok', to: 'cart', label: 'no', style: 'dashed' },
  ],
};

test('an agent draws a diagram next to its tile, and the Diagrams panel lists it', async ({ page, hub }) => {
  const p = await hub.project('Diagram tool');
  const c = await hub.console(p.wid);
  const call = await hub.tools(c.id, p.path);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: 'Studios', exact: true }).first().click();
  await page.getByRole('menuitem', { name: 'Diagrams' }).click();
  const panel = page.getByRole('complementary', { name: 'Diagrams' });
  await expect(panel.getByText('None yet.')).toBeVisible();

  const wrong = await call('board_diagram', { diagram: { ...checkout, edges: [{ from: 'cart', to: 'shipping' }] } });
  expect(wrong.isError).toBe(true);
  expect(wrong.text).toContain('invalid diagram: edge cart→shipping points at a node that does not exist. Fix it and call board_diagram again.');

  const drawn = await call('board_diagram', { diagram: checkout });
  expect(drawn.isError, drawn.text).toBe(false);
  expect(drawn.text).toContain('Drew "Checkout flow" on the board');
  expect(drawn.text).toContain('next to your tile: 4 boxes and 4 arrows');

  // the shapes, right of the console's tile
  const board = await hub.call('GET', `/api/workspaces/${p.wid}/board`);
  const tile = board.nodes.find((n: any) => n.id === c.node);
  const mine = board.nodes.filter((n: any) => typeof n.data.diagramId === 'string' && n.data.diagramId.startsWith('dg_'));
  expect(mine.length).toBeGreaterThan(8);
  expect(Math.min(...mine.map((n: any) => n.x))).toBeGreaterThanOrEqual(tile.x + tile.w);
  const box = mine.find((n: any) => n.data.diagramKey === 'node:pay');
  await expect(page.locator(`.react-flow__node[data-id="${box.id}"]`)).toContainText('Payment API');
  await expect(page.locator('.react-flow__node .shape-label', { hasText: 'Order saved' })).toHaveCount(1);

  // listed without a reload, by the console that drew it
  const session = await hub.call('GET', `/api/sessions/${c.id}`);
  const row = panel.locator('.diagram-row', { hasText: 'Checkout flow' });
  await expect(row).toBeVisible();
  await expect(row).toContainText(`@${session.handle}`);
  await expect(row.getByRole('button', { name: 'Export PNG' })).toBeVisible();
});
