import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';

// The Code view (DESIGN.md §3.19): a project as a full code editor at /p/<id>/code, with no board.
// Opening a project asks Board or Code until the hub's ui.openProjectsIn says; the setting is in
// Settings › General, a project card's menu offers both views, the switcher of the Code view stays
// in the Code view and the one of an editor card goes to boards. A phone gets bottom navigation.
//
// The other specs open boards: global-setup sets ui.openProjectsIn to "board" for the whole run.
// These tests set it as they need and put "board" back after each, whatever happened.

const setOpenIn = (hub: Hub, v: '' | 'board' | 'code') => hub.call('PATCH', '/api/settings', { ui: { openProjectsIn: v } });
const openIn = async (hub: Hub) => (await hub.call('GET', '/api/settings')).ui.openProjectsIn as string;

test.afterEach(async ({ hub }) => {
  await setOpenIn(hub, 'board');
});

/** A project with a file or two in its folder. */
async function project(hub: Hub, name: string) {
  const p = await hub.project(name);
  writeFileSync(join(p.path, 'README.md'), `# ${name}\n`);
  writeFileSync(join(p.path, 'app.ts'), 'export const api = 1;\n');
  return p;
}

const card = (page: Page, pid: string) => page.locator(`[data-project="${pid}"]`);
const codeUrl = (pid: string) => new RegExp(`/p/${pid}/code$`);
const boardUrl = (pid: string, wid: string) => new RegExp(`/p/${pid}/w/${wid}$`);

test('opening a project asks Board or Code, and remembers the choice on the hub', async ({ page, hub }) => {
  test.slow();
  await setOpenIn(hub, '');
  const p = await project(hub, 'Harbor Lounge');
  await page.goto('/');
  await card(page, p.id).locator('.project-open').click();
  const dialog = page.getByRole('dialog', { name: `Open “${p.name}”` });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText('How do you want to work on this project?')).toBeVisible();
  // Board view is chosen first, and the primary button says what it opens
  const boardChoice = dialog.getByRole('radio', { name: /Board view/ });
  const codeChoice = dialog.getByRole('radio', { name: /Code view/ });
  await expect(boardChoice).toHaveAttribute('aria-checked', 'true');
  await expect(dialog.getByRole('button', { name: 'Open in Board view' })).toBeVisible();
  await codeChoice.click();
  await expect(codeChoice).toHaveAttribute('aria-checked', 'true');
  await expect(dialog.getByRole('button', { name: 'Open in Code view' })).toBeVisible();
  // ticked first, as the owner approved it
  const remember = dialog.getByRole('checkbox', { name: 'Remember my choice for every project' });
  await expect(remember).toBeChecked();

  // Cancel stays on Home and changes nothing
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  await expect(page).toHaveURL(/\/$/);
  expect(await openIn(hub)).toBe('');

  // once without remembering: the Code view, and the next open asks again
  await card(page, p.id).locator('.project-open').click();
  await dialog.getByRole('radio', { name: /Code view/ }).click();
  await remember.uncheck();
  await dialog.getByRole('button', { name: 'Open in Code view' }).click();
  await expect(page).toHaveURL(codeUrl(p.id));
  await expect(page.locator('.cv-bar')).toBeVisible({ timeout: 20_000 });
  expect(await openIn(hub)).toBe('');
  await page.goto('/');
  await card(page, p.id).locator('.project-open').click();
  await expect(dialog).toBeVisible();

  // remembered (the box is ticked again in a new dialog): written to the hub, and the next project
  // opens there without a question
  await dialog.getByRole('radio', { name: /Code view/ }).click();
  await expect(remember).toBeChecked();
  await dialog.getByRole('button', { name: 'Open in Code view' }).click();
  await expect(page).toHaveURL(codeUrl(p.id));
  await expect.poll(() => openIn(hub)).toBe('code');
  const q = await project(hub, 'Tidepool');
  await page.goto('/');
  await card(page, q.id).locator('.project-open').click();
  await expect(page).toHaveURL(codeUrl(q.id));
  await expect(page.getByRole('dialog')).toHaveCount(0);

  // Settings › General shows it and changes it: Board view, then Ask every time
  await page.goto('/settings/general');
  const group = page.getByRole('radiogroup', { name: 'Open projects in' });
  await expect(group.getByRole('radio', { name: /^Code view/ })).toHaveAttribute('aria-checked', 'true');
  await group.getByRole('radio', { name: /^Board view/ }).click();
  await expect.poll(() => openIn(hub)).toBe('board');
  await expect(group.getByRole('radio', { name: /^Board view/ })).toHaveAttribute('aria-checked', 'true');
  await page.goto('/');
  await card(page, q.id).locator('.project-open').click();
  await expect(page).toHaveURL(boardUrl(q.id, q.wid));
  await page.goto('/settings/general');
  await group.getByRole('radio', { name: /^Ask every time/ }).click();
  await expect.poll(() => openIn(hub)).toBe('');
  // saved: the pane has the hub's answer (and takes no more while it waits for it)
  await expect(group.getByRole('radio', { name: /^Ask every time/ })).toHaveAttribute('aria-checked', 'true');
  await expect(group.getByRole('radio', { name: /^Ask every time/ })).toBeEnabled();
  // another browser's change comes live: the radio follows the hub
  await setOpenIn(hub, 'code');
  await expect(group.getByRole('radio', { name: /^Code view/ })).toHaveAttribute('aria-checked', 'true');
});

