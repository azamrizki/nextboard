import { spawn } from 'node:child_process';
import { writeFileSync } from 'node:fs';
import { connect } from 'node:net';
import { join } from 'node:path';
import { expect, test } from './hub';

const portOpen = (port: number) =>
  new Promise<boolean>((resolve) => {
    const c = connect(port, '127.0.0.1');
    c.once('connect', () => {
      c.destroy();
      resolve(true);
    });
    c.once('error', () => resolve(false));
  });

// A program that already runs, with no inspector open: the panel lists it, opens the inspector,
// stops at a breakpoint, and on Detach the program keeps running and the inspector is closed.
test('the debugger attaches to a running node process and lets go of it', async ({ page, hub }) => {
  const p = await hub.project('Attach');
  const file = join(p.path, 'server.js');
  writeFileSync(file, 'let n = 0;\nsetInterval(() => {\n  n = n + 1;\n  console.log("tick", n);\n}, 100);\n');
  // --inspect-port=0: the signal picks a free port, since 9229 may be taken on this machine
  const program = spawn(process.execPath, ['--inspect-port=127.0.0.1:0', file], { cwd: p.path, stdio: 'ignore' });
  try {
    await new Promise((r) => setTimeout(r, 700));
    await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_edit000003', kind: 'editor', x: 0, y: 0, w: 1100, h: 700, data: {} } }]);
    await page.goto(`/p/${p.id}/w/${p.wid}?card=n_edit000003`);
    await page.getByRole('treeitem', { name: 'server.js' }).click();
    await expect(page.locator('.wb-tab', { hasText: 'server.js' })).toBeVisible({ timeout: 15_000 });
    await page.getByRole('button', { name: 'Run and Debug' }).click();

    const choose = page.getByRole('combobox', { name: 'Debug configuration' });
    await expect(choose.locator('option', { hasText: 'Attach to a running Node.js process' })).toHaveCount(1, { timeout: 15_000 });
    await choose.selectOption('Attach to a running Node.js process');
    await page.getByRole('button', { name: 'Start debugging (F5)' }).click();

    const dialog = page.getByRole('dialog', { name: 'Attach to a Node.js process' });
    await expect(dialog).toContainText('anyone with a login');
    await dialog.getByRole('button', { name: new RegExp(`pid ${program.pid}\\b`) }).click();
    await expect(dialog).toHaveCount(0, { timeout: 15_000 });

    const detach = page.getByRole('button', { name: 'Detach (Shift+F5)' });
    await expect(detach).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.wb-dbg-console, .wb-panel').getByText('Attached.', { exact: false }).first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText('tick', { exact: false }).first()).toBeVisible({ timeout: 15_000 });

    const listening = await page.evaluate(async ({ pid, id }) => {
      const r = await fetch(`/api/projects/${id}/dap/node/inspect`, { method: 'POST', headers: { 'content-type': 'application/json', 'X-NextBoard': '1' }, body: JSON.stringify({ pid }) });
      return (await r.json()) as { port: number; opened: boolean };
    }, { pid: program.pid, id: p.id });
    expect(listening.opened).toBe(false);
    expect(await portOpen(listening.port)).toBe(true);

    await detach.click();
    await expect(detach).toHaveCount(0, { timeout: 15_000 });
    await expect.poll(() => portOpen(listening.port), { timeout: 10_000 }).toBe(false);
    expect(program.exitCode).toBeNull();
    expect(program.signalCode).toBeNull();
  } finally {
    program.kill('SIGKILL');
  }
});
