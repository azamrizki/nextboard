// Phase 6 endpoints (docs/API-orchestration.md). Every call goes through `transport`,
// which is the hub by default and an in-browser mock when the dev flag is on
// (localStorage nb.orchMock = "1" or ?orchmock=1), so the UI can be built and tested
// before a hub implements the contract. The mock is a lazy chunk: never loaded otherwise.

import { apiRequest, type HttpMethod } from '../api';
import type { FsStat, Session } from '../lib/types';
import type {
  MessageOutput,
  CommandRequest,
  CommandResponse,
  Confirmation,
  Delivery,
  DeployRequest,
  Fact,
  Forward,
  Handoff,
  HandoffRequest,
  Job,
  Layout,
  LoopDef,
  LoopHistoryEntry,
  LoopInput,
  LoopTemplate,
  MemoryBoth,
  MemoryDoc,
  Mention,
  Mission,
  Order,
  PlannerStatus,
  MemoryBlock,
  MemoryPlan,
  PushResult,
  SpawnRequest,
  SpawnResponse,
  ThreadMessage,
} from './types';

export type Transport = <T>(method: HttpMethod, path: string, body?: unknown) => Promise<T>;

const hub: Transport = (method, path, body) => apiRequest(method, path, method === 'GET' || method === 'DELETE' ? undefined : (body ?? {}));

let transport: Transport = hub;
let ready: Promise<void> = Promise.resolve();

export function mockRequested(): boolean {
  try {
    if (new URLSearchParams(window.location.search).get('orchmock') === '1') {
      window.localStorage.setItem('nb.orchMock', '1');
      return true;
    }
    return window.localStorage.getItem('nb.orchMock') === '1';
  } catch {
    return false;
  }
}

let mockOn = false;
export const isMock = () => mockOn;

/** Load and install the mock backend (dev flag). Idempotent. */
export function installMock(): Promise<void> {
  if (mockOn) return ready;
  mockOn = true;
  ready = import('./mock/backend').then((m) => {
    transport = m.createMockTransport(hub);
  });
  return ready;
}

async function call<T>(method: HttpMethod, path: string, body?: unknown): Promise<T> {
  await ready;
  return transport<T>(method, path, body);
}

const enc = encodeURIComponent;
type Ok = { ok: boolean };
const items = <T>(r: { items?: T[] | null } | T[] | null | undefined): T[] => (Array.isArray(r) ? r : (r?.items ?? []));

/** What to send to whom (docs/API-orchestration.md §12). */
export interface MemorySendBody {
  memoryNodeId?: string;
  workspaceId?: string;
  targets?: string[];
  blocks?: MemoryBlock[];
  force?: boolean;
}

