import { execFileSync, spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { request as pwRequest, type Page } from '@playwright/test';
import { buildLabel } from '../src/lib/build';
import { expect, test } from './hub';

// Which build runs, and updating it from About this hub (docs/API.md Hub update). The shared hub
// of the tests is a dev build, which asks GitHub nothing. A second hub, stamped as release
// v0.9.0, runs as a stand-in for the installed service: a fake GitHub on loopback publishes
// v0.9.1, a fake systemctl names the hub as the service and records the restart, and the binary
// replaced is a copy of its own. Nothing reaches github.com, a real service or a real binary.

const headers = { 'X-NextBoard': '1' };
const REPO_ROOT = fileURLToPath(new URL('../..', import.meta.url));

test('the header says which build runs and opens About this hub', async ({ page, hub }, info) => {
  const asked: string[] = [];
  page.on('request', (r) => r.url().includes('/api/hub/update') && asked.push(r.url()));
  const h = await hub.call('GET', '/api/hub');
  const label = buildLabel(h);
  expect(h.channel).toBe('dev');
  expect(typeof h.builtAt).toBe('string');
  await page.goto('/');

  if (info.project.name === 'phone') {
    // no room in a phone's header: the build is in its menu
    await expect(page.locator('.app-header .build-label')).toHaveCount(0);
    await page.getByRole('button', { name: 'More', exact: true }).click();
    const item = page.getByRole('menu', { name: 'More' }).getByRole('menuitem', { name: /About this hub/ });
    await expect(item).toContainText(label);
    await item.click();
  } else {
    const button = page.getByRole('button', { name: `About this hub: NextBoard ${label}` });
    await expect(button).toBeVisible();
    await expect(button).toHaveText(label);
    // muted and monospace, as small as the header's other text
    const style = await button.evaluate((el) => ({ font: getComputedStyle(el).fontFamily, size: parseFloat(getComputedStyle(el).fontSize) }));
    expect(style.font).toMatch(/Mono/);
    expect(style.size).toBeLessThanOrEqual(12);
    await button.click();
  }
  const about = page.getByRole('dialog', { name: 'About this hub' });
  await expect(about).toBeVisible();
  const row = (name: string) => about.locator('dt', { hasText: new RegExp(`^${name}$`) }).locator('xpath=following-sibling::dd[1]');
  await expect(row('Version')).toContainText(h.version);
  await expect(row('Version').locator('.badge')).toHaveText('dev');
  await expect(row('Commit')).toHaveText(/^(unknown|[0-9a-f]{7,12}( · with uncommitted changes)?)$/);
  if (h.builtAt) await expect(row('Built')).toContainText(/ago|just now/);
  await expect(row('Update')).toHaveText('dev build');
  // the rows it had stay
  await expect(row('Signed in as')).toHaveText('Admin');
  await shot(page, `${info.project.name}-about-dev`);
  await expect(row('Listening on')).toContainText('127.0.0.1:');
  // a dev build asks no one about releases, not even the hub
  expect(asked).toEqual([]);
});

/** SWEEP_SHOTS=<folder> saves what the owner sees, as sweep.spec.ts does. */
async function shot(page: Page, name: string) {
  const dir = process.env.SWEEP_SHOTS;
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  await page.screenshot({ path: join(dir, `${name}.png`) });
}

/** A port nothing listens on. */
function freePort(): Promise<number> {
  return new Promise((done, fail) => {
    const s = createServer();
    s.once('error', fail);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address() as { port: number };
      s.close(() => done(port));
    });
  });
}

const GOARCH: Record<string, string> = { x64: 'amd64', arm64: 'arm64' };

