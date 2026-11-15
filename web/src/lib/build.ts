// Which build a hub runs, for people: the header's label and the rows of About this hub
// (docs/API.md Hub). An older hub sends only its version.

import type { BuildInfo } from './types';

export type Channel = 'stable' | 'beta' | 'dev';

const SEMVER = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/;

/** The hub's own word, or the same rule as internal/version.Channel for a hub that sends none. */
export function buildChannel(b: Pick<BuildInfo, 'version' | 'channel'>): Channel {
  if (b.channel === 'stable' || b.channel === 'beta' || b.channel === 'dev') return b.channel;
  const m = SEMVER.exec(b.version ?? '');
  if (!m) return 'dev';
  if (!m[4]) return 'stable';
  const [head, ...rest] = m[4].split('.');
  return ['alpha', 'beta', 'rc'].includes(head) && rest.every((p) => /^\d+$/.test(p)) ? 'beta' : 'dev';
}

/** The commit's hash without the "-dirty" mark; "" when the build recorded none. */
export function commitHash(commit: string | undefined): string {
  const c = (commit ?? '').trim().replace(/-dirty$/, '');
  return /^[0-9a-f]{4,40}$/i.test(c) ? c.toLowerCase() : '';
}

/** Built from a tree with uncommitted changes. */
export function isDirty(commit: string | undefined): boolean {
  return !!commitHash(commit) && /-dirty$/.test((commit ?? '').trim());
}

/** "v0.9.0", whether the hub wrote the v or not. */
export function versionTag(version: string): string {
  const v = (version ?? '').trim();
  return v ? `v${v.replace(/^v/, '')}` : '';
}

/**
 * The header's label: "v0.9.0 · 3f2a1c9", "v0.9.0-beta.2 · 3f2a1c9", "dev · 3f2a1c9*" (* =
 * uncommitted changes); just the version when no commit was recorded.
 */
export function buildLabel(b: BuildInfo): string {
  const head = buildChannel(b) === 'dev' ? 'dev' : versionTag(b.version);
  const hash = commitHash(b.commit).slice(0, 7);
  if (!hash) return head;
  return `${head} · ${hash}${isDirty(b.commit) ? '*' : ''}`;
}

/** The commit's page on GitHub, or null when the commit or repository is not known. */
export function commitUrl(repo: string | undefined, commit: string | undefined): string | null {
  const hash = commitHash(commit);
  if (!hash || !repo || !/^[\w.-]+\/[\w.-]+$/.test(repo)) return null;
  return `https://github.com/${repo}/commit/${hash}`;
}

/** Same version, whatever the v. */
export function sameVersion(a: string | undefined, b: string | undefined): boolean {
  return !!a && !!b && a.trim().replace(/^v/, '') === b.trim().replace(/^v/, '');
}

/** The script a page of the web UI loads: it changes with the bundle. */
export function bundleScript(html: string): string {
  return /<script[^>]+type="module"[^>]*src="([^"]+)"/.exec(html)?.[1] ?? /<script[^>]+src="([^"]+)"[^>]*type="module"/.exec(html)?.[1] ?? '';
}
