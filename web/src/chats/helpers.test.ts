import { describe, expect, it } from 'vitest';
import type { Project, Session } from '../lib/types';
import { ROOT_VALUE } from '../lib/placement';
import {
  changesLine,
  chatAction,
  clashOf,
  cleanHandle,
  dayGroup,
  filterChats,
  groupByDay,
  inChat,
  isResumeDivider,
  cleanPath,
  laterThan,
  nextSeen,
  refusedNow,
  sessionOfResult,
  swapStamp,
  unreadOf,
  placeChip,
  readHandleTaken,
  sortLeadChats,
  statusText,
  swapCandidates,
  swapCheck,
  swapReasonShort,
} from './helpers';
import type { ChatView, LeadChat } from './types';

// Made-up data, the same as the mockups: the Shop project with parts api (on a server), web and
// docs (on This PC).
const shop: Project = {
  id: 'p_shop',
  name: 'Shop',
  machineId: 'm_devbox',
  path: '/srv/shop/api',
  parts: [
    { id: 'pt_api', name: 'api', machineId: 'm_devbox', path: '/srv/shop/api' },
    { id: 'pt_web', name: 'web', machineId: 'local', path: '/home/alice/code/shop-web' },
    { id: 'pt_docs', name: 'docs', machineId: 'local', path: '/home/alice/code/shop-docs' },
  ],
  createdAt: '2026-11-01T00:00:00Z',
  lastOpenedAt: '2026-11-17T00:00:00Z',
  workspaces: [],
  sessionCount: 0,
  runningCount: 0,
};
const machines: Record<string, string> = { local: 'This PC', m_devbox: 'devbox' };
const machineName = (id: string) => machines[id] ?? '';

function chat(over: Partial<ChatView>): ChatView {
  return {
    id: 'ch_1',
    projectId: 'p_shop',
    agent: 'claude',
    agentSessionId: 'aaaa-1111',
    machineId: 'local',
    cwd: '/home/alice/code/shop-web',
    title: 'Build the sign-up form with validation',
    messages: 18,
    startedAt: '2026-11-14T09:00:00Z',
    lastAt: '2026-11-17T08:00:00Z',
    handle: 'Bintang',
    status: 'closed',
    ...over,
  };
}

function session(over: Partial<Session>): Session {
  return {
    id: 's_1',
    name: 'Claude Code',
    handle: 'Bintang',
    agent: 'claude',
    preset: 'claude',
    agentKind: 'agent',
    projectId: 'p_shop',
    machineId: 'local',
    cwd: '/home/alice/code/shop-web',
    status: 'running',
    agentState: 'ready',
    ...over,
  } as Session;
}

describe('grouping by day', () => {
  const now = new Date(2026, 10, 17, 15, 0);
  it('names the calendar day, not the hours since', () => {
    expect(dayGroup(new Date(2026, 10, 17, 0, 5).toISOString(), now)).toBe('Today');
    expect(dayGroup(new Date(2026, 10, 16, 23, 55).toISOString(), now)).toBe('Yesterday');
    expect(dayGroup(new Date(2026, 10, 13, 12, 0).toISOString(), now)).toBe('Earlier this week');
    expect(dayGroup(new Date(2026, 10, 2, 12, 0).toISOString(), now)).toBe('Earlier');
    expect(dayGroup('', now)).toBe('Earlier');
    // a clock a little ahead on the hub is still today
    expect(dayGroup(new Date(2026, 10, 17, 15, 1).toISOString(), now)).toBe('Today');
  });

  it('keeps the order given and starts a group where the day changes', () => {
    const at = [new Date(2026, 10, 17, 14).toISOString(), new Date(2026, 10, 17, 9).toISOString(), new Date(2026, 10, 16, 20).toISOString(), new Date(2026, 9, 1).toISOString()];
    const groups = groupByDay(at, (x) => x, now);
    expect(groups.map((g) => [g.title, g.items.length])).toEqual([
      ['Today', 2],
      ['Yesterday', 1],
      ['Earlier', 1],
    ]);
  });
});