test('a link with no view asks too; a project card always offers both views', async ({ page, hub }) => {
  await setOpenIn(hub, '');
  const p = await project(hub, 'Lumen Garden');
  const dialog = page.getByRole('dialog', { name: `Open “${p.name}”` });

  // /p/<id>: the question over the page; Board view opens the board in its place
  await page.goto(`/p/${p.id}`);
  await expect(dialog).toBeVisible();
  await dialog.getByRole('checkbox', { name: 'Remember my choice for every project' }).uncheck();
  await dialog.getByRole('button', { name: 'Open in Board view' }).click();
  await expect(page).toHaveURL(boardUrl(p.id, p.wid));
  await expect(page.locator('.board-canvas')).toBeVisible();
  // the app's own way back to /p/<id> after that is the board: no question
  await page.evaluate((pid) => {
    window.history.pushState(null, '', `/p/${pid}`);
    window.dispatchEvent(new Event('nb:navigate'));
  }, p.id);
  await expect(page).toHaveURL(boardUrl(p.id, p.wid));
  await expect(dialog).toHaveCount(0);
  // Cancel from a link goes Home
  await page.goto(`/p/${p.id}`);
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(page).toHaveURL(/\/$/);

  // the card's menu: both views, whatever the setting
  await page.getByRole('button', { name: `Actions for ${p.name}` }).click();
  await expect(page.getByRole('menuitem', { name: 'Open in Board view' })).toBeVisible();
  await page.getByRole('menuitem', { name: 'Open in Code view' }).click();
  await expect(page).toHaveURL(codeUrl(p.id));
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await setOpenIn(hub, 'code');
  await page.goto('/');
  await page.getByRole('button', { name: `Actions for ${p.name}` }).click();
  await page.getByRole('menuitem', { name: 'Open in Board view' }).click();
  await expect(page).toHaveURL(boardUrl(p.id, p.wid));

  // a link with the setting at "code" lands in the Code view
  await page.goto(`/p/${p.id}`);
  await expect(page).toHaveURL(codeUrl(p.id));
  await expect(page.locator('.cv-bar')).toBeVisible({ timeout: 20_000 });
});

