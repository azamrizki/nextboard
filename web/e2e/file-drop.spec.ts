import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator } from '@playwright/test';
import { expect, test } from './hub';

// Files dragged from the computer (docs/DESIGN.md §3.13 "Files from the computer"): every place that
// takes a pasted file takes a dropped one the same way. A console gets the file on its machine and
// its path typed, the lead's box attaches it, the editor saves it next to the open file and writes
// its name, the file tree saves it in the folder it was dropped on. Anywhere else the board's own
// drop still makes cards.

type Drop = { name: string; text: string; type?: string };

/** What a browser hands over when files from the computer are dropped on el. */
async function dropFiles(el: Locator, files: Drop[]) {
  await el.evaluate((node, list) => {
    const dt = new DataTransfer();
    for (const f of list) dt.items.add(new File([f.text], f.name, { type: f.type ?? 'text/plain' }));
    const r = node.getBoundingClientRect();
    const init = { clientX: r.left + r.width / 2, clientY: r.top + Math.min(r.height / 2, 40), bubbles: true, cancelable: true, dataTransfer: dt };
    node.dispatchEvent(new DragEvent('dragenter', init));
    node.dispatchEvent(new DragEvent('dragover', init));
    node.dispatchEvent(new DragEvent('drop', init));
  }, files);
}

const read = (dir: string, file: string) => {
  try {
    return readFileSync(join(dir, file), 'utf8');
  } catch {
    return '';
  }
};

test('a file dropped on an agent console is sent to its machine and its path typed', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light', 'one board is enough');
  test.setTimeout(90_000);
  const p = await hub.project('Drop console');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const s = await hub.call('POST', '/api/sessions', { agent: 'fakeclaude', workspaceId: p.wid });
  try {
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState, { timeout: 20_000 }).toBe('ready');
    const term = page.locator(`[data-session="${s.id}"] .nb-term`);
    await expect(term).toBeVisible();
    await dropFiles(term, [{ name: 'crash report.txt', text: 'stack trace here\n' }]);
    // the console has the keyboard: what was typed goes with Enter
    await page.waitForTimeout(800);
    await page.keyboard.press('Enter');
    // the hub names the file safely on its machine
    await expect.poll(() => read(p.path, '.fake-claude.log'), { timeout: 20_000 }).toContain('crash-report.txt');
    const path = /(\/\S*\/attachments\/\S*crash-report\.txt)/.exec(read(p.path, '.fake-claude.log'))?.[1] ?? '';
    expect(path).not.toBe('');
    expect(readFileSync(path, 'utf8')).toBe('stack trace here\n');
    // the board did not take it too
    await expect(page.getByRole('dialog', { name: /crash report/ })).toHaveCount(0);
  } finally {
    await hub.call('DELETE', `/api/sessions/${s.id}`);
  }
});

test('files dropped on the lead are attached, on the board card and in the command bar', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light', 'one board is enough');
  const p = await hub.project('Drop lead');
  const LEAD = 'n_orch00drop1';
  await hub.ops(p.wid, [{ op: 'put', node: { id: LEAD, kind: 'orchestrator', x: 0, y: 0, w: 480, h: 560, data: { title: 'Lead' } } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const card = page.locator(`.react-flow__node[data-id="${LEAD}"]`);
  await expect(card.getByRole('combobox', { name: 'Message for Lead' })).toBeVisible({ timeout: 15_000 });

  await dropFiles(card.locator('.orch-body'), [{ name: 'spec.md', text: '# Spec\n' }]);
  const chip = card.locator('.attach-chip', { hasText: 'spec.md' });
  await expect(chip).toBeVisible();
  await expect(chip.locator('.attach-meta')).not.toHaveText('Attaching…', { timeout: 15_000 });
  await expect(card.getByRole('combobox', { name: 'Message for Lead' })).toBeFocused();
  await expect(page.getByRole('dialog', { name: /spec\.md/ })).toHaveCount(0);

  await dropFiles(page.locator('.cmdbar-wrap'), [{ name: 'notes.txt', text: 'remember this\n' }]);
  await expect(page.locator('.cmdbar-wrap .attach-chip', { hasText: 'notes.txt' })).toBeVisible();
  await expect(page.getByRole('dialog', { name: /notes\.txt/ })).toHaveCount(0);

  // the empty board still takes a drop as cards
  await dropFiles(page.locator('.react-flow__pane'), [{ name: 'idea.txt', text: 'a card\n' }]);
  const dialog = page.getByRole('dialog', { name: 'Add “idea.txt”' });
  await expect(dialog).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(page.locator('.board-canvas.is-drop')).toHaveCount(0);
});

test('a file dropped in the editor lands next to the open file, and one on the tree in that folder', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light', 'one board is enough');
  const p = await hub.project('Drop editor');
  writeFileSync(join(p.path, 'app.ts'), '// Drop editor\n');
  mkdirSync(join(p.path, 'docs'));
  writeFileSync(join(p.path, 'docs', 'keep.txt'), 'keep\n');
  const EDITOR = 'n_edit00drop1';
  await hub.ops(p.wid, [{ op: 'put', node: { id: EDITOR, kind: 'editor', x: 0, y: 0, w: 1200, h: 720, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=${EDITOR}`);
  const side = page.locator('.wb-side');
  await side.getByRole('treeitem', { name: 'app.ts' }).click({ timeout: 20_000 });
  await expect(page.locator('.wb-tab', { hasText: 'app.ts' })).toBeVisible();
  const editor = page.locator('.wb-monaco').first();
  await expect(editor.locator('.view-lines')).toContainText('Drop editor');

  await dropFiles(editor, [{ name: 'fixture.json', text: '{"ok":true}\n', type: 'application/json' }]);
  await expect.poll(() => read(p.path, 'fixture.json'), { timeout: 15_000 }).toBe('{"ok":true}\n');
  // its name is written where it was dropped
  await expect(editor.locator('.view-lines')).toContainText('fixture.json');

  await dropFiles(side.getByRole('treeitem', { name: 'docs' }), [{ name: 'guide.txt', text: 'how to\n' }]);
  await expect.poll(() => read(join(p.path, 'docs'), 'guide.txt'), { timeout: 15_000 }).toBe('how to\n');
  await expect(page.getByRole('dialog', { name: /guide\.txt/ })).toHaveCount(0);
});
