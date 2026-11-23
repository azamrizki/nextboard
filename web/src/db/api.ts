// The database browser's endpoints (docs/API-db.md).

import { apiRequest } from '../api';
import type {
  Activity,
  Candidate,
  Change,
  Connection,
  DbRequest,
  Filter,
  HistoryItem,
  KeyPage,
  KeyValue,
  KVInfo,
  Result,
  RowsPage,
  SavedQuery,
  Schema,
  Secrets,
  Sort,
  Status,
  TableInfo,
  TestResult,
} from './types';

const enc = encodeURIComponent;
const get = <T>(p: string) => apiRequest<T>('GET', p);
const post = <T>(p: string, b?: unknown) => apiRequest<T>('POST', p, b ?? {});

export interface RunOpts {
  session?: string;
  sql: string;
  confirmed?: boolean;
  confirmName?: string;
  maxRows?: number;
  schema?: string;
}

export interface RowsRequest {
  schema: string;
  table: string;
  filters?: Filter[];
  where?: string;
  sort?: Sort[];
  offset?: number;
  limit?: number;
}

export const dbApi = {
  list: (pid: string) => get<{ connections: Connection[]; status: Record<string, Status> }>(`/api/projects/${enc(pid)}/dbs`),
  create: (pid: string, connection: Connection, secrets: Secrets, detectRef?: string) => post<Connection>(`/api/projects/${enc(pid)}/dbs`, { connection, secrets, detectRef }),
  detect: (pid: string) => get<{ candidates: Candidate[] }>(`/api/projects/${enc(pid)}/dbs/detect`),
  get: (id: string) => get<{ connection: Connection; status: Status }>(`/api/dbs/${enc(id)}`),
  replace: (id: string, connection: Connection, secrets: Secrets) => apiRequest<Connection>('PUT', `/api/dbs/${enc(id)}`, { connection, secrets }),
  remove: (id: string) => apiRequest<{ ok: boolean }>('DELETE', `/api/dbs/${enc(id)}`),
  test: (connection: Connection, secrets: Secrets, id?: string, detectRef?: string) => post<TestResult>('/api/dbs/test', { connection, secrets, id, detectRef }),
  trust: (id: string, hop: number, fingerprint: string) => post<Connection>(`/api/dbs/${enc(id)}/trust`, { hop, fingerprint }),
  password: (id: string, password: string) => post<{ ok: boolean }>(`/api/dbs/${enc(id)}/password`, { password }),
  disconnect: (id: string) => post<{ ok: boolean }>(`/api/dbs/${enc(id)}/disconnect`),
  schema: (id: string, current = '') => get<{ schema?: Schema; kv?: KVInfo }>(`/api/dbs/${enc(id)}/schema${current ? `?current=${enc(current)}` : ''}`),
  table: (id: string, schema: string, name: string) => get<TableInfo>(`/api/dbs/${enc(id)}/table?schema=${enc(schema)}&name=${enc(name)}`),
  rows: (id: string, rq: RowsRequest) => post<RowsPage>(`/api/dbs/${enc(id)}/rows`, rq),
  run: (id: string, o: RunOpts) => post<{ results: Result[] }>(`/api/dbs/${enc(id)}/run`, o),
  finish: (id: string, session: string, commit: boolean) => post<{ ok: boolean }>(`/api/dbs/${enc(id)}/sessions/${enc(session)}/finish`, { commit }),
  cancel: (id: string, session: string) => post<{ stopped: boolean }>(`/api/dbs/${enc(id)}/sessions/${enc(session)}/cancel`),
  apply: (id: string, body: { schema: string; table: string; changes: Change[]; preview?: boolean; confirmed?: boolean }) => post<{ sql: string[]; affected?: number[] }>(`/api/dbs/${enc(id)}/apply`, body),
  history: (id: string, limit = 100) => get<{ items: HistoryItem[] }>(`/api/dbs/${enc(id)}/history?limit=${limit}`),
  activity: (id: string, limit = 100) => get<{ items: Activity[] }>(`/api/dbs/${enc(id)}/activity?limit=${limit}`),
  keys: (id: string, q: { db: number; cursor?: number; match?: string; type?: string; count?: number }) =>
    get<KeyPage>(`/api/dbs/${enc(id)}/keys?db=${q.db}&cursor=${q.cursor ?? 0}&match=${enc(q.match ?? '*')}&type=${enc(q.type ?? '')}&count=${q.count ?? 500}`),
  key: (id: string, db: number, name: string, page = '') => get<KeyValue>(`/api/dbs/${enc(id)}/key?db=${db}&name=${enc(name)}&page=${enc(page)}`),
  writeKey: (id: string, body: Record<string, unknown>) => post<{ result: string }>(`/api/dbs/${enc(id)}/key`, body),
  command: (id: string, body: { db: number; line: string; confirmed?: boolean; confirmName?: string }) => post<{ output: string }>(`/api/dbs/${enc(id)}/command`, body),
  queries: (pid: string) => get<{ items: SavedQuery[] }>(`/api/projects/${enc(pid)}/db-queries`),
  saveQuery: (pid: string, q: Partial<SavedQuery>) => post<SavedQuery>(`/api/projects/${enc(pid)}/db-queries`, q),
  deleteQuery: (pid: string, id: string) => apiRequest<{ ok: boolean }>('DELETE', `/api/projects/${enc(pid)}/db-queries/${enc(id)}`),
  requests: () => get<{ items: DbRequest[] }>('/api/db-requests'),
  approve: (id: string, body: { session?: boolean; confirmName?: string }) => post<{ ok: boolean }>(`/api/db-requests/${enc(id)}/approve`, body),
  deny: (id: string, note = '') => post<{ ok: boolean }>(`/api/db-requests/${enc(id)}/deny`, { note }),
  aiFilters: (id: string, body: { schema: string; table: string; text: string }) => post<{ filters: Filter[]; sort: Sort[]; where: string; note: string }>(`/api/dbs/${enc(id)}/ai/filters`, body),
  aiSQL: (id: string, body: { mode: 'write' | 'explain' | 'fix' | 'optimize'; sql?: string; prompt?: string; error?: string; schema?: string }) =>
    post<{ sql: string; text: string }>(`/api/dbs/${enc(id)}/ai/sql`, body),
  aiChat: (id: string, body: { message: string; history: { role: 'user' | 'assistant'; text: string }[]; table?: string; schema?: string }) =>
    post<{ run: string; jobId: string }>(`/api/dbs/${enc(id)}/ai/chat`, body),
};

/** Downloads an export: the hub streams it, the browser saves it. */
export async function downloadExport(id: string, body: Record<string, unknown>, name: string): Promise<void> {
  const res = await fetch(`/api/dbs/${enc(id)}/export`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-NextBoard': '1' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const j = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(j.error || `export failed (${res.status})`);
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}
