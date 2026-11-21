import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';
import { endConsoles } from './two-parts';

// The editor core of the code view (docs/API-studio.md §7.15): a grid of up to four groups split
// by dragging a tab to an edge, Split Down and Ctrl+K Ctrl+\, kept over a reload and shown one at
// a time on a phone; a terminal moved to the editor area and back, the same shell all along; and
// a board's agent opened as a tab of the code view, woken, and left running when the tab closes.

const EDITOR = 'n_edit000001';

async function editor(page: Page, hub: Hub, name: string, files: Record<string, string>) {
  const p = await hub.project(name);
  for (const [f, text] of Object.entries(files)) writeFileSync(join(p.path, f), text);
  await hub.ops(p.wid, [{ op: 'put', node: { id: EDITOR, kind: 'editor', x: 0, y: 0, w: 1200, h: 720, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=${EDITOR}`);
  await expect(page.locator('.wb')).toBeVisible();
  return p;
}

/** A group by its number in reading order (left to right, then down). */
const group = (page: Page, n: number) => page.getByRole('region', { name: `Editor group ${n}` });
const tabIn = (g: Locator, name: string) => g.locator('.wb-tab', { hasText: name });
const box = async (l: Locator) => (await l.boundingBox())!;

/** Drag with the mouse, as a hand does (HTML drag and drop), checking what shows before the drop. */
async function drag(page: Page, from: Locator, to: Locator, fx: number, fy: number, before?: () => Promise<void>) {
  const a = await box(from);
  const b = await box(to);
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.mouse.down();
  await page.mouse.move(b.x + b.width * fx, b.y + b.height * fy, { steps: 10 });
  if (before) await before();
  await page.mouse.up();
}

/** A shell at its prompt: past zsh's first-run questions, as the test hub's home has no .zshrc. */
async function shellReady(page: Page, term: Locator) {
  const rows = term.locator('.xterm-rows');
  const text = async () => (await rows.innerText()).trimEnd();
  await expect.poll(async () => (await text()).length, { timeout: 15_000 }).toBeGreaterThan(0);
  await page.waitForTimeout(500);
  if ((await text()).includes('Type one of the keys')) {
    await term.click();
    await page.keyboard.press('q');
  }
  await expect.poll(async () => /[%$#]$/.test(await text()), { timeout: 15_000 }).toBe(true);
}

async function tabMenu(page: Page, tab: Locator, label: string) {
  await tab.click({ button: 'right' });
  await page.getByRole('menu', { name: 'Tab actions' }).getByRole('menuitem', { name: new RegExp(`^${label}( Ctrl\\+.*)?$`) }).click();
}

test('a tab dragged to an edge splits there; Split Down and Ctrl+K Ctrl+\\ fill a 2×2 grid that a reload keeps', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  const p = await editor(page, hub, 'Grid', { 'alpha.ts': 'export const alpha = 1;\n', 'beta.ts': 'export const beta = 2;\n' });
  const side = page.locator('.wb-side');
  for (const f of ['alpha.ts', 'beta.ts']) await side.getByRole('treeitem', { name: f }).dblclick({ timeout: 20_000 });
  const g1 = group(page, 1);
  await expect(tabIn(g1, 'beta.ts')).toBeVisible();

  // drag beta.ts to the right edge of its group: the half it takes shows, then a group there
  await drag(page, tabIn(g1, 'beta.ts'), g1, 0.93, 0.6, async () => {
    await expect(page.locator('.wb-drop-zone.is-right')).toHaveText('Drop to split right');
  });
  await expect(page.locator('.wb-group')).toHaveCount(2);
  const g2 = group(page, 2);
  await expect(tabIn(g2, 'beta.ts')).toBeVisible();
  await expect(tabIn(g1, 'beta.ts')).toHaveCount(0);
  expect((await box(g2)).x).toBeGreaterThan((await box(g1)).x + (await box(g1)).width - 4);

  // Split Down from the tab's menu: beta.ts again, under the right group
  await tabMenu(page, tabIn(g2, 'beta.ts'), 'Split Down');
  await expect(page.locator('.wb-group')).toHaveCount(3);
  const g3 = group(page, 3);
  await expect(tabIn(g3, 'beta.ts')).toBeVisible();
  expect(Math.abs((await box(g3)).x - (await box(g2)).x)).toBeLessThan(2);
  expect((await box(g3)).y).toBeGreaterThan((await box(g2)).y + 40);

  // Ctrl+K Ctrl+\ in the left editor: alpha.ts below it, and the chord is over (the next key types)
  await g1.locator('.monaco-editor .view-line', { hasText: 'alpha' }).click();
  await page.keyboard.press('Control+k');
  await page.keyboard.press('Control+\\');
  await expect(page.locator('.wb-group')).toHaveCount(4);
  const below = group(page, 2);
  await expect(tabIn(below, 'alpha.ts')).toBeVisible();
  expect(Math.abs((await box(below)).x - (await box(g1)).x)).toBeLessThan(2);
  await expect(below.locator('.monaco-editor.focused')).toBeVisible();
  await page.keyboard.type('Z');
  await expect(below.locator('.monaco-editor .view-line', { hasText: 'Zexport const alpha' })).toBeVisible();

  // the sash between the columns is dragged; the layout and its sizes survive a reload
  const sash = page.getByRole('separator', { name: 'Resize the groups side by side' });
  const before = (await box(g1)).width;
  const s = await box(sash);
  await page.mouse.move(s.x + 1, s.y + s.height / 2);
  await page.mouse.down();
  await page.mouse.move(s.x + 151, s.y + s.height / 2, { steps: 6 });
  await page.mouse.up();
  await expect.poll(async () => (await box(g1)).width).toBeGreaterThan(before + 100);
  const saved = () => page.evaluate((k) => JSON.parse(localStorage.getItem(k) ?? '{}').grid?.root?.dir ?? '', `nb.wb.${p.id}`);
  await expect.poll(saved).toBe('row');
  const wide = (await box(g1)).width;
  await page.reload();
  await expect(page.locator('.wb-group')).toHaveCount(4, { timeout: 20_000 });
  await expect(tabIn(group(page, 3), 'beta.ts')).toBeVisible();
  await expect(tabIn(group(page, 2), 'alpha.ts')).toBeVisible();
  expect(Math.abs((await box(group(page, 1))).width - wide)).toBeLessThan(4);

  // a phone: the active group alone, on the whole editor
  await page.setViewportSize({ width: 393, height: 873 });
  await expect(page.locator('.wb.is-narrow')).toBeVisible();
  await expect(page.locator('.wb-group:visible')).toHaveCount(1);
  await expect(page.getByRole('separator', { name: /^Resize the groups/ })).toHaveCount(0);
});

test('a terminal moves to the editor area and back, the same shell; one can start there and is killed from its tab', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  const p = await editor(page, hub, 'Terminal tab', { 'README.md': '# Terminals\n' });
  try {
    await expect(page.locator('.wb-tab', { hasText: 'README.md' })).toBeVisible({ timeout: 15_000 });
    await page.getByRole('button', { name: 'Toggle panel (Ctrl+`)' }).click();
    const panel = page.locator('.wb-panel');
    await expect(panel.locator('.wb-terminal .xterm')).toBeVisible({ timeout: 15_000 });
    await expect.poll(async () => ((await hub.call('GET', '/api/sessions')) as any[]).filter((s) => s.workspaceId === p.wid && s.panel).length, { timeout: 15_000 }).toBe(1);
    const sid: string = ((await hub.call('GET', '/api/sessions')) as any[]).find((s) => s.workspaceId === p.wid && s.panel).id;
    await shellReady(page, panel.locator('.wb-terminal .xterm'));
    await panel.locator('.wb-terminal .xterm').click();
    await page.keyboard.type('echo nb-moved-$((40+2))');
    await page.keyboard.press('Enter');
    await expect(panel.locator('.xterm-rows')).toContainText('nb-moved-42', { timeout: 15_000 });

    // to the editor area, from the menu beside New terminal: a tab of the same shell, its screen with it
    await panel.getByRole('button', { name: 'More terminal actions' }).click();
    await page.getByRole('menuitem', { name: 'Move Terminal to Editor Area' }).click();
    const tab = page.locator('.wb-pane-tab .xterm-rows');
    await expect(tab).toContainText('nb-moved-42', { timeout: 15_000 });
    await expect(panel.locator('.wb-terminal')).toHaveCount(0);
    await expect(panel).toContainText('No terminal here');
    // closed and opened again, the panel starts no new shell: this view has had one
    await page.getByRole('button', { name: 'Toggle panel (Ctrl+`)' }).click();
    await page.getByRole('button', { name: 'Toggle panel (Ctrl+`)' }).click();
    await expect(panel).toContainText('No terminal here');
    expect(((await hub.call('GET', '/api/sessions')) as any[]).filter((s) => s.workspaceId === p.wid && s.panel).map((s) => s.id)).toEqual([sid]);
    const termTab = page.locator('.wb-group .wb-tab', { hasText: 'Terminal' });
    await expect(termTab).toBeVisible();
    // it still runs: typed into here, it answers
    await page.locator('.wb-pane-tab .xterm').click();
    await page.keyboard.type('echo nb-tab-$((6*7))');
    await page.keyboard.press('Enter');
    await expect(tab).toContainText('nb-tab-42', { timeout: 15_000 });

    // and back to the panel from the tab's menu, still the same session
    await tabMenu(page, termTab, 'Move to Panel');
    await expect(page.locator('.wb-pane-tab')).toHaveCount(0);
    await expect(panel.locator('.xterm-rows')).toContainText('nb-tab-42', { timeout: 15_000 });
    const mine = ((await hub.call('GET', '/api/sessions')) as any[]).filter((s) => s.workspaceId === p.wid && s.panel);
    expect(mine.map((s) => s.id)).toEqual([sid]);
    expect(mine[0].status).toBe('running');

    // a new one straight in the editor area; its close asks, then kills only it
    await panel.getByRole('button', { name: 'More terminal actions' }).click();
    await page.getByRole('menuitem', { name: 'New Terminal in Editor Area' }).click();
    await expect(page.locator('.wb-pane-tab .xterm')).toBeVisible({ timeout: 15_000 });
    await expect.poll(async () => ((await hub.call('GET', '/api/sessions')) as any[]).filter((s) => s.workspaceId === p.wid && s.panel).length).toBe(2);
    await expect(panel.locator('.wb-term-tab')).toHaveCount(1);
    await page.getByRole('button', { name: 'Kill this terminal' }).click();
    const dialog = page.getByRole('dialog', { name: /^Kill / });
    await dialog.getByRole('button', { name: 'Kill terminal' }).click();
    await expect(page.locator('.wb-pane-tab')).toHaveCount(0);
    await expect.poll(async () => ((await hub.call('GET', '/api/sessions')) as any[]).filter((s) => s.workspaceId === p.wid && s.panel && s.status !== 'exited').map((s) => s.id)).toEqual([sid]);
  } finally {
    await endConsoles(hub, p.wid);
  }
});

test('in the code view a board’s agent opens as a tab, wakes, and keeps running when the tab closes', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  test.setTimeout(90_000);
  const p = await hub.project('Board agents');
  writeFileSync(join(p.path, 'README.md'), '# Board agents\n');
  const s = await hub.call('POST', '/api/sessions', { agent: 'fakeclaude', workspaceId: p.wid });
  const state = async () => hub.call('GET', `/api/sessions/${s.id}`);
  try {
    await expect.poll(async () => (await state()).agentState, { timeout: 20_000 }).toBe('ready');
    await hub.call('POST', `/api/sessions/${s.id}/sleep`);
    await expect.poll(async () => (await state()).status).toBe('asleep');

    await page.goto(`/p/${p.id}/code`);
    await expect(page.locator('.wb.is-page')).toBeVisible({ timeout: 20_000 });
    const bar = page.locator('.wb-right.is-page');
    if (!(await bar.isVisible())) await page.getByRole('button', { name: /^Toggle the lead and agents side bar/ }).click();

    // Lead first: the project's thread, with the Quick actions in its composer
    await expect(bar.getByRole('tab', { name: 'Lead' })).toHaveAttribute('aria-selected', 'true');
    await bar.getByRole('button', { name: 'Quick actions' }).click();
    await page.getByRole('menu', { name: 'Quick actions' }).getByRole('menuitem', { name: 'Chats' }).click();
    // a tool tab: it stays mounted, hidden, while another tab of its group shows
    const chats = page.locator('.wb-group .wb-tab', { hasText: 'Chats' });
    await expect(chats).toBeVisible();
    await expect(page.locator('.wb-tool-tab')).toBeVisible();
    await page.locator('.wb-group .wb-tab', { hasText: 'README.md' }).click();
    await expect(page.locator('.wb-tool-tab')).toHaveCount(1);
    await expect(page.locator('.wb-tool-tab')).toBeHidden();
    await chats.click();
    await expect(page.locator('.wb-tool-tab')).toBeVisible();

    // Agents: the board's agent, asleep, opened as a tab: the same console, woken
    await bar.getByRole('tab', { name: /^Agents/ }).click();
    const row = bar.locator('.wb-agent-row', { hasText: `@${s.handle}` });
    await expect(row).toContainText('Asleep');
    await row.getByRole('button', { name: `Open @${s.handle} as a tab` }).click();
    const tab = page.locator('.wb-group .wb-tab', { hasText: `@${s.handle}` });
    await expect(tab).toBeVisible();
    await expect(page.locator('.wb-pane-tab .xterm')).toBeVisible({ timeout: 15_000 });
    await expect.poll(async () => (await state()).status, { timeout: 20_000 }).toBe('running');
    await expect(row.getByRole('button', { name: `@${s.handle} is showing` })).toBeVisible();
    // no wire to this editor: it takes orders as its tile does, and has its tile's menu
    await expect(page.locator('.wb-pane-link:not(.wb-console-bar)')).toHaveCount(0);
    await expect(page.locator('.wb-console-bar').getByRole('button', { name: `Console menu of @${s.handle}` })).toBeVisible();

    // closing the tab asks nothing and leaves it running on its board
    await tab.getByRole('button', { name: 'Close this tab' }).click();
    await expect(tab).toHaveCount(0);
    await expect(page.getByRole('dialog', { name: /^Stop / })).toHaveCount(0);
    const after = await state();
    expect(after.status).toBe('running');
    await expect(row.getByRole('button', { name: `Open @${s.handle} as a tab` })).toBeVisible();
  } finally {
    await endConsoles(hub, p.wid);
  }
});