test('the Code view: its top bar, the tools, Board | Code, and switchers that keep their view', async ({ page, hub }) => {
  test.slow();
  const p = await project(hub, 'Shop');
  const q = await project(hub, 'Tidepool');
  // an editor card on Shop's board, the board the Code view binds
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_cvedit0001', kind: 'editor', x: 0, y: 0, w: 1200, h: 700, data: {} } }]);
  await page.goto(`/p/${p.id}/code`);
  const bar = page.locator('.cv-bar');
  await expect(bar).toBeVisible({ timeout: 20_000 });
  await expect(page).toHaveTitle(new RegExp(`Code · ${p.name}`));
  // no board, no app header, no command bar
  await expect(page.locator('.board-canvas')).toHaveCount(0);
  await expect(page.locator('.app-header')).toHaveCount(0);
  await expect(page.locator('.cmdbar-wrap')).toHaveCount(0);
  // the top bar: switcher, Board | Code, quick open, pills, ⋯
  const switcher = bar.getByRole('button', { name: `Project ${p.name}. Switch project` });
  await expect(switcher).toBeVisible();
  const views = bar.getByRole('group', { name: 'View' });
  await expect(views.locator('[aria-current="page"]')).toHaveText('Code');
  await expect(bar.locator('.conn')).toBeVisible();
  await bar.getByRole('button', { name: /Go to file/ }).click();
  await expect(page.locator('.wb-palette')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('.wb-palette')).toHaveCount(0);
  await bar.getByRole('button', { name: 'More' }).click();
  for (const item of ['Memory', 'Machines', 'Devices', 'Settings', /Switch to (dark|light) mode/, 'About this hub', 'Log out']) await expect(page.getByRole('menuitem', { name: item })).toBeVisible();
  await page.keyboard.press('Escape');

  // the editor: the project's files, and the lead's side bar the first time
  const side = page.locator('.wb-side');
  await side.getByRole('treeitem', { name: 'app.ts' }).click({ timeout: 20_000 });
  await expect(page.locator('.wb-tab', { hasText: 'app.ts' })).toBeVisible();
  await expect(page.locator('.wb-right')).toBeVisible();

  // the tools after Ask, each an editor tab; Studios is a menu of four
  const activity = page.getByRole('navigation', { name: 'Views' });
  for (const name of ['Chats', 'Memory', 'Studios', 'Vault', 'Browser', 'Loops', 'Mission Map']) await expect(activity.getByRole('button', { name, exact: true })).toBeVisible();
  await activity.getByRole('button', { name: 'Studios', exact: true }).click();
  for (const name of ['Documents', 'Diagrams', 'Markdown', 'Skills']) await expect(page.getByRole('menuitem', { name })).toBeVisible();
  await page.keyboard.press('Escape');
  await activity.getByRole('button', { name: 'Vault', exact: true }).click();
  await expect(page.locator('.wb-tab', { hasText: 'Vault' })).toBeVisible();
  // its button says it is the tab shown, as the views' buttons do
  await expect(activity.getByRole('button', { name: 'Vault', exact: true })).toHaveAttribute('aria-pressed', 'true');
  await expect(activity.getByRole('button', { name: 'Chats', exact: true })).toHaveAttribute('aria-pressed', 'false');
  // a second click focuses the same tab
  await side.getByRole('treeitem', { name: 'app.ts' }).click();
  await activity.getByRole('button', { name: 'Vault', exact: true }).click();
  await expect(page.locator('.wb-tab', { hasText: 'Vault' })).toHaveCount(1);

  // the Ports panel opens a page as a tab here, not as a tile (it lists at least this test hub's port)
  await page.getByRole('navigation', { name: 'Views' }).getByRole('button', { name: 'Toggle panel (Ctrl+`)' }).click();
  await page.getByRole('tab', { name: 'Ports' }).click();
  await expect(page.getByRole('button', { name: 'Open in a tab' }).first()).toBeVisible({ timeout: 20_000 });
  await expect(page.getByRole('button', { name: 'Open in a tile' })).toHaveCount(0);
  await page.getByRole('navigation', { name: 'Views' }).getByRole('button', { name: 'Toggle panel (Ctrl+`)' }).click();

  // the switcher opens another project's Code view; its menu offers this one's board
  await switcher.click();
  const menu = page.getByRole('menu', { name: 'Switch project' });
  await expect(menu.getByText('Switch project · opens in Code view')).toBeVisible();
  await expect(menu.getByRole('menuitem', { name: `Open ${p.name} in Board view` })).toBeVisible();
  await menu.getByRole('menuitem', { name: new RegExp(`^${q.name}`) }).click();
  await expect(page).toHaveURL(codeUrl(q.id));
  await expect(bar.getByRole('button', { name: `Project ${q.name}. Switch project` })).toBeVisible({ timeout: 20_000 });
  await bar.getByRole('button', { name: `Project ${q.name}. Switch project` }).click();
  await page.getByRole('menuitem', { name: `Open ${q.name} in Board view` }).click();
  await expect(page).toHaveURL(boardUrl(q.id, q.wid));

  // Board | Code: Board opens the board, the editor card there shows the same tabs (one editor state)
  await page.goto(`/p/${p.id}/code`);
  await page.locator('.cv-bar').getByRole('group', { name: 'View' }).getByRole('button', { name: /Board/ }).click();
  await expect(page).toHaveURL(boardUrl(p.id, p.wid));
  const edCard = page.locator('.react-flow__node[data-id="n_cvedit0001"]');
  await expect(edCard.locator('.wb-tab', { hasText: 'app.ts' })).toBeVisible({ timeout: 20_000 });
  // the Code view's side bar was open; the card's stays as it was (closed)
  await expect(edCard.locator('.wb-right')).toHaveCount(0);
  // the card belongs to its board's project: its title says which, with no project switcher
  await expect(edCard.locator('.wb-title .tile-title')).toHaveText(p.name);
  await expect(edCard.getByRole('button', { name: /Switch project$/ })).toHaveCount(0);
  // the Code view still switches: the header's Board | Code goes back to it
  await page.getByRole('group', { name: 'View' }).getByRole('button', { name: 'Code', exact: true }).click();
  await expect(page).toHaveURL(codeUrl(p.id));
  await page.locator('.cv-bar').getByRole('button', { name: `Project ${p.name}. Switch project` }).click();
  await page.getByRole('menu', { name: 'Switch project' }).getByRole('menuitem', { name: `Open ${p.name} in Board view` }).click();
  await expect(page).toHaveURL(boardUrl(p.id, p.wid));
});

