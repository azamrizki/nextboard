import type { Page } from '@playwright/test';
import { expect, test } from './hub';

// One console in two views (its card on the board, a tab in the Code view): the view last
// focused or typed into decides the terminal's size, and the other shows the terminal at that
// size, so neither draws for one size what it shows at another (an agent's input box twice).

/** The rows the terminal shown under `scope` has. */
const rowsIn = (page: Page, scope: string) => page.locator(`${scope} .xterm-rows > div`).count();

test('the view last used decides a console’s size; the other view follows it', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  test.setTimeout(90_000);
  const p = await hub.project('Two views');
  const s = await hub.call('POST', '/api/sessions', { agent: 'fakeclaude', workspaceId: p.wid });
  const size = async () => {
    const x = await hub.call('GET', `/api/sessions/${s.id}`);
    return { c: x.cols as number, r: x.rows as number };
  };
  try {
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState, { timeout: 20_000 }).toBe('ready');

    // the board: its card
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const card = '.react-flow__node .nb-term';
    await expect(page.locator(`${card} .xterm`)).toBeVisible({ timeout: 20_000 });

    // the Code view in a second tab: the same console as an editor tab, much bigger than the card
    const code = await page.context().newPage();
    await code.setViewportSize({ width: 1600, height: 1000 });
    await code.goto(`/p/${p.id}/code`);
    const bar = code.locator('.wb-right.is-page');
    await expect(code.locator('.wb.is-page')).toBeVisible({ timeout: 20_000 });
    if (!(await bar.isVisible())) await code.getByRole('button', { name: /^Toggle the lead and agents side bar/ }).click();
    await bar.getByRole('tab', { name: /^Agents/ }).click();
    await bar.locator('.wb-agent-row', { hasText: `@${s.handle}` }).getByRole('button', { name: `Open @${s.handle} as a tab` }).click();
    const tab = '.wb-pane-tab';
    await expect(code.locator(`${tab} .xterm`)).toBeVisible({ timeout: 15_000 });

    // focused in the Code view: its size is the terminal's, and the card shows that many rows
    await code.locator(`${tab} .xterm`).click();
    await expect.poll(async () => (await size()).r, { timeout: 10_000 }).toBeGreaterThan(30);
    const big = await size();
    await expect.poll(() => rowsIn(code, tab)).toBe(big.r);
    await expect.poll(() => rowsIn(page, card)).toBe(big.r);

    // the card focused: the terminal takes the card's smaller size, and the tab follows it
    await page.bringToFront();
    await page.locator(`${card} .xterm`).click();
    await expect.poll(async () => (await size()).r, { timeout: 10_000 }).toBeLessThan(big.r);
    const small = await size();
    expect(small.c).toBeLessThan(big.c);
    await expect.poll(() => rowsIn(page, card)).toBe(small.r);
    await expect.poll(() => rowsIn(code, tab)).toBe(small.r);

    // back to the Code view: its size again, the card follows
    await code.bringToFront();
    await code.locator(`${tab} .xterm`).click();
    await expect.poll(async () => (await size()).r).toBe(big.r);
    await expect.poll(() => rowsIn(page, card)).toBe(big.r);
    await code.close();
  } finally {
    await hub.call('DELETE', `/api/sessions/${s.id}`);
  }
});