describe('the place chip', () => {
  it('names a part with the machine its work is on, a server part’s too', () => {
    expect(placeChip(chat({ partId: 'pt_web' }), shop, machineName)).toMatchObject({ label: 'web · This PC', kind: 'part' });
    // the CLI of a server part runs on This PC; the chip names the server
    expect(placeChip(chat({ partId: 'pt_api', machineId: 'local', remoteMachineId: 'm_devbox', remoteCwd: '/srv/shop/api', cwd: '/data/agents/remote/m_devbox/ab12cd' }), shop, machineName)).toMatchObject({
      label: 'api · devbox',
      kind: 'part',
      title: '/srv/shop/api on devbox',
    });
  });

  it('calls a root chat "Root · all parts"', () => {
    expect(placeChip(chat({ root: true, cwd: '/data/roots/p_shop' }), shop, machineName)).toMatchObject({ label: 'Root · all parts', kind: 'root' });
  });

  it('follows a part’s new name, and says why an orphan cannot go back', () => {
    const renamed = { ...shop, parts: [{ ...shop.parts![1], name: 'frontend' }, shop.parts![0]] };
    expect(placeChip(chat({ partId: 'pt_web', partName: 'web' }), renamed, machineName).label).toBe('frontend · This PC');
    expect(placeChip(chat({ partId: 'pt_legacy', partName: 'legacy', status: 'orphan' }), shop, machineName)).toMatchObject({ label: 'legacy · part removed', kind: 'orphan' });
    expect(placeChip(chat({ partId: 'pt_web', status: 'orphan' }), shop, machineName)).toMatchObject({ label: 'web · folder missing', kind: 'orphan' });
  });

  it('shows only the machine for a console in no part', () => {
    expect(placeChip(chat({ partId: undefined }), shop, machineName)).toMatchObject({ label: 'This PC', kind: 'none' });
  });
});

describe('status and what a row offers', () => {
  it('live → Show, asleep → Wake, closed → Resume, orphan → transcript / continue elsewhere', () => {
    expect(chatAction(chat({ status: 'live', sessionId: 's_9' }))).toBe('show');
    expect(chatAction(chat({ status: 'asleep', sessionId: 's_9' }))).toBe('wake');
    expect(chatAction(chat({ status: 'closed' }))).toBe('resume');
    expect(chatAction(chat({ status: 'orphan' }))).toBe('orphan');
    // a holder the view lost: it can only be resumed
    expect(chatAction(chat({ status: 'live' }))).toBe('resume');
  });

  it('says which console holds it, by the handle it has now', () => {
    const sessions = { s_9: { handle: 'Arkan' } };
    expect(statusText(chat({ status: 'live', sessionId: 's_9', handle: 'Old' }), sessions)).toBe('live in @Arkan');
    expect(statusText(chat({ status: 'asleep', sessionId: 's_8', handle: 'Daffa' }), sessions)).toBe('asleep in @Daffa');
    expect(statusText(chat({ status: 'closed' }), sessions)).toBe('closed');
  });

  it('filters by place, agent and words, and leaves hidden chats out', () => {
    const list = [
      chat({ id: 'ch_a', partId: 'pt_web' }),
      chat({ id: 'ch_b', root: true, title: 'Wire checkout to the new payment API' }),
      chat({ id: 'ch_c', partId: 'pt_api', agent: 'codex', title: 'Write the orders table migration' }),
      chat({ id: 'ch_d', partId: 'pt_web', archived: true }),
    ];
    expect(filterChats(list, { place: 'pt_web' }).map((c) => c.id)).toEqual(['ch_a']);
    expect(filterChats(list, { place: ROOT_VALUE }).map((c) => c.id)).toEqual(['ch_b']);
    expect(filterChats(list, { agent: 'codex' }).map((c) => c.id)).toEqual(['ch_c']);
    expect(filterChats(list, { q: 'payment' }).map((c) => c.id)).toEqual(['ch_b']);
    // what the hub found in the transcripts counts too
    expect(filterChats(list, { q: 'zod schema', found: new Set(['ch_a']) }).map((c) => c.id)).toEqual(['ch_a']);
  });
});

