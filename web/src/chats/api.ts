// Chats REST (docs/API-agents.md agent chats, docs/API-orchestration.md lead chats).

import { apiRequest } from '../api';
import type { Session } from '../lib/types';
import type { ChatDetail, ChatTranscript, ChatView, ContinueBody, LeadChat, LeadChatList, ResumeBody } from './types';

const enc = encodeURIComponent;

export interface ChatQuery {
  agent?: string;
  /** "root" or a part id. */
  place?: string;
  q?: string;
  status?: string;
  limit?: number;
}

function query(q: ChatQuery): string {
  const p = new URLSearchParams();
  if (q.agent) p.set('agent', q.agent);
  if (q.place) p.set('place', q.place);
  if (q.q) p.set('q', q.q);
  if (q.status) p.set('status', q.status);
  if (q.limit) p.set('limit', String(q.limit));
  const s = p.toString();
  return s ? `?${s}` : '';
}

export const chatsApi = {
  list: (projectId: string, q: ChatQuery = {}, signal?: AbortSignal) =>
    apiRequest<{ items: ChatView[] | null }>('GET', `/api/projects/${enc(projectId)}/chats${query(q)}`, undefined, { signal }).then((r) => r?.items ?? []),
  get: (id: string, changes = false, signal?: AbortSignal) => apiRequest<ChatDetail>('GET', `/api/chats/${enc(id)}${changes ? '?changes=1' : ''}`, undefined, { signal }),
  transcript: (id: string, limit = 200) => apiRequest<ChatTranscript>('GET', `/api/chats/${enc(id)}/transcript?limit=${limit}&last=${limit}`),
  update: (id: string, body: { title?: string; archived?: boolean }) => apiRequest<ChatView>('PATCH', `/api/chats/${enc(id)}`, body),
  /** A new console at the chat's place (or a swap into `into`); the console that holds it now. */
  resume: (id: string, body: ResumeBody) => apiRequest<Session & { chatId?: string; missionId?: string }>('POST', `/api/chats/${enc(id)}/resume`, body),
  continueElsewhere: (id: string, body: ContinueBody) => apiRequest<Partial<Session> & Record<string, unknown>>('POST', `/api/chats/${enc(id)}/continue`, body),

  // the lead's chats: segments of a thread (tid = project id, or an orchestrator card's node id)
  leadChats: (threadId: string) => apiRequest<LeadChatList>('GET', `/api/threads/${enc(threadId)}/chats`),
  newLeadChat: (threadId: string) => apiRequest<LeadChat | LeadChatList>('POST', `/api/threads/${enc(threadId)}/chats`, {}),
  activateLeadChat: (threadId: string, chatId: string) => apiRequest<unknown>('POST', `/api/threads/${enc(threadId)}/chats/${enc(chatId)}/activate`, {}),
  renameLeadChat: (threadId: string, chatId: string, title: string) => apiRequest<LeadChat>('PATCH', `/api/threads/${enc(threadId)}/chats/${enc(chatId)}`, { title }),
  deleteLeadChat: (threadId: string, chatId: string) => apiRequest<unknown>('DELETE', `/api/threads/${enc(threadId)}/chats/${enc(chatId)}`),
};
