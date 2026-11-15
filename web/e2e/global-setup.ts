import { spawn, execFileSync, type ChildProcess } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync, openSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { request } from '@playwright/test';
import { PORT, STATE_FILE } from './playwright.config';

const FAKE_AGENT = fileURLToPath(new URL('./fake-agent.sh', import.meta.url));
const FAKE_WORKER = fileURLToPath(new URL('./fake-worker.sh', import.meta.url));
const FAKE_CLAUDE = fileURLToPath(new URL('./fake-claude.sh', import.meta.url));
const FAKE_CODEX = fileURLToPath(new URL('./fake-codex.sh', import.meta.url));

/**
 * A folder for the front of the hub's PATH holding the fake Codex: the hub finds `codex` there
 * before any real one, so "Create with AI" and anything else that runs Codex never reaches an
 * account (API-board §1).
 */
function fakeTools(root: string): string {
  const dir = join(root, 'tools');
  mkdirSync(dir, { recursive: true });
  symlinkSync(FAKE_CODEX, join(dir, 'codex'));
  return dir;
}

/** Starts the hub, sets it up, and gives back how to stop it. */
export default async function globalSetup() {
  const bin = process.env.NEXTBOARD_E2E_BIN;
  if (!bin) throw new Error('NEXTBOARD_E2E_BIN: the hub binary to test. `make test-e2e` sets it.');
  const root = mkdtempSync(join(tmpdir(), 'nextboard-e2e-'));
  const data = join(root, 'data');
  const home = join(root, 'home');
  mkdirSync(data);
  mkdirSync(home);
  // Codex's own list of models, as a signed-in Codex keeps one: without it the hub would ask the
  // agent's command for it, and the fake agents answer nothing for 20 s
  mkdirSync(join(home, '.codex'));
  writeFileSync(
    join(home, '.codex', 'models_cache.json'),
    JSON.stringify({
      models: [
        { slug: 'e2e-large', display_name: 'E2E Large', description: 'Made up for the tests', visibility: 'list', priority: 1, default_reasoning_level: 'medium', supported_reasoning_levels: [{ effort: 'low' }, { effort: 'medium' }, { effort: 'high' }] },
        { slug: 'e2e-small', display_name: 'E2E Small', description: 'Made up for the tests', visibility: 'list', priority: 2, default_reasoning_level: 'low', supported_reasoning_levels: [{ effort: 'low' }, { effort: 'medium' }] },
      ],
    }),
  );
  // CODEX_HOME too: one set where the tests run would point the hub, and the fake Codex, at the
  // real ~/.codex (its images folder, its sessions)
  const env = { ...process.env, HOME: home, CODEX_HOME: join(home, '.codex'), PATH: `${fakeTools(root)}:${process.env.PATH ?? ''}` };
  const log = openSync(join(root, 'hub.log'), 'a');
  const base = `http://127.0.0.1:${PORT}`;

  // `timeout` so that a run killed halfway leaves no hub behind on a shared machine.
  const hub: ChildProcess = spawn('timeout', ['1800', bin, 'serve', '--listen', `127.0.0.1:${PORT}`, '--data-dir', data], { env, stdio: ['ignore', log, log] });
  // Only once it is gone: a hub still stopping writes the folder back.
  const stop = async () => {
    const gone = new Promise((done) => hub.once('exit', done));
    hub.kill('SIGTERM');
    await Promise.race([gone, new Promise((done) => setTimeout(done, 8000))]);
    // the hub's log outlives the run: a hub that died halfway leaves only refused connections
    try {
      copyFileSync(join(root, 'hub.log'), join(tmpdir(), `nextboard-e2e-hub-${PORT}.log`));
    } catch {
      /* no log */
    }
    rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
    rmSync(STATE_FILE, { force: true });
  };

  try {
    const ctx = await request.newContext({ baseURL: base, extraHTTPHeaders: { 'X-NextBoard': '1' } });
    const deadline = Date.now() + 20_000;
    for (;;) {
      const up = await ctx.get('/').then((r) => r.ok(), () => false);
      if (up) break;
      if (Date.now() > deadline) throw new Error(`the hub did not start; its log is in ${root}/hub.log`);
      await new Promise((r) => setTimeout(r, 200));
    }
    const link = execFileSync(bin, ['setup-link', '--data-dir', data], { env }).toString();
    const code = /code=([^\s&]+)/.exec(link)?.[1];
    if (!code) throw new Error(`no setup code in: ${link}`);
    const setup = await ctx.post('/api/setup', { data: { code, password: 'rahasia123', hubName: 'devbox' } });
    if (!setup.ok()) throw new Error(`setup: ${setup.status()}`);
    const agent = await ctx.post('/api/agents', { data: { id: 'fakecli', name: 'Fake CLI', command: FAKE_AGENT, base: 'codex' } });
    if (!agent.ok()) throw new Error(`agent: ${agent.status()}`);
    const worker = await ctx.post('/api/agents', { data: { id: 'fakeworker', name: 'Fake Worker', command: FAKE_WORKER } });
    if (!worker.ok()) throw new Error(`worker: ${worker.status()}`);
    // like Claude Code: the hub gives it its conversation id, so it can sleep and resume (ROADMAP #50)
    const claude = await ctx.post('/api/agents', { data: { id: 'fakeclaude', name: 'Fake Claude', command: FAKE_CLAUDE, base: 'claude' } });
    if (!claude.ok()) throw new Error(`fake claude: ${claude.status()}`);
    writeFileSync(STATE_FILE, JSON.stringify(await ctx.storageState()));
    await ctx.dispose();
  } catch (err) {
    hub.kill('SIGTERM');
    throw err;
  }
  process.env.NEXTBOARD_E2E_ROOT = root;
  return stop;
}
