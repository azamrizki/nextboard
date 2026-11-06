import type { Page } from '@playwright/test';
import { expect, test } from './hub';

// The board on a touch screen (DESIGN.md §3.5): everything a right-click or a key does is a tap or
// a long press away, two fingers move the board over any card, and any card can fill the screen.

const touch = (name: string) => ['phone', 'tablet-portrait', 'tablet-landscape'].includes(name);

async function finger(page: Page) {
  const cdp = await page.context().newCDPSession(page);
  const send = (type: string, points: { x: number; y: number }[]) => cdp.send('Input.dispatchTouchEvent', { type, touchPoints: points });
  return {
    async longPress(x: number, y: number) {
      await send('touchStart', [{ x, y }]);
      await page.waitForTimeout(700);
      await send('touchEnd', []);
    },
    /** Two fingers around (x, y), from `from` px apart to `to` px apart. */
    async pinch(x: number, y: number, from: number, to: number) {
      const at = (d: number) => [
        { x: x - d / 2, y },
        { x: x + d / 2, y },
      ];
      await send('touchStart', at(from));
      for (let i = 1; i <= 8; i++) await send('touchMove', at(from + ((to - from) * i) / 8));
      await send('touchEnd', []);
    },
  };
}

/** A point where nothing but the board is: on a phone the cards are small and anywhere. */
const emptySpot = (page: Page) =>
  page.evaluate(() => {
    const pane = document.querySelector('.react-flow__pane')!.getBoundingClientRect();
    for (let y = pane.top + 120; y < pane.bottom - 120; y += 24)
      for (let x = pane.left + 80; x < pane.right - 40; x += 24) if (document.elementFromPoint(x, y)?.classList.contains('react-flow__pane')) return { x, y };
    throw new Error('no empty spot on the board');
  });

/** A point on a line that no card covers. */
const onLine = (page: Page) =>
  page.locator('.react-flow__edge-interaction').first().evaluate((el) => {
    const path = el as SVGPathElement;
    const len = path.getTotalLength();
    for (let f = 0.05; f < 1; f += 0.05) {
      const pt = path.getPointAtLength(len * f).matrixTransform(path.getScreenCTM()!);
      if (document.elementFromPoint(pt.x, pt.y) === path) return { x: pt.x, y: pt.y };
    }
    throw new Error('the line is under the cards');
  });

const zoomOf = (page: Page) => page.evaluate(() => new DOMMatrix(getComputedStyle(document.querySelector('.react-flow__viewport')!).transform).a);

async function board(page: Page, hub: Parameters<Parameters<typeof test>[1]>[0]['hub'], name: string) {
  const p = await hub.project(name);
  const c = await hub.console(p.wid);
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_kanb000001', kind: 'kanban', x: 800, y: 40, w: 520, h: 420, data: {} } }]);
  await page.goto(`/p/${p.id}`);
  // a phone opens on its card list: these tests are about the canvas
  await expect(page.locator('.app-header')).toBeVisible();
  if ((await page.locator('html').getAttribute('data-layout')) === 'phone') await page.locator('.phone-tabs').getByRole('button', { name: 'Board' }).click();
  await expect(page.locator('.react-flow__node')).toHaveCount(2);
  await page.waitForTimeout(600);
  return { p, console: c };
}

test('every kind of card can be added with a tap', async ({ page, hub }, info) => {
  test.skip(!touch(info.project.name), 'touch screens');
  const { p } = await board(page, hub, 'Tap');
  await page.locator('.rail-left').getByRole('button', { name: 'Insert' }).click();
  const fly = page.locator('.rail-flyout-insert');
  for (const kind of ['Checklist', 'Kanban', 'Calendar', 'Credentials', 'Web page', 'Memory tile']) await expect(fly.getByRole('button', { name: kind })).toBeVisible();
  await fly.getByRole('button', { name: 'Checklist' }).click();
  await expect.poll(async () => (await hub.call('GET', `/api/workspaces/${p.wid}/board`)).nodes.some((n: { kind: string }) => n.kind === 'checklist')).toBe(true);
});

test('a long press does what a right-click does', async ({ page, hub }, info) => {
  test.skip(!touch(info.project.name), 'touch screens');
  await board(page, hub, 'Press');
  const f = await finger(page);
  // on the empty board: the ring
  const spot = await emptySpot(page);
  await f.longPress(spot.x, spot.y);
  await expect(page.locator('.ring')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('.ring')).toHaveCount(0);
  // on a card: its menu, and the lift that ends the press does not count as a tap
  const head = await page.locator('.react-flow__node').filter({ hasText: 'Kanban' }).locator('.tile-head').boundingBox();
  await f.longPress(head!.x + 40, head!.y + head!.height / 2);
  await expect(page.getByRole('menuitem', { name: 'Focus' })).toBeVisible();
});

