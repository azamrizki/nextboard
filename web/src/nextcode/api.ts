// NextCode's endpoints (docs/API-agents.md §30). Every route answers 409 not_nextcode for a
// terminal console.

import { apiRequest } from '../api';
import type { Answer, ChatPage, PermissionMode, Status } from './types';

const base = (id: string) => `/api/sessions/${encodeURIComponent(id)}/nextcode`;

/** The chat's socket path (JSON text frames, a snapshot first). */
export const chatSocketPath = (id: string) => `${base(id)}/ws`;

type Ok = { ok: boolean };

/**
 * PUT …/nextcode/model: "" is the CLI's own choice, a field left out stays as it is. account: the
 * login (pf_…) or API provider (ap_…) the model is on; another than the console's moves the chat
 * there (One NextCode), with the account's own model and effort for what is left out.
 */
export interface ModelChange {
  model?: string;
  effort?: string;
  account?: string;
}

export const nextcodeApi = {
  /** The latest items (300), or those before an item. */
  page: (id: string, before?: string, limit?: number) => {
    const q = new URLSearchParams();
    if (before) q.set('before', before);
    if (limit) q.set('limit', String(limit));
    const qs = q.toString();
    return apiRequest<ChatPage>('GET', `${base(id)}${qs ? `?${qs}` : ''}`);
  },
  /** 202: starts the CLI when none runs; while the agent works, the CLI queues it. */
  send: (id: string, text: string) => apiRequest<Ok>('POST', `${base(id)}/messages`, { text }),
  answer: (id: string, requestId: string, a: Answer) => apiRequest<Ok>('POST', `${base(id)}/requests/${encodeURIComponent(requestId)}`, a),
  interrupt: (id: string) => apiRequest<Ok>('POST', `${base(id)}/interrupt`, {}),
  setMode: (id: string, mode: PermissionMode) => apiRequest<{ status: Status }>('PUT', `${base(id)}/mode`, { mode }),
  /**
   * The model switches at once (the running CLI is told); the effort applies from the CLI's next
   * start, which the hub brings about by ending an idle CLI. 400 bad_model, 409 not_nextcode.
   */
  setModel: (id: string, c: ModelChange) => apiRequest<{ status: Status }>('PUT', `${base(id)}/model`, c),
};

// ---- NextCode's skills and memory (Clean NextCode): Settings › NextCode keeps them ----

export interface NcSkill {
  name: string;
  description: string;
  /** The SKILL.md on the hub, which the prompt points at. */
  path: string;
  body?: string;
  updatedAt: string;
}

export interface NcMemoryScope {
  /** "global" or a project's id. */
  scope: string;
  bytes: number;
  updatedAt: string;
}

const skillPath = (name: string) => `/api/nextcode/skills/${encodeURIComponent(name)}`;
const memoryPath = (scope: string) => `/api/nextcode/memory/${encodeURIComponent(scope)}`;

export const kitApi = {
  skills: () => apiRequest<{ items: NcSkill[]; dir: string }>('GET', '/api/nextcode/skills'),
  skill: (name: string) => apiRequest<NcSkill>('GET', skillPath(name)),
  putSkill: (name: string, description: string, body: string) => apiRequest<NcSkill>('PUT', skillPath(name), { description, body }),
  deleteSkill: (name: string) => apiRequest<Ok>('DELETE', skillPath(name)),
  memories: () => apiRequest<{ items: NcMemoryScope[]; dir: string }>('GET', '/api/nextcode/memory'),
  memory: (scope: string) => apiRequest<{ scope: string; text: string; path: string }>('GET', memoryPath(scope)),
  putMemory: (scope: string, text: string) => apiRequest<{ scope: string; text: string }>('PUT', memoryPath(scope), { text }),
};
