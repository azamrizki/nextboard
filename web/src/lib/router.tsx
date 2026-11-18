// Minimal history-API router: usePath(), navigate(), <Link>.
// The hub serves index.html for unknown paths, so deep links work.

import { useSyncExternalStore, type AnchorHTMLAttributes, type MouseEvent } from 'react';
import { prefs } from './format';

const NAV_EVENT = 'nb:navigate';

export function navigate(to: string, replace = false, state: unknown = null) {
  const current = window.location.pathname + window.location.search;
  if (to === current) return;
  if (replace) window.history.replaceState(state, '', to);
  else window.history.pushState(state, '', to);
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

/** One query parameter of the current URL, kept up to date. */
export function useSearchParam(name: string): string | null {
  const search = useSyncExternalStore(subscribe, () => window.location.search);
  return new URLSearchParams(search).get(name);
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
  | { page: 'code'; projectId: string }
  | { page: 'settings'; pane: string }
  | { page: 'print'; kind: 'documents' | 'markdown'; docId?: string; versionId?: string }
  | { page: 'unknown' };

/** Parse an app pathname: "/", "/machines", "/devices", "/memory[/:projectId]", "/p/:projectId[/w/:workspaceId]", "/p/:projectId/code". */
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
  // no pane: a phone shows the list of panes, the others open the first one
  if (st) return { page: 'settings', pane: st[1] ?? '' };
  const code = /^\/p\/([^/]+)\/code\/?$/.exec(path);
  if (code) return { page: 'code', projectId: decodeURIComponent(code[1]) };
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

/** The project's Code view: its editor full screen, no board (docs/DESIGN.md §3.19). */
export function codePath(projectId: string): string {
  return `/p/${encodeURIComponent(projectId)}/code`;
}

// The page this tab was opened on, before any navigation of the app. A link to a project with no
// view ("/p/<id>" typed, bookmarked or opened from outside) asks where to go once (§3.19); the app's
// own ways to a board name it or mean the board.
const entryPath = typeof window === 'undefined' ? '' : window.location.pathname + window.location.search;
let entryTaken = false;
const entryListeners = new Set<() => void>();

/** Whether this is that link to this project, not yet answered. The answer is given once. */
export function isEntryLink(projectId: string): boolean {
  if (entryTaken) return false;
  const r = parseRoute(entryPath);
  return r.page === 'board' && !r.workspaceId && r.projectId === projectId && !entryPath.includes('?');
}

/** The entry link has been answered: later visits to "/p/<id>" are the board's. */
export function takeEntryLink() {
  if (entryTaken) return;
  entryTaken = true;
  entryListeners.forEach((fn) => fn());
}

/** Whether the entry link has been answered, kept up to date (App renders the board page then, once). */
export function useEntryTaken(): boolean {
  return useSyncExternalStore(
    (fn) => {
      entryListeners.add(fn);
      return () => entryListeners.delete(fn);
    },
    () => entryTaken,
  );
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

/**
 * The board an editor binds or a project opens on: the one pinned (bound before, while it is
 * still there), else the one opened last in this browser, else the first.
 */
export function boardOf<W extends { id: string }>(workspaces: W[] | null | undefined, pinned?: string | null, last?: string | null): W | undefined {
  const list = workspaces ?? [];
  return (pinned ? list.find((w) => w.id === pinned) : undefined) ?? (last ? list.find((w) => w.id === last) : undefined) ?? list[0];
}

/** Workspace to open for a project (last used on this client). */
export function lastWorkspace(projectId: string): string | null {
  return prefs.get(`lastws.${projectId}`);
}
