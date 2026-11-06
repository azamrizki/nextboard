import { expect, test } from './hub';

// A board on a phone (DESIGN.md §3.5): Board, Cards and Lead tabs; a card from the list fills the
// screen, a swipe goes to the next one, and Back returns to the list.

test.beforeEach(({}, info) => test.skip(info.project.name !== 'phone', 'phones'));

test('the card list opens a card, a swipe goes on, Back returns to the list', async ({ page, hub }) => {
  const p = await hub.project('Pocket');
  await hub.console(p.wid);
  // below the console, so it comes after it in reading order
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_kanb000001', kind: 'kanban', x: 0, y: 3000, w: 520, h: 420, data: {} } }]);
  await page.goto(`/p/${p.id}`);
  const tabs = page.locator('.phone-tabs');
  await tabs.getByRole('button', { name: 'Cards' }).click();
  const rows = page.locator('.card-row');
  await expect(rows).toHaveCount(2);
  await expect(rows.first()).toContainText('@');
  await expect(rows.nth(1)).toContainText('Kanban');

  await rows.first().click();
  await expect(page).toHaveURL(/[?&]card=/);
  const bar = page.getByRole('toolbar', { name: 'Focused card' });
  await expect(bar.getByRole('button', { name: 'Cards' })).toBeVisible();
  await expect(page.locator('.card-list')).toHaveCount(0);
  // the tab bar stays, below the card
  const card = await page.locator('.react-flow__node.is-focused').boundingBox();
  const tabBar = await tabs.boundingBox();
  expect(card!.y + card!.height).toBeLessThanOrEqual(tabBar!.y);

  // a swipe to the left over the bar: the next card
  const b = await bar.locator('.focus-title').boundingBox();
  const cdp = await page.context().newCDPSession(page);
  const y = b!.y + b!.height / 2;
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: b!.x + b!.width - 10, y }] });
  for (let i = 1; i <= 6; i++) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: b!.x + b!.width - 10 - i * 25, y }] });
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await expect(page).toHaveURL(/[?&]card=n_kanb000001/);
  await expect(bar).toContainText('Kanban');

  // Back: the list, not the first card
  await page.goBack();
  await expect(page).not.toHaveURL(/[?&]card=/);
  await expect(rows).toHaveCount(2);
});

test('a card added from the list lands on the board, and the tab is remembered', async ({ page, hub }) => {
  const p = await hub.project('Pocket');
  await page.goto(`/p/${p.id}`);
  const tabs = page.locator('.phone-tabs');
  await tabs.getByRole('button', { name: 'Cards' }).click();
  await expect(page.locator('.card-list-empty')).toBeVisible();
  await page.getByRole('button', { name: 'Add a card' }).click();
  await page.getByRole('dialog', { name: 'Add a card' }).getByRole('button', { name: 'Checklist' }).click();
  await expect(page.locator('.card-row')).toHaveCount(1);
  await expect.poll(async () => (await hub.call('GET', `/api/workspaces/${p.wid}/board`)).nodes.map((n: { kind: string }) => n.kind)).toEqual(['checklist']);

  // the Board tab: the canvas with a short rail, whose drawing tools went to Insert
  await tabs.getByRole('button', { name: 'Board' }).click();
  const rail = page.locator('.rail-left');
  await expect(rail.getByRole('button', { name: /^Pen/ })).toHaveCount(0);
  await rail.getByRole('button', { name: 'Insert' }).click();
  await expect(page.locator('.rail-flyout-insert').getByRole('button', { name: 'Pen' })).toBeVisible();
  // the grid ends above the tab bar
  const grid = await page.locator('.insert-tiles').boundingBox();
  expect(grid!.y + grid!.height).toBeLessThanOrEqual((await tabs.boundingBox())!.y);
  await rail.getByRole('button', { name: 'Insert' }).click();
  await expect(page.locator('.cmdbar-wrap')).toBeHidden();

  // the Lead tab: the thread fills the screen and has no close button
  await tabs.locator('.tab-bar-item').nth(2).click();
  await expect(page.getByRole('region', { name: 'Thread' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Close the thread' })).toHaveCount(0);
  await expect(page.locator('.rail-left')).toBeHidden();

  // this phone opens on the tab it showed last
  await page.reload();
  await expect(page.getByRole('region', { name: 'Thread' })).toBeVisible();
});
