import { spawn, execFileSync, type ChildProcess } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync, openSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { request } from '@playwright/test';
import { PORT, STATE_FILE } from './playwright.config';

const FAKE_AGENT = fileURLToPath(new URL('./fake-agent.sh', import.meta.url));

/** Starts the hub, sets it up, and gives back how to stop it. */
export default async function globalSetup() {
  const bin = process.env.NEXTBOARD_E2E_BIN;
  if (!bin) throw new Error('NEXTBOARD_E2E_BIN: the hub binary to test. `make test-e2e` sets it.');
  const root = mkdtempSync(join(tmpdir(), 'nextboard-e2e-'));
  const data = join(root, 'data');
  const home = join(root, 'home');
  mkdirSync(data);
  mkdirSync(home);
  const env = { ...process.env, HOME: home };
  const log = openSync(join(root, 'hub.log'), 'a');
  const base = `http://127.0.0.1:${PORT}`;

  // `timeout` so that a run killed halfway leaves no hub behind on a shared machine.
  const hub: ChildProcess = spawn('timeout', ['600', bin, 'serve', '--listen', `127.0.0.1:${PORT}`, '--data-dir', data], { env, stdio: ['ignore', log, log] });
  // Only once it is gone: a hub still stopping writes the folder back.
  const stop = async () => {
    const gone = new Promise((done) => hub.once('exit', done));
    hub.kill('SIGTERM');
    await Promise.race([gone, new Promise((done) => setTimeout(done, 8000))]);
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
    writeFileSync(STATE_FILE, JSON.stringify(await ctx.storageState()));
    await ctx.dispose();
  } catch (err) {
    hub.kill('SIGTERM');
    throw err;
  }
  process.env.NEXTBOARD_E2E_ROOT = root;
  return stop;
}
