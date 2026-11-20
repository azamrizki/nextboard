import { execFileSync, spawn } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { join } from 'node:path';
import { crc32, deflateSync } from 'node:zlib';
import { request as pwRequest, type Page } from '@playwright/test';
import { expect, test } from './hub';
import { SSHD } from './sshd';

// Many at once (DESIGN.md §3.18, API-board §1, docs/API.md Machines): select mode on the project
// list, the Archived view, the remove dialog, machines that are in use, groups; a project's cover
// and icon, uploaded and made by the fake Codex (e2e/fake-codex.sh, first on the hub's PATH); and
// the card of a project in several parts. Only made-up names.

const headers = { 'X-NextBoard': '1' };

/** A PNG of one colour, w × h. */
function png(w: number, h: number, rgb: [number, number, number]): Buffer {
  const chunk = (type: string, data: Buffer) => {
    const head = Buffer.alloc(4);
    head.writeUInt32BE(data.length);
    const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
    const sum = Buffer.alloc(4);
    sum.writeUInt32BE(crc32(body) >>> 0);
    return Buffer.concat([head, body, sum]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr.set([8, 2, 0, 0, 0], 8);
  const row = Buffer.concat([Buffer.from([0]), Buffer.from(Array.from({ length: w }, () => rgb).flat())]);
  const raw = Buffer.concat(Array.from({ length: h }, () => row));
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk('IHDR', ihdr), chunk('IDAT', deflateSync(raw)), chunk('IEND', Buffer.alloc(0))]);
}

const card = (page: Page, id: string) => page.locator(`.project-card[data-project="${id}"]`);
const bar = (page: Page) => page.getByRole('toolbar', { name: /^Selected / });
const tag = () => Date.now().toString(36).slice(-5);

async function filterTo(page: Page, text: string) {
  await page.getByRole('searchbox', { name: /^Filter / }).fill(text);
}

test('several projects: Shift picks a run, Ctrl one, then archive, find them under Archived, restore', async ({ page, hub }) => {
  const t = `Kestrel${tag()}`;
  const list = [];
  for (let i = 0; i < 4; i++) list.push(await hub.project(t));
  await page.goto('/');
  await filterTo(page, t);
  for (const p of list) await expect(card(page, p.id)).toBeVisible();

  // the order on screen decides what a Shift run takes
  const order = await page.locator('.project-card').evaluateAll((els) => els.map((e) => (e as HTMLElement).dataset.project));
  const [first, , third, last] = order as string[];
  const name = (id: string) => list.find((p) => p.id === id)!.name;

  await page.getByRole('button', { name: 'Select', exact: true }).click();
  await expect(bar(page)).toContainText('Select projects');
  await page.getByRole('checkbox', { name: name(first), exact: true }).click();
  await page.getByRole('checkbox', { name: name(third), exact: true }).click({ modifiers: ['Shift'] });
  await expect(bar(page)).toContainText('3 selected');
  await expect(page.getByRole('checkbox', { name: name(last), exact: true })).toHaveAttribute('aria-checked', 'false');
  await page.getByRole('checkbox', { name: name(order[1]!), exact: true }).click({ modifiers: ['ControlOrMeta'] });
  await expect(bar(page)).toContainText('2 selected');
  await page.getByRole('checkbox', { name: name(order[1]!), exact: true }).click({ modifiers: ['ControlOrMeta'] });
  await expect(bar(page)).toContainText('3 selected');
  // select-all takes what the filter shows, and a second press none of it
  await page.getByRole('button', { name: 'Select all' }).click();
  await expect(bar(page)).toContainText('4 selected');
  await page.getByRole('button', { name: 'Select none' }).click();
  await expect(bar(page)).toContainText('Select projects');
  await page.getByRole('checkbox', { name: name(first), exact: true }).click();
  await page.getByRole('checkbox', { name: name(third), exact: true }).click({ modifiers: ['Shift'] });

  // Archive is the remove dialog with Archive chosen
  await bar(page).getByRole('button', { name: 'Archive' }).click();
  const dialog = page.getByRole('dialog', { name: 'Remove 3 projects?' });
  await expect(dialog.getByRole('radio', { name: /^Archive \(restore any time\)/ })).toHaveAttribute('aria-checked', 'true');
  await expect(dialog).toContainText('folders and files stay on disk');
  await dialog.getByRole('button', { name: 'Archive 3 projects' }).click();
  await expect(dialog).toBeHidden();
  await expect(bar(page)).toBeHidden();
  const archivedIds = order.slice(0, 3) as string[];
  for (const id of archivedIds) await expect(card(page, id)).toHaveCount(0);
  await expect(card(page, last)).toBeVisible();

  const archived = (await hub.call('GET', '/api/projects?archived=1')) as { id: string; archivedAt?: string }[];
  for (const id of archivedIds) expect(archived.find((p) => p.id === id)?.archivedAt).toBeTruthy();
  const active = (await hub.call('GET', '/api/projects')) as { id: string }[];
  expect(active.filter((p) => archivedIds.includes(p.id))).toEqual([]);
  for (const p of list) expect(existsSync(p.path)).toBe(true);

  // the Archived view: each with Restore, and the same select mode
  await page.getByRole('radio', { name: /^Archived/ }).click();
  const rows = page.getByRole('list', { name: 'Archived projects' });
  for (const id of archivedIds) await expect(rows.getByRole('article', { name: name(id) })).toBeVisible();
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  for (const id of archivedIds) await rows.getByRole('checkbox', { name: `Select ${name(id)}` }).click();
  await expect(bar(page)).toContainText('3 selected');
  await bar(page).getByRole('button', { name: 'Restore' }).click();
  await expect(bar(page)).toBeHidden();
  await expect.poll(async () => ((await hub.call('GET', '/api/projects')) as { id: string }[]).filter((p) => archivedIds.includes(p.id)).length).toBe(3);
  // the Archived view gives way to the projects once it is empty
  const projectsTab = page.getByRole('radio', { name: /^Projects/ });
  if (await projectsTab.isVisible()) await projectsTab.click();
  for (const id of archivedIds) await expect(card(page, id)).toBeVisible();
});

