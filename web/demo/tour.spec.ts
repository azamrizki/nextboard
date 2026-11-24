import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import { OUT } from './playwright.config';
import { Recorder } from './record';
import { OVERLAY, assertPrivate, call, caption, click, consoles, glide, hideTitleCard, nextcodeBoard, openShop, platformBoard, point, projects, releaseBoard, seedVault, shopBoard, titleCard, type } from './demo';

// The product video: about a minute and a half through the demo hub, in the dark theme, recorded
// from the page. Its story: a project in three parts, opened as a board, work sent with an
// @mention, the agent at work, its report in the thread, its change reviewed in the code card;
// then a glance at the rest: a team from a template, consoles at the project's root and in its
// parts, Mission Map, NextCode planning and making a change, the Code view and its databases,
// memory and the vault. Captions and the cursor are drawn into the page (demo.ts); everything
// else is the real UI.

// Chrome sends the frames in CSS pixels, whatever the device scale
const W = 1600;
const H = 900;

test.use({ viewport: { width: W, height: H }, colorScheme: 'dark' });

test('product tour', async ({ page, request }) => {
  test.setTimeout(600_000);
  const shop = projects()['acme-shop'];
  await shopBoard(request, shop);
  const release = await releaseBoard(request, shop);
  const platform = await platformBoard(request, shop);
  const nc = await nextcodeBoard(request, shop);
  const tile = (id: string) => page.locator(`.react-flow__node[data-id="${id}"]`);
  const head = (id: string) => tile(id).locator('.nb-node-header, .tile-head').first();
  const nodes = await consoles(request, shop.wid);
  const sessions: { agent: string; handle: string }[] = await call(request, 'GET', '/api/sessions');
  const tester = sessions.find((s) => s.agent === 'tester')!.handle;
  const fit = () => click(page, page.getByRole('button', { name: 'Fit to content' }), { ms: 450 });

  // unrecorded: the storefront in its tile, and the vault with its logins, left unlocked in
  // this tab (every later move is inside the app, so it stays so)
  await page.addInitScript(OVERLAY);
  await page.goto(`/p/${shop.id}/w/${shop.wid}`);
  await openShop(page);
  await page.getByRole('button', { name: /^Credentials/ }).first().click();
  await seedVault(page);
  await page.keyboard.press('Escape');
  for (const id of Object.values(nodes)) await expect(tile(id)).toContainText('demo agent', { timeout: 15_000 });
  await expect.poll(async () => (await call(request, 'GET', `/api/sessions/${nc.sid}`)).agentState, { timeout: 20_000 }).toBe('ready');
  await page.getByRole('link', { name: 'All projects' }).first().click();
  await expect(page.getByText('acme-shop', { exact: true }).first()).toBeVisible();
  await page.waitForTimeout(800);
  await assertPrivate(page);

  mkdirSync(OUT, { recursive: true });
  const rec = new Recorder(page, OUT);
  await rec.start(W, H);

  // 1. title
  await titleCard(page, 'NextBoard', 'Your agents. Your machines. One board.');
  await page.waitForTimeout(2300);
  await hideTitleCard(page);
  await page.waitForTimeout(400);

  // 2. projects, one of them in three parts
  await caption(page, 'Your projects, on a hub you run yourself', 'acme-shop is one project in three parts: the storefront here, its API and worker on servers');
  const shopCard = page.locator(`.project-card[data-project="${shop.id}"]`).first();
  await click(page, shopCard.locator('.project-partsline'), { ms: 800 });
  await expect(page.getByRole('list', { name: 'Parts of acme-shop' })).toBeVisible();
  await page.waitForTimeout(1900);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);
  await click(page, shopCard.locator('.project-open'), { ms: 600 });
  // the first time, it asks: a board or a code editor
  const choice = page.getByRole('dialog', { name: 'Open “acme-shop”' });
  if (await choice.waitFor({ timeout: 3000 }).then(() => true, () => false)) {
    await caption(page, 'Open it as a board, or as a code editor', 'Both show the same agents, groups and cards');
    await page.waitForTimeout(900);
    await click(page, choice.getByRole('radio', { name: /Code view/ }), { ms: 500 });
    await page.waitForTimeout(500);
    await click(page, choice.getByRole('radio', { name: /Board view/ }), { ms: 450 });
    await page.waitForTimeout(300);
    await click(page, choice.getByRole('button', { name: 'Open in Board view' }), { ms: 450 });
  }

  // 3. the board
  for (const id of Object.values(nodes)) await expect(tile(id)).toContainText('demo agent', { timeout: 15_000 });
  await page.waitForTimeout(500);
  await caption(page, 'Every agent on one canvas', 'Working, needs you or idle, without opening each terminal');
  for (const id of [nodes.builder, nodes.reviewer, nodes.tester]) {
    await point(page, head(id), 500, 0.85, 0.5);
    await page.waitForTimeout(350);
  }

  // 4. answer the reviewer from its tile
  await caption(page, 'Answer an agent right in its tile');
  await click(page, head(nodes.reviewer), { double: true, dx: 0.4 });
  await page.waitForTimeout(900);
  await click(page, tile(nodes.reviewer).locator('.xterm-screen'), { dx: 0.3, dy: 0.5 });
  await page.waitForTimeout(300);
  await page.keyboard.press('1');
  await expect(tile(nodes.reviewer)).toContainText('Applied.');
  await page.waitForTimeout(1200);

  // 5. hand out work with a mention (from here to the report is the README's GIF)
  await fit();
  await page.waitForTimeout(500);
  await caption(page, 'Hand out work with @mentions', 'Or ask the lead, Aruna, to plan it and split it across the team');
  await page.waitForTimeout(300);
  const clipFrom = rec.mark();
  const bar = page.getByRole('combobox', { name: 'Order for your agents' });
  await click(page, bar);
  await type(page, `@${tester.slice(0, 2)}`, 80);
  await expect(page.getByRole('listbox', { name: 'Direct to' })).toBeVisible();
  await page.waitForTimeout(500);
  await page.keyboard.press('Tab');
  await type(page, 'add coupon codes to checkout, with a test for an expired code', 28);
  await page.waitForTimeout(300);
  await page.keyboard.press('Enter');
  const thread = page.getByRole('region', { name: 'Thread' });
  await expect(thread.getByText(`Sent to @${tester}`, { exact: false }).first()).toBeVisible();
  await page.waitForTimeout(1300);
  await click(page, page.getByRole('button', { name: 'Close the thread' }).first(), { ms: 450 });

  // 6. watch it work
  await caption(page, 'Agents work in real terminals on your machine', 'And keep running after you close the browser');
  await click(page, head(nodes.tester), { double: true, dx: 0.4 });
  await expect(tile(nodes.tester)).toContainText('46 tests pass', { timeout: 30_000 });
  await page.waitForTimeout(900);

  // 7. its report, in the thread
  await caption(page, 'Its report comes back to the thread');
  await click(page, page.getByRole('button', { name: /^Open the thread/ }).first(), { ms: 500 });
  await expect(thread.getByRole('button', { name: /^See output/ })).toHaveCount(1, { timeout: 20_000 });
  await page.waitForTimeout(2600);
  const clipTo = rec.mark();

  // 8. review the change in the code card
  await click(page, page.getByRole('button', { name: 'Close the thread' }).first(), { ms: 450 });
  await caption(page, 'Review what each agent changed', 'In a full code editor: diffs, git, Local History, language servers');
  await click(page, page.getByRole('button', { name: 'Code editor' }));
  // acme-shop has three parts: the editor asks where it shows, Root or a part; the storefront's
  const place = page.getByRole('dialog', { name: 'Open the code editor' });
  if (await place.waitFor({ timeout: 3000 }).then(() => true, () => false)) {
    await click(page, place.getByRole('radio', { name: 'Storefront' }), { ms: 450 });
    await click(page, place.getByRole('button', { name: /the editor$/ }), { ms: 450 });
  }
  const review = page.getByRole('button', { name: 'Review', exact: true });
  await expect(review).toBeVisible({ timeout: 20_000 });
  await page.waitForTimeout(500);
  await click(page, review, { ms: 450 });
  const changed = page.locator('.wb-side').getByText('checkout.ts', { exact: true }).first();
  await expect(changed).toBeVisible({ timeout: 15_000 });
  await page.waitForTimeout(400);
  await click(page, changed, { ms: 450 });
  await expect(page.getByRole('button', { name: 'Keep file' })).toBeVisible({ timeout: 15_000 });
  await page.waitForTimeout(2000);

  // 9. a team from a template
  await caption(page, 'Start a whole team from a template', 'Each group in a part of its own, and as many agents as it needs');
  await click(page, page.getByRole('button', { name: 'Quick actions' }));
  await click(page, page.getByRole('menu', { name: 'Quick actions' }).getByRole('menuitem', { name: 'Layouts' }));
  const layouts = page.getByRole('dialog', { name: 'Layouts' });
  await expect(layouts).toBeVisible();
  // behind the dialog meanwhile: the code card goes, so the board fits as it was
  const board = await call(request, 'GET', `/api/workspaces/${shop.wid}/board`);
  const editors = board.nodes.filter((n: { kind: string }) => n.kind === 'editor').map((n: { id: string }) => ({ op: 'del', id: n.id }));
  if (editors.length) await call(request, 'POST', `/api/workspaces/${shop.wid}/board/ops`, { clientId: 'c_demo', baseRev: 0, ops: editors });
  await page.waitForTimeout(300);
  await click(page, layouts.getByRole('option', { name: /Front & back/ }), { ms: 500 });
  await page.waitForTimeout(350);
  // each group at the root or in a part of its own (a select: its list is not drawn headless)
  for (const [group, part] of [['Frontend', 'Storefront'], ['Backend', 'API']]) {
    const where = layouts.getByLabel(`Place ${group} in`);
    if (!(await where.count())) break;
    await point(page, where, 400);
    await where.selectOption({ label: part });
    await page.waitForTimeout(350);
  }
  const more = layouts.getByRole('button', { name: /^More consoles in / }).first();
  await click(page, more, { ms: 500 });
  await page.waitForTimeout(350);
  await click(page, more, { ms: 150 });
  await page.waitForTimeout(800);
  await click(page, layouts.getByRole('button', { name: 'Close' }).last(), { ms: 500 });
  // the code card is gone: the board fits at once, so no empty view shows in between
  await page.getByRole('button', { name: 'Fit to content' }).click();
  await page.waitForTimeout(700);

  // 10. the storefront beside its agents
  await caption(page, 'Preview the app next to the agents building it');
  await click(page, head('n_demoweb001'), { double: true, dx: 0.3 });
  await page.waitForTimeout(1900);

  // 11. consoles at the project's root, and in its parts on the servers
  await caption(page, 'Work at the root, or in one part', 'A root console reaches every part; the API and Worker ones work on their servers');
  await click(page, page.getByRole('tab', { name: /^Platform/ }).first(), { ms: 550 });
  await expect(page).toHaveURL(new RegExp(`/w/${platform}`));
  await expect(page.locator('.react-flow__node .node-part').first()).toBeVisible({ timeout: 15_000 });
  await page.getByRole('button', { name: 'Fit to content' }).click();
  await page.waitForTimeout(400);
  for (const chip of await page.locator('.react-flow__node .node-part').all()) {
    await point(page, chip, 450);
    await page.waitForTimeout(400);
  }

  // 12. who directs whom
  await caption(page, 'See who directs whom', 'Mission Map: lead cards and the command bar, their groups and consoles');
  await click(page, page.getByRole('tab', { name: /^Release/ }).first(), { ms: 550 });
  await expect(page).toHaveURL(new RegExp(`/w/${release}`));
  await page.waitForTimeout(800);
  await page.keyboard.press('x');
  await expect(page.getByRole('dialog', { name: 'Mission Map' })).toBeVisible();
  await page.waitForTimeout(2100);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);

  // 13. NextCode: a plan to approve, then the change, the tests and the report
  await caption(page, 'NextCode, the hub’s own code agent', 'A chat on Claude Code, Codex or your API key: it plans, you decide');
  await click(page, page.getByRole('tab', { name: /^Coupons/ }).first(), { ms: 550 });
  await expect(page).toHaveURL(new RegExp(`/w/${nc.wid}`));
  // the card full size: Focus, from its menu
  await click(page, head(nc.node), { dx: 0.3 });
  await point(page, head(nc.node), 200, 0.3);
  const hb = (await head(nc.node).boundingBox())!;
  await page.mouse.click(hb.x + hb.width * 0.3, hb.y + hb.height / 2, { button: 'right' });
  await click(page, page.getByRole('menuitem', { name: 'Focus', exact: true }), { ms: 450 });
  const chat = page.locator('.react-flow__node.is-focused .nc-chat');
  await expect(chat).toBeVisible({ timeout: 15_000 });
  await click(page, chat.getByRole('combobox', { name: `Message to @${nc.handle}` }), { ms: 450 });
  await type(page, 'Make each coupon work once per customer', 32);
  await page.keyboard.press('Enter');
  const approve = chat.getByRole('button', { name: 'Approve, accept edits' });
  await expect(approve).toBeVisible({ timeout: 20_000 });
  await page.waitForTimeout(2200);
  await click(page, approve, { ms: 500 });
  await caption(page, 'It edits, runs the tests and reports', 'Your decision stays in the chat, under the plan');
  await expect(chat.getByText('All 47 tests pass', { exact: false })).toBeVisible({ timeout: 30_000 });
  await page.waitForTimeout(2200);
  await click(page, page.getByRole('toolbar', { name: 'Focused card' }).getByRole('button').first(), { ms: 450 });
  await page.waitForTimeout(300);

  // 14. the same project as a code editor, and its databases
  await caption(page, 'The same project as a code editor', 'Files, terminals, git and the agents of its board, in one window');
  await click(page, page.getByRole('group', { name: 'View' }).getByRole('button', { name: /Code/ }), { ms: 500 });
  await expect(page.locator('.wb.is-page')).toBeVisible({ timeout: 20_000 });
  await page.waitForTimeout(1600);
  const databases = page.getByRole('navigation', { name: 'Views' }).getByRole('button', { name: 'Databases', exact: true });
  if (await databases.count()) {
    await caption(page, 'Its databases, beside the code', 'PostgreSQL, MySQL and Redis, over SSH too; agents ask before they write');
    await click(page, databases, { ms: 500 });
    const dbSide = page.locator('section.db-side');
    const conn = dbSide.getByRole('button', { name: /^acme_shop/ });
    if (await conn.waitFor({ timeout: 5000 }).then(() => true, () => false)) {
      await click(page, conn, { ms: 450 });
      const orders = dbSide.getByRole('tree', { name: 'acme_shop schema' }).getByRole('button', { name: /^orders/ });
      await expect(orders).toBeVisible({ timeout: 20_000 });
      await click(page, orders, { ms: 450 });
      await expect(page.locator('.tool-db:visible .db-foot')).toContainText('of 320', { timeout: 15_000 });
      await page.waitForTimeout(2200);
    }
  }
  await click(page, page.getByRole('group', { name: 'View' }).getByRole('button', { name: /Board/ }), { ms: 500 });
  await expect(page.locator('.react-flow__node').first()).toBeVisible({ timeout: 15_000 });
  await page.waitForTimeout(500);

  // 15. memory
  await caption(page, 'One memory per project, shared by every agent', 'Each agent is sent only what it does not have yet');
  await click(page, page.getByRole('button', { name: /^Memory/ }).first());
  await page.waitForTimeout(1400);
  // its galaxy: the memory page
  // the link wraps over two lines: its middle is between them, so the click goes to its first piece
  const link = page.getByRole('link', { name: 'memory page' }).first();
  const at = await link.evaluate((el) => {
    const r = el.getClientRects()[0];
    return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  });
  await glide(page, at.x, at.y, 500);
  await page.waitForTimeout(140);
  await page.mouse.down();
  await page.waitForTimeout(70);
  await page.mouse.up();
  await expect(page).toHaveURL(/\/memory/);
  await page.waitForTimeout(2900);

  // 16. the vault
  await caption(page, 'A zero-knowledge credentials vault', 'Agents use a secret through one approved command, without seeing it');
  await click(page, page.getByRole('button', { name: /^Credentials/ }).first());
  await expect(page.getByRole('button', { name: 'Lock the vault' })).toBeVisible();
  await page.waitForTimeout(400);
  await click(page, page.getByText('Payments sandbox').first(), { ms: 500 });
  await page.waitForTimeout(1900);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);
  await assertPrivate(page);

  // 17. the end
  await caption(page);
  await titleCard(page, 'NextBoard', 'Open source · MIT', ['github.com/azamrizki/nextboard']);
  await page.waitForTimeout(3000);

  await rec.stop();
  rec.encode(join(OUT, 'demo.mp4'), W, H);
  rec.gif(join(OUT, 'demo.mp4'), join(OUT, 'demo.gif'), clipFrom, clipTo);
});
