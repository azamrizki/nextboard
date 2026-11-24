import { spawn, execFileSync, type ChildProcess } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { chmodSync, copyFileSync, existsSync, mkdirSync, openSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { createServer as netServer } from 'node:net';
import { userInfo } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, request, type APIRequestContext } from '@playwright/test';
import { PORT, ROOT, ROUTER_PORT, SITE_PORT, STATE_FILE } from './playwright.config';
import { PAINT } from './demo';
import { PARTS, PROJECTS, type Art, type DemoPart, type DemoProject } from './projects';

// A hub of made-up data for the screenshots and the video (README "Screenshots"), apart from any
// real one: its own port, and a home folder of its own in which every name, file and commit is
// invented. It is started with a clean environment, so nothing of the machine it runs on shows:
// no agent CLI, no account, no Tailscale name, no SSH config. The agents are web/demo/agent.sh.
//
// The "servers" of acme-shop's other parts are one throwaway OpenSSH server on a loopback port,
// with keys made here, whose sessions get a demo home and a bare PATH; without /usr/sbin/sshd the
// project keeps one part. The API provider is a stand-in on loopback that only lists models.
//
// NextCode's engine is web/demo/nextcode.sh, found where Claude Code's CLI would be. The database
// browser gets a PostgreSQL and a Redis of the shop's own: two throwaway containers named
// nb-dbtest-demo-<pid>-*, started here from images already pulled, and removed when the demo ends;
// without Docker or the images the shop has no database. No other container is touched.

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

/**
 * A folder with its history: commits by made-up authors, days apart, and a bare "origin" in the
 * demo home that has all but the last `unpushed` of them.
 */