test('at phone width the strip scrolls its active tab into view', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light', 'one board is enough');
  const files: Record<string, string> = {};
  for (let i = 1; i <= 8; i++) files[`a-rather-long-file-name-${i}.ts`] = `export const n${i} = ${i};\n`;
  await editor(page, hub, 'Strip', files);
  const side = page.locator('.wb-side');
  for (const f of Object.keys(files)) await side.getByRole('treeitem', { name: f }).dblclick({ timeout: 20_000 });
  const strip = page.locator('.wb-group .wb-tabs').first();
  const tab = (n: number) => strip.locator('.wb-tab', { hasText: `a-rather-long-file-name-${n}.ts` });
  await tab(1).click();
  await page.setViewportSize({ width: 393, height: 873 });
  await expect(page.locator('.wb.is-narrow')).toBeVisible();
  // the side bar lies over the editor on a phone
  const scrim = page.getByRole('button', { name: 'Close the side bar' });
  if (await scrim.isVisible()) await scrim.dispatchEvent('click');
  const inStrip = async (n: number) => {
    const s = (await strip.boundingBox())!;
    const t = (await tab(n).boundingBox())!;
    return t.x >= s.x - 1 && t.x + t.width <= s.x + s.width + 1;
  };
  await expect.poll(() => inStrip(1)).toBe(true);
  // the previous tab of the first is the last one: far off to the right, brought into view
  await page.locator('.monaco-editor .view-line', { hasText: 'n1' }).first().click();
  await page.keyboard.press('Alt+PageUp');
  await expect(tab(8)).toHaveClass(/is-active/);
  await expect.poll(() => inStrip(8)).toBe(true);
});

