import { spawn, execFileSync, type ChildProcess } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { chmodSync, copyFileSync, existsSync, mkdirSync, openSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { createServer as netServer } from 'node:net';
import { userInfo } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { request, type APIRequestContext } from '@playwright/test';
import { PORT, ROOT, ROUTER_PORT, SITE_PORT, STATE_FILE } from './playwright.config';
import { PARTS, PROJECTS, type DemoPart, type DemoProject } from './projects';

// A hub of made-up data for the screenshots and the video (README "Screenshots"), apart from any
// real one: its own port, and a home folder of its own in which every name, file and commit is
// invented. It is started with a clean environment, so nothing of the machine it runs on shows:
// no agent CLI, no account, no Tailscale name, no SSH config. The agents are web/demo/agent.sh.
//
// The "servers" of acme-shop's other parts are one throwaway OpenSSH server on a loopback port,
// with keys made here, whose sessions get a demo home and a bare PATH; without /usr/sbin/sshd the
// project keeps one part. The API provider is a stand-in on loopback that only lists models.

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

/** A folder with its history: commits by made-up authors, days apart. */
function makeRepo(home: string, dir: string, p: Pick<DemoProject, 'files' | 'commits' | 'dirty'>) {
  write(dir, p.files);
  if (!p.commits) return dir;
  const git = (env: Record<string, string>, ...a: string[]) => execFileSync('git', a, { cwd: dir, env: { ...process.env, HOME: home, GIT_CONFIG_NOSYSTEM: '1', ...env }, stdio: 'ignore' });
  git({}, 'init', '-q', '-b', 'main');
  const day = 86_400_000;
  const start = Date.now() - p.commits.length * day;
  for (const [i, c] of p.commits.entries()) {
    if (c.files) write(dir, c.files);
    const at = new Date(start + i * day).toISOString();
    const email = `${c.author.toLowerCase().split(' ')[0]}@example.com`;
    const env = { GIT_AUTHOR_NAME: c.author, GIT_AUTHOR_EMAIL: email, GIT_AUTHOR_DATE: at, GIT_COMMITTER_NAME: c.author, GIT_COMMITTER_EMAIL: email, GIT_COMMITTER_DATE: at };
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

/** A stand-in for an OpenAI-compatible router: it lists models to the key made for it. */
function router(key: string): Promise<Server> {
  const server = createServer((req, res) => {
    if (req.headers.authorization !== `Bearer ${key}`) {
      res.writeHead(401, { 'content-type': 'application/json' });
      res.end('{"error":{"message":"invalid key"}}');
      return;
    }
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(req.url?.endsWith('/models') ? '{"object":"list","data":[{"id":"coder-large"},{"id":"coder-small"},{"id":"reasoner-pro"}]}' : '{"id":"demo"}');
  });
  return new Promise((done) => server.listen(ROUTER_PORT, '127.0.0.1', () => done(server)));
}

function freePort(): Promise<number> {
  return new Promise((done, fail) => {
    const srv = netServer();
    srv.once('error', fail);
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as { port: number }).port;
      srv.close(() => done(port));
    });
  });
}

