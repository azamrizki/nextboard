// Desktop apps of This PC (docs/API-gui.md).

import { apiRequest } from '../api';

export interface GuiApp {
  id: string;
  name: string;
  comment?: string;
  categories?: string[];
  acceptsFolder: boolean;
}

export interface GuiCatalog {
  available: boolean;
  missing: string[];
  hint?: string;
  /** The kind of display new sessions get. */
  display?: 'wayland' | 'x11';
  /** Render node in use. */
  gpu?: string;
  /** A hardware video encoder works. */
  video: boolean;
  /** What it can encode, best first. */
  videoCodecs: string[];
  /** Sessions get their own sound output. */
  audio: boolean;
  apps: GuiApp[];
}

export interface GuiSession {
  id: string;
  appId: string;
  name: string;
  title: string;
  state: 'starting' | 'running' | 'stopping';
  display: 'wayland' | 'x11';
  /** Has its own sound output. */
  audio: boolean;
  /** Has its own microphone source. */
  mic?: boolean;
  /** Which browser of this kind, each with its own profile. Absent on the first. */
  instance?: number;
  startedAt: string;
}

/** NextBoard Chromium (docs/API-gui.md §8.3). */
export interface BrowserStatus {
  id: string;
  name: string;
  supported: boolean;
  installed?: string;
  latest?: string;
  checkedAt?: string;
  autoUpdate: boolean;
  state: 'idle' | 'checking' | 'downloading' | 'installing' | 'error';
  done?: number;
  total?: number;
  error?: string;
  /** How it is sandboxed here; "setup" = the owner runs setupCommand once. */
  sandbox?: 'namespaces' | 'helper' | 'setup';
  sandboxHelper?: string;
  setupCommand?: string;
  dir?: string;
}

export type GuiEvent =
  | { type: 'guiBrowser'; browser: BrowserStatus }
  | { type: 'guiSession'; session: GuiSession }
  | { type: 'guiSessionRemoved'; id: string; appId: string; reason: 'closed' | 'stopped' | 'failed'; message?: string };

const enc = encodeURIComponent;

export const guiApi = {
  catalog: () => apiRequest<GuiCatalog>('GET', '/api/gui/apps'),
  sessions: () => apiRequest<GuiSession[] | null>('GET', '/api/gui/sessions').then((r) => r ?? []),
  launch: (body: { appId: string; path?: string; width?: number; height?: number }) => apiRequest<GuiSession>('POST', '/api/gui/sessions', body),
  openUrl: (body: { url: string; width?: number; height?: number; here?: boolean; instance?: number }) => apiRequest<{ session: GuiSession; url: string; newTab: boolean }>('POST', '/api/gui/browser/open', body),
  stop: (id: string) => apiRequest<{ ok: boolean }>('DELETE', `/api/gui/sessions/${enc(id)}`),
  browser: () => apiRequest<BrowserStatus>('GET', '/api/gui/browser'),
  installBrowser: () => apiRequest<BrowserStatus>('POST', '/api/gui/browser/install'),
  checkBrowser: () => apiRequest<BrowserStatus>('POST', '/api/gui/browser/check'),
  setBrowserAutoUpdate: (autoUpdate: boolean) => apiRequest<BrowserStatus>('PATCH', '/api/gui/browser', { autoUpdate }),
  removeBrowser: () => apiRequest<BrowserStatus>('DELETE', '/api/gui/browser'),
};

export const guiIconUrl = (appId: string) => `/api/gui/apps/${enc(appId)}/icon`;
export const guiViewerUrl = (s: GuiSession) => `/gui-viewer.html?session=${enc(s.id)}&display=${enc(s.display)}`;