test('the Code view keeps the board it bound while another is opened; it moves only when that board goes', async ({ page, hub }) => {
  const p = await project(hub, 'Harbor Lounge');
  await page.goto(`/p/${p.id}/code`);
  await page.locator('.wb-side').getByRole('treeitem', { name: 'app.ts' }).click({ timeout: 20_000 });
  await expect(page.locator('.wb-tab', { hasText: 'app.ts' })).toBeVisible();
  const mark = () => page.evaluate(() => (document.querySelector('.wb.is-page') as (HTMLElement & { cvMark?: number }) | null)?.cvMark ?? 0);
  await page.evaluate(() => ((document.querySelector('.wb.is-page') as HTMLElement & { cvMark?: number }).cvMark = 1));
  // another tab opens a second board: this browser's last board changes, and the project with it
  const w2 = await hub.call('POST', `/api/projects/${p.id}/workspaces`, {});
  await page.evaluate(([pid, wid]) => localStorage.setItem(`nb.lastws.${pid}`, wid), [p.id, w2.id]);
  await hub.call('PATCH', `/api/projects/${p.id}`, { opened: true });
  await page.waitForTimeout(1500);
  // the same workbench, its tabs as they were
  expect(await mark()).toBe(1);
  await expect(page.locator('.wb-tab', { hasText: 'app.ts' })).toBeVisible();
  // the bound board goes: the editor binds the other, its tabs kept (they are the project's)
  await hub.call('DELETE', `/api/workspaces/${p.wid}`);
  await expect.poll(mark, { timeout: 15_000 }).toBe(0);
  await expect(page.locator('.cv-bar')).toBeVisible();
  await expect(page.locator('.wb-tab', { hasText: 'app.ts' })).toBeVisible({ timeout: 20_000 });
});

test('a file is downloaded from its menu in the tree and from its tab', async ({ page, hub }) => {
  const p = await project(hub, 'Download Shop');
  await page.goto(`/p/${p.id}/code`);
  const tree = page.getByRole('tree', { name: 'Project files' });
  const row = tree.getByRole('treeitem', { name: 'app.ts' });
  await row.click({ button: 'right', timeout: 20_000 });
  const fromTree = page.waitForEvent('download');
  await page.getByRole('menuitem', { name: 'Download' }).click();
  const a = await fromTree;
  // saved under its own name with its bytes, though a text file would otherwise be shown
  expect(a.suggestedFilename()).toBe('app.ts');
  expect(readFileSync((await a.path())!, 'utf8')).toBe('export const api = 1;\n');

  await row.dblclick();
  const tab = page.locator('.wb-tab', { hasText: 'app.ts' });
  await tab.click({ button: 'right' });
  const fromTab = page.waitForEvent('download');
  await page.getByRole('menuitem', { name: 'Download' }).click();
  expect((await fromTab).suggestedFilename()).toBe('app.ts');
});

