// REST calls of the memory page (docs/API-orchestration.md §12).

import { apiRequest } from '../api';
import type { Job, PushResult } from '../orch/types';
import type { GalaxyView, InitResult, MemoryDocument, MemoryHit } from './types';

const enc = encodeURIComponent;
const owned = (owner: string) => (owner ? `?projectId=${enc(owner)}` : '');

export const memoryApi = {
  galaxy: () => apiRequest<GalaxyView>('GET', '/api/memory/galaxy'),
  search: (q: string, owner: string, signal?: AbortSignal) =>
    apiRequest<{ hits: MemoryHit[] | null }>('GET', `/api/memory/search?q=${enc(q)}${owner ? `&projectId=${enc(owner)}` : ''}`, undefined, { signal }).then((r) => r.hits ?? []),
  doc: (owner: string, name: string) => apiRequest<MemoryDocument>('GET', `/api/memory/docs/${enc(name)}${owned(owner)}`),
  save: (owner: string, name: string, body: { title?: string; description?: string; content: string; ifMtime?: string }) =>
    apiRequest<MemoryDocument>('PUT', `/api/memory/docs/${enc(name)}${owned(owner)}`, body),
  remove: (owner: string, name: string) => apiRequest<{ ok: boolean }>('DELETE', `/api/memory/docs/${enc(name)}${owned(owner)}`),
  init: (projectId: string, body: { effort: 'quick' | 'balanced' | 'thorough'; agent?: string }) =>
    apiRequest<Job>('POST', `/api/projects/${enc(projectId)}/memory/init`, body),
  applyInit: (projectId: string, body: InitResult) => apiRequest<GalaxyView>('POST', `/api/projects/${enc(projectId)}/memory/init/apply`, body),
  blocked: (owner: string) => apiRequest<{ commands: string[]; inherited?: string[] }>('GET', `/api/memory/blocked${owned(owner)}`),
  saveBlocked: (owner: string, commands: string[]) => apiRequest<{ commands: string[] }>('PUT', `/api/memory/blocked${owned(owner)}`, { commands }),
  pushCritical: (projectId: string) => apiRequest<PushResult>('POST', `/api/projects/${enc(projectId)}/memory/push`, { scopes: ['critical'] }),
};
