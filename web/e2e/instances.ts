import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { existsSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import { createServer } from 'node:http';
import { join } from 'node:path';
import { request } from '@playwright/test';
import { hubEnv, stopFakeUnits } from './global-setup';

// Several instances on one machine (README, docs/API.md Instances). The shared e2e hub is the
// `default` instance: its data folder is <root>/data, not under the scratch home, so it keeps the
// legacy names (cookie `nb_session_http`). A second hub, the instance `personal`, runs in the
// same scratch home (~/.nextboard-personal), on its own port, with the same fake tools first on
// PATH, so the default instance finds it as a sibling. An instance either of them creates from
// the web is started by the fake systemctl (fake-service.sh) and ended with this hub. At most
// three hubs at once: the shared one, personal, and the one a test creates.

export const PERSONAL = 'personal';
export const PERSONAL_PASSWORD = 'pribadi123';
export const PERSONAL_HUB_NAME = 'devbox · personal';
export const DEFAULT_PASSWORD = 'rahasia123';
export const headers = { 'X-NextBoard': '1' };

export interface InstanceHub {
  name: string;
  base: string;
  port: number;
  dataDir: string;
  password: string;
  hubName: string;
  stop: () => Promise<void>;
}

export function scratch() {
  const root = process.env.NEXTBOARD_E2E_ROOT!;
  const bin = process.env.NEXTBOARD_E2E_BIN!;
  return { root, bin, home: join(root, 'home'), env: hubEnv(root) };
}

export function freePort(): Promise<number> {
  return new Promise((done, fail) => {
    const s = createServer();
    s.once('error', fail);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address() as { port: number };
      s.close(() => done(port));
    });
  });
}

/** A one-time setup code of an instance, as `nextboard setup-link --instance <name>` prints it. */
export function setupCode(name: string): string {
  const { bin, env } = scratch();
  const link = execFileSync(bin, ['setup-link', '--instance', name], { env }).toString();
  const code = /code=([^\s&]+)/.exec(link)?.[1];
  if (!code) throw new Error(`no setup code in: ${link}`);
  return code;
}

/**
 * Starts the instance `personal` (`serve --instance personal`, so its folder is
 * ~/.nextboard-personal of the scratch home) and sets it up with a password of its own.
 */
export async function startPersonal(): Promise<InstanceHub> {
  const { home, bin, env } = scratch();
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const dataDir = join(home, `.nextboard-${PERSONAL}`);
  const log = join(process.env.NEXTBOARD_E2E_ROOT!, `hub-${PERSONAL}.log`);
  // `timeout` so that a run killed halfway leaves no hub behind on a shared machine
  const proc: ChildProcess = spawn('sh', ['-c', `exec timeout 600 "$0" serve --instance ${PERSONAL} --listen 127.0.0.1:${port} >>"$1" 2>&1`, bin, log], { env, stdio: 'ignore' });
  const stop = async () => {
    if (proc.exitCode === null && proc.signalCode === null) {
      const gone = new Promise((done) => proc.once('exit', done));
      proc.kill('SIGTERM');
      await Promise.race([gone, new Promise((done) => setTimeout(done, 8000))]);
    }
    await removeInstances();
  };
  try {
    const api = await request.newContext({ baseURL: base, extraHTTPHeaders: headers });
    const deadline = Date.now() + 20_000;
    while (!(await api.get('/api/health').then((r) => r.ok(), () => false))) {
      if (proc.exitCode !== null) throw new Error(`the ${PERSONAL} hub exited (${proc.exitCode}); its log is ${log}`);
      if (Date.now() > deadline) throw new Error(`the ${PERSONAL} hub did not start; its log is ${log}`);
      await new Promise((r) => setTimeout(r, 200));
    }
    const setup = await api.post('/api/setup', { data: { code: setupCode(PERSONAL), password: PERSONAL_PASSWORD, hubName: PERSONAL_HUB_NAME } });
    if (!setup.ok()) throw new Error(`setup of ${PERSONAL}: ${setup.status()} ${await setup.text()}`);
    await api.dispose();
  } catch (err) {
    await stop();
    throw err;
  }
  return { name: PERSONAL, base, port, dataDir, password: PERSONAL_PASSWORD, hubName: PERSONAL_HUB_NAME, stop };
}

/**
 * Ends every hub the fake systemctl started, and removes the named instances' folders and units
 * from the scratch home, so that the next project's run (light, then dark) starts from none.
 */
export async function removeInstances() {
  const { home } = scratch();
  await stopFakeUnits(home);
  for (const f of readdirSync(home)) if (f.startsWith('.nextboard-')) rmSync(join(home, f), { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  const units = join(home, '.config', 'systemd', 'user');
  if (existsSync(units)) for (const f of readdirSync(units)) if (/^nextboard(-.+)?\.service$/.test(f)) rmSync(join(units, f), { force: true });
}

/** The calls the fake service manager got, one per line (`systemctl --user restart nextboard-x`). */
export function serviceCalls(): string[] {
  const { home } = scratch();
  const file = join(home, '.fake-systemd', 'calls.log');
  if (!existsSync(file)) return [];
  return readFileSync(file, 'utf8').split('\n').filter(Boolean);
}
