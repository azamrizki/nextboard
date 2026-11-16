import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { expect, test } from './hub';

// An orchestrator card wired to an editor card directs the agents opened inside that editor
// (docs/API-studio.md §7.8). The link is made from the agent's pane, and it is the same
// orchestrator → editor wire as one drawn on the board.

const EDITOR = 'n_edit000001';
const LEAD = 'n_orch000001';

/** The editor card on its own, with the agent's pane in a tab (opened again if a reload lost it). */
async function openEditor(page: Page, pid: string, wid: string, handle?: string) {
  await page.goto(`/p/${pid}/w/${wid}?card=${EDITOR}`);
  await expect(page.locator('.wb-tab', { hasText: 'README.md' })).toBeVisible({ timeout: 15_000 });
  const scrim = page.getByRole('button', { name: 'Close the side bar' });
  if (await scrim.isVisible()) await scrim.dispatchEvent('click');
  if (handle && !(await page.locator('.wb-pane-tab').isVisible())) {
    await page.getByRole('button', { name: 'Open an agent in a new tab' }).click();
    await page.getByRole('menuitem', { name: `Show @${handle}` }).click();
  }
}

test('an orchestrator card directs the agent opened in an editor it is wired to', async ({ page, hub }) => {
  test.setTimeout(120_000);
  const p = await hub.project('Lead and editor');
  writeFileSync(join(p.path, 'README.md'), '# Lead and editor\n');
  await hub.ops(p.wid, [
    { op: 'put', node: { id: EDITOR, kind: 'editor', x: 0, y: 0, w: 1200, h: 720, data: {} } },
    { op: 'put', node: { id: LEAD, kind: 'orchestrator', x: 1300, y: 0, w: 480, h: 560, data: { title: 'Lead' } } },
  ]);
  const wires = async () => ((await hub.call('GET', `/api/workspaces/${p.wid}/board`)).edges ?? []).filter((e: any) => e.kind === 'wire' && e.source === LEAD && e.target === EDITOR).length;
  let sid = '';
  try {
    // an agent inside the editor: no tile, and it records the card it was opened in
    await openEditor(page, p.id, p.wid);
    await page.getByRole('button', { name: 'Open an agent in a new tab' }).click();
    await page.getByRole('menuitem', { name: 'Fake Worker' }).click();
    const pane = page.locator('.wb-pane-tab');
    await expect(pane.locator('.xterm')).toBeVisible({ timeout: 15_000 });
    await expect
      .poll(async () => ((await hub.call('GET', '/api/sessions')) as any[]).find((s) => s.panel && s.workspaceId === p.wid && s.agent === 'fakeworker')?.id ?? '', { timeout: 15_000 })
      .not.toBe('');
    const s = ((await hub.call('GET', '/api/sessions')) as any[]).find((x) => x.panel && x.workspaceId === p.wid && x.agent === 'fakeworker');
    sid = s.id;
    expect(s.card).toBe(EDITOR);
    const handle: string = s.handle;

    // connect from the pane's menu: the wire is on the board
    const link = pane.locator('.wb-pane-link');
    await expect(link.locator('.wb-pane-directed')).toHaveCount(0);
    await link.getByRole('button', { name: 'Orchestrator link' }).click();
    await page.getByRole('menuitem', { name: 'Lead…' }).click();
    await expect.poll(wires).toBe(1);
    await expect(link.locator('.wb-pane-directed')).toHaveText('Directed by Lead');

    // an order typed in the orchestrator card that names the agent reaches it
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${sid}`)).agentState, { timeout: 20_000 }).toBe('ready');
    await page.goto(`/p/${p.id}/w/${p.wid}?card=${LEAD}`);
    const card = page.locator(`.react-flow__node[data-id="${LEAD}"]`);
    await expect(card.getByText('1 agent').first()).toBeVisible({ timeout: 15_000 });
    const box = card.getByRole('combobox', { name: 'Message for Lead' });
    await box.fill(`@${handle} run the tests`);
    await box.press('Enter');
    await expect
      .poll(async () => ((await hub.call('GET', `/api/sessions/${sid}/deliveries`)).items as any[]).some((d) => d.source === 'order' && d.text.includes('run the tests')), { timeout: 30_000 })
      .toBe(true);

    // and disconnecting from the pane takes the wire away
    await openEditor(page, p.id, p.wid, handle);
    const again = page.locator('.wb-pane-tab .wb-pane-link');
    await again.getByRole('button', { name: 'Orchestrator link' }).click();
    await page.getByRole('menuitem', { name: 'Disconnect from Lead' }).click();
    await expect.poll(wires).toBe(0);
    await expect(again.locator('.wb-pane-directed')).toHaveCount(0);
  } finally {
    if (sid) await hub.call('DELETE', `/api/sessions/${sid}`);
  }
});
