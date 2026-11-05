// Minimal history-API router: usePath(), navigate(), <Link>.
// The hub serves index.html for unknown paths, so deep links work.

import { useSyncExternalStore, type AnchorHTMLAttributes, type MouseEvent } from 'react';
import { prefs } from './format';

const NAV_EVENT = 'nb:navigate';

export function navigate(to: string, replace = false) {
  const current = window.location.pathname + window.location.search;
  if (to === current) return;
  if (replace) window.history.replaceState(null, '', to);
  else window.history.pushState(null, '', to);
  window.dispatchEvent(new Event(NAV_EVENT));
}

function subscribe(fn: () => void) {
  window.addEventListener('popstate', fn);
  window.addEventListener(NAV_EVENT, fn);
  return () => {
    window.removeEventListener('popstate', fn);
    window.removeEventListener(NAV_EVENT, fn);
  };
}

/** Current pathname (without query string). */
export function usePath(): string {
  return useSyncExternalStore(subscribe, () => window.location.pathname);
}

type LinkProps = AnchorHTMLAttributes<HTMLAnchorElement> & { to: string };

/** Anchor that navigates client-side (modifier-clicks still open new tabs). */
export function Link({ to, onClick, ...rest }: LinkProps) {
  const handle = (e: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(e);
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    navigate(to);
  };
  return <a href={to} onClick={handle} {...rest} />;
}

// ---- app routes -------------------------------------------------------------------

export type Route =
  | { page: 'home' }
  | { page: 'machines' }
  | { page: 'devices' }
  | { page: 'memory'; projectId?: string }
  | { page: 'board'; projectId: string; workspaceId: string }
  | { page: 'settings'; pane: string }
  | { page: 'print'; kind: 'documents' | 'markdown'; docId?: string; versionId?: string }
  | { page: 'unknown' };

/** Parse an app pathname: "/", "/machines", "/devices", "/memory[/:projectId]", "/p/:projectId[/w/:workspaceId]". */
export function parseRoute(path: string): Route {
  if (path === '/' || path === '') return { page: 'home' };
  if (path === '/machines') return { page: 'machines' };
  if (path === '/devices') return { page: 'devices' };
  const mem = /^\/memory(?:\/([^/]+))?\/?$/.exec(path);
  if (mem) return { page: 'memory', projectId: mem[1] ? decodeURIComponent(mem[1]) : undefined };
  const pd = /^\/print\/documents\/([^/]+)\/([^/]+)\/?$/.exec(path);
  if (pd) return { page: 'print', kind: 'documents', docId: decodeURIComponent(pd[1]), versionId: decodeURIComponent(pd[2]) };
  if (path === '/print/markdown') return { page: 'print', kind: 'markdown' };
  const st = /^\/settings(?:\/([a-z-]+))?\/?$/.exec(path);
  if (st) return { page: 'settings', pane: st[1] ?? 'agents' };
  const m = /^\/p\/([^/]+)(?:\/w\/([^/]+))?\/?$/.exec(path);
  if (m) return { page: 'board', projectId: decodeURIComponent(m[1]), workspaceId: m[2] ? decodeURIComponent(m[2]) : '' };
  return { page: 'unknown' };
}

export function memoryPath(projectId?: string): string {
  return projectId ? `/memory/${encodeURIComponent(projectId)}` : '/memory';
}

export function boardPath(projectId: string, workspaceId?: string): string {
  const p = `/p/${encodeURIComponent(projectId)}`;
  return workspaceId ? `${p}/w/${encodeURIComponent(workspaceId)}` : p;
}

/** Read (and optionally consume) a query parameter of the current URL. */
export function takeSearchParam(name: string): string | null {
  const url = new URL(window.location.href);
  const v = url.searchParams.get(name);
  if (v !== null) {
    url.searchParams.delete(name);
    window.history.replaceState(null, '', url.pathname + (url.search ? url.search : ''));
  }
  return v;
}

/** Workspace to open for a project (last used on this client). */
export function lastWorkspace(projectId: string): string | null {
  return prefs.get(`lastws.${projectId}`);
}