test.describe('on a phone held sideways', () => {
  test.use({ viewport: { width: 873, height: 393 }, isMobile: true, hasTouch: true });
  test('a card wider than 640 px still shows one group at a time', async ({ page, hub }, info) => {
    test.skip(info.project.name !== 'light', 'one board is enough');
    const p = await hub.project('Sideways');
    for (const f of ['left.ts', 'right.ts']) writeFileSync(join(p.path, f), `export const x = '${f}';\n`);
    await hub.ops(p.wid, [{ op: 'put', node: { id: EDITOR, kind: 'editor', x: 0, y: 0, w: 1200, h: 720, data: {} } }]);
    // two groups side by side, as a desktop left them
    const state = {
      v: 2,
      groups: ['left.ts', 'right.ts'].map((f) => ({ tabs: [{ path: join(p.path, f), preview: false }], active: join(p.path, f) })),
      activeGroup: 1,
      grid: { v: 2, root: { dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 0.5 } },
      sideOpen: false,
    };
    await page.addInitScript(([k, v]) => localStorage.setItem(k, v), [`nb.wb.${p.id}`, JSON.stringify(state)] as const);
    await page.goto(`/p/${p.id}/w/${p.wid}?card=${EDITOR}`);
    await expect(page.locator('.wb.is-one-group')).toBeVisible({ timeout: 20_000 });
    await expect(page.locator('.wb.is-narrow')).toHaveCount(0);
    await expect(page.locator('.wb-group')).toHaveCount(2);
    await expect(page.locator('.wb-group:visible')).toHaveCount(1);
    await expect(page.locator('.wb-group:visible .wb-tab', { hasText: 'right.ts' })).toBeVisible();
    await expect(page.getByRole('separator', { name: /^Resize the groups/ })).toHaveCount(0);
  });
});