/** The parts' servers: one OpenSSH server on loopback, with a demo home and a bare PATH. */
async function sshd(home: string): Promise<{ proc: ChildProcess; port: number; key: string } | null> {
  if (!existsSync('/usr/sbin/sshd')) return null;
  const dir = join(home, '.local', 'share', 'sshd');
  mkdirSync(dir, { recursive: true });
  // a comment of its own: ssh-keygen would write user@host of this machine
  for (const k of ['hostkey', 'user']) execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-C', 'demo@devbox', '-f', join(dir, k)]);
  writeFileSync(join(dir, 'authorized_keys'), readFileSync(join(dir, 'user.pub')), { mode: 0o600 });
  const port = await freePort();
  const config = [
    `Port ${port}`,
    'ListenAddress 127.0.0.1',
    `HostKey ${dir}/hostkey`,
    `PidFile ${dir}/sshd.pid`,
    `AuthorizedKeysFile ${dir}/authorized_keys`,
    'UsePAM no',
    'PasswordAuthentication no',
    'KbdInteractiveAuthentication no',
    'PubkeyAuthentication yes',
    'StrictModes no',
    'PrintMotd no',
    'Subsystem sftp internal-sftp',
    // the sessions see the demo's server folder as home, and no CLI of this machine
    `SetEnv HOME=${home}/srv PATH=/usr/bin:/bin LANG=C.UTF-8`,
  ];
  writeFileSync(join(dir, 'sshd_config'), config.join('\n') + '\n', { mode: 0o600 });
  const proc = spawn('timeout', ['1500', '/usr/sbin/sshd', '-D', '-e', '-f', join(dir, 'sshd_config')], { stdio: 'ignore' });
  for (let i = 0; i < 50; i++) {
    const up = await new Promise<boolean>((done) => {
      const s = netServer();
      s.once('error', () => done(true));
      s.listen(port, '127.0.0.1', () => s.close(() => done(false)));
    });
    if (up) return { proc, port, key: readFileSync(join(dir, 'user'), 'utf8') };
    await new Promise((r) => setTimeout(r, 100));
  }
  proc.kill('SIGTERM');
  return null;
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
  for (const p of PROJECTS) dirs.set(p.slug, makeRepo(home, join(home, 'code', p.slug), p));
  for (const pt of PARTS) makeRepo(home, join(home, 'srv', pt.slug), pt);
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
  const hub: ChildProcess = spawn('timeout', ['1500', bin, 'serve', '--listen', `127.0.0.1:${PORT}`, '--data-dir', data], { env, stdio: ['ignore', log, log] });
  const routerKey = `sk-demo-${randomBytes(18).toString('base64url')}`;
  const shop = await site();
  const llm = await router(routerKey);
  const ssh = await sshd(home);
  const halt = () => {
    shop.close();
    llm.close();
    ssh?.proc.kill('SIGTERM');
  };
  const stop = async () => {
    halt();
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
    // the tester and the writer on Claude Code's base: a conversation id, hooks, a transcript
    for (const [id, name, color, base] of [
      ['builder', 'Builder', '#7c6cf0', ''],
      ['reviewer', 'Reviewer', '#e0913a', ''],
      ['tester', 'Tester', '#3aa676', 'claude'],
      ['writer', 'Writer', '#d0679d', 'claude'],
      ['qa', 'QA', '#3a9bd6', ''],
      ['mobile', 'Mobile QA', '#c4a23a', ''],
      ['changelog', 'Changelog', '#9a7cd6', ''],
    ]) {
      await call(ctx, 'POST', '/api/agents', { id, name, command: 'demo-agent', args: [id], color, env: [`DEMO_PATCH=${patch}`], ...(base ? { base } : {}) });
    }

    // consoles and the command bar start the tester, and no CLI the machine happens to have can
    // start at all (no usage pill either: it is about those CLIs)
    const off = Object.fromEntries(['claude', 'codex', 'gemini', 'opencode', 'aider', 'amp', 'auggie', 'cline', 'codebuff', 'cn', 'copilot', 'crush', 'cursor-agent', 'droid', 'goose', 'kimi', 'kiro', 'pi', 'qwen', 'vibe'].map((a) => [a, true]));
    // no pop-up per finished turn or question either: the board and its header say it
    await call(ctx, 'PATCH', '/api/settings', { agents: { defaults: { local: 'tester' }, disabled: off }, notifications: { needsYou: false, finished: false } });
    // the hub's own project for its home folder: the demo has its three
    for (const p of await call(ctx, 'GET', '/api/projects')) if (p.path === home) await call(ctx, 'DELETE', `/api/projects/${p.id}`);

    for (const p of PROJECTS) {
      const made = await call(ctx, 'POST', '/api/projects', { machineId: 'local', path: dirs.get(p.slug), name: p.name });
      p.id = made.id;
      p.wid = made.workspaces[0].id;
      if (p.memory) await call(ctx, 'PUT', `/api/projects/${made.id}/memory`, { scope: 'project', content: p.memory });
      for (const m of p.pages ?? []) await call(ctx, 'PUT', `/api/memory/docs/${m.name}?projectId=${made.id}`, { title: m.title, description: m.description ?? '', content: m.content });
    }
    const shopId = PROJECTS[0].id!;

    // acme-shop in three parts: the storefront here, its API and worker each on a server
    if (ssh) {
      await call(ctx, 'PATCH', `/api/projects/${shopId}/parts/main`, { name: 'Storefront' });
      const user = userInfo().username;
      const machines = new Map<string, string>();
      for (const pt of PARTS as DemoPart[]) {
        let id = machines.get(pt.machine);
        if (!id) {
          let m = await call(ctx, 'POST', '/api/machines', { name: pt.machine, host: '127.0.0.1', port: ssh.port, user, authMethod: 'key', privateKey: ssh.key, persistMode: 'direct' });
          if (m.status === 'hostkey') m = await call(ctx, 'POST', `/api/machines/${m.id}/hostkey`, { accept: true, fingerprint: m.pendingHostKey.fingerprint });
          if (m.status !== 'connected') throw new Error(`machine ${pt.machine}: ${m.status} ${m.error ?? ''}`);
          id = m.id as string;
          machines.set(pt.machine, id);
        }
        const p = PROJECTS.find((x) => x.slug === pt.project)!;
        await call(ctx, 'POST', `/api/projects/${p.id}/parts`, { name: pt.name, machineId: id, path: join(home, 'srv', pt.slug) });
      }
    }

    // an API provider, to run consoles on a key of one's own: a stand-in on loopback
    await call(ctx, 'POST', '/api/providers', {
      name: 'Team router',
      kind: 'openai',
      api: 'responses',
      baseUrl: `http://localhost:${ROUTER_PORT}/v1`,
      model: 'coder-large',
      fastModel: 'coder-small',
      apiKey: routerKey,
      prices: { 'coder-large': { input: 1.25, output: 10 }, 'coder-small': { input: 0.25, output: 2 } },
      budget: 40,
    });

    // monitors of the storefront on its dev server, all on the public status page
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
    halt();
    hub.kill('SIGTERM');
    throw err;
  }
  return stop;
}