describe('the old handle and swapping into a console', () => {
  const c = chat({});

  // the hub's chats.SwapReason (internal/chats/rules.go), rule for rule
  it('offers a swap only into a console of the same agent and login', () => {
    expect(swapCheck(c, session({}))).toBe('');
    expect(swapCheck(c, session({ agent: 'codex', preset: 'codex' }))).toBe('other_agent');
    expect(swapCheck(c, session({ profileId: 'pf_work' }))).toBe('other_agent');
    expect(swapCheck(chat({ profileId: 'pf_work' }), session({ profileId: 'pf_work' }))).toBe('');
    expect(swapCheck(c, session({ providerId: 'ap_router' }))).toBe('other_agent');
    expect(swapCheck(c, session({ signIn: true }))).toBe('other_agent');
    expect(swapCheck(c, session({ agentKind: 'shell' }))).toBe('other_agent');
  });

  it('… in the same project, machine and folder, and root or not', () => {
    expect(swapCheck(c, session({ cwd: '/home/alice/code/shop-docs' }))).toBe('other_folder');
    // the folder as path.Clean has it
    expect(swapCheck(c, session({ cwd: '/home/alice/code/shop-web/' }))).toBe('');
    expect(swapCheck(c, session({ cwd: '/home/alice/code//x/../shop-web' }))).toBe('');
    expect(swapCheck(c, session({ projectId: 'p_other' }))).toBe('other_folder');
    expect(swapCheck(c, session({ machineId: 'm_devbox' }))).toBe('other_folder');
    expect(swapCheck(c, session({ remoteMachineId: 'm_devbox' }))).toBe('other_folder');
    expect(swapCheck(c, session({ root: true }))).toBe('other_folder');
  });

  it('… idle at its prompt or its shell, or with its agent ended; anything else is busy', () => {
    expect(swapCheck(c, session({ agentState: 'shell' }))).toBe('');
    expect(swapCheck(c, session({ agentState: 'exited' }))).toBe('');
    expect(swapCheck(c, session({ agentState: 'working' }))).toBe('busy');
    expect(swapCheck(c, session({ agentState: 'needsYou' }))).toBe('busy');
    expect(swapCheck(c, session({ agentState: 'starting' }))).toBe('busy');
    expect(swapCheck(c, session({ status: 'asleep' }))).toBe('busy');
    expect(swapCheck(c, session({ status: 'exited' }))).toBe('busy');
    expect(swapCheck(c, session({ paused: true }))).toBe('busy');
    expect(swapCheck(c, session({ queue: 1 }))).toBe('busy');
    expect(swapCheck(c, session({ manualAgent: true }))).toBe('busy');
    // no such console here
    expect(swapCheck(c, undefined)).toBe('gone');
    expect(swapReasonShort('other_folder')).toBe('in another folder');
  });

  it('forgets the hub’s refusal once that console changed', () => {
    const busy = session({ id: 's_2', agentState: 'working' });
    const refusal = { reason: 'busy', stamp: swapStamp(busy) };
    expect(refusedNow(refusal, busy)).toBe('busy');
    expect(refusedNow(refusal, { ...busy, agentState: 'ready' })).toBe('');
    expect(refusedNow(undefined, busy)).toBe('');
  });

  it('lists the consoles of the chat’s folder, the idle ones first', () => {
    const sessions = {
      s_1: session({ id: 's_1', handle: 'Cantika', agentState: 'working' }),
      s_2: session({ id: 's_2', handle: 'Bintang' }),
      s_3: session({ id: 's_3', handle: 'Dewi', cwd: '/elsewhere' }),
      s_4: session({ id: 's_4', handle: 'Eka', panel: true }),
    };
    expect(swapCandidates(c, sessions).map((x) => [x.session.handle, x.reason])).toEqual([
      ['Bintang', ''],
      ['Cantika', 'busy'],
    ]);
  });

  it('knows the clash from the hub’s answer, with the next free name', () => {
    const hub = readHandleTaken({ code: 'handle_taken', taken: 'Bintang', by: 's_2', byChat: 'Payment webhook signature check', next: 'Cantika' });
    expect(hub).toEqual({ taken: 'Bintang', by: 's_2', byHandle: undefined, byChat: 'Payment webhook signature check', next: 'Cantika', swappable: undefined, reason: undefined });
    const sessions = { s_2: session({ id: 's_2', handle: 'Bintang', agentState: 'working' }) };
    expect(clashOf(c, sessions, hub)).toEqual({ taken: 'Bintang', by: 's_2', byChat: 'Payment webhook signature check', next: 'Cantika', reason: 'busy' });
    // the console as it is now wins over the hub's answer of a moment ago: it went idle
    expect(clashOf(c, { s_2: session({ id: 's_2', handle: 'Bintang' }) }, { ...hub!, swappable: false, reason: 'busy' })?.reason).toBe('');
    expect(clashOf(c, sessions, { ...hub!, swappable: true })?.reason).toBe('busy');
    // a console unknown here: the hub's word
    expect(clashOf(c, {}, { ...hub!, swappable: false, reason: 'other_folder' })?.reason).toBe('other_folder');
    expect(clashOf(c, {}, { ...hub!, swappable: true })?.reason).toBe('');
    expect(readHandleTaken({ code: 'handle_taken' })).toBeNull();
  });

  it('sees the clash from the consoles it knows before the hub says the next name', () => {
    expect(clashOf(c, { s_2: session({ id: 's_2', handle: 'Bintang' }) }, null)).toEqual({ taken: 'Bintang', by: 's_2', byChat: '', next: '', reason: '' });
    // the console holding the chat itself is no clash
    expect(clashOf(chat({ sessionId: 's_2' }), { s_2: session({ id: 's_2', handle: 'Bintang' }) }, null)).toBeNull();
    expect(clashOf(c, { s_5: session({ id: 's_5', handle: 'Fajar' }) }, null)).toBeNull();
  });

  it('takes a typed handle with or without the @', () => {
    expect(cleanHandle(' @Cantika ')).toBe('Cantika');
    expect(cleanHandle('Cantika')).toBe('Cantika');
  });
});

