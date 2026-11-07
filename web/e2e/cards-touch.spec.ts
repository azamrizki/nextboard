import type { Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';

// Every card by finger (DESIGN.md §3.5): what a mouse does by hover, double click or drag and
// drop, a touch screen does by tap, long press or a grip. Some of it is for every screen: Rename
// in a card's menu, Straighten for a bent line, a calendar day's full list, and a link to a card.

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
    /** From one point to another; `hold` ms still at the end before the lift. */
    async drag(from: { x: number; y: number }, to: { x: number; y: number }, steps = 12, hold = 0) {
      await send('touchStart', [from]);
      for (let i = 1; i <= steps; i++) {
        await send('touchMove', [{ x: from.x + ((to.x - from.x) * i) / steps, y: from.y + ((to.y - from.y) * i) / steps }]);
        await page.waitForTimeout(16);
      }
      if (hold) await page.waitForTimeout(hold);
      await send('touchEnd', []);
    },
  };
}

const centre = async (page: Page, selector: string) => {
  const b = (await page.locator(selector).first().boundingBox())!;
  return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
};

type Node = { id: string; kind: string; x: number; y: number; w: number; h: number; data: Record<string, any> };
const nodeOf = async (hub: Hub, wid: string, id: string): Promise<Node> => (await hub.call('GET', `/api/workspaces/${wid}/board`)).nodes.find((n: Node) => n.id === id);

