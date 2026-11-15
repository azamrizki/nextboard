import { existsSync, lstatSync, readFileSync, readlinkSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';
import { SSHD } from './sshd';
import { consolesOf, endConsoles, twoParts } from './two-parts';

// A console at the project root (v0.9.0-beta.2, docs/API-agents.md §21.2): it runs on This PC in
// a folder the hub makes, with a link to each part on This PC and, for a part on a server, a
// folder holding only a note, plus an AGENTS.md / CLAUDE.md listing every part. Nothing of the
// server is copied: its files are reached through the hub's tools, giving the part.

let sshd: SSHD;
test.beforeAll(async () => {
  test.skip(!SSHD.available(), 'no /usr/sbin/sshd');
  sshd = await SSHD.start();
});
test.afterAll(() => sshd?.stop());

test('a console at the root reaches both parts through its folder', async ({ page, hub }) => {
  test.slow();
  const p = await twoParts(hub, 'Shop', { sshd });
  try {
    const s = await hub.call('POST', '/api/sessions', { agent: 'fakelister', workspaceId: p.wid, root: true });
    expect(s.root).toBe(true);
    expect(s.partId ?? '').toBe('');
    expect(s.machineId).toBe('local');
    const dir = join(process.env.NEXTBOARD_E2E_ROOT!, 'data', 'roots', p.id);
    expect(s.cwd).toBe(dir);
    // the project says where its root is (two parts or more)
    expect((await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).rootDir).toBe(dir);

    // what the agent found there: a link to admin, a note for frontend, the instruction files
    const seen = join(dir, '.nb-e2e-root');
    await expect.poll(() => existsSync(seen), { timeout: 15_000 }).toBe(true);
    const lines = readFileSync(seen, 'utf8').trim().split('\n');
    expect(lines).toContain('link admin: server.go ');
    expect(lines).toContain('file AGENTS.md');
    expect(lines).toContain('file CLAUDE.md');
    const note = lines.find((l) => l.startsWith('note frontend: '));
    expect(note, lines.join('\n')).toBeTruthy();
    expect(note).toContain(`server "${p.server}"`);
    expect(note).toContain(p.front.path);
    expect(note).toContain('nothing of them is copied here');
    expect(note).toContain('part: "frontend"');
    // the server's files are not here
    expect(existsSync(join(dir, 'frontend', 'index.html'))).toBe(false);
    expect(readlinkSync(join(dir, 'admin'))).toBe(p.admin.path);

    // the instruction file lists every part, where it is and how to reach it
    const doc = readFileSync(join(dir, 'AGENTS.md'), 'utf8');
    expect(readFileSync(join(dir, 'CLAUDE.md'), 'utf8')).toBe(doc);
    expect(doc).toContain(`# ${p.name} — project root (made by NextBoard)`);
    expect(doc).toMatch(new RegExp(`\\| admin \\| This PC \\| ${p.admin.path} \\| \\./admin \\(link\\) \\|`));
    expect(doc).toMatch(new RegExp(`\\| frontend \\| server "${p.server}" \\| ${p.front.path} \\| \\./frontend \\(note\\) \\|`));
    expect(doc).toContain('Git: each part is its own repository. The root itself is not one.');

    // the server part through the hub's tools: read, and read only (the project's default for
    // parts on other machines); the part on This PC with the agent's own tools
    const tool = await hub.tools(s.id, dir);
    const read = await tool('read_file', { part: 'frontend', path: 'index.html' });
    expect(read.isError, read.text).toBe(false);
    expect(read.text).toContain('greeting from frontend');
    const found = await tool('grep', { part: 'frontend', pattern: 'greeting' });
    expect(found.text).toContain('index.html');
    const write = await tool('write_file', { part: 'frontend', path: 'index.html', content: 'gone\n' });
    expect(write.isError).toBe(true);
    expect(write.text).toContain('frontend is read only for you');
    expect(readFileSync(join(p.front.path, 'index.html'), 'utf8')).toContain('greeting from frontend');
    const own = await tool('write_file', { part: 'admin', path: 'server.go', content: 'gone\n' });
    expect(own.isError).toBe(true);
    expect(own.text).toContain('admin is on This PC: use your own tools in ./admin');

    // its tile says where it works
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const tile = page.locator('.react-flow__node').filter({ has: page.locator('.node-part') });
    await expect(tile.locator('.node-part')).toHaveText('Root');
    await expect(tile.locator('.node-part')).toHaveAttribute('title', `Works at the root of ${p.name}: every part, from This PC`);

    // a part renamed: the root follows, the old entry goes, what the agent wrote stays
    await hub.call('PATCH', `/api/projects/${p.id}/parts/${p.front.id}`, { name: 'web' });
    await expect.poll(() => existsSync(join(dir, 'web', 'NEXTBOARD-PART.md')), { timeout: 10_000 }).toBe(true);
    expect(existsSync(join(dir, 'frontend'))).toBe(false);
    expect(readFileSync(join(dir, 'AGENTS.md'), 'utf8')).toContain('| web | server ');
    expect(existsSync(seen)).toBe(true);
    // full access for parts on other machines: the root's note says it may change web too
    await hub.call('PATCH', `/api/projects/${p.id}`, { partAccess: 'full' });
    await expect.poll(() => readFileSync(join(dir, 'web', 'NEXTBOARD-PART.md'), 'utf8'), { timeout: 10_000 }).toContain('Change it with write_file, edit_file and bash');
    expect(lstatSync(join(dir, 'admin')).isSymbolicLink()).toBe(true);
  } finally {
    await endConsoles(hub, p.wid);
  }
});

test('a console in a part works in that part, and a part and the root at once is refused', async ({ hub, request }) => {
  test.slow();
  const p = await twoParts(hub, 'Shop', { sshd });
  try {
    const local = await hub.call('POST', '/api/sessions', { agent: 'fakecli', workspaceId: p.wid, partId: p.admin.id });
    expect(local.partId).toBe(p.admin.id);
    expect(local.root ?? false).toBe(false);
    expect(local.cwd).toBe(p.admin.path);
    // a console of the server part: the agent runs on This PC and works on the server
    const remote = await hub.call('POST', '/api/sessions', { agent: 'fakecli', workspaceId: p.wid, partId: p.front.id });
    expect(remote.partId).toBe(p.front.id);
    expect(remote.root ?? false).toBe(false);
    // a part and the root at once is no placement
    const both = await request.fetch('/api/sessions', { method: 'POST', headers: { 'X-NextBoard': '1' }, data: { agent: 'fakecli', workspaceId: p.wid, root: true, partId: p.admin.id } });
    expect(both.status()).toBe(400);
    expect((await both.json()).code).toBe('bad_placement');
    expect((await consolesOf(hub, p.wid)).length).toBe(2);
  } finally {
    await endConsoles(hub, p.wid);
  }
});
