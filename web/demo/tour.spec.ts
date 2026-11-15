import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import { OUT } from './playwright.config';
import { Recorder } from './record';
import { OVERLAY, assertPrivate, call, caption, click, consoles, hideTitleCard, openShop, point, projects, releaseBoard, seedVault, shopBoard, titleCard, type } from './demo';

// The product video: about a minute through the demo hub, in the dark theme, recorded from the
// page. Its story: work sent with an @mention, the agent at work, its report in the thread, its
// change reviewed in the code card; then a glance at the rest. Captions and the cursor are drawn
// into the page (demo.ts); everything else is the real UI.

// Chrome sends the frames in CSS pixels, whatever the device scale
const W = 1600;
const H = 900;

test.use({ viewport: { width: W, height: H }, colorScheme: 'dark' });

test('product tour', async ({ page, request }) => {
  test.setTimeout(600_000);
  const shop = projects()['acme-shop'];
  await shopBoard(request, shop);
  const release = await releaseBoard(request, shop);
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
  await page.getByRole('link', { name: 'All projects' }).first().click();
  await expect(page.getByText('acme-shop', { exact: true }).first()).toBeVisible();
  await page.waitForTimeout(800);
  await assertPrivate(page);

  mkdirSync(OUT, { recursive: true });
  const rec = new Recorder(page, OUT);
  await rec.start(W, H);

  // 1. title
  await titleCard(page, 'NextBoard', 'Your agents. Your machines. One board.');
  await page.waitForTimeout(2600);
  await hideTitleCard(page);
  await page.waitForTimeout(400);

  // 2. projects, one of them in three parts
  await caption(page, 'Your projects, on a hub you run yourself', 'acme-shop spans three parts: the storefront here, its API and worker on servers');
  const partsList = page.getByRole('list', { name: 'Parts of acme-shop' });
  await point(page, partsList, 800);
  await page.waitForTimeout(1800);
  await click(page, page.getByText('acme-shop', { exact: true }).first(), { ms: 600 });

  // 3. the board
  for (const id of Object.values(nodes)) await expect(tile(id)).toContainText('demo agent', { timeout: 15_000 });
  await page.waitForTimeout(500);
  await caption(page, 'Every agent on one canvas', 'Working, needs you or idle, without opening each terminal');
  for (const id of [nodes.builder, nodes.reviewer, nodes.tester]) {
    await point(page, head(id), 550, 0.85, 0.5);
    await page.waitForTimeout(450);
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
  await page.waitForTimeout(2600);

  // 9. a team from a template
  await caption(page, 'Start a whole team from a template', 'And choose how many agents each group gets before you deploy');
  await click(page, page.getByRole('button', { name: 'Layouts' }).first());
  const layouts = page.getByRole('dialog', { name: 'Layouts' });
  await expect(layouts).toBeVisible();
  // behind the dialog meanwhile: the code card goes, so the board fits as it was
  const board = await call(request, 'GET', `/api/workspaces/${shop.wid}/board`);
  const editors = board.nodes.filter((n: { kind: string }) => n.kind === 'editor').map((n: { id: string }) => ({ op: 'del', id: n.id }));
  if (editors.length) await call(request, 'POST', `/api/workspaces/${shop.wid}/board/ops`, { clientId: 'c_demo', baseRev: 0, ops: editors });
  await page.waitForTimeout(300);
  await click(page, layouts.getByRole('option', { name: /Front & back/ }), { ms: 500 });
  await page.waitForTimeout(600);
  const more = layouts.getByRole('button', { name: /^More consoles in / }).first();
  await click(page, more, { ms: 500 });
  await page.waitForTimeout(350);
  await click(page, more, { ms: 150 });
  await page.waitForTimeout(1100);
  await click(page, layouts.getByRole('button', { name: 'Close' }).last(), { ms: 500 });
  // the code card is gone: the board fits at once, so no empty view shows in between
  await page.getByRole('button', { name: 'Fit to content' }).click();
  await page.waitForTimeout(700);

  // 10. the storefront beside its agents
  await caption(page, 'Preview the app next to the agents building it');
  await click(page, head('n_demoweb001'), { double: true, dx: 0.3 });
  await page.waitForTimeout(2200);

  // 11. who directs whom
  await caption(page, 'See who directs whom', 'Mission Map: lead cards and the command bar, their groups and consoles');
  await click(page, page.getByRole('tab', { name: /^Release/ }).first(), { ms: 550 });
  await expect(page).toHaveURL(new RegExp(`/w/${release}`));
  await page.waitForTimeout(1000);
  await page.keyboard.press('x');
  await expect(page.getByRole('dialog', { name: 'Mission Map' })).toBeVisible();
  await page.waitForTimeout(2400);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);

  // 12. memory
  await caption(page, 'One memory per project, shared by every agent', 'Each agent is sent only what it does not have yet');
  await click(page, page.getByRole('link', { name: 'Memory' }).first());
  await page.waitForTimeout(3300);

  // 13. the vault
  await caption(page, 'A zero-knowledge credentials vault', 'Agents use a secret through one approved command, without seeing it');
  await click(page, page.getByRole('button', { name: /^Credentials/ }).first());
  await expect(page.getByRole('button', { name: 'Lock the vault' })).toBeVisible();
  await page.waitForTimeout(400);
  await click(page, page.getByText('Payments sandbox').first(), { ms: 500 });
  await page.waitForTimeout(2200);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(300);
  await assertPrivate(page);

  // 14. the end
  await caption(page);
  await titleCard(page, 'NextBoard', 'Open source · MIT', ['github.com/azamrizki/nextboard']);
  await page.waitForTimeout(3500);

  await rec.stop();
  rec.encode(join(OUT, 'demo.mp4'), W, H);
  rec.gif(join(OUT, 'demo.mp4'), join(OUT, 'demo.gif'), clipFrom, clipTo);
});
