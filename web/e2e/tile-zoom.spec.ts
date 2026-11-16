import type { Locator, Page } from '@playwright/test';
import { expect, test } from './hub';
import { endConsoles } from './two-parts';

// A double click on a tile's header zooms the board to that tile (v0.10.0), on every kind of
// card: consoles, groups, the Code editor's title bar, and the cards with a common header. A
// double click on a header button or on a name to rename stays theirs.

const viewport = (page: Page) => page.locator('.react-flow__viewport').getAttribute('style');

/** A spot of the header that is neither a button nor the name: hovering shows the buttons first. */
async function emptySpot(page: Page, head: Locator): Promise<{ x: number; y: number }> {
  const box = (await head.boundingBox())!;
  await page.mouse.move(box.x + 4, box.y + 4);
  await page.waitForTimeout(250);
  const spot = await page.evaluate(([bx, by, bw, bh]) => {
    for (let f = 0.25; f < 0.95; f += 0.02) {
      const x = bx + bw * f;
      const y = by + Math.min(10, bh / 2);
      const el = document.elementFromPoint(x, y) as HTMLElement | null;
      if (el && !el.closest('.nodrag, button, input, .tile-title, .node-title, .group-name, .wb-command-center')) return { x, y };
    }
    return null;
  }, [box.x, box.y, box.width, box.height]);
  expect(spot, 'an empty spot on the header').not.toBeNull();
  return spot!;
}

test('a double click on a tile header zooms to the tile', async ({ page, hub }) => {
  test.slow();
  const p = await hub.project('Zoom');
  const c = await hub.console(p.wid);
  await hub.ops(p.wid, [
    { op: 'put', node: { id: 'n_zoomedit01', kind: 'editor', x: 1600, y: 0, w: 900, h: 600, data: {} } },
    { op: 'put', node: { id: 'n_zoomkanb01', kind: 'kanban', x: 0, y: 1000, w: 600, h: 400, data: {} } },
    { op: 'put', node: { id: 'n_zoomorch01', kind: 'orchestrator', x: 900, y: 1000, w: 500, h: 500, data: {} } },
    { op: 'put', node: { id: 'n_zoommem001', kind: 'memory', x: 1600, y: 1000, w: 360, h: 380, data: { blocks: [] } } },
    { op: 'put', node: { id: 'n_zoomchk001', kind: 'checklist', x: 2200, y: 1000, w: 360, h: 300, data: { title: 'Ship', items: [] } } },
    { op: 'put', node: { id: 'n_zoomgrp001', kind: 'group', x: -900, y: -200, w: 700, h: 500, data: { name: 'Crew', role: 'general', color: '', members: [c.node] } } },
  ]);
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    await expect(page.locator(`.react-flow__node[data-id="n_zoomedit01"] .wb-title`)).toBeVisible({ timeout: 20_000 });
    const heads: [string, string][] = [
      [c.node, '.nb-node-header'],
      ['n_zoomedit01', '.wb-title'],
      ['n_zoomkanb01', '.tile-head'],
      ['n_zoomorch01', '.tile-head'],
      ['n_zoommem001', '.tile-head'],
      ['n_zoomchk001', '.tile-head'],
      ['n_zoomgrp001', '.group-head'],
    ];
    for (const [id, sel] of heads) {
      await page.keyboard.press('Escape');
      await page.locator('.react-flow__pane').click({ position: { x: 5, y: 200 } });
      await page.keyboard.press('Shift+1');
      await page.waitForTimeout(500);
      const before = await viewport(page);
      const head = page.locator(`.react-flow__node[data-id="${id}"] ${sel}`).first();
      const { x, y } = await emptySpot(page, head);
      await page.mouse.dblclick(x, y);
      await expect.poll(() => viewport(page), { message: `zoom to ${id}` }).not.toBe(before);
      // centred on the tile
      await page.waitForTimeout(400);
      const tile = (await page.locator(`.react-flow__node[data-id="${id}"]`).boundingBox())!;
      const canvas = (await page.locator('.react-flow').boundingBox())!;
      expect(Math.abs(tile.y + tile.height / 2 - (canvas.y + canvas.height / 2)), `${id} centred`).toBeLessThan(canvas.height * 0.2);
    }

    // a double click on a header button stays the button's: the board does not move
    await page.keyboard.press('Shift+1');
    await page.waitForTimeout(500);
    const still = await viewport(page);
    const menuBtn = page.locator('.react-flow__node[data-id="n_zoomkanb01"] .tile-head button').last();
    await menuBtn.dblclick();
    await page.keyboard.press('Escape');
    await page.waitForTimeout(500);
    expect(await viewport(page)).toBe(still);

    // and a double click on a console's name still renames it
    await page.locator(`.react-flow__node[data-id="${c.node}"] .node-title > span`).first().dblclick();
    await expect(page.locator(`.react-flow__node[data-id="${c.node}"] input`).first()).toBeVisible();
    await page.keyboard.press('Escape');
  } finally {
    await endConsoles(hub, p.wid);
  }
});
