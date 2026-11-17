import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ThreadMessage } from '../orch/types';
import type { ChatView, LeadChat } from './types';

// The hub is not asked here: its answers are made up, one at a time.
const api = vi.hoisted(() => ({ list: vi.fn(), leadChats: vi.fn() }));
vi.mock('./api', () => ({ chatsApi: api }));

const { chatsStore } = await import('./store');

const lc = (id: string, lastAt: string, messages: number): LeadChat => ({ id, threadId: 'p_shop', title: id, createdAt: '2026-11-17T08:00:00Z', lastAt, messages });
const msg = (id: string, chatId: string, at: string): ThreadMessage => ({ id, threadId: 'p_shop', chatId, at, role: 'user', kind: 'order', text: 'status' });
const lead = () => chatsStore.get().lead.p_shop;

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

beforeEach(() => {
  chatsStore.reset();
  api.list.mockReset();
  api.leadChats.mockReset();
});

describe('lead chats follow the thread’s messages', () => {
  const at = (s: number) => `2026-11-17T10:00:${String(s).padStart(2, '0')}.123456789+07:00`;

  it('counts a new message into its chat, which becomes the newest', () => {
    chatsStore.setLead('p_shop', { active: 'lc_a', items: [lc('lc_a', at(30), 4), lc('lc_b', at(10), 2)] });
    chatsStore.apply({ type: 'thread', threadId: 'p_shop', message: msg('m_1', 'lc_b', at(40)) });
    expect(lead().items.map((c) => [c.id, c.messages, c.lastAt])).toEqual([
      ['lc_b', 3, at(40)],
      ['lc_a', 4, at(30)],
    ]);
  });

  it('does not count a message twice, an update of one, or one the list counted already', () => {
    chatsStore.setLead('p_shop', { active: 'lc_a', items: [lc('lc_a', at(30), 4)] });
    chatsStore.apply({ type: 'thread', threadId: 'p_shop', message: msg('m_1', 'lc_a', at(40)) });
    chatsStore.apply({ type: 'thread', threadId: 'p_shop', message: { ...msg('m_1', 'lc_a', at(40)), text: 'edited' } });
    // the list came after the message: its time is the chat's last already
    chatsStore.apply({ type: 'thread', threadId: 'p_shop', message: msg('m_2', 'lc_a', at(40)) });
    // "Still thinking…" being replaced keeps its time
    chatsStore.apply({ type: 'thread', threadId: 'p_shop', message: msg('m_0', 'lc_a', at(20)) });
    expect(lead().items[0].messages).toBe(5);
  });

  it('tells nanoseconds apart within one millisecond', () => {
    chatsStore.setLead('p_shop', { active: 'lc_a', items: [lc('lc_a', '2026-11-17T10:00:00.123000100Z', 1)] });
    chatsStore.apply({ type: 'thread', threadId: 'p_shop', message: msg('m_1', 'lc_a', '2026-11-17T10:00:00.123000200Z') });
    expect(lead().items[0].messages).toBe(2);
  });

  it('asks for the list when a message names a chat it does not have', () => {
    api.leadChats.mockResolvedValue({ active: 'lc_a', items: [lc('lc_a', at(30), 1), lc('lc_new', at(50), 1)] });
    chatsStore.setLead('p_shop', { active: 'lc_a', items: [lc('lc_a', at(30), 1)] });
    chatsStore.apply({ type: 'thread', threadId: 'p_shop', message: msg('m_1', 'lc_new', at(50)) });
    expect(api.leadChats).toHaveBeenCalledTimes(1);
  });

  it('asks for the true counts on every leadChats event', () => {
    api.leadChats.mockResolvedValue({ active: 'lc_a', items: [lc('lc_a', at(45), 7)] });
    chatsStore.setLead('p_shop', { active: 'lc_a', items: [lc('lc_a', at(30), 1)] });
    chatsStore.apply({ type: 'leadChats', threadId: 'p_shop', active: 'lc_a' });
    expect(api.leadChats).toHaveBeenCalledTimes(1);
    return vi.waitFor(() => expect(lead().items[0].messages).toBe(7));
  });
});

describe('agent chats and a list on its way', () => {
  const view = (title: string, lastAt: string): ChatView => ({
    id: 'ch_1',
    projectId: 'p_shop',
    agent: 'claude',
    machineId: 'local',
    cwd: '/home/alice/code/shop-web',
    title,
    messages: 2,
    startedAt: '2026-11-17T08:00:00Z',
    lastAt,
    handle: 'Bintang',
    status: 'closed',
  });

  it('asks again when a chat event comes while the list loads, so the older list does not stay', async () => {
    const first = deferred<ChatView[]>();
    api.list.mockReturnValueOnce(first.promise).mockResolvedValueOnce([{ ...view('fix the login redirect', '2026-11-17T09:05:00Z'), status: 'live', sessionId: 's_1' }]);
    const loading = chatsStore.load('p_shop');
    // the console took the chat while the list was on its way
    chatsStore.apply({ type: 'chat', chat: { ...view('fix the login redirect', '2026-11-17T09:05:00Z'), status: 'live', sessionId: 's_1' } });
    first.resolve([view('fix the login redirect', '2026-11-17T09:00:00Z')]);
    await loading;
    await vi.waitFor(() => expect(chatsStore.get().projects.p_shop.items[0].status).toBe('live'));
    expect(api.list).toHaveBeenCalledTimes(2);
  });

  it('applies a chat event to a list shown, at once', () => {
    api.list.mockResolvedValue([]);
    return chatsStore.load('p_shop').then(() => {
      chatsStore.apply({ type: 'chat', chat: view('seeders for the demo shop', '2026-11-17T09:00:00Z') });
      expect(chatsStore.get().projects.p_shop.items.map((c) => c.title)).toEqual(['seeders for the demo shop']);
      expect(api.list).toHaveBeenCalledTimes(1);
    });
  });
});
