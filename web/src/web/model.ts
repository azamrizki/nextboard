// Web tile data (node kind "web", docs/API-studio.md §4.7) and URL helpers. The tile
// shows a localhost dev server or an outside site through the hub's preview proxy:
// the address bar keeps the page's own URL (http://localhost:5173/…) while the iframe
// loads the preview origin (<page scheme>://<this host>:<listenPort>/…).

import { prefs } from '../lib/format';
import { newId } from '../lib/ids';
import type { BoardNode, Op } from '../lib/types';
import type { BoardController } from '../board/controller';

export const DEVICES = [
  { id: 'fit', label: 'Fit tile', w: 0, h: 0, group: 'fit' },
  { id: 'desktop-1920', label: '24 in monitor', w: 1920, h: 1080, group: 'display' },
  { id: 'desktop-2560', label: '27 in monitor', w: 2560, h: 1440, group: 'display' },
  { id: 'desktop-3840', label: '32 in 4K', w: 3840, h: 2160, group: 'display' },
  { id: 'phone-375', label: 'iPhone SE', w: 375, h: 667, group: 'phone' },
  { id: 'phone-393', label: 'iPhone 14 Pro', w: 393, h: 852, group: 'phone' },
  { id: 'phone-430', label: 'iPhone 14 Pro Max', w: 430, h: 932, group: 'phone' },
  { id: 'phone-412', label: 'Pixel 7', w: 412, h: 915, group: 'phone' },
  { id: 'phone-360', label: 'Galaxy S8+', w: 360, h: 740, group: 'phone' },
] as const;
export type DeviceId = (typeof DEVICES)[number]['id'];

export const DIAL_PORTS = [3000, 3001, 4200, 5173, 8000, 8080];
export const MAX_TABS = 12;
export const ZOOM_MIN = 50;
export const ZOOM_MAX = 250;

export interface WebTab {
  id: string;
  url: string;
  title: string;
}

export interface WebData {
  url: string;
  previewId: string;
  machineId: string;
  port: number;
  device: DeviceId;
  zoom: number;
  auto: boolean;
  tabs: WebTab[];
  activeTab: string;
  /** The browser showing an outside site (docs/API-gui.md §8). */
  sessionId: string;
  appId: string;
  name: string;
  /** Which browser of that kind: each has its own profile, and this tile comes back to its own. */
  instance: number;
  /** `url` is a site shown through the proxy, on the tailnet or the internet (docs/API-studio.md §4.9). */
  tailnet: boolean;
  /** The tile was asked to use the real browser, even for a site that loads through the proxy. */
  realBrowser: boolean;
}

const str = (v: unknown) => (typeof v === 'string' ? v : '');

export function readWeb(n: BoardNode): WebData {
  const d = n.data;
  const device = DEVICES.some((x) => x.id === d.device) ? (d.device as DeviceId) : 'fit';
  const zoom = typeof d.zoom === 'number' && Number.isFinite(d.zoom) ? Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, Math.round(d.zoom / 10) * 10)) : 100;
  const tabs = Array.isArray(d.tabs)
    ? (d.tabs as unknown[])
        .map((t) => (t && typeof t === 'object' ? (t as Record<string, unknown>) : {}))
        .filter((t) => typeof t.id === 'string' && typeof t.url === 'string')
        .map((t) => ({ id: String(t.id), url: String(t.url), title: str(t.title) }))
        .slice(0, MAX_TABS)
    : [];
  return {
    url: str(d.url),
    previewId: str(d.previewId),
    machineId: str(d.machineId),
    port: typeof d.port === 'number' ? d.port : 0,
    device,
    zoom,
    auto: d.auto === true,
    tabs,
    activeTab: str(d.activeTab),
    sessionId: str(d.sessionId),
    appId: str(d.appId),
    name: str(d.name),
    instance: typeof d.instance === 'number' && d.instance > 0 ? d.instance : 0,
    tailnet: d.tailnet === true,
    realBrowser: d.realBrowser === true,
  };
}

export interface LocalTarget {
  scheme: 'http' | 'https';
  port: number;
  /** path + search + hash */
  path: string;
  /** A site on the tailnet; without it, a port of the project's machine. */
  host?: string;
}

const LOCAL_HOSTS = new Set(['localhost', '127.0.0.1', '0.0.0.0', '[::1]', '::1']);

