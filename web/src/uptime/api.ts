// Uptime monitoring (docs/API-uptime.md).

import { apiRequest } from '../api';

export type MonitorType = 'http' | 'tcp' | 'ping' | 'dns' | 'push';
export type MonitorStatus = 'up' | 'down' | 'pending' | 'paused' | 'waiting';

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
  url?: string;
  method?: string;
  accepted?: string;
  keyword?: string;
  keywordInvert?: boolean;
  ignoreTls?: boolean;
  headers?: { name: string; value: string }[];
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

export interface UptimeSettings {
  webhook?: string;
  lastError?: string;
  lastErrorAt?: string;
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
  testWebhook: (pid: string) => apiRequest<{ ok: boolean }>('POST', `/api/projects/${enc(pid)}/uptime/settings/test`, {}),
};
