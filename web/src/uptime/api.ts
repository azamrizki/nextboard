// Uptime monitoring (docs/API-uptime.md).

import { apiRequest } from '../api';

export type MonitorType = 'http' | 'tcp' | 'ping' | 'dns' | 'push' | 'websocket' | 'docker' | 'steam';
export type MonitorStatus = 'up' | 'down' | 'pending' | 'paused' | 'waiting' | 'maintenance';
export type JsonOp = '==' | '!=' | 'contains' | '<' | '>' | '<=' | '>=';

/** One check. s: 0 down, 1 up, 2 pending. ms is -1 when no time was measured. */
export interface Beat {
  t: number;
  s: 0 | 1 | 2;
  ms: number;
  m?: string;
}

export interface MonitorConfig {
  name: string;
  type: MonitorType;
  machineId?: string;
  /** The part of the project it watches (ROADMAP #49): a label. */
  partId?: string;
  url?: string;
  method?: string;
  accepted?: string;
  keyword?: string;
  keywordInvert?: boolean;
  ignoreTls?: boolean;
  headers?: { name: string; value: string }[];
  /** A value of a JSON answer ("data.items[0].status") compared with jsonExpect. */
  jsonPath?: string;
  jsonOp?: JsonOp;
  jsonExpect?: string;
  container?: string;
  host?: string;
  port?: number;
  dnsType?: string;
  dnsServer?: string;
  dnsExpect?: string;
  intervalSec?: number;
  retryIntervalSec?: number;
  timeoutSec?: number;
  retries?: number;
  active?: boolean;
  notify?: boolean;
  /** On the project's public status page, under publicName and nothing else of it. */
  public?: boolean;
  publicName?: string;
  /** Monitors with the same group sit under one heading; tags filter them. Not on the public page. */
  group?: string;
  tags?: string[];
}

export interface Cert {
  subject: string;
  issuer: string;
  notAfter: string;
}

export interface Monitor extends MonitorConfig {
  id: string;
  projectId: string;
  intervalSec: number;
  retryIntervalSec: number;
  timeoutSec: number;
  retries: number;
  active: boolean;
  notify: boolean;
  pushToken?: string;
  createdAt: string;
  status: MonitorStatus;
  target: string;
  since?: string;
  msg?: string;
  ms: number;
  lastCheck?: string;
  avgMs24h: number;
  uptime24h: number | null;
  uptime7d: number | null;
  uptime30d: number | null;
  uptime1y: number | null;
  cert?: Cert;
  recent?: Beat[];
}

/** The public status page of a project. The token is its address. */
export interface PublicPageSettings {
  enabled: boolean;
  token?: string;
  title?: string;
}

/** A maintenance window: its monitors are not checked, counted, or alerted on while it is on. */
export interface MaintenanceWindow {
  id?: string;
  title: string;
  /** None: every monitor of the project. */
  monitorIds?: string[];
  mode: 'manual' | 'once' | 'weekly';
  active?: boolean;
  start?: string;
  end?: string;
  /** 0 Sunday to 6 Saturday. */
  days?: number[];
  from?: string;
  minutes?: number;
  tz?: string;
}

export interface UptimeSettings {
  webhook?: string;
  lastError?: string;
  lastErrorAt?: string;
  publicPage?: PublicPageSettings;
  maintenance?: MaintenanceWindow[];
}

export interface UptimeEvent {
  t: number;
  status: 'up' | 'down';
  msg?: string;
  durSec?: number;
}

export interface SeriesPoint {
  t: number;
  up: number;
  down: number;
  pending?: number;
  avgMs: number;
  minMs: number;
  maxMs: number;
}

export type SeriesRange = '1h' | '24h' | '7d' | '30d' | '1y';

export interface Series {
  range: SeriesRange;
  from: number;
  to: number;
  stepSec: number;
  points: SeriesPoint[];
  stats: { uptime: number | null; avgMs: number; minMs: number; maxMs: number; checks: number };
}

const enc = encodeURIComponent;

export const uptimeApi = {
  list: (pid: string) => apiRequest<{ items: Monitor[] | null; settings: UptimeSettings }>('GET', `/api/projects/${enc(pid)}/uptime`),
  create: (pid: string, body: MonitorConfig) => apiRequest<Monitor>('POST', `/api/projects/${enc(pid)}/uptime`, body),
  update: (id: string, body: Partial<MonitorConfig>) => apiRequest<Monitor>('PATCH', `/api/uptime/${enc(id)}`, body),
  remove: (id: string) => apiRequest<{ ok: boolean }>('DELETE', `/api/uptime/${enc(id)}`),
  check: (id: string) => apiRequest<{ ok: boolean }>('POST', `/api/uptime/${enc(id)}/check`, {}),
  history: (id: string, range: SeriesRange) => apiRequest<Series>('GET', `/api/uptime/${enc(id)}/history?range=${range}`),
  events: (id: string) => apiRequest<{ items: UptimeEvent[] | null }>('GET', `/api/uptime/${enc(id)}/events?limit=50`),
  setWebhook: (pid: string, webhook: string) => apiRequest<UptimeSettings>('PUT', `/api/projects/${enc(pid)}/uptime/settings`, { webhook }),
  setPublicPage: (pid: string, body: { enabled: boolean; title: string }) => apiRequest<PublicPageSettings>('PUT', `/api/projects/${enc(pid)}/uptime/public`, body),
  rotatePublicPage: (pid: string) => apiRequest<PublicPageSettings>('POST', `/api/projects/${enc(pid)}/uptime/public/rotate`, {}),
  setMaintenance: (pid: string, windows: MaintenanceWindow[]) => apiRequest<UptimeSettings>('PUT', `/api/projects/${enc(pid)}/uptime/maintenance`, { windows }),
  testWebhook: (pid: string) => apiRequest<{ ok: boolean }>('POST', `/api/projects/${enc(pid)}/uptime/settings/test`, {}),
};