/** A fake GitHub on loopback with one release of example-owner/nextboard. */
async function fakeGitHub(dir: string, tag: string, exe: string): Promise<{ server: Server; base: string; hits: string[] }> {
  const repo = 'example-owner/nextboard';
  const name = `nextboard_${tag.slice(1)}_${process.platform}_${GOARCH[process.arch] ?? process.arch}.tar.gz`;
  const stage = join(dir, 'stage');
  mkdirSync(stage);
  writeFileSync(join(stage, 'nextboard'), exe, { mode: 0o755 });
  // padding that does not compress, so the download is long enough to show how far it is
  writeFileSync(join(stage, 'README.md'), randomBytes(2 << 20));
  execFileSync('tar', ['-czf', join(dir, name), '-C', stage, 'README.md', 'nextboard']);
  const archive = readFileSync(join(dir, name));
  const sums = `${createHash('sha256').update(archive).digest('hex')}  ${name}\n`;
  const hits: string[] = [];
  const server = createServer((req, res) => {
    hits.push(req.url ?? '');
    const base = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
    const dl = `/${repo}/releases/download/${tag}/`;
    if (req.url === `/repos/${repo}/releases/latest`) {
      res.setHeader('Content-Type', 'application/json');
      res.end(
        JSON.stringify({
          tag_name: tag,
          html_url: `${base}/${repo}/releases/tag/${tag}`,
          draft: false,
          prerelease: false,
          assets: [
            { name, browser_download_url: base + dl + name, size: archive.length },
            { name: 'checksums.txt', browser_download_url: base + dl + 'checksums.txt' },
          ],
        }),
      );
    } else if (req.url === dl + name) {
      res.setHeader('Content-Length', String(archive.length));
      res.end(archive);
    } else if (req.url === dl + 'checksums.txt') {
      res.end(sums);
    } else {
      res.statusCode = 404;
      res.end('not found');
    }
  });
  await new Promise<void>((done) => server.listen(0, '127.0.0.1', done));
  return { server, base: `http://127.0.0.1:${(server.address() as { port: number }).port}`, hits };
}

/** Fails the test on an error in the page or a 5xx, as the shared fixture does for its page. */
function watch(page: Page, problems: string[]) {
  page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/favicon|WebSocket|Failed to load resource/.test(m.text())) problems.push(`console: ${m.text()}`);
  });
  page.on('response', (r) => r.status() >= 500 && problems.push(`${r.status()} ${r.url()}`));
}