function makeRepo(home: string, dir: string, p: Pick<DemoProject, 'files' | 'commits' | 'dirty' | 'unpushed'>) {
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
  const origin = join(home, 'remotes', `${basename(dir)}.git`);
  mkdirSync(origin, { recursive: true });
  execFileSync('git', ['init', '-q', '--bare', '-b', 'main', origin], { env: { ...process.env, HOME: home, GIT_CONFIG_NOSYSTEM: '1' }, stdio: 'ignore' });
  git({}, 'remote', 'add', 'origin', origin);
  git({}, 'push', '-q', 'origin', `HEAD~${Math.min(p.unpushed ?? 0, p.commits.length - 1)}:refs/heads/main`);
  git({}, 'fetch', '-q', 'origin');
  git({}, 'branch', '-q', '--set-upstream-to=origin/main', 'main');
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

const PG_IMAGE = 'postgres:18.6-alpine';
const REDIS_IMAGE = 'redis:8.4.8-alpine';

const docker = (...args: string[]) => execFileSync('docker', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();

/** The shop's database: customers, products, coupons, 320 orders and their lines; made-up names. */
const SHOP_SQL = `
  CREATE TABLE customers (id serial PRIMARY KEY, name text NOT NULL, email text NOT NULL UNIQUE, city text, joined_at date NOT NULL);
  CREATE TABLE products (sku text PRIMARY KEY, name text NOT NULL, price_cents int NOT NULL, stock int NOT NULL);
  CREATE TABLE coupons (code text PRIMARY KEY, kind text NOT NULL, value int NOT NULL, expires date NOT NULL);
  CREATE TABLE orders (id serial PRIMARY KEY, customer_id int NOT NULL REFERENCES customers(id), total_cents int NOT NULL, coupon text REFERENCES coupons(code), status text NOT NULL, placed_at timestamptz NOT NULL);
  CREATE TABLE order_lines (order_id int REFERENCES orders(id), sku text REFERENCES products(sku), qty int NOT NULL, PRIMARY KEY (order_id, sku));
  CREATE VIEW coupon_uses AS SELECT coupon, customer_id, count(*) AS uses FROM orders WHERE coupon IS NOT NULL GROUP BY 1, 2;
  INSERT INTO customers (name, email, city, joined_at)
    SELECT f || ' ' || l, lower(f) || '.' || lower(l) || '@example.com',
      (ARRAY['Lisbon','Austin','Osaka','Nairobi','Toronto','Melbourne','Berlin','Bogotá'])[1 + (g * 7) % 8], DATE '2026-11-20' - (g * 7) % 400
    FROM generate_series(1, 48) g,
      LATERAL (SELECT (ARRAY['Ava','Ben','Chloe','Dev','Elena','Felix','Grace','Hugo','Iris','Jonah','Kemi','Liam'])[1 + g % 12] AS f,
        (ARRAY['Okafor','Hart','Park','Chen','Silva','Novak','Reyes','Tanaka'])[1 + (g / 12 + g) % 8] AS l) n;
  INSERT INTO products VALUES ('pour-over','Pour-over set',3400,42), ('desk-planter','Desk planter',1800,65), ('day-pack','Day pack',5900,18),
    ('wool-socks','Wool socks',1200,140), ('cedar-candle','Cedar candle',2200,57), ('tea-kettle','Tea kettle',4100,23);
  INSERT INTO coupons VALUES ('SPRING10','percent',10,'2027-05-31'), ('WELCOME5','amount',500,'2027-12-31'), ('FREESHIP','amount',499,'2026-12-31');
  INSERT INTO orders (customer_id, total_cents, coupon, status, placed_at)
    SELECT 1 + (g * 17) % 48, 1200 + (g * 2371) % 14000, CASE WHEN g % 5 = 0 THEN 'SPRING10' WHEN g % 11 = 0 THEN 'WELCOME5' END,
      (ARRAY['paid','shipped','shipped','delivered','delivered','refunded'])[1 + g % 6], TIMESTAMPTZ '2026-11-24 09:30:00+00' - g * INTERVAL '4 hours 13 minutes'
    FROM generate_series(1, 320) g;
  INSERT INTO order_lines SELECT g, (ARRAY['pour-over','desk-planter','day-pack','wool-socks','cedar-candle','tea-kettle'])[1 + g % 6], 1 + g % 3 FROM generate_series(1, 320) g;
  INSERT INTO order_lines SELECT g, (ARRAY['pour-over','desk-planter','day-pack','wool-socks','cedar-candle','tea-kettle'])[1 + (g + 3) % 6], 1 FROM generate_series(1, 320) g WHERE g % 3 = 0;`;

interface Databases {
  names: string[];
  pg: { port: number; password: string };
  redis: { port: number };
}

/** The shop's PostgreSQL and Redis, or null without Docker or the images. */
async function databases(): Promise<Databases | null> {
  try {
    docker('image', 'inspect', PG_IMAGE);
    docker('image', 'inspect', REDIS_IMAGE);
  } catch {
    return null;
  }
  const prefix = `nb-dbtest-demo-${process.pid}`;
  const names = [`${prefix}-pg`, `${prefix}-redis`];
  const password = `demo-${randomBytes(9).toString('base64url')}`;
  docker('run', '-d', '--rm', '--name', names[0], '-e', `POSTGRES_PASSWORD=${password}`, '-e', 'POSTGRES_DB=acme_shop', '-p', '127.0.0.1::5432', PG_IMAGE);
  docker('run', '-d', '--rm', '--name', names[1], '-p', '127.0.0.1::6379', REDIS_IMAGE);
  const portOf = (c: string, p: string) => Number(docker('port', c, p).split('\n')[0].split(':').pop());
  const psql = (sql: string) => docker('exec', names[0], 'psql', '-h', '127.0.0.1', '-U', 'postgres', '-d', 'acme_shop', '-v', 'ON_ERROR_STOP=1', '-tAc', sql);
  const until = async (what: string, ok: () => boolean) => {
    for (const end = Date.now() + 60_000; Date.now() < end; await new Promise((r) => setTimeout(r, 500))) {
      try {
        if (ok()) return;
      } catch {
        /* not yet */
      }
    }
    throw new Error(`${what} did not start`);
  };
  try {
    // over TCP: the image's first, temporary server listens on its socket only
    await until('PostgreSQL', () => psql('SELECT 1') === '1');
    await until('Redis', () => docker('exec', names[1], 'redis-cli', 'ping') === 'PONG');
    psql(SHOP_SQL);
    const cli = (...a: string[]) => docker('exec', names[1], 'redis-cli', ...a);
    for (const [id, items] of [['c41', 'pour-over 1 wool-socks 2'], ['c07', 'day-pack 1'], ['c23', 'tea-kettle 1 cedar-candle 3']] as const) cli('HSET', `cart:${id}`, ...items.split(' '));
    cli('SET', 'session:9f2c', '{"customer":41,"cart":"cart:c41"}', 'EX', '86400');
    cli('SET', 'session:1b7e', '{"customer":7,"cart":"cart:c07"}', 'EX', '86400');
    cli('RPUSH', 'queue:receipts', 'order:318', 'order:319', 'order:320');
    for (const [sku, n] of [['pour-over', '42'], ['day-pack', '18'], ['tea-kettle', '23']]) cli('SET', `stock:${sku}`, n);
    cli('SET', 'coupon:SPRING10:uses', '64');
    // new containers bring up network interfaces: a page loading meanwhile fails with
    // ERR_NETWORK_CHANGED, so let them settle first (web/e2e/db-browser.spec.ts)
    await new Promise((r) => setTimeout(r, 3000));
    return { names, pg: { port: portOf(names[0], '5432/tcp'), password }, redis: { port: portOf(names[1], '6379/tcp') } };
  } catch (err) {
    docker('rm', '-f', ...names);
    throw err;
  }
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

/** Each project's cover and icon, painted in a browser page (demo.ts PAINT). */
async function paintAll(arts: Art[]): Promise<{ cover: Buffer; icon: Buffer }[]> {
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage();
    const out = [];
    for (const art of arts) {
      const r: { cover: string; icon: string } = await page.evaluate(`(${PAINT})(${JSON.stringify(art)})`);
      out.push({ cover: Buffer.from(r.cover, 'base64'), icon: Buffer.from(r.icon, 'base64') });
    }
    return out;
  } finally {
    await browser.close();
  }
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
  copyFileSync(join(HERE, 'nextcode.sh'), join(tools, 'demo-nextcode'));
  writeFileSync(join(tools, 'tailscale'), '#!/bin/sh\nexit 1\n');
  for (const f of ['demo-agent', 'demo-nextcode', 'tailscale']) chmodSync(join(tools, f), 0o755);
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
  const dbs = await databases();
  const halt = () => {
    shop.close();
    llm.close();
    ssh?.proc.kill('SIGTERM');
    // only the two containers made above
    if (dbs) {
      try {
        docker('rm', '-f', ...dbs.names);
      } catch {
        /* already gone */
      }
    }
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
      // the Platform board: one at the project's root, and one in each server part. An agent in a
      // part on a server is on a CLI's base, whose tools reach that server (only shells run there)
      ['architect', 'Architect', '#5b8def', ''],
      ['backend', 'Backend', '#2bb3a3', 'claude'],
      ['jobs', 'Jobs', '#d9822b', 'claude'],
    ]) {
      await call(ctx, 'POST', '/api/agents', { id, name, command: 'demo-agent', args: [id], color, env: [`DEMO_PATCH=${patch}`], ...(base ? { base } : {}) });
    }

    // consoles and the command bar start the tester, and no CLI the machine happens to have can
    // start at all (no usage pill either: it is about those CLIs)
    const off = Object.fromEntries(['claude', 'codex', 'gemini', 'opencode', 'aider', 'amp', 'auggie', 'cline', 'codebuff', 'cn', 'copilot', 'crush', 'cursor-agent', 'droid', 'goose', 'kimi', 'kiro', 'pi', 'qwen', 'vibe'].map((a) => [a, true]));
    // no pop-up per finished turn or question either: the board and its header say it
    // NextCode runs on Claude Code's CLI: here the demo's stand-in (nextcode.sh)
    await call(ctx, 'PATCH', '/api/settings', {
      agents: { defaults: { local: 'tester' }, disabled: off, paths: { local: { claude: join(tools, 'demo-nextcode') } } },
      notifications: { needsYou: false, finished: false },
    });
    // the hub's own project for its home folder: the demo has its own (permanently, not archived)
    for (const p of await call(ctx, 'GET', '/api/projects')) if (p.path === home) await call(ctx, 'DELETE', `/api/projects/${p.id}?permanent=1`);

    // each project's first part (every part has a "pt_…" id since v0.9.0-beta.2)
    const firstPart = new Map<string, string>();
    for (const p of PROJECTS) {
      const made = await call(ctx, 'POST', '/api/projects', { machineId: 'local', path: dirs.get(p.slug), name: p.name });
      p.id = made.id;
      firstPart.set(p.slug, made.parts[0].id);
      p.wid = made.workspaces[0].id;
      if (p.memory) await call(ctx, 'PUT', `/api/projects/${made.id}/memory`, { scope: 'project', content: p.memory });
      for (const m of p.pages ?? []) await call(ctx, 'PUT', `/api/memory/docs/${m.name}?projectId=${made.id}`, { title: m.title, description: m.description ?? '', content: m.content });
    }
    // covers and icons, as uploaded pictures; then the old ones go to the archive
    const dressed = PROJECTS.filter((p) => p.art);
    const art = await paintAll(dressed.map((p) => p.art!));
    for (const [i, p] of dressed.entries()) {
      const asset = async (body: Buffer) => {
        const r = await ctx.fetch('/api/assets', { method: 'POST', data: body, headers: { 'Content-Type': 'image/webp' } });
        if (!r.ok()) throw new Error(`POST /api/assets: ${r.status()} ${await r.text()}`);
        return (await r.json()).id as string;
      };
      await call(ctx, 'PATCH', `/api/projects/${p.id}`, { cover: { assetId: await asset(art[i].cover) }, icon: { assetId: await asset(art[i].icon) } });
    }
    for (const p of PROJECTS) if (p.archived) await call(ctx, 'DELETE', `/api/projects/${p.id}`);
    const shopId = PROJECTS[0].id!;

    // acme-shop in three parts: the storefront here, its API and worker each on a server
    if (ssh) {
      await call(ctx, 'PATCH', `/api/projects/${shopId}/parts/${firstPart.get(PROJECTS[0].slug)}`, { name: 'Storefront' });
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

    // the shop's databases, as the owner would add them: its PostgreSQL and its Redis
    if (dbs) {
      await call(ctx, 'POST', `/api/projects/${shopId}/dbs`, {
        connection: { name: 'acme_shop', driver: 'postgres', env: 'staging', host: '127.0.0.1', port: dbs.pg.port, database: 'acme_shop', user: 'postgres', savePassword: true, tls: 'off', reach: { kind: 'direct' } },
        secrets: { password: dbs.pg.password },
      });
      await call(ctx, 'POST', `/api/projects/${shopId}/dbs`, { connection: { name: 'cache', driver: 'redis', env: 'local', host: '127.0.0.1', port: dbs.redis.port, reach: { kind: 'direct' } }, secrets: {} });
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