describe('small readings of the hub', () => {
  it('cleans a folder as Go’s path.Clean does', () => {
    expect(cleanPath('/home/alice/code/')).toBe('/home/alice/code');
    expect(cleanPath('/a//b/./c/..')).toBe('/a/b');
    expect(cleanPath('/..')).toBe('/');
    expect(cleanPath('')).toBe('.');
    expect(cleanPath('a/../..')).toBe('..');
  });

  it('finds the console in a resume’s answer, flat or nested', () => {
    expect(sessionOfResult({ id: 's_9', handle: 'Cantika', chatId: 'ch_1' })?.id).toBe('s_9');
    expect(sessionOfResult({ session: { id: 's_8' } })?.id).toBe('s_8');
    expect(sessionOfResult({ ok: true })).toBeNull();
    expect(sessionOfResult(null)).toBeNull();
  });

  it('orders the hub’s times to the nanosecond', () => {
    expect(laterThan('2026-11-17T10:00:01Z', '2026-11-17T10:00:00Z')).toBe(true);
    expect(laterThan('2026-11-17T10:00:00.123000200Z', '2026-11-17T10:00:00.1230001Z')).toBe(true);
    expect(laterThan('2026-11-17T10:00:00.1230001Z', '2026-11-17T10:00:00.123000100Z')).toBe(false);
    expect(laterThan('2026-11-17T17:00:00+07:00', '2026-11-17T09:59:59Z')).toBe(true);
    expect(laterThan('2026-11-17T10:00:00Z', '')).toBe(true);
    expect(laterThan('', '2026-11-17T10:00:00Z')).toBe(false);
  });
});

