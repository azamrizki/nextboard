// Agents asking to use a credential (docs/API-vault.md, "Agent memakai credential").
//
// The item's own blob says which projects' agents may ask for it, and only that counts. The hub
// keeps a plain-text catalog of names so that agents can see what to ask for; the dialog that
// hands a value out reads the blob again, so an entry nobody wrote here opens nothing.

import { apiRequest, errorMessage } from '../api';
import type { VaultItem } from './model';

export interface AgentValue {
  name: string;
  value: string;
}

const CONTROL = /[\u0000-\u001f\u007f]/;
const MAX_VALUES = 32;

/**
 * What a command can be given from this item, under the names agents see. Cards and identities
 * give nothing of their own: a payment card or a passport number has no business in a command.
 */
export function agentValues(it: VaultItem): AgentValue[] {
  const out: AgentValue[] = [];
  const seen = new Set<string>();
  const add = (name: string, value: string | undefined) => {
    const n = name.trim();
    if (!n || !value || n.length > 100 || CONTROL.test(n) || seen.has(n.toLowerCase())) return;
    seen.add(n.toLowerCase());
    out.push({ name: n, value });
  };
  const v = it.values;
  if (it.type === 'login') {
    add('username', v.username);
    add('password', v.password);
  } else if (it.type === 'note') add('note', it.notes);
  else if (it.type === 'ssh') {
    add('privateKey', v.privateKey);
    add('publicKey', v.publicKey);
  }
  for (const f of it.fields) add(f.name, f.value);
  return out.slice(0, MAX_VALUES);
}

export interface AgentEntry {
  id: string;
  name: string;
  type: string;
  projects: string[];
  values: string[];
  updatedAt?: string;
}

/** The catalog entry this item should have: none when it is not shared, in the trash, or gives nothing. */
export function agentEntryFor(it: VaultItem, known?: Set<string>): AgentEntry | null {
  if (it.deletedAt || !it.agents) return null;
  const projects = [...new Set(it.agents.projects)].filter((p) => !known || known.has(p)).sort();
  const values = agentValues(it).map((x) => x.name);
  const name = it.name.replace(new RegExp(CONTROL.source, 'g'), ' ').trim().slice(0, 200).trim();
  if (!projects.length || !values.length || !name) return null;
  return { id: it.id, name, type: it.type, projects, values };
}

export const sameEntry = (a: AgentEntry | undefined, b: AgentEntry | null) =>
  !!a && !!b && a.name === b.name && a.type === b.type && a.projects.join('\n') === b.projects.join('\n') && a.values.join('\n') === b.values.join('\n');

// ---- requests ----

export interface CredUse {
  item: string;
  name: string;
  value: string;
  /** None: the value goes to the command's standard input. */
  env?: string;
}

export type CredState = 'waiting' | 'running' | 'done' | 'denied' | 'expired' | 'failed';

export interface CredRequest {
  id: string;
  state: CredState;
  sessionId: string;
  handle: string;
  agent: string;
  projectId: string;
  workspaceId: string;
  machineId: string;
  machine: string;
  cwd: string;
  command: string;
  why: string;
  uses: CredUse[];
  timeoutMs: number;
  createdAt: string;
  expiresAt: string;
  finishedAt?: string;
  exitCode?: number;
  durationMs?: number;
  note?: string;
  by?: string;
}

const enc = encodeURIComponent;

export const agentApi = {
  list: () => apiRequest<{ items: AgentEntry[] | null }>('GET', '/api/vault/agents'),
  put: (e: AgentEntry | { id: string; projects: [] }) => apiRequest<{ entry: AgentEntry | null }>('PUT', `/api/vault/agents/${enc(e.id)}`, e),
  requests: () => apiRequest<{ items: CredRequest[] | null }>('GET', '/api/vault/requests'),
  approve: (id: string, values: string[]) => apiRequest<CredRequest>('POST', `/api/vault/requests/${enc(id)}/approve`, { values }),
  deny: (id: string, note: string) => apiRequest<CredRequest>('POST', `/api/vault/requests/${enc(id)}/deny`, { note }),
  log: (limit = 100) => apiRequest<{ items: CredRequest[] | null }>('GET', `/api/vault/agent-log?limit=${limit}`),
};

/** Put this item's entry on the hub in line with its blob. */
export async function syncAgentEntry(it: VaultItem, known?: Set<string>) {
  const want = agentEntryFor(it, known);
  try {
    await agentApi.put(want ?? { id: it.id, projects: [] });
  } catch (err) {
    console.warn('vault: could not update what agents may ask for', errorMessage(err));
  }
}

/**
 * Bring the whole catalog in line with the open vault: entries the blobs do not back go, the rest
 * is rewritten where it differs. Items that would not open are left as they are.
 */
export async function reconcileAgents(items: VaultItem[], known?: Set<string>) {
  try {
    const hub = new Map((await agentApi.list()).items?.map((e) => [e.id, e]) ?? []);
    for (const it of items) {
      const want = agentEntryFor(it, known);
      const have = hub.get(it.id);
      if (want ? !sameEntry(have, want) : have) await agentApi.put(want ?? { id: it.id, projects: [] });
    }
  } catch (err) {
    console.warn('vault: could not check what agents may ask for', errorMessage(err));
  }
}

// ---- opening the approval dialog from elsewhere (a toast, a notification) ----

const openers = new Set<(id?: string) => void>();

export function onOpenCredentialRequest(fn: (id?: string) => void) {
  openers.add(fn);
  return () => {
    openers.delete(fn);
  };
}

export const openCredentialRequest = (id?: string) => openers.forEach((fn) => fn(id));