test('one project deleted permanently: the word is asked for, the running console counted, the folder kept', async ({ page, hub }) => {
  const p = await hub.project('Plover');
  await hub.console(p.wid);
  await page.goto('/');
  await page.getByRole('button', { name: `Actions for ${p.name}` }).click();
  await page.getByRole('menuitem', { name: 'Remove project…' }).click();
  const dialog = page.getByRole('dialog', { name: `Remove “${p.name}”?` });
  await expect(dialog).toContainText(/1 running console will stop/);
  await dialog.getByRole('radio', { name: /^Delete permanently/ }).click();
  await expect(dialog).toContainText('What is deleted');
  await expect(dialog).toContainText('Project memory');
  const go = dialog.getByRole('button', { name: 'Delete permanently' });
  await expect(go).toBeDisabled();
  await dialog.getByLabel('Type delete to confirm').fill('delet');
  await expect(go).toBeDisabled();
  await dialog.getByLabel('Type delete to confirm').fill('delete');
  await go.click();
  await expect(dialog).toBeHidden();
  await expect(card(page, p.id)).toHaveCount(0);
  const all = [...((await hub.call('GET', '/api/projects')) as { id: string }[]), ...((await hub.call('GET', '/api/projects?archived=1')) as { id: string }[])];
  expect(all.find((x) => x.id === p.id)).toBeUndefined();
  expect(existsSync(p.path)).toBe(true);
});