test('a release hub run by its service updates itself from About this hub', async ({ browser }, info) => {
  test.skip(info.project.name !== 'light', 'one run is enough: it builds a hub of its own');
  test.setTimeout(240_000);
  // the e2e hub's build, stamped as a release: the build cache has every package, so only the
  // link step runs
  const work = mkdtempSync(join(REPO_ROOT, 'bin', 'e2e-update-'));
  const root = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'release-'));
  const binDir = join(work, 'install');
  mkdirSync(binDir);
  mkdirSync(join(work, 'gotmp'));
  const bin = join(realpathSync(binDir), 'nextboard');
  const v = 'github.com/azamrizki/nextboard/internal/version';
  try {
    execFileSync(
      'nice',
      ['-n', '19', 'go', 'build', '-buildvcs=false', '-ldflags', `-X ${v}.Version=0.9.0 -X ${v}.Commit=abc1234 -X ${v}.Date=2026-11-01T10:00:00Z -X ${v}.Repo=example-owner/nextboard`, '-o', bin, './cmd/nextboard'],
      { cwd: REPO_ROOT, env: { ...process.env, CGO_ENABLED: '0', GOTMPDIR: join(work, 'gotmp') }, stdio: 'pipe' },
    );
  } catch (err) {
    rmSync(work, { recursive: true, force: true });
    throw err;
  }

  const data = join(root, 'data');
  const home = join(root, 'home');
  const tools = join(root, 'tools');
  for (const d of [data, home, tools, join(home, '.config', 'systemd', 'user')]) mkdirSync(d, { recursive: true });
  // the unit `nextboard install` writes, for this binary
  writeFileSync(join(home, '.config', 'systemd', 'user', 'nextboard.service'), `[Service]\nType=simple\nExecStart=${bin} serve --data-dir ${data}\n`);
  // the service manager: the hub (its caller) is the service's main process; a restart is noted
  const calls = join(root, 'systemctl.log');
  writeFileSync(join(tools, 'systemctl'), `#!/bin/sh\necho "$*" >> "${calls}"\ncase "$*" in\n*MainPID*) echo "$PPID" ;;\nesac\n`, { mode: 0o755 });
  const fakeExe = '#!/bin/sh\necho "fake nextboard v0.9.1"\n';
  const gh = await fakeGitHub(root, 'v0.9.1', fakeExe);

  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const env = { ...process.env, HOME: home, CODEX_HOME: join(home, '.codex'), PATH: `${tools}:${process.env.PATH ?? ''}`, NEXTBOARD_TEST_RELEASE_API: gh.base };
  const proc = spawn('timeout', ['300', bin, 'serve', '--listen', `127.0.0.1:${port}`, '--data-dir', data], { env, stdio: 'ignore' });
  const ctx = await browser.newContext({ baseURL: base, storageState: undefined });
  const problems: string[] = [];
  try {
    const api = await pwRequest.newContext({ baseURL: base, extraHTTPHeaders: headers });
    await expect.poll(() => api.get('/api/health').then((r) => r.ok(), () => false), { timeout: 20_000 }).toBe(true);
    const health = await (await api.get('/api/health')).json();
    expect(health).toMatchObject({ version: '0.9.0', commit: 'abc1234', builtAt: '2026-11-01T10:00:00Z', channel: 'stable' });
    const link = execFileSync(bin, ['setup-link', '--data-dir', data], { env }).toString();
    const code = /code=([^\s&]+)/.exec(link)![1];
    expect((await api.post('/api/setup', { data: { code, password: 'rahasia123', hubName: 'releasebox' } })).ok()).toBe(true);
    await api.dispose();
    expect((await ctx.request.post('/api/auth/login', { data: { password: 'rahasia123' }, headers })).ok()).toBe(true);

    const page = await ctx.newPage();
    watch(page, problems);
    // the restarted hub answers with the new version; this one only pretends to restart
    const restarted = () => existsSync(calls) && readFileSync(calls, 'utf8').includes('--user --no-block restart nextboard');
    const asRestarted = { version: '0.9.1', commit: 'def5678' };
    await page.route('**/api/health', async (route) => {
      if (!restarted()) return route.continue();
      await route.fulfill({ json: { ...health, ...asRestarted } });
    });
    await page.route('**/api/hub', async (route) => {
      if (!restarted()) return route.continue();
      const real = await route.fetch();
      await route.fulfill({ response: real, json: { ...(await real.json()), ...asRestarted } });
    });
    await page.goto('/');

    // opening the app with a newer release out asks once (DESIGN.md §3.4 Update reminder)
    const popup = page.getByRole('dialog', { name: 'Update available' });
    await expect(popup).toBeVisible();
    await expect(popup).toContainText('NextBoard v0.9.1 is out. This hub runs 0.9.0.');
    await expect(popup.getByRole('link', { name: 'What’s new' })).toHaveAttribute('href', `${gh.base}/example-owner/nextboard/releases/tag/v0.9.1`);
    await expect(popup.getByRole('button', { name: 'Update now' })).toBeVisible();
    await expect(popup).toContainText('Skip for now asks again in 24 hours.');
    await expect(popup).toHaveCSS('opacity', '1');
    await shot(page, 'release-reminder');
    // Skip this version: kept by the hub, so no browser asks again for v0.9.1
    await popup.getByRole('button', { name: 'Skip this version' }).click();
    await expect(popup).toBeHidden();
    const skipped = await (await ctx.request.get('/api/hub/update')).json();
    expect(skipped).toMatchObject({ skipped: 'v0.9.1', remind: false });
    await page.reload();
    await expect(page.getByRole('button', { name: /^About this hub: NextBoard v0\.9\.0/ })).toBeVisible();
    await expect(popup).toHaveCount(0);
    // Skip for now: as if a newer release asked, the popup puts it off for a day on the hub
    await page.route('**/api/hub/update', async (route) => {
      if (route.request().method() !== 'GET') return route.continue();
      const real = await route.fetch();
      await route.fulfill({ response: real, json: { ...(await real.json()), skipped: '', remind: true } });
    });
    await page.reload();
    await expect(popup).toBeVisible();
    const later = page.waitForRequest((r) => r.url().endsWith('/api/hub/update/remind') && r.method() === 'POST');
    await popup.getByRole('button', { name: 'Skip for now' }).click();
    expect((await later).postDataJSON()).toEqual({ action: 'later' });
    await expect(popup).toBeHidden();
    const putOff = await (await ctx.request.get('/api/hub/update')).json();
    expect(Date.parse(putOff.remindAfter) - Date.now()).toBeGreaterThan(23 * 3600_000);
    await page.unroute('**/api/hub/update');

    // Check for updates: as if the hub's hour-old answer said "up to date", the button makes it
    // ask GitHub now (?fresh=1), and the release shows
    await page.route('**/api/hub/update', async (route) => {
      if (route.request().method() !== 'GET' || route.request().url().includes('fresh=1')) return route.continue();
      const real = await route.fetch();
      const { latest: _l, url: _u, restarts: _r, ...rest } = await real.json();
      await route.fulfill({ response: real, json: { ...rest, status: 'current', remind: false } });
    });
    await page.reload();
    await page.getByRole('button', { name: /^About this hub: NextBoard v0\.9\.0/ }).click();
    const panel = page.getByRole('dialog', { name: 'About this hub' });
    const updateRow = panel.locator('dt', { hasText: /^Update$/ }).locator('xpath=following-sibling::dd[1]');
    await expect(updateRow).toContainText('Up to date');
    await expect(updateRow).toContainText(/checked (just now|\d+ min ago)/);
    const fresh = page.waitForRequest((r) => r.url().includes('/api/hub/update?fresh=1'));
    await updateRow.getByRole('button', { name: 'Check for updates' }).click();
    await fresh;
    await expect(updateRow.getByRole('link', { name: 'v0.9.1 available' })).toBeVisible();
    await shot(page, 'release-check-now');
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await page.unroute('**/api/hub/update');

    // the label, with a dot: a newer release is out
    const label = page.getByRole('button', { name: 'About this hub: NextBoard v0.9.0 · abc1234, v0.9.1 available' });
    await expect(label).toHaveText('v0.9.0 · abc1234');
    await expect(label.locator('.dot')).toHaveCount(1);
    await label.click();
    const about = page.getByRole('dialog', { name: 'About this hub' });
    const row = (name: string) => about.locator('dt', { hasText: new RegExp(`^${name}$`) }).locator('xpath=following-sibling::dd[1]');
    await expect(row('Version')).toContainText('0.9.0');
    await expect(row('Version').locator('.badge')).toHaveText('stable');
    await expect(row('Commit').getByRole('link', { name: 'abc1234' })).toHaveAttribute('href', 'https://github.com/example-owner/nextboard/commit/abc1234');
    await expect(row('Built')).toContainText(/2026/);
    await expect(row('Built')).toContainText(/ago/);
    await expect(row('Update').getByRole('link', { name: 'v0.9.1 available' })).toHaveAttribute('href', `${gh.base}/example-owner/nextboard/releases/tag/v0.9.1`);
    await expect(row('Update')).toContainText('nextboard update');
    await shot(page, 'release-available');
    // GitHub was asked once, by the hub, for the latest stable release
    expect(gh.hits.filter((u) => u.startsWith('/repos/'))).toEqual(['/repos/example-owner/nextboard/releases/latest']);

    await row('Update').getByRole('button', { name: 'Update to v0.9.1' }).click();
    const confirm = page.getByRole('dialog', { name: 'Update to v0.9.1?' });
    await expect(confirm).toContainText('checks its SHA-256');
    await expect(confirm).toContainText('restarts');
    await expect(confirm).toContainText('Nothing else changes');
    await shot(page, 'release-confirm');
    await confirm.getByRole('button', { name: 'Update to v0.9.1' }).click();
    await expect(confirm).toBeHidden();

    // the panel opens again to show it go: the swap, the restart, the hub back as v0.9.1
    await expect(row('Update')).toHaveText(/Restarting…|Updated to v0.9.1/, { timeout: 20_000 });
    await expect.poll(restarted, { timeout: 10_000 }).toBe(true);
    await expect(row('Update')).toContainText('Updated to v0.9.1', { timeout: 20_000 });
    await expect(page.getByRole('status').filter({ hasText: 'Updated to v0.9.1.' })).toBeVisible();
    await shot(page, 'release-updated');
    expect(readFileSync(bin, 'utf8')).toBe(fakeExe);
    expect(existsSync(`${bin}.old`)).toBe(true);
    expect(existsSync(`${bin}.new`)).toBe(false);
    // the label follows the hub it now talks to
    await expect(page.getByRole('button', { name: /^About this hub: NextBoard v0\.9\.1 · def5678/ })).toBeVisible();
    // and the real hub knows it swapped, until it restarts for real
    const after = await (await ctx.request.get('/api/hub/update')).json();
    expect(after.installed).toBe('v0.9.1');
    expect(problems).toEqual([]);
  } finally {
    await ctx.close();
    const gone = proc.exitCode !== null ? Promise.resolve() : new Promise((done) => proc.once('exit', done));
    proc.kill('SIGTERM');
    gh.server.close();
    await Promise.race([gone, new Promise((done) => setTimeout(done, 8000))]);
    rmSync(work, { recursive: true, force: true });
  }
});