/** "localhost:5173/x", "http://127.0.0.1:3000" → target; anything else → null. */
export function localTarget(raw: string): LocalTarget | null {
  let s = raw.trim();
  if (!s) return null;
  if (/^\d{2,5}(\/|$)/.test(s)) s = `localhost:${s}`;
  if (!/^[a-z]+:\/\//i.test(s)) s = `http://${s}`;
  try {
    const u = new URL(s);
    if (!LOCAL_HOSTS.has(u.hostname)) return null;
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return null;
    const port = Number(u.port || (u.protocol === 'https:' ? 443 : 80));
    if (!(port >= 1 && port <= 65535)) return null;
    return { scheme: u.protocol === 'https:' ? 'https' : 'http', port, path: `${u.pathname}${u.search}${u.hash}` || '/' };
  } catch {
    return null;
  }
}

/** The target of a site shown through the proxy, or null. */
export function siteTarget(raw: string): LocalTarget | null {
  try {
    const u = new URL(raw);
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return null;
    const scheme = u.protocol === 'https:' ? 'https' : 'http';
    return { scheme, host: u.hostname, port: Number(u.port || (scheme === 'https' ? 443 : 80)), path: `${u.pathname}${u.search}${u.hash}` || '/' };
  } catch {
    return null;
  }
}

/** http(s) URL of an external site, or null. */
export function externalUrl(raw: string): string | null {
  let s = raw.trim();
  if (!s || localTarget(s)) return null;
  // a bare IP rarely has a certificate, unlike a name
  if (!/^[a-z]+:\/\//i.test(s)) s = `${/^(\d{1,3}\.){3}\d{1,3}(:|\/|$)/.test(s) ? 'http' : 'https'}://${s}`;
  try {
    const u = new URL(s);
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.toString() : null;
  } catch {
    return null;
  }
}

export function displayUrl(t: LocalTarget): string {
  if (!t.host) return `${t.scheme}://localhost:${t.port}${t.path === '/' ? '/' : t.path}`;
  return `${t.scheme}://${t.host}${t.port === (t.scheme === 'https' ? 443 : 80) ? '' : `:${t.port}`}${t.path}`;
}

/** What the address bar and the tile's title call a target. */
export const targetLabel = (t: LocalTarget) => t.host ?? `localhost:${t.port}`;

/**
 * Origin of a preview listener as seen from this page: same protocol and hostname as the
 * page, the preview's listenPort (§4.2). Never hard-code https: the desktop app serves the
 * UI over http on loopback and maps listenPort to its own pinned loopback listeners. The hub's
 * listeners are plain HTTP; behind nginx each preview port is served over HTTPS too.
 */
export const previewOrigin = (listenPort: number) => `${window.location.protocol}//${window.location.hostname}:${listenPort}`;

/** Map a URL inside the preview origin back to the page's own URL. */
export function toDevUrl(href: string, listenPort: number, scheme: string, port: number, host?: string): string {
  try {
    const u = new URL(href);
    if (u.origin !== previewOrigin(listenPort)) return href;
    return displayUrl({ scheme: scheme === 'https' ? 'https' : 'http', port, host, path: `${u.pathname}${u.search}${u.hash}` });
  } catch {
    return href;
  }
}

export const DEFAULT_WEB_SIZE = { w: 960, h: 640 };

/** A web tile (empty = the port dial). */
export function webNode(ctl: BoardController, rect: { x: number; y: number; w: number; h: number }, url = ''): BoardNode {
  return ctl.make('web', rect, { url, device: 'fit', zoom: 100, auto: false });
}

/** Add (or find) the console-log card of a web tile, wired with a "log" edge. */
export function logCardFor(ctl: BoardController, web: BoardNode, previewId: string): string {
  const existing = ctl.sync.state.edges.find((e) => e.kind === 'log' && e.source === web.id);
  if (existing && ctl.node(existing.target)) return existing.target;
  const node = ctl.make('weblog', { x: web.x + web.w + 40, y: web.y, w: 460, h: Math.min(web.h, 420) }, { previewId, filter: 'all' });
  const ops: Op[] = [
    { op: 'put', node },
    { op: 'putEdge', edge: { id: newId('e'), source: web.id, target: node.id, kind: 'log', data: {} } },
  ];
  ctl.apply(ops);
  return node.id;
}

// ---- sites that work better in the real browser ----

const REAL_BROWSER_KEY = 'web.realBrowserHosts';

function realBrowserHosts(): string[] {
  try {
    const v = JSON.parse(prefs.get(REAL_BROWSER_KEY) ?? '[]');
    return Array.isArray(v) ? v.filter((h): h is string => typeof h === 'string') : [];
  } catch {
    return [];
  }
}

/** Was this site moved to the real browser? Then it goes straight there, from this browser. */
export const prefersRealBrowser = (host: string) => realBrowserHosts().includes(host);

export function rememberRealBrowser(host: string, on: boolean) {
  const rest = realBrowserHosts().filter((h) => h !== host);
  prefs.set(REAL_BROWSER_KEY, JSON.stringify(on ? [...rest, host] : rest));
}