test('groups: several deleted at once, what was inside moves up', async ({ page, hub }) => {
  const t = tag();
  const clients = await hub.call('POST', '/api/project-groups', { name: `Clients ${t}` });
  const globex = await hub.call('POST', '/api/project-groups', { name: `Globex ${t}`, parentId: clients.id });
  const umbrella = await hub.call('POST', '/api/project-groups', { name: `Umbrella ${t}` });
  const p = await hub.project('Portal');
  await hub.call('PATCH', `/api/projects/${p.id}`, { groupId: globex.id });

  await page.goto('/');
  await page.getByRole('radio', { name: /^Groups/ }).click();
  const list = page.getByRole('list', { name: 'Project groups' });
  await expect(list.getByRole('button', { name: `Globex ${t}`, exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  await list.getByRole('checkbox', { name: `Select group Clients ${t}` }).click();
  await list.getByRole('checkbox', { name: `Select group Umbrella ${t}` }).click();
  await bar(page).getByRole('button', { name: 'Delete groups' }).click();
  const dialog = page.getByRole('dialog', { name: 'Delete 2 groups?' });
  await expect(dialog).toContainText('1 sub-group');
  await dialog.getByRole('button', { name: 'Delete groups' }).click();
  await expect(dialog).toBeHidden();
  await expect(list.getByRole('button', { name: `Clients ${t}`, exact: true })).toHaveCount(0);

  const groups = (await hub.call('GET', '/api/project-groups')) as { id: string; parentId?: string }[];
  expect(groups.find((g) => g.id === clients.id || g.id === umbrella.id)).toBeUndefined();
  expect(groups.find((g) => g.id === globex.id)?.parentId ?? '').toBe('');
  const moved = ((await hub.call('GET', '/api/projects')) as { id: string; groupId?: string }[]).find((x) => x.id === p.id);
  expect(moved?.groupId).toBe(globex.id);
});

test('a cover and an icon: uploaded, on the card without moving it, removed', async ({ page, hub }) => {
  const p = await hub.project('Heron');
  await page.goto('/');
  const c = card(page, p.id);
  const cover = c.locator('.project-cover');
  await expect(cover).toBeVisible();
  const before = (await cover.boundingBox())!.height;
  const head = (await c.locator('.project-head').boundingBox())!.height;

  await page.getByRole('button', { name: `Actions for ${p.name}` }).click();
  await page.getByRole('menuitem', { name: 'Cover & icon…' }).click();
  const dialog = page.getByRole('dialog', { name: `Cover & icon of ${p.name}` });
  await dialog.getByLabel('Upload a cover').setInputFiles({ name: 'poster.png', mimeType: 'image/png', buffer: png(2400, 1200, [32, 96, 160]) });
  await expect(c.locator('.project-cover img.is-loaded')).toBeVisible();
  await dialog.getByLabel('Upload an icon').setInputFiles({ name: 'mark.png', mimeType: 'image/png', buffer: png(300, 200, [200, 120, 40]) });
  await expect(c.locator('.project-icon img.is-loaded')).toBeVisible();
  await expect(dialog.getByText('Uploaded', { exact: false }).first()).toBeVisible();

  const saved = ((await hub.call('GET', '/api/projects')) as { id: string; cover?: { assetId: string; source: string }; icon?: { assetId: string; source: string } }[]).find((x) => x.id === p.id)!;
  expect(saved.cover?.source).toBe('upload');
  expect(saved.icon?.source).toBe('upload');
  // the picture was made smaller in the browser before it went: the cover at most 1600 wide, the icon a square
  const asset = async (id: string) => page.evaluate(async (u) => {
    const b = await createImageBitmap(await (await fetch(u)).blob());
    return [b.width, b.height];
  }, `/api/assets/${id}`);
  expect(await asset(saved.cover!.assetId)).toEqual([1600, 800]);
  expect(await asset(saved.icon!.assetId)).toEqual([200, 200]);
  // the boxes had their size before the pictures came
  expect((await cover.boundingBox())!.height).toBe(before);
  expect((await c.locator('.project-head').boundingBox())!.height).toBe(head);

  await dialog.getByRole('region', { name: 'Cover' }).getByRole('button', { name: 'Remove' }).click();
  await expect(c.locator('.project-cover img')).toHaveCount(0);
  await expect.poll(async () => ((await hub.call('GET', '/api/projects')) as { id: string; cover?: unknown }[]).find((x) => x.id === p.id)?.cover).toBeUndefined();
  await dialog.getByRole('button', { name: 'Done' }).click();
});

test('Create with AI makes a cover with the hub’s Codex, shows the job, and can be cancelled', async ({ page, hub }) => {
  const cap = await hub.call('GET', '/api/art/capability');
  expect(cap).toMatchObject({ available: true, tool: 'codex' });
  const p = await hub.project('Osprey');
  writeFileSync(join(p.path, 'README.md'), '# Osprey\n\nA made-up shop for kites and string.\n');
  await page.goto('/');
  await page.getByRole('button', { name: `Actions for ${p.name}` }).click();
  await page.getByRole('menuitem', { name: 'Cover & icon…' }).click();
  const dialog = page.getByRole('dialog', { name: `Cover & icon of ${p.name}` });
  await expect(dialog).toContainText('sends the project’s name, README excerpt');
  const coverSlot = dialog.getByRole('region', { name: 'Cover' });
  // first the optional context, kept for next time
  await coverSlot.getByRole('button', { name: 'Create with AI' }).click();
  const ask = page.getByRole('dialog', { name: `Create: cover of ${p.name}` });
  await ask.getByLabel(/^Context/).fill('Kites over a windy beach at sunset.');
  await expect(ask).toContainText('35/1000');
  await ask.getByRole('button', { name: 'Create with this context' }).click();
  await expect(ask).toHaveCount(0);
  await expect(coverSlot.getByRole('status')).toBeVisible();
  await expect(coverSlot.getByRole('button', { name: 'Cancel' })).toBeVisible();
  await expect(coverSlot.getByRole('button', { name: 'Create again' })).toBeVisible({ timeout: 30_000 });
  await expect(card(page, p.id).locator('.project-cover img.is-loaded')).toBeVisible();
  const saved = ((await hub.call('GET', '/api/projects')) as { id: string; cover?: { source: string } }[]).find((x) => x.id === p.id)!;
  expect(saved.cover?.source).toBe('ai');
  await expect(coverSlot).toContainText('Made with AI');
  await coverSlot.getByRole('button', { name: 'Create again' }).click();
  const again = page.getByRole('dialog', { name: `Create again: cover of ${p.name}` });
  await expect(again.getByLabel(/^Context/)).toHaveValue('Kites over a windy beach at sunset.');
  await again.getByRole('button', { name: 'Cancel' }).click();
  await expect(again).toHaveCount(0);

  // the icon's job, cancelled while it runs: nothing changes
  const iconSlot = dialog.getByRole('region', { name: 'Icon' });
  await iconSlot.getByRole('button', { name: 'Create with AI' }).click();
  await page.getByRole('dialog', { name: `Create: icon of ${p.name}` }).getByRole('button', { name: 'Create', exact: true }).click();
  await iconSlot.getByRole('button', { name: 'Cancel' }).click();
  await expect(iconSlot).toContainText('Cancelled', { timeout: 15_000 });
  const after = ((await hub.call('GET', '/api/projects')) as { id: string; icon?: unknown }[]).find((x) => x.id === p.id)!;
  expect(after.icon).toBeUndefined();
});

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

test('a hub with no Codex offers no Create with AI', async ({ browser }) => {
  test.slow();
  const bin = process.env.NEXTBOARD_E2E_BIN!;
  const root = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'nocodex-'));
  const data = join(root, 'data');
  const home = join(root, 'home');
  mkdirSync(data);
  mkdirSync(home);
  // every folder that holds a codex leaves the PATH: the fake one's and any real one's
  const PATH = (process.env.PATH ?? '').split(':').filter((d) => d && !existsSync(join(d, 'codex'))).join(':');
  const env = { ...process.env, HOME: home, CODEX_HOME: join(home, '.codex'), PATH };
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const proc = spawn('timeout', ['300', bin, 'serve', '--listen', `127.0.0.1:${port}`, '--data-dir', data], { env, stdio: 'ignore' });
  const ctx = await browser.newContext({ baseURL: base, storageState: undefined });
  try {
    const api = await pwRequest.newContext({ baseURL: base, extraHTTPHeaders: headers });
    await expect.poll(() => api.get('/').then((r) => r.ok(), () => false), { timeout: 20_000 }).toBe(true);
    const link = execFileSync(bin, ['setup-link', '--data-dir', data], { env }).toString();
    const code = /code=([^\s&]+)/.exec(link)![1];
    expect((await api.post('/api/setup', { data: { code, password: 'rahasia123', hubName: 'plainbox' } })).ok()).toBe(true);
    const cap = await (await api.get('/api/art/capability')).json();
    expect(cap.available).toBe(false);
    const folder = mkdtempSync(join(root, 'project-'));
    const p = await (await api.post('/api/projects', { data: { machineId: 'local', path: folder, name: 'Wren' } })).json();
    await api.dispose();

    expect((await ctx.request.post('/api/auth/login', { data: { password: 'rahasia123' }, headers })).ok()).toBe(true);
    const page = await ctx.newPage();
    await page.goto('/');
    await page.getByRole('button', { name: 'Actions for Wren' }).click();
    await page.getByRole('menuitem', { name: 'Cover & icon…' }).click();
    const dialog = page.getByRole('dialog', { name: 'Cover & icon of Wren' });
    await expect(dialog.getByRole('button', { name: 'Upload' })).toHaveCount(2);
    await expect(dialog.getByRole('button', { name: /Create with AI|Create again/ })).toHaveCount(0);
    expect(p.id).toBeTruthy();
  } finally {
    await ctx.close();
    proc.kill('SIGTERM');
  }
});

test.describe('on servers over SSH', () => {
  let sshd: SSHD;
  test.beforeAll(async () => {
    test.skip(!SSHD.available(), 'no /usr/sbin/sshd');
    sshd = await SSHD.start();
  });
  test.afterAll(() => sshd?.stop());

  test('machines: one in use cannot be picked and says by what; the others archive, restore and go', async ({ page, hub, request }, info) => {
    const t = `${info.project.name}-${tag()}`;
    const api = await sshd.machine(hub, `api-server-${t}`);
    const worker = await sshd.machine(hub, `worker-1-${t}`);
    const cache = await sshd.machine(hub, `cache-${t}`);
    const dir = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'remote-'));
    const shop = await hub.call('POST', '/api/projects', { machineId: api, path: dir, name: `acme-shop ${t}`.slice(0, 24) });

    // the hub refuses it too, and says which projects use it
    const bulk = await hub.call('POST', '/api/machines/bulk', { ids: [api], action: 'archive' });
    expect(bulk.results[0]).toMatchObject({ id: api, ok: false });
    const del = await request.fetch(`/api/machines/${api}?permanent=1`, { method: 'DELETE', headers });
    expect(del.status()).toBe(409);
    expect((await del.json()).projects).toEqual([{ id: shop.id, name: shop.name }]);

    await page.goto('/machines');
    await page.getByRole('button', { name: 'Select', exact: true }).click();
    const apiCard = page.getByRole('article', { name: `api-server-${t}` });
    await expect(apiCard.getByRole('checkbox', { name: `Select api-server-${t}` })).toBeDisabled();
    await expect(apiCard).toContainText(`Used by ${shop.name}`);
    await page.getByRole('article', { name: `worker-1-${t}` }).getByRole('checkbox').click();
    await page.getByRole('article', { name: `cache-${t}` }).getByRole('checkbox').click();
    await expect(bar(page)).toContainText('2 selected');
    await bar(page).getByRole('button', { name: 'Archive' }).click();
    await expect(bar(page)).toBeHidden();
    await expect(page.getByRole('article', { name: `worker-1-${t}` })).toHaveCount(0);
    const archived = (await hub.call('GET', '/api/machines?archived=1')) as { id: string; archivedAt?: string }[];
    expect(archived.filter((m) => m.id === worker || m.id === cache).map((m) => !!m.archivedAt)).toEqual([true, true]);
    const active = (await hub.call('GET', '/api/machines')) as { id: string }[];
    expect(active.some((m) => m.id === worker || m.id === cache)).toBe(false);

    await page.getByRole('radio', { name: /^Archived/ }).click();
    const rows = page.getByRole('list', { name: 'Archived machines' });
    await rows.getByRole('article', { name: `worker-1-${t}` }).getByRole('button', { name: 'Restore' }).click();
    await expect(rows.getByRole('article', { name: `worker-1-${t}` })).toHaveCount(0);
    await rows.getByRole('button', { name: `Delete cache-${t} permanently` }).click();
    const dialog = page.getByRole('dialog', { name: `Delete cache-${t} permanently?` });
    await dialog.getByRole('button', { name: 'Delete permanently' }).click();
    await expect(dialog).toBeHidden();
    const all = [...((await hub.call('GET', '/api/machines')) as { id: string }[]), ...((await hub.call('GET', '/api/machines?archived=1')) as { id: string }[])];
    expect(all.some((m) => m.id === cache)).toBe(false);
    expect(all.some((m) => m.id === worker)).toBe(true);

    await hub.call('DELETE', `/api/projects/${shop.id}?permanent=1`);
    for (const id of [api, worker]) await hub.call('DELETE', `/api/machines/${id}?permanent=1`);
  });

  test('a project in several parts: one line of parts, its git added up, every part in the list', async ({ page, hub }, info) => {
    const t = `${info.project.name.slice(0, 2)}${tag()}`;
    const apiM = await sshd.machine(hub, `api-server-${t}`);
    const workerM = await sshd.machine(hub, `worker-1-${t}`);
    const root = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'acme-'));
    const git = (dir: string, ...a: string[]) =>
      execFileSync('git', ['-C', dir, ...a], { env: { ...process.env, GIT_AUTHOR_NAME: 'E2E', GIT_AUTHOR_EMAIL: 'e2e@example.com', GIT_COMMITTER_NAME: 'E2E', GIT_COMMITTER_EMAIL: 'e2e@example.com' } });
    const dirs = ['storefront', 'api', 'worker'].map((n) => {
      const d = join(root, n);
      mkdirSync(d);
      writeFileSync(join(d, 'README.md'), `# ${n}\n`);
      git(d, 'init', '-q', '-b', 'main');
      git(d, 'add', '-A');
      git(d, 'commit', '-qm', 'first');
      return d;
    });
    writeFileSync(join(dirs[0], 'README.md'), '# storefront\n\nchanged\n');
    const p = await hub.call('POST', '/api/projects', { machineId: 'local', path: dirs[0], name: `acme-shop ${t}`, partName: 'storefront' });
    await hub.call('POST', `/api/projects/${p.id}/parts`, { name: 'api', machineId: apiM, path: dirs[1] });
    await hub.call('POST', `/api/projects/${p.id}/parts`, { name: 'worker', machineId: workerM, path: dirs[2] });
    const single = await hub.project('Lone');

    await page.goto('/');
    await page.getByRole('radio', { name: 'Host', exact: true }).click();
    // by host, a project of several parts is under every machine one of its parts is on
    await expect(card(page, p.id)).toHaveCount(3);
    const c = card(page, p.id).first();
    await expect(c.locator('.partsline-sum')).toHaveText('3 parts · 3 machines');
    // no one folder stands for the project, and the git row adds the parts up
    await expect(c.locator('.project-path')).toHaveCount(0);
    await expect(c.locator('.project-git')).toContainText('3 repos', { timeout: 20_000 });
    // a card says Clean or changed in its foot
    await expect(c.locator('.project-foot .project-state')).toHaveText('1 with changes');
    await expect(c.locator('.project-git .project-branch-name')).toHaveText('main');
    // one row: the chips that fit and +N, never a second line
    const line = c.locator('.project-partsline');
    const fit = await line.evaluate((el) => {
      const shown = el.querySelectorAll('.partsline-chip:not(.is-out)').length;
      const more = el.querySelector('.partsline-more:not(.is-out)')?.textContent ?? '+0';
      return { shown, more: Number(more.slice(1)), tall: el.getBoundingClientRect().height };
    });
    expect(fit.shown + fit.more).toBe(3);
    expect(fit.tall).toBeLessThanOrEqual(19);
    // as tall as a one-part card
    const sameHeight = async () => {
      const lone = (await card(page, single.id).locator('.project-head').boundingBox())!.height;
      expect(Math.abs((await c.locator('.project-head').boundingBox())!.height - lone)).toBeLessThanOrEqual(1);
    };
    await sameHeight();

    // the full list, from the row
    await line.click();
    const pop = page.getByRole('dialog', { name: `Parts of ${p.name}` }).or(page.locator('.parts-pop'));
    await expect(pop).toBeVisible();
    for (const [i, n] of ['storefront', 'api', 'worker'].entries()) {
      await expect(pop.locator('.parts-pop-row').nth(i)).toContainText(n);
    }
    await expect(pop.locator('.parts-pop-row').nth(1)).toContainText(`api-server-${t}`);
    await expect(pop.locator('.parts-pop-row').nth(0)).toContainText('1 changed');
    await page.keyboard.press('Escape');
    await expect(pop).toBeHidden();

    // arranged by group, the card says where it is as one-part cards do: how many machines, not
    // the first part's one; the parts line then does not count them again
    await page.getByRole('radio', { name: 'Group', exact: true }).click();
    await expect(c.locator('.project-place')).toHaveText('3 machines');
    await expect(c.locator('.project-place')).toHaveAttribute('title', new RegExp(`^On .+, api-server-${t}, worker-1-${t}$`));
    await expect(c.locator('.partsline-sum')).toHaveText('3 parts');
    await sameHeight();

    await hub.call('DELETE', `/api/projects/${p.id}?permanent=1`);
    for (const id of [apiM, workerM]) await hub.call('DELETE', `/api/machines/${id}?permanent=1`);
  });
});
