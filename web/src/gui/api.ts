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

export type GuiEvent =
  | { type: 'guiSession'; session: GuiSession }
  | { type: 'guiSessionRemoved'; id: string; appId: string; reason: 'closed' | 'stopped' | 'failed'; message?: string };

const enc = encodeURIComponent;

export const guiApi = {
  catalog: () => apiRequest<GuiCatalog>('GET', '/api/gui/apps'),
  sessions: () => apiRequest<GuiSession[] | null>('GET', '/api/gui/sessions').then((r) => r ?? []),
  launch: (body: { appId: string; path?: string; width?: number; height?: number }) => apiRequest<GuiSession>('POST', '/api/gui/sessions', body),
  openUrl: (body: { url: string; width?: number; height?: number; here?: boolean; instance?: number }) => apiRequest<{ session: GuiSession; url: string; newTab: boolean }>('POST', '/api/gui/browser/open', body),
  stop: (id: string) => apiRequest<{ ok: boolean }>('DELETE', `/api/gui/sessions/${enc(id)}`),
};

export const guiIconUrl = (appId: string) => `/api/gui/apps/${enc(appId)}/icon`;
export const guiViewerUrl = (s: GuiSession) => `/gui-viewer.html?session=${enc(s.id)}&display=${enc(s.display)}`;
