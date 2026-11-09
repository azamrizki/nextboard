import { spawn } from 'node:child_process';
import { expect, test } from './hub';

// What a browser keeps from a page, and what it only gives to a secure one. Real Chrome in the
// tile, real PipeWire behind it, and Chromium's fake microphone in front.
test.use({
  permissions: ['microphone'],
  launchOptions: { args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] },
});

async function browserTile(hub: any, page: any, name: string) {
  const status = await hub.call('GET', '/api/gui/apps');
  test.skip(!status.available, 'no display packages on this machine');
  const p = await hub.project(name);
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_inputtile1', kind: 'web', x: 0, y: 0, w: 900, h: 620, data: { url: 'https://example.com/', realBrowser: true } } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const tile = page.locator('.react-flow__node[data-id="n_inputtile1"]');
  await tile.getByRole('button', { name: 'Open here' }).click();
  return tile;
}

const sessions = async (hub: any) => (await hub.call('GET', '/api/gui/sessions')) as { id: string; title: string; mic?: boolean; state: string }[];

async function closeAll(hub: any) {
  for (const s of await sessions(hub)) await hub.call('DELETE', `/api/gui/sessions/${s.id}`);
  await expect.poll(async () => (await sessions(hub)).length, { timeout: 30_000 }).toBe(0);
}

test('the keys a browser holds reach the app from the tile menu', async ({ page, hub }, info) => {
  test.slow();
  try {
    const tile = await browserTile(hub, page, `Keys ${info.project.name}`);
    await expect.poll(async () => (await sessions(hub))[0]?.state, { timeout: 60_000 }).toBe('running');
    const menu = tile.getByRole('button', { name: /^Keys for / });
    await expect(menu).toBeVisible({ timeout: 30_000 });

    // Ctrl+T opens a tab in the real browser: its window title says so
    await menu.click();
    await page.getByRole('menuitem', { name: 'New tab (Ctrl+T)' }).click();
    await expect.poll(async () => (await sessions(hub))[0].title, { timeout: 20_000 }).toMatch(/New Tab/i);

    // Ctrl+W closes it again, and the first page comes back
    await menu.click();
    await page.getByRole('menuitem', { name: 'Close tab (Ctrl+W)' }).click();
    await expect.poll(async () => (await sessions(hub))[0].title, { timeout: 20_000 }).not.toMatch(/New Tab/i);
  } finally {
    await closeAll(hub);
  }
});

test('the microphone reaches the session, and only after a click', async ({ page, hub }, info) => {
  test.slow();
  try {
    const tile = await browserTile(hub, page, `Mic ${info.project.name}`);
    await expect.poll(async () => (await sessions(hub))[0]?.state, { timeout: 60_000 }).toBe('running');
    const [s] = await sessions(hub);
    test.skip(!s.mic, 'no PipeWire on this machine');

    // Off to begin with: the hub has no microphone socket open.
    const on = tile.getByRole('button', { name: 'Turn the microphone on' });
    await expect(on).toBeVisible({ timeout: 30_000 });
    await expect(tile).not.toContainText('microphone on');

    // record from what the app in the session records from
    const rec = spawn('pw-record', ['--target', `nextboard_${s.id}_mic`, '--raw', '--format', 's16', '--rate', '48000', '--channels', '1', '-'], { stdio: ['ignore', 'pipe', 'ignore'] });
    let loud = false;
    rec.stdout.on('data', (b: Buffer) => {
      for (let i = 0; i + 1 < b.length; i += 2) if (Math.abs(b.readInt16LE(i)) > 500) loud = true;
    });
    try {
      await on.click();
      await expect(tile).toContainText('microphone on', { timeout: 15_000 });
      await expect.poll(() => loud, { timeout: 20_000 }).toBe(true);

      // Off: the hub takes a second viewer again, as the socket is closed.
      await tile.getByRole('button', { name: 'Turn the microphone off' }).click();
      await expect(tile).not.toContainText('microphone on', { timeout: 15_000 });
      await expect(on).toBeVisible();
    } finally {
      rec.kill('SIGKILL');
    }
  } finally {
    await closeAll(hub);
  }
});

// The owner's hub runs on plain HTTP, where a page gets no microphone at all. It says so.
test('on plain HTTP the microphone button explains itself', async ({ page, hub }, info) => {
  test.slow();
  await page.context().addInitScript(() => Object.defineProperty(window, 'isSecureContext', { value: false }));
  try {
    const tile = await browserTile(hub, page, `Plain ${info.project.name}`);
    await expect.poll(async () => (await sessions(hub))[0]?.state, { timeout: 60_000 }).toBe('running');
    const [s] = await sessions(hub);
    test.skip(!s.mic, 'no PipeWire on this machine');
    await tile.getByRole('button', { name: 'Turn the microphone on' }).click({ timeout: 30_000 });
    await expect(page.getByText('plain HTTP', { exact: false }).first()).toBeVisible({ timeout: 15_000 });
    await expect(tile).not.toContainText('microphone on');
  } finally {
    await closeAll(hub);
  }
});