test.describe('on a phone', () => {
  test.use({ viewport: { width: 393, height: 873 }, deviceScaleFactor: 2.75, isMobile: true, hasTouch: true });

  test('the Code view has bottom navigation: Files, Editor, Terminal, and Lead and Agents as sheets', async ({ page, hub }) => {
    test.slow();
    const p = await project(hub, 'Shop');
    await page.goto(`/p/${p.id}/code`);
    const nav = page.getByRole('navigation', { name: 'Code view' });
    await expect(nav).toBeVisible({ timeout: 20_000 });
    const tab = (name: string) => nav.getByRole('button', { name, exact: true });
    // one row on top: switcher, Board | Code, ⋯; no quick open, no status bar
    const bar = page.locator('.cv-bar');
    await expect(bar.getByRole('button', { name: `Project ${p.name}. Switch project` })).toBeVisible();
    await expect(bar.getByRole('group', { name: 'View' })).toBeVisible();
    await expect(bar.getByRole('button', { name: 'More' })).toBeVisible();
    await expect(bar.getByRole('button', { name: /Go to file/ })).toHaveCount(0);
    await expect(page.locator('.wb-status')).toHaveCount(0);
    // nothing wider than the screen
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(393);

    // Editor first, the README opened by itself
    await expect(tab('Editor')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('.wb-tab', { hasText: 'README.md' })).toBeVisible({ timeout: 20_000 });
    await expect(page.locator('.wb-side')).toHaveCount(0);

    // Files: the side view full screen, the tools in its ⋯ Tools menu
    await tab('Files').click();
    await expect(tab('Files')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('.wb-side')).toBeVisible();
    await expect(page.locator('.wb-center')).toBeHidden();
    const side = (await page.locator('.wb-side').boundingBox())!;
    expect(side.width).toBeGreaterThan(300);
    await page.getByRole('navigation', { name: 'Views' }).getByRole('button', { name: 'Tools' }).click();
    for (const name of ['Chats', 'Memory', 'Documents', 'Diagrams', 'Markdown', 'Skills', 'Vault', 'Browser', 'Loops', 'Mission Map', 'Databases']) await expect(page.getByRole('menuitem', { name, exact: true })).toBeVisible();
    await page.keyboard.press('Escape');
    // a file opened there shows the Editor
    await page.locator('.wb-side').getByRole('treeitem', { name: 'app.ts' }).click({ timeout: 20_000 });
    await expect(tab('Editor')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('.wb-tab', { hasText: 'app.ts' })).toBeVisible();
    await expect(page.locator('.wb-side')).toHaveCount(0);

    // Terminal: the panel at full height
    await tab('Terminal').click();
    await expect(tab('Terminal')).toHaveAttribute('aria-current', 'page');
    const panel = page.locator('.wb-panel-wrap');
    await expect(panel).toBeVisible();
    const box = (await panel.boundingBox())!;
    expect(box.height).toBeGreaterThan(500);
    await tab('Editor').click();
    await expect(panel).toHaveCount(0);

    // Lead and Agents: sheets over the view, the item current while one shows
    await tab('Lead').click();
    const lead = page.getByRole('dialog').filter({ has: page.locator('.cv-sheet-body.is-lead') });
    await expect(lead).toBeVisible();
    await expect(tab('Lead')).toHaveAttribute('aria-current', 'page');
    await lead.getByRole('button', { name: 'Close dialog' }).click();
    await expect(lead).toHaveCount(0);
    await expect(tab('Editor')).toHaveAttribute('aria-current', 'page');
    await tab('Agents').click();
    const agents = page.getByRole('dialog', { name: 'Agents' });
    await expect(agents).toBeVisible();
    await agents.getByRole('button', { name: 'Close dialog' }).click();
    await expect(agents).toHaveCount(0);
  });
});
