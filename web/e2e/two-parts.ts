import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Hub } from './hub';
import type { SSHD } from './sshd';

// A project of two equal parts (v0.9.0-beta.2, docs/API-board.md §1): "admin", the project's own
// folder on This PC, and "frontend", a folder on This PC or, with a throwaway sshd, on a server
// as the hub meets one (SFTP, commands over SSH). Neither part is special.

export const git = (cwd: string, ...args: string[]) => execFileSync('git', args, { cwd, encoding: 'utf8', env: { ...process.env, GIT_CONFIG_NOSYSTEM: '1' } }).trim();

/** A repository with its own author: the hub's home has no git identity. */
export function repo(dir: string, subject = 'first') {
  git(dir, 'init', '-q', '-b', 'main');
  git(dir, 'config', 'user.name', 'Test Person');
  git(dir, 'config', 'user.email', 'test@example.com');
  git(dir, 'add', '-A');
  git(dir, 'commit', '-qm', subject);
}

export interface Part {
  id: string;
  name: string;
  path: string;
  machineId: string;
}

export interface TwoParts {
  id: string;
  wid: string;
  name: string;
  admin: Part;
  front: Part;
  /** The server's name in the hub, when frontend is on the sshd. */
  server?: string;
}

let unique = 0;

/**
 * admin holds server.go, frontend index.html and src/app.ts; each line with "greeting" in it, so a
 * search finds one hit in each part. With git, each part is a repository of its own.
 */
export async function twoParts(hub: Hub, base: string, opts: { sshd?: SSHD; git?: boolean; front?: string } = {}): Promise<TwoParts> {
  const p = await hub.project(base);
  writeFileSync(join(p.path, 'server.go'), 'package main\n\n// greeting from admin\nfunc main() {}\n');
  const dir = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, opts.sshd ? 'remote-' : 'front-'));
  mkdirSync(join(dir, 'src'));
  writeFileSync(join(dir, 'index.html'), '<p>greeting from frontend</p>\n');
  writeFileSync(join(dir, 'src', 'app.ts'), 'export const app = 1;\n');
  if (opts.git) {
    repo(p.path, 'admin base');
    repo(dir, 'frontend base');
  }
  let machineId = 'local';
  let server: string | undefined;
  if (opts.sshd) {
    server = `devbox-${(unique++).toString(36)}${process.pid.toString(36)}`;
    machineId = await opts.sshd.machine(hub, server);
  }
  const first = (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).parts[0];
  await hub.call('PATCH', `/api/projects/${p.id}/parts/${first.id}`, { name: 'admin' });
  const made = await hub.call('POST', `/api/projects/${p.id}/parts`, { name: opts.front ?? 'frontend', machineId, path: dir });
  const parts = made.parts as { id: string; name: string; path: string; machineId: string }[];
  const front = parts.find((pt) => pt.path === dir)!;
  return {
    id: p.id,
    wid: p.wid,
    name: p.name,
    admin: { id: first.id, name: 'admin', path: p.path, machineId: 'local' },
    front: { id: front.id, name: front.name, path: dir, machineId },
    server,
  };
}

/** Ends every console of a board, so that the next test starts from none. */
export async function endConsoles(hub: Hub, wid: string) {
  for (const s of await hub.call('GET', '/api/sessions')) if (s.workspaceId === wid) await hub.call('DELETE', `/api/sessions/${s.id}`);
}

/** The consoles of a board, as the hub has them. */
export async function consolesOf(hub: Hub, wid: string): Promise<{ id: string; partId?: string; root?: boolean; machineId: string; cwd: string; agent?: string; preset?: string }[]> {
  return ((await hub.call('GET', '/api/sessions')) as any[]).filter((s) => s.workspaceId === wid);
}