describe('the closed thread’s unread badge', () => {
  it('counts what came in the chat in use since it was read', () => {
    let seen = nextSeen(null, { open: false, ready: true, chat: 'lc_a', count: 4 });
    expect(unreadOf(seen, false, 'lc_a', 4)).toBe(0);
    expect(unreadOf(seen, false, 'lc_a', 6)).toBe(2);
    // opening reads it
    seen = nextSeen(seen, { open: true, ready: true, chat: 'lc_a', count: 6 });
    expect(unreadOf(seen, false, 'lc_a', 6)).toBe(0);
  });

  it('starts again at the new chat once its messages are in, not with the old count', () => {
    const seen = nextSeen(null, { open: false, ready: true, chat: 'lc_a', count: 30 });
    // another chat in use, its messages still loading: nothing counts yet
    let now = nextSeen(seen, { open: false, ready: false, chat: 'lc_b', count: 0 });
    expect(unreadOf(now, false, 'lc_b', 0)).toBe(0);
    now = nextSeen(now, { open: false, ready: true, chat: 'lc_b', count: 3 });
    expect(unreadOf(now, false, 'lc_b', 3)).toBe(0);
    expect(unreadOf(now, false, 'lc_b', 4)).toBe(1);
    // the same mark when nothing changed (no render for nothing)
    expect(nextSeen(now, { open: false, ready: true, chat: 'lc_b', count: 4 })).toBe(now);
  });
});

describe('what changed since', () => {
  it('says it in one line, the hub’s own line first', () => {
    expect(changesLine({ summary: '6 new commits in web, 2 memory facts' })).toBe('6 new commits in web, 2 memory facts');
    expect(changesLine({ commits: [{ part: 'web', subjects: ['a', 'b'] }, { part: 'api', subjects: [] }], facts: ['POST /api/signup exists'] })).toBe('2 new commits in web, 1 new memory fact');
    expect(changesLine({ commits: [], facts: [] })).toBe('');
    expect(changesLine(null)).toBe('');
  });
});

describe('lead chats', () => {
  const lc = (id: string, lastAt: string): LeadChat => ({ id, threadId: 'p_shop', title: id, createdAt: lastAt, lastAt, messages: 3 });

  it('puts the current chat first, then the newest', () => {
    const list = [lc('lc_old', '2026-11-10T00:00:00Z'), lc('lc_new', '2026-11-16T00:00:00Z'), lc('lc_cur', '2026-11-01T00:00:00Z')];
    expect(sortLeadChats(list, 'lc_cur').map((c) => c.id)).toEqual(['lc_cur', 'lc_new', 'lc_old']);
  });

  it('shows the chat in use, and messages from before chats', () => {
    expect(inChat({ chatId: 'lc_a' }, 'lc_a')).toBe(true);
    expect(inChat({ chatId: 'lc_b' }, 'lc_a')).toBe(false);
    expect(inChat({}, 'lc_a')).toBe(true);
    expect(inChat({ chatId: 'lc_b' }, '')).toBe(true);
  });

  it('draws the hub’s resume note as a divider', () => {
    expect(isResumeDivider({ kind: 'note', role: 'hub', source: 'resume', text: 'Resumed — Aruna has all 12 messages in full.' })).toBe(true);
    expect(isResumeDivider({ kind: 'note', role: 'hub', text: 'Resumed — Aruna has all 12 messages in full.' })).toBe(true);
    expect(isResumeDivider({ kind: 'note', role: 'hub', source: 'api', text: 'Resumed @Arkan' })).toBe(false);
    expect(isResumeDivider({ kind: 'reply', role: 'hub', text: 'Resumed — sure.' })).toBe(false);
  });
});