test('a card fills the screen in focus mode, and the board is left as it was', async ({ page, hub }, info) => {
  const { p } = await board(page, hub, 'Focus');
  const before = await hub.call('GET', `/api/workspaces/${p.wid}/board`);
  const zoom = await zoomOf(page);
  const kanban = page.locator('.react-flow__node').filter({ hasText: 'Kanban' });
  if (touch(info.project.name)) await kanban.getByRole('button', { name: /^Focus/ }).click();
  else {
    await kanban.locator('.tile-head').click({ button: 'right' });
    await page.getByRole('menuitem', { name: 'Focus' }).click();
  }
  await expect(page).toHaveURL(/[?&]card=n_kanb000001/);
  const bar = page.getByRole('toolbar', { name: 'Focused card' });
  await expect(bar).toContainText('Kanban');
  // it fills the area under the bar, at 1:1, and the board's own controls step aside
  await expect.poll(() => zoomOf(page)).toBe(1);
  const area = await page.locator('.focus-area').boundingBox();
  const card = await kanban.boundingBox();
  expect(Math.abs(card!.width - area!.width)).toBeLessThan(2);
  expect(Math.abs(card!.height - area!.height)).toBeLessThan(2);
  await expect(page.locator('.rail-left')).toBeHidden();
  await expect(page.locator('.cmdbar-wrap')).toBeHidden();
  // the next card, then Back: straight to the board, not to the first card
  await bar.getByRole('button', { name: /^Previous card|^Next card/ }).and(page.locator(':enabled')).first().click();
  await expect(page).not.toHaveURL(/card=n_kanb000001/);
  await expect(page).toHaveURL(/[?&]card=/);
  await page.goBack();
  await expect(page).not.toHaveURL(/[?&]card=/);
  await expect(page.locator('.rail-left')).toBeVisible();
  await expect.poll(() => zoomOf(page)).toBeCloseTo(zoom, 2);
  // nothing was written: other screens see the board unchanged
  const after = await hub.call('GET', `/api/workspaces/${p.wid}/board`);
  const geo = (b: { nodes: { id: string; x: number; y: number; w: number; h: number }[] }) => b.nodes.map((n) => [n.id, n.x, n.y, n.w, n.h].join()).sort();
  expect(geo(after)).toEqual(geo(before));
  // Escape leaves it too, unless a field has the keyboard
  if (!touch(info.project.name)) {
    await kanban.locator('.tile-head').click({ button: 'right' });
    await page.getByRole('menuitem', { name: 'Focus' }).click();
    await expect(page).toHaveURL(/[?&]card=/);
    await page.locator('.focus-title').click();
    await page.keyboard.press('Escape');
    await expect(page).not.toHaveURL(/[?&]card=/);
  }
});

test('two fingers zoom the board over a console too', async ({ page, hub }, info) => {
  test.skip(!touch(info.project.name), 'touch screens');
  await board(page, hub, 'Pinch');
  const term = await page.locator('.react-flow__node').filter({ has: page.locator('.xterm') }).locator('.xterm').boundingBox();
  const zoom = await zoomOf(page);
  await (await finger(page)).pinch(term!.x + term!.width / 2, term!.y + term!.height / 2, 60, 180);
  expect(await zoomOf(page)).toBeGreaterThan(zoom * 1.5);
});

test('a tap on a line opens its menu, and several cards can be picked by tapping', async ({ page, hub }, info) => {
  test.skip(!['tablet-portrait', 'tablet-landscape'].includes(info.project.name), 'tablets');
  const p = await hub.project('Lines');
  const c = await hub.console(p.wid);
  // a memory card well away from the console, wired to it: the line between them is in the open
  await hub.ops(p.wid, [
    { op: 'put', node: { id: 'n_mem0000001', kind: 'memory', x: -1400, y: 0, w: 360, h: 380, data: { blocks: [] } } },
    { op: 'putEdge', edge: { id: 'e_wire000000', kind: 'wire', source: 'n_mem0000001', target: c.node, data: {} } },
  ]);
  await page.goto(`/p/${p.id}`);
  await expect(page.locator('.react-flow__edge')).toHaveCount(1);
  await page.waitForTimeout(600);
  const mid = await onLine(page);
  await page.touchscreen.tap(mid.x, mid.y);
  await expect(page.getByRole('menuitem', { name: 'Disconnect wire' })).toBeVisible();
  await page.keyboard.press('Escape');

  await page.locator('.rail-left').getByRole('button', { name: 'Insert' }).click();
  await page.locator('.rail-flyout-insert').getByRole('button', { name: 'Select several' }).click();
  await expect(page.locator('.pick-banner')).toContainText('Tap cards');
  for (const n of await page.locator('.react-flow__node').all()) {
    const head = await n.locator('.tile-head, .nb-node-header').first().boundingBox();
    await page.touchscreen.tap(head!.x + 24, head!.y + head!.height / 2);
  }
  await expect(page.locator('.pick-banner')).toContainText('2 selected');
  await page.locator('.pick-banner').getByRole('button', { name: 'Done' }).click();
  await expect(page.locator('.pick-banner')).toHaveCount(0);
});
