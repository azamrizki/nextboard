import { readFileSync, readdirSync, readlinkSync } from 'node:fs';
import { expect, test } from './hub';

// NextBoard Chromium for real (docs/API-gui.md §8.3): the hub downloads Chrome for Testing,
// and a web tile then runs it, sandboxed, from the hub's folder. It downloads about 170 MB, so
// it runs only when asked: NEXTBOARD_E2E_DOWNLOAD_BROWSER=1.

const procs = () =>
  readdirSync('/proc')
    .filter((d) => /^\d+$/.test(d))
    .map((pid) => {
      try {
        return { pid, argv: readFileSync(`/proc/${pid}/cmdline`, 'utf8').split('\0'), env: [] as string[] };
      } catch {
        return null;
      }
    })
    .filter((p): p is { pid: string; argv: string[]; env: string[] } => !!p);

// a process of NextBoard Chromium itself: its program comes first, not inside another command
const managed = (p: { argv: string[] }) => /^\S*\/browsers\/chrome-[\d.]+\/chrome-linux[\w-]*\/chrome(\s|$)/.test(p.argv.join(' '));

test('a web tile runs NextBoard Chromium, sandboxed, once the hub has downloaded it', async ({ page, hub }, info) => {
  test.skip(process.env.NEXTBOARD_E2E_DOWNLOAD_BROWSER !== '1', 'downloads a browser: NEXTBOARD_E2E_DOWNLOAD_BROWSER=1');
  test.skip(info.project.name !== 'light', 'once');
  test.setTimeout(900_000);
  const gui = await hub.call('GET', '/api/gui/apps');
  test.skip(!gui.available, 'no desktop apps here');
  let st = await hub.call('GET', '/api/gui/browser');
  test.skip(!st.supported, 'not on this machine');

  await hub.call('POST', '/api/gui/browser/install');
  await expect
    .poll(async () => (st = await hub.call('GET', '/api/gui/browser')).state, { timeout: 600_000, intervals: [2000] })
    .toMatch(/^(idle|error)$/);
  expect(st.error ?? '').toBe('');
  expect(st.installed).toMatch(/^\d+\.\d+\.\d+\.\d+$/);
  test.skip(st.sandbox === 'setup', `this machine needs: ${st.setupCommand}`);

  // an outside site: a local address would be a preview, not a page for the real browser
  const url = 'https://example.com/';
  const p = await hub.project('Managed browser');
  const sessions = async () => (await hub.call('GET', '/api/gui/sessions')) as { id: string; appId: string; state: string }[];
  // the passkey extension, which branded Chrome no longer loads from the command line
  await hub.call('PUT', '/api/vault/passkeys', { enabled: true });
  try {
    await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_managedweb', kind: 'web', x: 0, y: 0, w: 1100, h: 760, data: { url, realBrowser: true } } }]);
    await page.goto(`/p/${p.id}/w/${p.wid}?card=n_managedweb`);
    const tile = page.locator('.react-flow__node.is-focused');
    await tile.getByRole('button', { name: 'Open here' }).click({ timeout: 20_000 });
    await expect.poll(async () => (await sessions())[0]?.state, { timeout: 90_000 }).toBe('running');
    expect((await sessions())[0].appId).toBe('nextboard-chromium');

    // the browser's own process: from the hub's folder, on a sandbox, without the testing bar
    const browser = procs().find((x) => managed(x) && !x.argv.join(' ').includes('--type='));
    expect(browser, 'the browser process').toBeTruthy();
    // Chrome rewrites its process title: the whole command line can be one string
    const cmd = ` ${browser!.argv.join(' ')} `;
    expect(cmd).not.toContain(' --no-sandbox ');
    expect(cmd).toContain(' --disable-infobars ');
    expect(cmd).toContain(' --disable-features=MediaRouter,');
    const ext = / --load-extension=(\S+)/.exec(cmd)?.[1];
    expect(ext, 'the passkey extension is handed to it').toBeTruthy();
    // and it takes it: the profile lists the extension's folder
    const profileDir = / --user-data-dir=(\S+)/.exec(cmd)![1];
    await expect
      .poll(
        () => {
          for (const f of ['Secure Preferences', 'Preferences']) {
            try {
              if (readFileSync(`${profileDir}/Default/${f}`, 'utf8').includes(JSON.stringify(ext).slice(1, -1))) return true;
            } catch {
              /* not written yet */
            }
          }
          return false;
        },
        { timeout: 30_000 },
      )
      .toBe(true);
    // and its pages are sandboxed: each renderer is in a namespace of its own, which this test's
    // user cannot even look into (Chrome also overwrites its environment, so that tells nothing)
    const ours = readlinkSync('/proc/self/ns/pid');
    const rendererProcs = () => procs().filter((x) => managed(x) && x.argv.join(' ').includes(' --type=renderer '));
    await expect.poll(() => rendererProcs().length, { timeout: 30_000 }).toBeGreaterThan(0);
    await page.waitForTimeout(3000);
    const renderers = rendererProcs();
    const seen = renderers.map((r) => {
      let ns = 'hidden';
      try {
        ns = readlinkSync(`/proc/${r.pid}/ns/pid`);
      } catch {
        /* unreadable: another namespace */
      }
      let seccomp = '?';
      try {
        seccomp = /Seccomp:\s+(\d)/.exec(readFileSync(`/proc/${r.pid}/status`, 'utf8'))?.[1] ?? '?';
      } catch {
        /* gone */
      }
      const flags = r.argv.join(' ').match(/--(extension-process|renderer-client-id=\d+|no-zygote|lang=[\w-]+)/g) ?? [];
      return { pid: r.pid, ns: ns === ours ? 'ours' : ns, seccomp, flags: flags.join(' ') };
    });
    for (const r of seen) expect(r, `renderer ${r.pid}`).toMatchObject({ seccomp: '2' });
    for (const r of seen) expect(r.ns, `renderer ${r.pid}`).not.toBe('ours');

    await page.waitForTimeout(4000);
    await tile.screenshot({ path: info.outputPath('managed-browser-tile.png') });
  } finally {
    await hub.call('PUT', '/api/vault/passkeys', { enabled: false });
    for (const s of await sessions()) await hub.call('DELETE', `/api/gui/sessions/${s.id}`);
    await expect.poll(async () => (await sessions()).length, { timeout: 30_000 }).toBe(0);
  }
});

test('Settings offers NextBoard Chromium, and downloads nothing by itself', async ({ page, hub }) => {
  const st = await hub.call('GET', '/api/gui/browser');
  await page.goto('/settings/studios');
  const card = page.getByRole('region', { name: 'NextBoard Chromium' });
  await expect(card).toBeVisible();
  if (!st.supported) {
    await expect(card.getByText('Only on Linux', { exact: false })).toBeVisible();
    return;
  }
  if (st.installed) return; // a run with NEXTBOARD_E2E_DOWNLOAD_BROWSER before this one
  await expect(card.getByRole('button', { name: 'Download' })).toBeVisible();
  await expect(card.getByText('Not downloaded', { exact: false })).toBeVisible();
  await expect(card.getByRole('switch', { name: 'Keep it up to date' }).or(card.getByRole('checkbox', { name: 'Keep it up to date' }))).toBeVisible();
  expect((await hub.call('GET', '/api/gui/browser')).state).toBe('idle');
});