export const orchApi = {
  // §2 delivery
  deliver: (sessionId: string, body: { text: string; submit?: boolean; source?: string; guard?: string; deadlineSec?: number }) =>
    call<Delivery>('POST', `/api/sessions/${enc(sessionId)}/deliveries`, body),
  deliveries: (sessionId: string) => call<{ items: Delivery[] | null }>('GET', `/api/sessions/${enc(sessionId)}/deliveries`).then(items),
  projectDeliveries: (projectId: string) => call<{ items: Delivery[] | null }>('GET', `/api/deliveries?projectId=${enc(projectId)}&active=1`).then(items),
  cancelDelivery: (id: string) => call<Ok>('DELETE', `/api/deliveries/${enc(id)}`),

  // §3 commands
  command: (projectId: string, body: CommandRequest) => call<CommandResponse>('POST', `/api/projects/${enc(projectId)}/commands`, body),
  mentions: (projectId: string, q: { workspaceId: string; cardId?: string; q: string }) =>
    call<Mention[] | { items: Mention[] | null } | null>(
      'GET',
      `/api/projects/${enc(projectId)}/mentions?workspaceId=${enc(q.workspaceId)}${q.cardId ? `&cardId=${enc(q.cardId)}` : ''}&q=${enc(q.q)}`,
    ).then(items),
  planner: () => call<PlannerStatus>('GET', '/api/planner'),
  decide: (id: string, decision: 'confirm' | 'cancel', choice?: string) =>
    call<Confirmation>('POST', `/api/confirmations/${enc(id)}`, choice ? { decision, choice } : { decision }),

  // §4 threads & orders
  messageOutput: (threadId: string, messageId: string) => call<MessageOutput>('GET', `/api/threads/${enc(threadId)}/messages/${enc(messageId)}/output`),
  saveMessageOutput: (threadId: string, messageId: string) => call<FsStat>('POST', `/api/threads/${enc(threadId)}/messages/${enc(messageId)}/output/save`, {}),
  messages: (threadId: string, before?: string, limit = 50) =>
    call<{ items: ThreadMessage[] | null; hasMore: boolean; confirmations?: Confirmation[]; planning?: string }>(
      'GET',
      `/api/threads/${enc(threadId)}/messages?limit=${limit}${before ? `&before=${enc(before)}` : ''}`,
    ),
  clearThread: (threadId: string) => call<Ok>('DELETE', `/api/threads/${enc(threadId)}`),
  orders: (threadId: string) => call<{ items: Order[] | null }>('GET', `/api/threads/${enc(threadId)}/orders?active=1`).then(items),
  order: (id: string) => call<Order>('GET', `/api/orders/${enc(id)}`),
  cancelOrder: (id: string) => call<Order>('POST', `/api/orders/${enc(id)}/cancel`),
  resendOrder: (id: string) => call<Order>('POST', `/api/orders/${enc(id)}/resend`),

  // §5 spawn
  spawn: (workspaceId: string, body: SpawnRequest) => call<SpawnResponse>('POST', `/api/workspaces/${enc(workspaceId)}/spawn`, body),

  // §7 mission
  mission: (workspaceId: string, scope?: 'project') =>
    call<Mission>('GET', `/api/workspaces/${enc(workspaceId)}/mission${scope ? `?scope=${scope}` : ''}`),

  // §8 pause / resume
  pause: (sessionId: string) => call<Session>('POST', `/api/sessions/${enc(sessionId)}/pause`),
  resume: (sessionId: string) => call<Session>('POST', `/api/sessions/${enc(sessionId)}/resume`),
  pauseAll: (workspaceId: string) => call<{ sessions: string[] | null }>('POST', `/api/workspaces/${enc(workspaceId)}/pause-all`),
  resumeAll: (workspaceId: string) => call<{ sessions: string[] | null }>('POST', `/api/workspaces/${enc(workspaceId)}/resume-all`),

  // §9 handoff, §10 forwards
  handoff: (sessionId: string, body: HandoffRequest) => call<Handoff>('POST', `/api/sessions/${enc(sessionId)}/handoff`, body),
  forwards: (projectId: string) => call<{ items: Forward[] | null }>('GET', `/api/projects/${enc(projectId)}/forwards`).then(items),
  cancelForward: (id: string) => call<Ok>('DELETE', `/api/forwards/${enc(id)}`),

  // §11 loops
  loops: (projectId: string) => call<LoopDef[] | { items: LoopDef[] | null } | null>('GET', `/api/projects/${enc(projectId)}/loops`).then(items),
  createLoop: (projectId: string, body: LoopInput) => call<LoopDef>('POST', `/api/projects/${enc(projectId)}/loops`, body),
  updateLoop: (id: string, body: Partial<LoopInput>) => call<LoopDef>('PATCH', `/api/loops/${enc(id)}`, body),
  deleteLoop: (id: string) => call<Ok>('DELETE', `/api/loops/${enc(id)}`),
  startLoop: (id: string) => call<LoopDef>('POST', `/api/loops/${enc(id)}/start`),
  stopLoop: (id: string) => call<LoopDef>('POST', `/api/loops/${enc(id)}/stop`),
  runLoopNow: (id: string) => call<LoopDef>('POST', `/api/loops/${enc(id)}/run-now`),
  stopAllLoops: () => call<{ stopped: number }>('POST', '/api/loops/stop-all'),
  loopHistory: (id: string, limit = 50) => call<{ items: LoopHistoryEntry[] | null }>('GET', `/api/loops/${enc(id)}/history?limit=${limit}`).then(items),
  loopTemplates: () => call<LoopTemplate[] | { items: LoopTemplate[] | null } | null>('GET', '/api/loops/templates').then(items),

  // §12 memory
  memory: (projectId: string) => call<MemoryBoth>('GET', `/api/projects/${enc(projectId)}/memory`),
  saveMemory: (projectId: string, body: { scope: 'project' | 'global'; content: string; ifMtime?: string }) =>
    call<MemoryDoc>('PUT', `/api/projects/${enc(projectId)}/memory`, body),
  scanMemory: (projectId: string, body: { effort: 'quick' | 'balanced' | 'thorough'; agent?: string }) =>
    call<Job>('POST', `/api/projects/${enc(projectId)}/memory/scan`, body),
  acceptFacts: (projectId: string, body: { scope: 'project' | 'global'; facts: Fact[] }) =>
    call<MemoryDoc>('POST', `/api/projects/${enc(projectId)}/memory/accept`, body),
  refreshFact: (projectId: string, fact: string) => call<Job>('POST', `/api/projects/${enc(projectId)}/memory/refresh`, { fact }),
  pushMemory: (projectId: string, body: MemorySendBody) => call<PushResult>('POST', `/api/projects/${enc(projectId)}/memory/push`, body),
  memoryPlan: (projectId: string, body: MemorySendBody) => call<MemoryPlan>('POST', `/api/projects/${enc(projectId)}/memory/plan`, body),
  pushTile: (workspaceId: string, nodeId: string, targets?: string[]) =>
    call<PushResult & { empty: boolean }>('POST', `/api/workspaces/${enc(workspaceId)}/tiles/${enc(nodeId)}/push`, { targets: targets ?? [] }),
  job: (id: string) => call<Job>('GET', `/api/jobs/${enc(id)}`),
  cancelJob: (id: string) => call<Job>('POST', `/api/jobs/${enc(id)}/cancel`),

  // §13 layouts
  layouts: () => call<Layout[] | { items: Layout[] | null } | null>('GET', '/api/layouts').then(items),
  saveLayout: (body: { name?: string; workspaceId: string; nodeIds?: string[] }) => call<Layout>('POST', '/api/layouts', body),
  renameLayout: (id: string, name: string) => call<Layout>('PATCH', `/api/layouts/${enc(id)}`, { name }),
  deleteLayout: (id: string) => call<Ok>('DELETE', `/api/layouts/${enc(id)}`),
  /** What a layout looks like with these group sizes: the hub does the geometry. Deploys nothing. */
  sizedLayout: (id: string, groupSizes: Record<string, number>) => call<Layout>('POST', `/api/layouts/${enc(id)}/sized`, { groupSizes }),
  deploy: (workspaceId: string, body: DeployRequest) =>
    call<{ sessions: Session[] | null; nodeIds: string[] | null }>('POST', `/api/workspaces/${enc(workspaceId)}/deploy`, body),
};
