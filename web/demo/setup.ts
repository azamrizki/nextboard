import { spawn, execFileSync, type ChildProcess } from 'node:child_process';
import { chmodSync, copyFileSync, existsSync, mkdirSync, openSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { request, type APIRequestContext } from '@playwright/test';
import { PORT, ROOT, SITE_PORT, STATE_FILE } from './playwright.config';
import { PROJECTS, type DemoProject } from './projects';

// A hub of made-up data for the screenshots and the video (README "Screenshots"), apart from any
// real one: its own port, and a home folder of its own in which every name, file and commit is
// invented. It is started with a clean environment, so nothing of the machine it runs on shows:
// no agent CLI, no account, no Tailscale name, no SSH config. The agents are web/demo/agent.sh.

const HERE = dirname(fileURLToPath(import.meta.url));
const MARK = '.nextboard-demo';

function fresh(root: string) {
  if (existsSync(root)) {
    // only ever a folder this setup made
    if (!existsSync(join(root, MARK))) throw new Error(`${root} exists and is not a demo folder: set NEXTBOARD_DEMO_ROOT`);
    rmSync(root, { recursive: true, force: true });
  }
  mkdirSync(root, { recursive: true });
  writeFileSync(join(root, MARK), '');
}

function write(base: string, files: Record<string, string>) {
  for (const [rel, body] of Object.entries(files)) {
    mkdirSync(dirname(join(base, rel)), { recursive: true });
    writeFileSync(join(base, rel), body);
  }
}

/** The project's folder with its history: commits by a made-up author, days apart. */
function makeProject(home: string, p: DemoProject) {
  const dir = join(home, 'code', p.slug);
  write(dir, p.files);
  if (!p.commits) return dir;
  const git = (env: Record<string, string>, ...a: string[]) => execFileSync('git', a, { cwd: dir, env: { ...process.env, HOME: home, ...env }, stdio: 'ignore' });
  git({}, 'init', '-q', '-b', 'main');
  const day = 86_400_000;
  const start = Date.now() - p.commits.length * day;
  for (const [i, c] of p.commits.entries()) {
    if (c.files) write(dir, c.files);
    const at = new Date(start + i * day).toISOString();
    const env = { GIT_AUTHOR_NAME: c.author, GIT_AUTHOR_EMAIL: `${c.author.toLowerCase().split(' ')[0]}@example.com`, GIT_AUTHOR_DATE: at, GIT_COMMITTER_NAME: c.author, GIT_COMMITTER_EMAIL: `${c.author.toLowerCase().split(' ')[0]}@example.com`, GIT_COMMITTER_DATE: at };
    git(env, 'add', '-A');
    git(env, 'commit', '-q', '--allow-empty', '-m', c.message);
  }
  if (p.dirty) write(dir, p.dirty);
  return dir;
}

/** The storefront the web tile shows, on a port of its own. */
function site(): Promise<Server> {
  const page = readFileSync(join(HERE, 'site', 'index.html'));
  const server = createServer((req, res) => {
    if (req.url?.startsWith('/api/health')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end('{"ok":true}');
      return;
    }
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end(page);
  });
  return new Promise((done) => server.listen(SITE_PORT, '127.0.0.1', () => done(server)));
}

async function call(ctx: APIRequestContext, method: string, url: string, data?: unknown) {
  const r = await ctx.fetch(url, { method, data });
  if (!r.ok()) throw new Error(`${method} ${url}: ${r.status()} ${await r.text()}`);
  return r.json();
}

export default async function globalSetup() {
  const bin = process.env.NEXTBOARD_E2E_BIN;
  if (!bin) throw new Error('NEXTBOARD_E2E_BIN: the hub binary. `make demo` sets it.');
  fresh(ROOT);
  const home = ROOT;
  const data = join(home, '.nextboard');
  const tools = join(home, '.local', 'bin');
  mkdirSync(tools, { recursive: true });
  // the agents, and a tailscale that knows nothing, ahead of the real one in /usr/bin
  copyFileSync(join(HERE, 'agent.sh'), join(tools, 'demo-agent'));
  writeFileSync(join(tools, 'tailscale'), '#!/bin/sh\nexit 1\n');
  for (const f of ['demo-agent', 'tailscale']) chmodSync(join(tools, f), 0o755);
  const prompt = "PS1='demo@devbox:\\w\\$ '\n";
  writeFileSync(join(home, '.bashrc'), prompt);
  writeFileSync(join(home, '.bash_profile'), '. ~/.bashrc\n');
  writeFileSync(join(home, '.gitconfig'), '[user]\n\tname = Demo Dev\n\temail = dev@example.com\n[init]\n\tdefaultBranch = main\n');

  const dirs = new Map<string, string>();
  for (const p of PROJECTS) dirs.set(p.slug, makeProject(home, p));
  // what the tester's canned work leaves in the folder (agent.sh)
  const patch = join(home, '.local', 'share', 'demo-patch');
  write(patch, PROJECTS[0].patch ?? {});

  const env: Record<string, string> = {
    HOME: home,
    USER: 'demo',
    LOGNAME: 'demo',
    PATH: `${tools}:/usr/local/bin:/usr/bin:/bin`,
    SHELL: '/bin/bash',
    LANG: 'C.UTF-8',
    TERM: 'xterm-256color',
    TMPDIR: process.env.TMPDIR || '/tmp',
  };
  const log = openSync(join(home, 'hub.log'), 'a');
  const hub: ChildProcess = spawn('timeout', ['1200', bin, 'serve', '--listen', `127.0.0.1:${PORT}`, '--data-dir', data], { env, stdio: ['ignore', log, log] });
  const shop = await site();
  const stop = async () => {
    shop.close();
    const gone = new Promise((done) => hub.once('exit', done));
    hub.kill('SIGTERM');
    await Promise.race([gone, new Promise((done) => setTimeout(done, 8000))]);
    if (!process.env.NEXTBOARD_DEMO_KEEP) rmSync(ROOT, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
    rmSync(STATE_FILE, { force: true });
  };

  try {
    const ctx = await request.newContext({ baseURL: `http://127.0.0.1:${PORT}`, extraHTTPHeaders: { 'X-NextBoard': '1' } });
    const deadline = Date.now() + 20_000;
    while (!(await ctx.get('/').then((r) => r.ok(), () => false))) {
      if (Date.now() > deadline) throw new Error(`the hub did not start; its log is ${home}/hub.log`);
      await new Promise((r) => setTimeout(r, 200));
    }
    const link = execFileSync(bin, ['setup-link', '--data-dir', data], { env }).toString();
    const code = /code=([^\s&]+)/.exec(link)?.[1];
    if (!code) throw new Error(`no setup code in: ${link}`);
    await call(ctx, 'POST', '/api/setup', { code, password: 'demo-password', hubName: 'devbox' });
    for (const [id, name, color] of [
      ['builder', 'Builder', '#7c6cf0'],
      ['reviewer', 'Reviewer', '#e0913a'],
      ['tester', 'Tester', '#3aa676'],
    ]) {
      await call(ctx, 'POST', '/api/agents', { id, name, command: 'demo-agent', args: [id], color, env: [`DEMO_PATCH=${patch}`] });
    }

    // consoles and the command bar start the tester, and no CLI the machine happens to have can
    // start at all (no usage pill either: it is about those CLIs)
    const off = Object.fromEntries(['claude', 'codex', 'gemini', 'opencode', 'aider', 'amp', 'auggie', 'cline', 'codebuff', 'cn', 'copilot', 'crush', 'cursor-agent', 'droid', 'goose', 'kimi', 'kiro', 'pi', 'qwen', 'vibe'].map((a) => [a, true]));
    await call(ctx, 'PATCH', '/api/settings', { agents: { defaults: { local: 'tester' }, disabled: off } });
    // the hub's own project for its home folder: the demo has its three
    for (const p of await call(ctx, 'GET', '/api/projects')) if (p.path === home) await call(ctx, 'DELETE', `/api/projects/${p.id}`);

    for (const p of PROJECTS) {
      const made = await call(ctx, 'POST', '/api/projects', { machineId: 'local', path: dirs.get(p.slug), name: p.name });
      p.id = made.id;
      p.wid = made.workspaces[0].id;
      if (p.memory) await call(ctx, 'PUT', `/api/projects/${made.id}/memory`, { scope: 'project', content: p.memory });
      for (const m of p.pages ?? []) await call(ctx, 'PUT', `/api/memory/docs/${m.name}?projectId=${made.id}`, { title: m.title, description: m.description ?? '', content: m.content });
    }
    // monitors of the storefront on its dev server, all on the public status page
    const shopId = PROJECTS[0].id!;
    const dev = `http://127.0.0.1:${SITE_PORT}`;
    for (const [name, publicName, url, keyword] of [
      ['Storefront', 'Storefront', `${dev}/`, 'acme'],
      ['API health', 'API', `${dev}/api/health`, 'ok'],
      ['Checkout', 'Checkout', `${dev}/checkout`, 'Order summary'],
    ]) {
      await call(ctx, 'POST', `/api/projects/${shopId}/uptime`, { type: 'http', name, url, keyword, intervalSec: 20, notify: false, public: true, publicName, group: 'Production' });
    }
    await call(ctx, 'PUT', `/api/projects/${shopId}/uptime/public`, { enabled: true, title: 'Acme Shop status' });

    writeFileSync(join(home, '.local', 'share', 'demo-projects.json'), JSON.stringify(PROJECTS.map((p) => ({ slug: p.slug, id: p.id, wid: p.wid, path: dirs.get(p.slug) }))));
    writeFileSync(STATE_FILE, JSON.stringify(await ctx.storageState()));
    await ctx.dispose();
  } catch (err) {
    shop.close();
    hub.kill('SIGTERM');
    throw err;
  }
  return stop;
}