/** A board with one card, shown in focus mode (1:1, so a finger lands where it means to). */
async function focused(page: Page, hub: Hub, name: string, node: Omit<Node, 'x' | 'y'>) {
  const p = await hub.project(name);
  await hub.ops(p.wid, [{ op: 'put', node: { x: 0, y: 0, ...node } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=${node.id}`);
  await expect(page.locator('.react-flow__node.is-focused')).toBeVisible();
  await page.waitForTimeout(400);
  return p;
}

const kanban = (n: number) => ({
  id: 'n_kanb000001',
  kind: 'kanban',
  w: 720,
  h: 460,
  data: {
    columns: [
      { id: 'c1', title: 'To do', cards: Array.from({ length: n }, (_, i) => ({ id: `a${i}`, title: `Task ${i + 1}` })) },
      { id: 'c2', title: 'Doing', cards: [] },
    ],
  },
});

test('kanban: a finger scrolls the cards, the grip moves one, a long press opens its menu', async ({ page, hub }, info) => {
  test.skip(!touch(info.project.name), 'touch screens');
  const p = await focused(page, hub, 'Grip', kanban(40));
  const f = await finger(page);
  const list = page.locator('.kb-col').first().locator('.kb-cards');

  // a swipe up over a card scrolls its column, and moves nothing
  const card = await centre(page, '.kb-card');
  await f.drag(card, { x: card.x, y: card.y - 220 });
  await expect.poll(() => list.evaluate((el) => el.scrollTop)).toBeGreaterThan(40);
  expect((await nodeOf(hub, p.wid, 'n_kanb000001')).data.columns[1].cards).toHaveLength(0);
  // the swipe may still be flinging: back at the top only once it stays there for two frames,
  // or the grip below is measured on a card that is still moving
  await expect
    .poll(() =>
      list.evaluate((el) => {
        el.scrollTop = 0;
        return new Promise<number>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done(el.scrollTop))));
      }),
    )
    .toBe(0);

  // the "…" of a card is there without a hover
  expect(await page.locator('.kb-card-menu').first().evaluate((el) => getComputedStyle(el).opacity)).toBe('1');

  // the grip drags the first card into the other column; on a phone that column is off to the
  // right, and holding the card at the edge brings it over
  const grip = await centre(page, '.kb-card .kb-grip');
  const target = await centre(page, '.kb-col:nth-child(2) .kb-cards');
  const width = page.viewportSize()!.width;
  await f.drag(grip, { x: Math.min(target.x, width - 12), y: grip.y + 60 }, 16, target.x > width - 12 ? 1500 : 0);
  await expect.poll(async () => (await nodeOf(hub, p.wid, 'n_kanb000001')).data.columns[1].cards.map((c: { id: string }) => c.id)).toEqual(['a0']);

  // a long press on a card: that card's menu, not the board's
  const next = await centre(page, '.kb-col:nth-child(1) .kb-card-main');
  await f.longPress(next.x, next.y);
  await expect(page.getByRole('menuitem', { name: 'Mark as done' })).toBeVisible();
  await expect(page.getByRole('menuitem', { name: 'Focus' })).toHaveCount(0);
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('checklist: a tap edits an item, and its remove button is there', async ({ page, hub }, info) => {
  test.skip(!touch(info.project.name), 'touch screens');
  const p = await focused(page, hub, 'Tick', {
    id: 'n_chk0000001',
    kind: 'checklist',
    w: 320,
    h: 360,
    data: { title: 'List', items: [{ id: 'i1', text: 'Buy milk' }, { id: 'i2', text: 'Call home' }] },
  });
  await page.locator('.check-text', { hasText: 'Buy milk' }).tap();
  const input = page.getByRole('textbox', { name: 'Item text' });
  await expect(input).toBeFocused();
  await input.fill('Buy oat milk');
  await input.press('Enter');
  await expect.poll(async () => (await nodeOf(hub, p.wid, 'n_chk0000001')).data.items[0].text).toBe('Buy oat milk');

  const remove = page.getByRole('button', { name: 'Remove Call home' });
  expect(await remove.evaluate((el) => getComputedStyle(el).opacity)).toBe('1');
  await remove.tap();
  await expect.poll(async () => (await nodeOf(hub, p.wid, 'n_chk0000001')).data.items).toHaveLength(1);
});

test('memory tile: bricks are reordered by their grip', async ({ page, hub }, info) => {
  test.skip(!touch(info.project.name), 'touch screens');
  const p = await focused(page, hub, 'Stack', { id: 'n_mem0000001', kind: 'memory', w: 360, h: 380, data: { blocks: [{ name: 'alpha' }, { name: 'beta' }, { name: 'gamma' }] } });
  const grips = page.locator('.memory-grip');
  await expect(grips).toHaveCount(3);
  const from = await centre(page, '.memory-brick[data-key]:nth-of-type(4) .memory-grip');
  const to = await centre(page, '.memory-brick[data-key]:nth-of-type(2)');
  await (await finger(page)).drag(from, to);
  await expect.poll(async () => (await nodeOf(hub, p.wid, 'n_mem0000001')).data.blocks.map((b: { name: string }) => b.name)).toEqual(['gamma', 'alpha', 'beta']);
});

test('calendar: every event of a day fits the screen, and "more" lists them all', async ({ page, hub }, info) => {
  const day = `${new Date().toISOString().slice(0, 7)}-14`;
  const events = ['One', 'Two', 'Three', 'Four', 'Five', 'Six', 'Seven', 'Eight', 'Nine'].map((title, i) => ({ id: `e${i}`, title, date: day }));
  await focused(page, hub, 'Days', { id: 'n_cal0000001', kind: 'calendar', w: 560, h: 460, data: { events } });
  // the card's own focus and size buttons step aside for the bar above it
  await expect(page.locator('.react-flow__node.is-focused .tile-focus')).toBeHidden();
  // a narrow calendar (a phone) has its month controls on a row of their own
  if (info.project.name === 'phone') await expect(page.locator('.cal-bar')).toBeVisible();
  const cell = page.locator(`.cal-day[data-date="${day}"]`);
  // as many as the cell has room for at the size it is shown, not at its size on the board
  const shown = await cell.locator('.cal-ev').count();
  const room = await cell.evaluate((el) => Math.floor((el.clientHeight - 24) / 20));
  expect(shown).toBeGreaterThanOrEqual(Math.min(8, room - 1));
  await cell.locator('.cal-more').click();
  const list = page.getByRole('dialog');
  await expect(list.locator('.cal-day-row')).toHaveCount(9);
  await list.getByRole('button', { name: /Seven/ }).click();
  await expect(page.getByRole('dialog', { name: 'Edit event' })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Title' })).toHaveValue('Seven');
});

test('Rename is in a card menu, and a link opens a card', async ({ page, hub }) => {
  const p = await hub.project('Names');
  await hub.ops(p.wid, [{ op: 'put', node: { x: 0, y: 0, ...kanban(1) } }]);
  // the short link of a project goes on to its workspace with the card
  await page.goto(`/p/${p.id}?card=n_kanb000001`);
  await expect(page).toHaveURL(new RegExp(`/w/${p.wid}\\?card=n_kanb000001`));
  await expect(page.getByRole('toolbar', { name: 'Focused card' })).toContainText('Kanban');

  await page.getByRole('button', { name: 'Actions for Kanban' }).click();
  await page.getByRole('menuitem', { name: 'Rename' }).click();
  const input = page.getByRole('textbox', { name: 'Board title' });
  await expect(input).toBeFocused();
  await input.fill('Sprint');
  await input.press('Enter');
  await expect.poll(async () => (await nodeOf(hub, p.wid, 'n_kanb000001')).data.title).toBe('Sprint');
});

test('a bent line has Straighten in its menu', async ({ page, hub }, info) => {
  test.skip(info.project.name === 'phone', 'the board itself: desktop and tablets');
  const p = await hub.project('Bend');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_line000001', kind: 'line', x: 0, y: 0, w: 400, h: 200, data: { points: [[0, 200], [100, 0], [400, 200]] } } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await expect(page.locator('.react-flow__node')).toHaveCount(1);
  await page.waitForTimeout(600);
  const at = await page.locator('.react-flow__node-line path').last().evaluate((el) => {
    const path = el as SVGPathElement;
    const len = path.getTotalLength();
    for (let f = 0.2; f < 0.8; f += 0.05) {
      const pt = path.getPointAtLength(len * f).matrixTransform(path.getScreenCTM()!);
      if (document.elementFromPoint(pt.x, pt.y)?.closest('.react-flow__node-line')) return { x: pt.x, y: pt.y };
    }
    throw new Error('no point on the line');
  });
  if (touch(info.project.name)) await (await finger(page)).longPress(at.x, at.y);
  else await page.mouse.click(at.x, at.y, { button: 'right' });
  await page.getByRole('menuitem', { name: 'Straighten' }).click();
  await expect
    .poll(async () => {
      const pts = (await nodeOf(hub, p.wid, 'n_line000001')).data.points as number[][];
      return Math.round(pts[1][0] - (pts[0][0] + pts[2][0]) / 2) === 0 && Math.round(pts[1][1] - (pts[0][1] + pts[2][1]) / 2) === 0;
    })
    .toBe(true);
});
