// DEV MOCK ONLY — loaded when localStorage nb.orchMock = "1" (or ?orchmock=1).
// Serves the Phase 6 endpoints (docs/API-orchestration.md) inside the browser on top of a
// Phase 5 hub, so the web UI can be built and tested before the hub implements them.
// Text reaches agents through the old manual-input endpoint (one line + Enter) only when
// the agent is READY; everything else (threads, loops, memory, layouts) lives here and in
// localStorage. Not a reference implementation: the hub is the source of truth.

import type { HttpMethod } from '../../api';
import { ApiError } from '../../api';
import { readGroup } from '../../board/kinds';
import { frameOf, directedTerminals } from '../../board/groups';
import type { Board, BoardEdge, BoardNode, HubEvent, Op, Session } from '../../lib/types';
import { store } from '../../store';
import type { Transport } from '../api';
import type {
  CommandRequest,
  Confirmation,
  Delivery,
  DeployRequest,
  Fact,
  Forward,
  Handoff,
  Job,
  Layout,
  LoopDef,
  LoopHistoryEntry,
  LoopInput,
  Order,
  SpawnRequest,
  SpawnSlot,
  ThreadMessage,
  UiAction,
} from '../types';
import { editDistance, parse } from './parser';

const MOCK_CLIENT = 'c_mockhubops0';
const rnd = () => Math.random().toString(36).slice(2, 12).padEnd(10, '0');
const mk = (p: string) => `${p}_${rnd()}`;
const iso = (t = Date.now()) => new Date(t).toISOString();
const sleep = (ms: number) => new Promise((r) => window.setTimeout(r, ms));
const norm = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, '');
const fail = (status: number, msg: string, code = ''): never => {
  throw new ApiError(status, msg, code);
};

function load<T>(key: string, d: T): T {
  try {
    const v = window.localStorage.getItem(`nb.mock.${key}`);
    return v ? (JSON.parse(v) as T) : d;
  } catch {
    return d;
  }
}
function save(key: string, v: unknown) {
  try {
    window.localStorage.setItem(`nb.mock.${key}`, JSON.stringify(v));
  } catch {
    /* ignore */
  }
}

export function createMockTransport(hub: Transport): Transport {
  // ---- state ----
  const threads: Record<string, ThreadMessage[]> = load('threads', {});
  const loops: Record<string, LoopDef> = load('loops', {});
  const loopHist: Record<string, LoopHistoryEntry[]> = load('loopHist', {});
  const memory: Record<string, { content: string; mtime: string }> = load('memory', {});
  const saved: Layout[] = load('layouts', []);
  const orders = new Map<string, Order>();
  const confs = new Map<string, { c: Confirmation; run: (choice?: string) => Promise<void> }>();
  const deliveries = new Map<string, Delivery>();
  const queues = new Map<string, string[]>();
  const paused = new Map<string, string>();
  const jobs = new Map<string, Job>();
  const handoffs = new Map<string, Handoff>();
  const forwards = new Map<string, Forward & { done: Set<string> }>();
  const lastOrder = new Map<string, string>();
  const lastGoal = new Map<string, number>();
  const lastIdleFire = new Map<string, number>();
  const forwardFired = new Map<string, number>();
  let persistTimer = 0;
  const persist = () => {
    window.clearTimeout(persistTimer);
    persistTimer = window.setTimeout(() => {
      for (const k of Object.keys(threads)) threads[k] = threads[k].slice(-200);
      save('threads', threads);
      save('loops', loops);
      save('loopHist', loopHist);
      save('memory', memory);
      save('layouts', saved);
    }, 300);
  };

  const emit = (ev: unknown) => store.apply(ev as HubEvent);
  const sessions = () => store.getState().sessions;

  // ---- session augmentation (paused / queue) ----
  const activeQueue = (sid: string) => (queues.get(sid) ?? []).filter((id) => ['queued', 'waiting', 'typing'].includes(deliveries.get(id)?.state ?? '')).length;
  const augment = (s: Session): Session => ({ ...s, paused: paused.has(s.id), pausedAt: paused.get(s.id) ?? null, queue: activeQueue(s.id) });
  const origUpsert = store.upsertSession.bind(store);
  store.upsertSession = (s: Session) => origUpsert(augment(s));
  const origApply = store.apply.bind(store);
  store.apply = (ev: HubEvent) => {
    if (ev.type === 'snapshot') ev = { ...ev, sessions: (ev.sessions ?? []).map(augment) };
    origApply(ev);
  };
  const refresh = (sid: string) => {
    const s = sessions()[sid];
    if (s) origUpsert(augment(s));
  };

  // ---- threads ----
  const msg = (threadId: string, m: Partial<ThreadMessage> & { kind: string; text: string }): ThreadMessage => {
    const full: ThreadMessage = { id: mk('tm'), threadId, at: iso(), role: 'hub', ...m };
    (threads[threadId] ??= []).push(full);
    persist();
    emit({ type: 'thread', threadId, message: full });
    return full;
  };
  const updateOrder = (o: Order) => {
    orders.set(o.id, o);
    emit({ type: 'order', order: o });
  };

  // ---- deliveries ----
  const deliver = (sid: string, text: string, source: string, extra: Partial<Delivery> = {}): Delivery => {
    const s = sessions()[sid];
    if (!s) fail(404, 'Session not found.', 'session_gone');
    if (s!.agentKind === 'shell' || s!.agent === 'shell') fail(409, `@${s!.handle} is a plain shell; orders only go to agents.`, 'not_agent');
    if (text.length > 32000) fail(413, 'Text too long.', 'text_too_long');
    if (activeQueue(sid) >= 20) fail(429, 'Queue full.', 'queue_full');
    const d: Delivery = {
      id: mk('dl'),
      sessionId: sid,
      handle: s!.handle ?? '',
      text,
      submit: true,
      source,
      guard: 'agent',
      forwardDepth: 0,
      state: 'queued',
      waitReason: '',
      createdAt: iso(),
      deadline: iso(Date.now() + (extra.deadline ? Number(extra.deadline) : 1800) * 1000),
      sentAt: null,
      confirmedAt: null,
      error: '',
      code: '',
      ...extra,
    };
    if (extra.deadline) d.deadline = iso(Date.now() + Number(extra.deadline) * 1000);
    deliveries.set(d.id, d);
    queues.set(sid, [...(queues.get(sid) ?? []), d.id]);
    emit({ type: 'delivery', delivery: d });
    refresh(sid);
    return d;
  };
  const setDelivery = (d: Delivery, patch: Partial<Delivery>) => {
    const n = { ...d, ...patch };
    deliveries.set(d.id, n);
    emit({ type: 'delivery', delivery: n });
    refresh(d.sessionId);
    if (n.orderId) {
      const o = orders.get(n.orderId);
      if (o) {
        const st = n.state === 'failed' || n.state === 'expired' ? 'failed' : n.state === 'cancelled' ? 'cancelled' : n.state === 'sent' || n.state === 'confirmed' ? 'delivered' : 'queued';
        const targets = (o.targets ?? []).map((t) => (t.deliveryId === n.id && !['working', 'finished', 'needsYou'].includes(t.state) ? { ...t, state: st, error: n.error } : t));
        updateOrder({ ...o, targets });
      }
    }
    return n;
  };
  let typing = false;
  const tick = async () => {
    if (typing) return;
    for (const [sid, q] of queues) {
      while (q.length && !['queued', 'waiting', 'typing', 'sent'].includes(deliveries.get(q[0])?.state ?? '')) q.shift();
      const id = q[0];
      if (!id) continue;
      let d = deliveries.get(id)!;
      const s = sessions()[sid];
      if (d.state === 'sent') {
        if (Date.now() - Date.parse(d.sentAt ?? iso()) > 8000) q.shift();
        continue;
      }
      if (!s || s.status === 'exited' || s.status === 'error') {
        setDelivery(d, { state: 'failed', error: 'The console is gone.', code: 'session_gone' });
        continue;
      }
      if (s.agentState === 'shell' || s.foreground === 'shell') {
        setDelivery(d, { state: 'failed', error: 'The agent is not in the foreground (never typed into a shell).', code: 'not_agent' });
        continue;
      }
      if (Date.parse(d.deadline ?? iso()) < Date.now()) {
        setDelivery(d, { state: 'expired', error: 'Deadline passed.' });
        continue;
      }
      const reason = paused.has(sid) ? 'paused' : s.agentState === 'ready' ? '' : (s.agentState ?? 'starting');
      if (reason) {
        if (d.state !== 'waiting' || d.waitReason !== reason) setDelivery(d, { state: 'waiting', waitReason: reason });
        continue;
      }
      typing = true;
      d = setDelivery(d, { state: 'typing', waitReason: '' });
      try {
        await hub('POST', `/api/sessions/${sid}/input`, { data: d.text.replace(/\r?\n/g, ' ') });
        await sleep(200);
        await hub('POST', `/api/sessions/${sid}/input`, { data: '\r' });
        setDelivery(d, { state: 'sent', sentAt: iso() });
      } catch (err) {
        setDelivery(d, { state: 'failed', error: err instanceof Error ? err.message : String(err) });
      } finally {
        typing = false;
      }
    }
  };
  window.setInterval(() => void tick(), 400);

  // ---- agent state transitions (orders, reports, forwards, idle loops) ----
  const lastState = new Map<string, string>();
  store.subscribe(() => {
    for (const s of Object.values(sessions())) {
      const prev = lastState.get(s.id);
      const cur = s.agentState ?? '';
      if (prev === cur) continue;
      lastState.set(s.id, cur);
      if (prev !== undefined) void onTransition(s, prev, cur);
    }
  });

  const onTransition = async (s: Session, from: string, to: string) => {
    if (to === 'working') {
      for (const id of queues.get(s.id) ?? []) {
        const d = deliveries.get(id);
        if (d?.state === 'sent') setDelivery(d, { state: 'confirmed', confirmedAt: iso() });
      }
    }
    for (const o of orders.values()) {
      const t = o.targets?.find((x) => x.sessionId === s.id && !['finished', 'failed', 'cancelled', 'timeout', 'skipped'].includes(x.state));
      if (!t) continue;
      let state = t.state;
      if (to === 'working' && ['delivered', 'queued', 'needsYou'].includes(t.state) && deliveries.get(t.deliveryId ?? '')?.state !== 'queued') state = 'working';
      else if (to === 'needsYou' && t.state === 'working') state = 'needsYou';
      else if (to === 'ready' && (t.state === 'working' || t.state === 'needsYou')) state = 'finished';
      else if ((to === 'shell' || to === 'exited' || to === 'error') && t.state !== 'queued') state = 'failed';
      if (state === t.state) continue;
      const now = iso();
      const targets = (o.targets ?? []).map((x) =>
        x === t ? { ...x, state, startedAt: state === 'working' ? now : x.startedAt, finishedAt: state === 'finished' || state === 'failed' ? now : x.finishedAt } : x,
      );
      updateOrder({ ...o, targets });
      if (state === 'finished' || state === 'failed') {
        let excerpt = '';
        let chars = 0;
        if (state === 'finished') {
          await sleep(600);
          try {
            const r = await hub<{ text: string; chars: number }>('GET', `/api/sessions/${s.id}/reply`);
            excerpt = r.text.slice(0, 400);
            chars = r.chars;
          } catch {
            /* no reply */
          }
        }
        const started = t.startedAt ? Date.parse(t.startedAt) : Date.parse(o.createdAt);
        msg(o.threadId, {
          role: 'agent',
          kind: 'report',
          text: excerpt,
          orderId: o.id,
          sessionId: s.id,
          handle: s.handle,
          report: { state, durationMs: Date.now() - started, excerpt, chars },
        });
        void forwardOnFinish(s.id, o.id);
      }
    }
    if (from === 'working' && to === 'ready') {
      void edgeForwards(s);
      for (const l of Object.values(loops)) {
        if (l.state?.status !== 'running') continue;
        const targets = loopTargets(l);
        if (!targets.some((x) => x.id === s.id)) continue;
        if (l.stop.goal) void goalCheck(l);
        if (l.trigger.kind === 'agentIdle') {
          const k = `${l.id}:${s.id}`;
          if (Date.now() - (lastIdleFire.get(k) ?? 0) < 120000) continue;
          lastIdleFire.set(k, Date.now());
          void runLoop(l, [s]);
        }
      }
    }
  };

  // ---- board helpers ----
  const board = (wid: string) => hub<Board>('GET', `/api/workspaces/${wid}/board`);
  const ops = (wid: string, list: Op[]) => hub('POST', `/api/workspaces/${wid}/board/ops`, { clientId: MOCK_CLIENT, baseRev: 0, ops: list });
  const wsOfThread = (tid: string, fallback?: string) => fallback ?? Object.values(store.getState().projects).find((p) => p.id === tid)?.workspaces?.[0]?.id ?? '';
  const nodeOfSession = (b: Board, sid: string) => (b.nodes ?? []).find((n) => n.kind === 'terminal' && n.data.sessionId === sid);
  const agentsIn = (wid: string) => Object.values(sessions()).filter((s) => s.workspaceId === wid && s.agentKind !== 'shell' && s.agent !== 'shell' && s.status === 'running');

  const resolve = async (who: string[], wid: string, cardId?: string | null) => {
    const b = await board(wid);
    const nodes = b.nodes ?? [];
    let scope = agentsIn(wid);
    if (cardId) {
      const allowed = new Set(directedTerminals(nodes, b.edges ?? [], cardId));
      scope = scope.filter((s) => {
        const n = nodeOfSession(b, s.id);
        return n && allowed.has(n.id);
      });
    }
    const found: Session[] = [];
    const unknown: string[] = [];
    const fuzzy: Session[] = [];
    let groupish = false;
    for (const w of who) {
      const k = norm(w);
      if (['everyone', 'all', 'everybody'].includes(k)) {
        found.push(...scope);
        groupish = true;
        continue;
      }
      const h = Object.values(sessions()).find((s) => s.handle && norm(s.handle) === k);
      if (h) {
        if (cardId && !scope.includes(h)) fail(400, `@${h.handle} is not wired to this card.`, 'not_wired');
        found.push(h);
        continue;
      }
      const g = nodes.find((n) => n.kind === 'group' && (norm(readGroup(n).name) === k || norm(readGroup(n).name.replace(/^group\s*/i, 'group')) === k));
      const roleGroups = nodes.filter((n) => n.kind === 'group' && (norm(readGroup(n).role) === k || norm(readGroup(n).role) === `${k}s`));
      const gs = g ? [g] : roleGroups;
      if (gs.length) {
        groupish = true;
        for (const gg of gs) for (const m of readGroup(gg).members) {
          const n = nodes.find((x) => x.id === m);
          const s = n ? sessions()[String(n.data.sessionId)] : undefined;
          if (s && scope.includes(s)) found.push(s);
        }
        continue;
      }
      const byAgent = scope.filter((s) => norm(s.agent ?? '') === k || norm(s.agent ?? '') + 's' === k);
      if (byAgent.length) {
        groupish = true;
        found.push(...byAgent);
        continue;
      }
      const near = scope.filter((s) => s.handle && editDistance(norm(s.handle), k) <= (k.length <= 4 ? 1 : 2));
      if (near.length) fuzzy.push(...near);
      else unknown.push(w);
    }
    return { found: [...new Set(found)], unknown, fuzzy: [...new Set(fuzzy)], groupish, scope, board: b };
  };

  const isFree = (s: Session) => s.agentState === 'ready' && !paused.has(s.id) && activeQueue(s.id) === 0;

  const sendOrder = (tid: string, cmdId: string, targets: Session[], text: string, mode: 'verbatim' | 'rewritten' = 'verbatim', note?: string) => {
    const o: Order = { id: mk('o'), threadId: tid, commandId: cmdId, text, createdAt: iso(), targets: [] };
    for (const s of targets) {
      try {
        const d = deliver(s.id, text, 'order', { orderId: o.id });
        o.targets!.push({ sessionId: s.id, handle: s.handle ?? '', deliveryId: d.id, state: 'queued', startedAt: null, finishedAt: null });
      } catch (err) {
        o.targets!.push({ sessionId: s.id, handle: s.handle ?? '', state: 'failed', error: err instanceof Error ? err.message : String(err) });
      }
    }
    updateOrder(o);
    lastOrder.set(tid, o.id);
    msg(tid, { kind: 'plan', text: note ?? (mode === 'verbatim' ? 'Sent as written' : 'Plan'), orderId: o.id, plan: targets.map((s) => ({ handle: s.handle ?? '', text })) });
    return o;
  };

  const confirm = (tid: string, cmdId: string, c: Omit<Confirmation, 'id' | 'threadId' | 'commandId' | 'expiresAt' | 'state'>, run: (choice?: string) => Promise<void>) => {
    const full: Confirmation = { ...c, id: mk('cf'), threadId: tid, commandId: cmdId, expiresAt: iso(Date.now() + 60000), state: 'pending' };
    confs.set(full.id, { c: full, run });
    emit({ type: 'confirmation', confirmation: full });
    msg(tid, { kind: 'confirm', text: full.prompt, confirmationId: full.id });
    window.setTimeout(() => {
      const cur = confs.get(full.id);
      if (cur && cur.c.state === 'pending') {
        cur.c = { ...cur.c, state: 'expired' };
        emit({ type: 'confirmation', confirmation: cur.c });
      }
    }, 60500);
    return full;
  };

  const decide = async (id: string, decision: string, choice?: string) => {
    const cur = confs.get(id);
    if (!cur) fail(404, 'Not found.');
    if (cur!.c.state !== 'pending') fail(409, 'Already decided.', 'already_decided');
    if (Date.parse(cur!.c.expiresAt) < Date.now()) fail(410, 'Expired.', 'expired');
    cur!.c = { ...cur!.c, state: decision === 'confirm' ? 'confirmed' : 'cancelled', decidedBy: 'admin' };
    emit({ type: 'confirmation', confirmation: cur!.c });
    if (decision === 'confirm') await cur!.run(choice);
    else msg(cur!.c.threadId, { kind: 'note', text: 'Cancelled. Nothing was done.' });
    return cur!.c;
  };

  // ---- spawn (also used by deploy and handoff) ----
  const defaultAgent = async (machineId: string, role?: string) => {
    const roleAgent = role ? (store.getState().settings?.agents.roleAgents?.[role] ?? '') : '';
    if (roleAgent) return roleAgent;
    try {
      return (await hub<{ defaultAgent: string }>('GET', `/api/machines/${machineId}/agents`)).defaultAgent || 'shell';
    } catch {
      return 'shell';
    }
  };
  const spawn = async (wid: string, req: SpawnRequest): Promise<{ sessions: Session[]; groupNodeId?: string }> => {
    const project = Object.values(store.getState().projects).find((p) => p.workspaces?.some((w) => w.id === wid));
    const slots: SpawnSlot[] = req.slots ?? Array.from({ length: req.count ?? 1 }, () => ({ agent: req.agent }));
    if (slots.length < 1 || slots.length > 12) fail(400, 'Spawn 1–12 consoles at a time.', 'too_many');
    const b = await board(wid);
    const existingGroup = req.group && 'id' in req.group ? (b.nodes ?? []).find((n) => n.id === (req.group as { id: string }).id) : undefined;
    if (req.group && 'id' in req.group && !existingGroup) fail(404, 'Group not found.', 'group_not_found');
    const role = existingGroup ? readGroup(existingGroup).role : req.group && 'role' in req.group ? req.group.role : undefined;
    const W = 720;
    const H = 440;
    const G = 40;
    const cols = slots.length <= 2 ? slots.length : slots.length <= 4 ? 2 : slots.length <= 9 ? 3 : 4;
    const rows = Math.ceil(slots.length / cols);
    let at = req.at;
    if (existingGroup) {
      const ms = readGroup(existingGroup).members.map((m) => (b.nodes ?? []).find((n) => n.id === m)).filter(Boolean) as BoardNode[];
      const maxX = Math.max(existingGroup.x + existingGroup.w, ...ms.map((n) => n.x + n.w));
      at = { x: maxX + G + (cols * (W + G)) / 2, y: existingGroup.y + 56 + (rows * (H + G)) / 2 };
    }
    if (!at) {
      const maxX = Math.max(0, ...(b.nodes ?? []).map((n) => n.x + n.w));
      at = { x: maxX + 80 + (cols * (W + G)) / 2, y: (rows * (H + G)) / 2 };
    }
    const x0 = at.x - (cols * (W + G) - G) / 2;
    const y0 = at.y - (rows * (H + G) - G) / 2;
    const made: Session[] = [];
    for (let i = 0; i < slots.length; i++) {
      const slot = slots[i];
      const machineId = slot.machineId ?? project?.machineId ?? 'local';
      const agent = slot.agent || (await defaultAgent(machineId, role));
      const s = await hub<Session>('POST', '/api/sessions', {
        workspaceId: wid,
        machineId,
        preset: agent,
        agent,
        profileId: slot.profileId,
        yolo: slot.yolo,
        cols: 90,
        rows: 22,
        node: { x: Math.round(x0 + (i % cols) * (W + G)), y: Math.round(y0 + Math.floor(i / cols) * (H + G)), w: W, h: H },
      });
      store.upsertSession(s);
      made.push(s);
    }
    await sleep(300);
    const after = await board(wid);
    const ids = made.map((s) => nodeOfSession(after, s.id)?.id).filter((x): x is string => !!x);
    const rects = ids.map((id) => (after.nodes ?? []).find((n) => n.id === id)!).map((n) => ({ x: n.x, y: n.y, w: n.w, h: n.h }));
    const list: Op[] = [];
    let groupNodeId: string | undefined;
    if (existingGroup) {
      const cur = (after.nodes ?? []).find((n) => n.id === existingGroup.id) ?? existingGroup;
      const members = [...readGroup(cur).members, ...ids];
      const allRects = members.map((m) => (after.nodes ?? []).find((n) => n.id === m)).filter(Boolean).map((n) => ({ x: n!.x, y: n!.y, w: n!.w, h: n!.h }));
      list.push({ op: 'patch', id: cur.id, set: { ...frameOf(allRects)!, data: { members } } });
      groupNodeId = cur.id;
    } else if (req.group && !('id' in req.group)) {
      const names = new Set((after.nodes ?? []).filter((n) => n.kind === 'group').map((n) => norm(readGroup(n).name)));
      let name = req.group.name?.trim() || '';
      if (!name) for (let i = 1; !name || names.has(norm(name)); i++) name = `Group ${i}`;
      else if (names.has(norm(name)) || Object.values(sessions()).some((s) => s.handle && norm(s.handle) === norm(name))) fail(409, `“${name}” is already used.`, 'name_conflict');
      const r = req.group.role ?? 'general';
      groupNodeId = mk('n');
      list.push({ op: 'put', node: { id: groupNodeId, kind: 'group', ...frameOf(rects)!, data: { name, role: r, color: req.group.color ?? 'sky', members: ids } } }, { op: 'z', id: groupNodeId, to: 'back' });
    }
    if (req.orchestratorId) {
      if (groupNodeId && !existingGroup) list.push({ op: 'putEdge', edge: { id: mk('e'), source: req.orchestratorId, target: groupNodeId, kind: 'wire', data: {} } });
      else for (const id of ids) list.push({ op: 'putEdge', edge: { id: mk('e'), source: req.orchestratorId, target: id, kind: 'wire', data: {} } });
    }
    if (list.length) await ops(wid, list);
    return { sessions: made, groupNodeId };
  };

  // ---- commands ----
  const command = async (pid: string, body: CommandRequest) => {
    const text = (body.text ?? '').trim();
    if (!text) fail(400, 'Type an order first.', 'empty_text');
    if (text.length > 4000) fail(413, 'Orders are limited to 4000 characters.', 'text_too_long');
    const tid = body.cardId || pid;
    const wid = body.workspaceId || wsOfThread(pid);
    const cmdId = mk('cmd');
    const out: ThreadMessage[] = [];
    const say = (m: Partial<ThreadMessage> & { kind: string; text: string }) => {
      const x = msg(tid, m);
      out.push(x);
      return x;
    };
    say({ role: 'user', kind: 'order', text, source: body.source ?? 'typed' });
    const uiActions: UiAction[] = [];
    let confirmation: Confirmation | null = null;
    let orderId: string | null = null;

    // answering a pending confirmation in this thread
    const pend = [...confs.values()].find((x) => x.c.threadId === tid && x.c.state === 'pending' && Date.parse(x.c.expiresAt) > Date.now());
    if (pend) {
      if (/^(yes|confirm|do it|go ahead)$/i.test(text)) {
        await decide(pend.c.id, 'confirm');
        return { commandId: cmdId, threadId: tid, handledBy: 'parser', orderId: null, uiActions, confirmation: null, messages: out };
      }
      if (/^(no|cancel)$/i.test(text)) {
        await decide(pend.c.id, 'cancel');
        return { commandId: cmdId, threadId: tid, handledBy: 'parser', orderId: null, uiActions, confirmation: null, messages: out };
      }
      pend.c = { ...pend.c, state: 'cancelled' };
      emit({ type: 'confirmation', confirmation: pend.c });
    }

    const agents = (await hub<{ id: string }[] | null>('GET', '/api/agents').catch(() => [])) ?? [];
    const p = parse(text, agents.map((a) => a.id));
    const scopeOf = async () => (await resolve([], wid, body.cardId)).scope;
    const who = (ss: Session[]) => ss.map((s) => `@${s.handle}`).join(', ');
    const word = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`;

    if ((p.op === 'ui' && (p.action === 'undo' || p.action === 'redo')) && body.cardId) {
      say({ kind: 'reply', text: 'Use the main command bar for that.' });
    } else
      switch (p.op) {
        case 'smalltalk':
          say({ kind: 'reply', text: 'Anytime.' });
          break;
        case 'bareMention':
          say({ kind: 'reply', text: `What should @${p.who} do?` });
          break;
        case 'ask': {
          const scope = await scopeOf();
          if (p.query === 'help') say({ kind: 'reply', text: 'Try “@Ash run the tests”, “spawn two reviewers”, “make a group of three called QA”, “pause”, “status”, “every 15 minutes run the tests”, “deploy Duo” or “open mission map”. The planner is off in this mock, so name agents with @.' });
          else if (p.query === 'finished') {
            const o = orders.get(lastOrder.get(tid) ?? '');
            say({ kind: 'reply', text: o ? (o.targets ?? []).map((t) => `@${t.handle}: ${t.state}`).join(' · ') : 'No order yet in this thread.' });
          } else {
            const by = (st: string) => scope.filter((s) => s.agentState === st).length;
            const needs = scope.filter((s) => s.agentState === 'needsYou');
            say({ kind: 'reply', text: `${by('working')} working · ${needs.length} need you · ${by('ready')} ready · ${scope.filter((s) => paused.has(s.id)).length} paused${needs.length ? `. Waiting for you: ${who(needs)}` : ''}` });
          }
          break;
        }
        case 'ui': {
          if (p.action === 'focus') {
            const t = Object.values(sessions()).find((s) => s.handle && norm(s.handle) === norm(p.target ?? ''));
            if (t) uiActions.push({ action: 'focus', target: t.id });
            else say({ kind: 'reply', text: `I can’t find “${p.target}”.` });
          } else uiActions.push({ action: p.action, panel: p.panel, tool: p.tool });
          break;
        }
        case 'cancel': {
          const o = orders.get(lastOrder.get(tid) ?? '');
          if (!o) say({ kind: 'reply', text: 'There is nothing to cancel.' });
          else {
            await cancelOrder(o.id);
            say({ kind: 'note', text: 'Cancelled what had not been typed yet. Text already pasted stays in the agent’s input.' });
          }
          break;
        }
        case 'send': {
          const r = p.who.length ? await resolve(p.who, wid, body.cardId) : { found: await scopeOf(), unknown: [], fuzzy: [], groupish: true, scope: await scopeOf() };
          if (r.unknown.length && !r.found.length && !r.fuzzy.length) {
            say({ kind: 'error', text: `I don’t know ${r.unknown.map((u) => `“${u}”`).join(', ')}. Pick a name from the @ menu.` });
            break;
          }
          if (r.fuzzy.length && !r.found.length) {
            const kind = r.fuzzy.length === 1 ? 'didYouMean' : 'sendToAll';
            confirmation = confirm(tid, cmdId, {
              kind,
              prompt: r.fuzzy.length === 1 ? `Did you mean @${r.fuzzy[0].handle}?` : `Send to all of ${who(r.fuzzy)}?`,
              options: [
                { id: 'confirm', label: r.fuzzy.length === 1 ? `Send to @${r.fuzzy[0].handle}` : `Send to ${r.fuzzy.length}` },
                { id: 'cancel', label: 'Cancel' },
              ],
              candidates: r.fuzzy.map((s) => ({ sessionId: s.id, handle: s.handle ?? '' })),
              steps: [{ op: 'send', targets: r.fuzzy.map((s) => s.id), text: p.text }],
            }, async () => {
              sendOrder(tid, cmdId, r.fuzzy, p.text);
            });
            break;
          }
          const everyone = p.who.length === 0 || p.who.some((w) => ['everyone', 'all', 'everybody'].includes(norm(w)));
          if (everyone && /^(?:give everyone the same prompt|tell everyone)/i.test(text)) {
            orderId = sendOrder(tid, cmdId, r.found, p.text, 'verbatim', `Same prompt to ${word(r.found.length, 'agent')}`).id;
          } else if (r.found.length > 1 || r.groupish) {
            const free = r.found.filter(isFree);
            const pick = free[0] ?? r.found[0];
            if (!pick) {
              say({ kind: 'note', text: 'Nobody to send it to: no agent is running here.' });
              break;
            }
            orderId = sendOrder(tid, cmdId, [pick], p.text, 'verbatim', `Sent to @${pick.handle}; ${word(Math.max(0, r.found.length - 1), 'agent')} left free (the planner would split this)`).id;
          } else if (r.found.length === 1) {
            orderId = sendOrder(tid, cmdId, r.found, p.text, p.verbatim ? 'verbatim' : 'rewritten', p.verbatim ? `Sent to @${r.found[0].handle} as written` : `Sent to @${r.found[0].handle} (planner off: your words as written)`).id;
          } else say({ kind: 'note', text: 'Nobody to send it to.' });
          break;
        }
        case 'unknown': {
          const scope = await scopeOf();
          if (scope.length === 1) orderId = sendOrder(tid, cmdId, scope, p.text, 'verbatim', `Sent to @${scope[0].handle}, the only agent here`).id;
          else say({ kind: 'note', text: scope.length ? 'Name an agent with @ or enable the planner.' : 'There is no agent here yet. Try “spawn two claude” or use New console.' });
          break;
        }
        case 'spawn': {
          const run = async () => {
            try {
              const r = await spawn(wid, { count: p.count, agent: p.agent, group: p.group ? { name: p.group.name, role: (p.role as never) ?? 'general' } : undefined, orchestratorId: body.cardId ?? undefined });
              msg(tid, { kind: 'reply', text: `Started ${word(r.sessions.length, 'console')}: ${who(r.sessions)}${r.groupNodeId ? ' in a new group' : ''}.` });
            } catch (err) {
              msg(tid, { kind: 'error', text: `Could not start the consoles: ${err instanceof Error ? err.message : err}` });
            }
          };
          if (p.count > 6) confirmation = confirm(tid, cmdId, { kind: 'bulkSpawn', prompt: `Start ${p.count} consoles at once?`, options: [{ id: 'confirm', label: `Start ${p.count}` }, { id: 'cancel', label: 'Cancel' }] }, run);
          else await run();
          break;
        }
        case 'groupThese': {
          say({ kind: 'reply', text: 'Select the consoles and press Ctrl+G to group them.' });
          break;
        }
        case 'pause':
        case 'resume': {
          const r = p.all ? { found: await scopeOf() } : await resolve(p.who, wid, body.cardId);
          for (const s of r.found) {
            if (p.op === 'pause') paused.set(s.id, iso());
            else paused.delete(s.id);
            refresh(s.id);
          }
          say({ kind: 'reply', text: r.found.length ? `${p.op === 'pause' ? 'Paused' : 'Resumed'} ${who(r.found)}.` : 'Nobody to ' + p.op + '.' });
          break;
        }
        case 'kill':
        case 'restart': {
          const r = p.all ? { found: await scopeOf() } : await resolve(p.who, wid, body.cardId);
          if (!r.found.length) {
            say({ kind: 'reply', text: 'Nobody matches that.' });
            break;
          }
          const verb = p.op === 'kill' ? 'Kill' : 'Restart';
          confirmation = confirm(
            tid,
            cmdId,
            {
              kind: 'destructive',
              prompt: p.op === 'kill' ? `Kill ${who(r.found)}? Their processes stop; the tiles stay on the board.` : `Restart ${who(r.found)}? Their current turn stops; the conversation resumes.`,
              options: [
                { id: 'confirm', label: `${verb} ${word(r.found.length, 'console')}` },
                { id: 'cancel', label: 'Cancel' },
              ],
              steps: [{ op: p.op, targets: r.found.map((s) => s.id) }],
            },
            async () => {
              for (const s of r.found) {
                const res = await (p.op === 'kill' ? hub<Session>('POST', `/api/sessions/${s.id}/kill`) : hub<Session>('POST', `/api/sessions/${s.id}/restart`, { mode: s.agentSessionId ? 'resume' : 'continue' })).catch(() => null);
                if (res) store.upsertSession(res);
              }
              msg(tid, { kind: 'reply', text: `${p.op === 'kill' ? 'Killed' : 'Restarted'} ${who(r.found)}.` });
            },
          );
          break;
        }
        case 'loop': {
          if (p.everySec < 60 || p.everySec > 86400) {
            say({ kind: 'error', text: 'Loops run every 1 minute to 24 hours.' });
            break;
          }
          const n = (await scopeOf()).length;
          const def: LoopInput = { projectId: pid, workspaceId: wid, name: p.text.slice(0, 40), enabled: true, trigger: { kind: 'interval', everySec: p.everySec }, text: p.text, targets: { kind: body.cardId ? 'orchestrator' : 'allAgents', ids: body.cardId ? [body.cardId] : [] }, stop: { maxRuns: 20 } };
          const perHour = 3600 / p.everySec;
          confirmation = confirm(tid, cmdId, { kind: 'loop', prompt: `Every ${p.everySec >= 3600 ? `${p.everySec / 3600} h` : `${p.everySec / 60} min`}, send “${p.text}” to ${word(n, 'agent')} (about ${Math.round(perHour * n * 10) / 10} prompts/hour) until 20 runs?`, options: [{ id: 'confirm', label: 'Create loop' }, { id: 'cancel', label: 'Cancel' }] }, async () => {
            const l = createLoop(pid, def, 'order');
            msg(tid, { kind: 'reply', text: `Loop “${l.name}” is running.` });
          });
          break;
        }
        case 'memoryPush': {
          const r = p.who.length ? await resolve(p.who, wid, body.cardId) : { found: await scopeOf() };
          const res = pushMemory(pid, r.found);
          say({ kind: 'reply', text: `Memory sent to ${word(res.delivered, 'agent')}${res.busy ? `; ${res.busy} busy` : ''}.` });
          break;
        }
        case 'memoryScan':
          uiActions.push({ action: 'openPanel', panel: 'memory' });
          say({ kind: 'reply', text: 'Opened the memory panel; pick an effort and scan.' });
          break;
        case 'deploy': {
          const all = [...BUILTIN, ...saved];
          const l = all.find((x) => norm(x.name) === norm(p.name)) ?? all.find((x) => norm(x.name).startsWith(norm(p.name)));
          if (!l) say({ kind: 'error', text: `There is no layout called “${p.name}”.` });
          else {
            const r = await deployLayout(wid, { layoutId: l.id });
            say({ kind: 'reply', text: `Deployed ${l.name}: ${word(r.sessions.length, 'console')}.` });
            uiActions.push({ action: 'fit' });
          }
          break;
        }
        case 'forward': {
          const a = await resolve(p.from, wid, body.cardId);
          const b = await resolve(p.to, wid, body.cardId);
          if (!a.found.length || !b.found.length) {
            say({ kind: 'error', text: 'Name who finishes and who receives, for example “when @Ash finishes, pass it to @Bea”.' });
            break;
          }
          const f: Forward & { done: Set<string> } = { id: mk('fw'), projectId: pid, sources: a.found.map((s) => s.id), targets: b.found.map((s) => s.id), mode: 'digest', state: 'waiting', createdAt: iso(), expiresAt: iso(Date.now() + 4 * 3600e3), done: new Set() };
          forwards.set(f.id, f);
          emit({ type: 'forward', forward: f });
          say({ kind: 'reply', text: `When ${who(a.found)} finish${a.found.length === 1 ? 'es' : ''}, I’ll pass the results to ${who(b.found)}.` });
          break;
        }
        case 'handoff': {
          const src = p.from ? (await resolve([p.from], wid)).found[0] : (await scopeOf()).find((s) => s.agentState === 'ready' || s.agentState === 'working');
          if (!src) {
            say({ kind: 'reply', text: 'Which console? Say “hand off to codex from @Ash”.' });
            break;
          }
          const h = startHandoff(src.id, { agent: p.agent, threadId: tid, workspaceId: wid });
          say({ kind: 'reply', text: `Handing @${src.handle}’s task to ${p.agent} (${h.state}).` });
          break;
        }
        case 'nodeAdd': {
          const b = await board(wid);
          const maxX = Math.max(0, ...(b.nodes ?? []).map((n) => n.x + n.w));
          const used = new Set((b.nodes ?? []).filter((n) => n.kind === 'orchestrator').map((n) => String(n.data.title)));
          let title = 'Orchestrator';
          for (let i = 2; used.has(title); i++) title = `Orchestrator ${i}`;
          const node: BoardNode =
            p.kind === 'orchestrator'
              ? { id: mk('n'), kind: 'orchestrator', x: maxX + 80, y: 0, w: 380, h: 460, data: { title } }
              : { id: mk('n'), kind: p.kind === 'note' ? 'note' : p.kind, x: maxX + 80, y: 0, w: 260, h: 220, data: p.kind === 'note' ? { text: p.text ?? '', color: 'yellow' } : { title: p.kind[0].toUpperCase() + p.kind.slice(1) } };
          await ops(wid, [{ op: 'put', node }]);
          uiActions.push({ action: 'focus', target: node.id });
          say({ kind: 'reply', text: `Added ${p.kind === 'orchestrator' ? title : `a ${p.kind}`}.` });
          break;
        }
      }
    return { commandId: cmdId, threadId: tid, handledBy: 'parser', orderId, uiActions, confirmation, messages: out };
  };

  const cancelOrder = async (id: string) => {
    const o = orders.get(id);
    if (!o) fail(404, 'Order not found.');
    for (const t of o!.targets ?? []) {
      const d = deliveries.get(t.deliveryId ?? '');
      if (d && (d.state === 'queued' || d.state === 'waiting')) setDelivery(d, { state: 'cancelled' });
    }
    const cur = orders.get(id)!;
    const n = { ...cur, targets: (cur.targets ?? []).map((t) => (t.state === 'queued' ? { ...t, state: 'cancelled' } : t)) };
    updateOrder(n);
    return n;
  };

  // ---- forwards ----
  const digestFor = async (sids: string[]) => {
    const parts: string[] = [];
    const names: string[] = [];
    for (const sid of sids) {
      const s = sessions()[sid];
      const r = await hub<{ text: string }>('GET', `/api/sessions/${sid}/reply`).catch(() => ({ text: '(no reply)' }));
      names.push(`@${s?.handle}`);
      parts.push(`## @${s?.handle}\n${r.text.slice(0, 20000)}`);
    }
    return `Results forwarded by NextBoard from ${names.join(' and ')}:\n\n${parts.join('\n\n')}\n\nUse these results for your next step.`.slice(0, 60000);
  };
  const forwardOnFinish = async (sid: string, _orderId: string) => {
    for (const f of forwards.values()) {
      if (f.state !== 'waiting' || !f.sources.includes(sid)) continue;
      f.done.add(sid);
      if (f.sources.every((x) => f.done.has(x))) {
        f.state = 'sent';
        emit({ type: 'forward', forward: f });
        const text = await digestFor(f.sources);
        for (const t of f.targets) deliver(t, text, 'forward', { forwardDepth: 1 });
      }
    }
  };
  const edgeForwards = async (s: Session) => {
    if (!s.workspaceId) return;
    const b = await board(s.workspaceId);
    const node = nodeOfSession(b, s.id);
    if (!node) return;
    const groupIds = (b.nodes ?? []).filter((n) => n.kind === 'group' && readGroup(n).members.includes(node.id)).map((n) => n.id);
    const edges = (b.edges ?? []).filter((e: BoardEdge) => e.kind === 'forward' && (e.source === node.id || groupIds.includes(e.source)));
    for (const e of edges) {
      if (Date.now() - (forwardFired.get(e.id) ?? 0) < 60000) continue;
      forwardFired.set(e.id, Date.now());
      const tn = (b.nodes ?? []).find((n) => n.id === e.target);
      if (!tn) continue;
      const targetNodes = tn.kind === 'group' ? readGroup(tn).members : [tn.id];
      const targets = targetNodes.map((id) => (b.nodes ?? []).find((n) => n.id === id)).map((n) => (n ? sessions()[String(n.data.sessionId)] : undefined)).filter((x): x is Session => !!x);
      const text = await digestFor([s.id]);
      const list = e.data.mode === 'split' ? targets.slice(0, 1) : targets;
      for (const t of list) {
        try {
          deliver(t.id, text, 'forward', { forwardDepth: 1 });
        } catch {
          /* not an agent */
        }
      }
    }
  };

  // ---- loops ----
  const loopTargets = (l: LoopDef): Session[] => {
    const all = agentsIn(l.workspaceId);
    if (l.targets.kind === 'allAgents') return all;
    if (l.targets.kind === 'handles') return all.filter((s) => l.targets.ids.includes(s.id));
    return all.filter((s) => cachedMembers.get(`${l.targets.kind}:${l.targets.ids[0]}`)?.has(s.id));
  };
  const cachedMembers = new Map<string, Set<string>>();
  const refreshMembers = async (l: LoopDef) => {
    if (l.targets.kind !== 'group' && l.targets.kind !== 'orchestrator') return;
    const b = await board(l.workspaceId);
    const id = l.targets.ids[0];
    const nodeIds = l.targets.kind === 'group' ? readGroup((b.nodes ?? []).find((n) => n.id === id) ?? ({ data: {} } as BoardNode)).members : directedTerminals(b.nodes ?? [], b.edges ?? [], id);
    cachedMembers.set(`${l.targets.kind}:${id}`, new Set(nodeIds.map((n) => String((b.nodes ?? []).find((x) => x.id === n)?.data.sessionId ?? ''))));
  };
  const hist = (l: LoopDef, e: LoopHistoryEntry) => {
    (loopHist[l.id] ??= []).unshift(e);
    loopHist[l.id] = loopHist[l.id].slice(0, 200);
    persist();
    emit({ type: 'loopRun', loopId: l.id, entry: e });
  };
  const putLoop = (l: LoopDef) => {
    loops[l.id] = l;
    persist();
    emit({ type: 'loop', loop: l });
    return l;
  };
  const stopLoop = (l: LoopDef, reason: string, status: 'stopped' | 'done' = 'done') => {
    const n = putLoop({ ...l, enabled: false, state: { ...(l.state ?? { runs: 0, status }), status, stoppedReason: reason, nextRunAt: undefined } });
    hist(n, { at: iso(), kind: 'stop', note: `Stopped: ${reason}` });
    return n;
  };
  const createLoop = (pid: string, input: LoopInput, by = 'user') => {
    if (input.trigger.kind === 'interval' && (input.trigger.everySec < 60 || input.trigger.everySec > 86400)) fail(400, 'Loops run every 1 minute to 24 hours.', 'bad_loop');
    if (input.trigger.kind === 'output' && input.trigger.pattern.length < 2) fail(400, 'The pattern needs at least 2 characters.', 'bad_loop');
    if (input.stop.maxRuns !== undefined && (input.stop.maxRuns < 1 || input.stop.maxRuns > 500)) fail(400, 'Run limit 1–500.', 'bad_loop');
    if (Object.keys(loops).length >= 50) fail(429, 'Too many loops.', 'too_many_loops');
    const l: LoopDef = {
      ...input,
      id: mk('lp'),
      projectId: pid,
      createdBy: by,
      createdAt: iso(),
      state: { status: input.enabled ? 'running' : 'stopped', runs: 0, startedAt: iso(), nextRunAt: input.enabled && input.trigger.kind === 'interval' ? iso(Date.now() + input.trigger.everySec * 1000) : undefined },
    };
    void refreshMembers(l);
    return putLoop(l);
  };
  const goalCheck = async (l: LoopDef): Promise<boolean | null> => {
    const g = l.stop.goal;
    if (!g) return null;
    if (Date.now() - (lastGoal.get(l.id) ?? 0) < 5000) return null;
    lastGoal.set(l.id, Date.now());
    if (g.kind === 'command') {
      hist(l, { at: iso(), kind: 'goalCheck', note: 'Command goals run on the hub; this check is inconclusive here.', goal: { met: false, reason: 'not checked' } });
      return null;
    }
    const texts: string[] = [];
    for (const s of loopTargets(l)) texts.push((await hub<{ text: string }>('GET', `/api/sessions/${s.id}/reply`).catch(() => ({ text: '' }))).text.toLowerCase());
    const want = g.text.toLowerCase();
    const met = texts.some((t) => t.includes(want) || (/pass/.test(want) && /passes|passed/.test(t)));
    hist(l, { at: iso(), kind: 'goalCheck', note: `Checked: “${g.text}”`, goal: { met, reason: met ? 'The last reply says so.' : 'Not there yet.' } });
    if (met) stopLoop(loops[l.id] ?? l, 'goal');
    return met;
  };
  const runLoop = async (l: LoopDef, only?: Session[]) => {
    await refreshMembers(l);
    const cur = loops[l.id] ?? l;
    if (cur.stop.goal && !only) {
      const met = await goalCheck(cur);
      if (met) return;
    }
    const targets = only ?? loopTargets(cur);
    const results: { sessionId: string; result: string }[] = [];
    for (const s of targets) {
      if (paused.has(s.id)) results.push({ sessionId: s.id, result: 'paused' });
      else if (s.agentState === 'needsYou') results.push({ sessionId: s.id, result: 'needsYou' });
      else if (s.agentState !== 'ready') results.push({ sessionId: s.id, result: 'busy' });
      else {
        try {
          deliver(s.id, cur.text, 'loop', { deadline: '60' as unknown as string });
          results.push({ sessionId: s.id, result: 'delivered' });
        } catch {
          results.push({ sessionId: s.id, result: 'notAgent' });
        }
      }
    }
    const delivered = results.filter((r) => r.result === 'delivered').length;
    const latest = loops[l.id] ?? cur;
    if (latest.state?.status !== 'running' && !only) return;
    const runs = (latest.state?.runs ?? 0) + (delivered ? 1 : 0);
    hist(latest, { at: iso(), kind: delivered ? 'run' : 'skip', note: delivered ? `Sent to ${delivered} of ${results.length}` : 'Nobody was free', targets: results });
    const next = putLoop({ ...latest, state: { ...(latest.state ?? { status: 'running', runs: 0 }), runs, lastRunAt: iso(), nextRunAt: latest.trigger.kind === 'interval' ? iso(Date.now() + latest.trigger.everySec * 1000) : latest.state?.nextRunAt } });
    if (latest.stop.maxRuns && runs >= latest.stop.maxRuns) stopLoop(next, 'maxRuns');
  };
  window.setInterval(() => {
    for (const l of Object.values(loops)) {
      if (l.state?.status !== 'running') continue;
      if (l.stop.maxDurationSec && l.state.startedAt && Date.now() - Date.parse(l.state.startedAt) > l.stop.maxDurationSec * 1000) {
        stopLoop(l, 'time');
        continue;
      }
      if (l.stop.until && Date.parse(l.stop.until) < Date.now()) {
        stopLoop(l, 'time');
        continue;
      }
      if (l.trigger.kind === 'interval' && l.state.nextRunAt && Date.parse(l.state.nextRunAt) <= Date.now()) {
        putLoop({ ...l, state: { ...l.state, nextRunAt: iso(Date.now() + l.trigger.everySec * 1000) } });
        void runLoop(l);
      }
    }
  }, 1000);

  // ---- memory ----
  const memDoc = (key: string) => memory[key] ?? { content: '', mtime: '' };
  const pushMemory = (pid: string, targets: Session[]) => {
    const content = memDoc(pid).content.slice(0, 4000);
    const text = `Project context from NextBoard (background only, not a task). Keep it in mind for later work and reply with just: OK\n\n${content}`;
    let delivered = 0;
    let busy = 0;
    let skipped = 0;
    let notAgent = 0;
    const ids: string[] = [];
    for (const s of targets) {
      if (s.agentKind === 'shell' || s.agent === 'shell' || s.agentState === 'shell') {
        notAgent++;
        continue;
      }
      if (paused.has(s.id) || s.status !== 'running') {
        skipped++;
        continue;
      }
      try {
        ids.push(deliver(s.id, text, 'memory').id);
        if (s.agentState === 'ready') delivered++;
        else busy++;
      } catch {
        skipped++;
      }
    }
    return { delivered, busy, skipped, notAgent, deliveryIds: ids };
  };

  // ---- handoff ----
  const startHandoff = (sid: string, body: { agent: string; machineId?: string; profileId?: string; workspaceId?: string; note?: string; threadId?: string }): Handoff => {
    const src = sessions()[sid];
    if (!src) fail(404, 'Session not found.');
    if (src!.agentKind === 'shell' || src!.agentState === 'shell') fail(400, 'Not an agent.', 'not_agent');
    if ([...handoffs.values()].some((h) => h.sourceSessionId === sid && !['done', 'failed', 'manual'].includes(h.state))) fail(409, 'A handoff is running.', 'handoff_running');
    const h: Handoff = { id: mk('ho'), sourceSessionId: sid, targetSessionId: null, agent: body.agent, state: 'collecting', createdAt: iso() };
    const set = (patch: Partial<Handoff>) => {
      Object.assign(h, patch);
      handoffs.set(h.id, { ...h });
      emit({ type: 'handoff', handoff: { ...h } });
    };
    set({});
    void (async () => {
      try {
        await sleep(700);
        set({ state: 'summarizing' });
        const r = await hub<{ text: string }>('GET', `/api/sessions/${sid}/reply`).catch(() => ({ text: '' }));
        await sleep(900);
        const brief = `Goal: continue @${src!.handle}’s task.\n\nState so far:\n${(r.text || '(no reply yet)').slice(0, 3000)}${body.note ? `\n\nNote from the user: ${body.note}` : ''}`;
        set({ state: 'spawning', brief });
        const wid = body.workspaceId ?? src!.workspaceId ?? '';
        const b = await board(wid);
        const node = nodeOfSession(b, sid);
        const group = node ? (b.nodes ?? []).find((n) => n.kind === 'group' && readGroup(n).members.includes(node.id)) : undefined;
        const at = node ? { x: node.x + node.w + 40 + node.w / 2, y: node.y + node.h / 2 } : undefined;
        const res = await spawn(wid, { slots: [{ agent: body.agent, machineId: body.machineId, profileId: body.profileId }], at, ...(group ? { group: { id: group.id } } : {}) });
        const target = res.sessions[0];
        set({ state: 'waitingReady', targetSessionId: target.id });
        const until = Date.now() + 45000;
        while (Date.now() < until && sessions()[target.id]?.agentState !== 'ready') await sleep(500);
        if (sessions()[target.id]?.agentState !== 'ready') {
          set({ state: 'manual' });
          if (body.threadId) msg(body.threadId, { kind: 'note', text: `@${target.handle} did not get ready in time. The brief is in the handoff dialog; paste it yourself.` });
          return;
        }
        set({ state: 'delivering' });
        deliver(target.id, `${brief}\n\nContinue this task from exactly this state. Do not redo finished work.`, 'handoff');
        await sleep(600);
        set({ state: 'done' });
        if (body.threadId) msg(body.threadId, { kind: 'note', text: `Handed @${src!.handle}’s task to @${target.handle}.` });
      } catch (err) {
        set({ state: 'failed', error: err instanceof Error ? err.message : String(err) });
      }
    })();
    return { ...h };
  };

  // ---- layouts ----
  const grid = (n: number, cols: number, group?: string, role?: string, ox = 0, oy = 0, prefix = 'l') =>
    Array.from({ length: n }, (_, i) => ({ key: `${prefix}${i + 1}`, role, group, x: ox + (i % cols) * 760, y: oy + Math.floor(i / cols) * 480, w: 720, h: 440 }));
  const BUILTIN: Layout[] = [
    { id: 'builtin:duo', name: 'Duo', builtin: true, lanes: grid(2, 2, 'g1'), groups: [{ key: 'g1', name: 'Duo', role: 'general', color: 'sky' }], nodes: [], wires: [] },
    { id: 'builtin:trio', name: 'Trio', builtin: true, lanes: grid(3, 3, 'g1'), groups: [{ key: 'g1', name: 'Trio', role: 'general', color: 'sky' }], nodes: [], wires: [] },
    { id: 'builtin:quartet', name: 'Quartet', builtin: true, lanes: grid(4, 2, 'g1'), groups: [{ key: 'g1', name: 'Quartet', role: 'general', color: 'sky' }], nodes: [], wires: [] },
    { id: 'builtin:two-quartets', name: 'Two quartets', builtin: true, lanes: [...grid(4, 2, 'g1', undefined, 0, 0, 'a'), ...grid(4, 2, 'g2', undefined, 1620, 0, 'b')], groups: [{ key: 'g1', name: 'Team A', role: 'general', color: 'sky' }, { key: 'g2', name: 'Team B', role: 'general', color: 'violet' }], nodes: [], wires: [] },
    {
      id: 'builtin:build-review',
      name: 'Build & review',
      builtin: true,
      lanes: [...grid(2, 2, 'g1', 'general', 0, 520, 'b'), { key: 'r1', role: 'reviewers', group: 'g2', x: 1600, y: 520, w: 720, h: 440 }],
      groups: [{ key: 'g1', name: 'Builders', role: 'general', color: 'sky' }, { key: 'g2', name: 'Review', role: 'reviewers', color: 'rose' }],
      nodes: [{ key: 'o1', kind: 'orchestrator', x: 0, y: 0, w: 380, h: 440, data: { title: 'Orchestrator' } }, { key: 'k1', kind: 'kanban', x: 440, y: 0, w: 1000, h: 440, data: { title: 'Work' } }],
      wires: [{ from: 'o1', to: 'g1', kind: 'wire' }, { from: 'o1', to: 'g2', kind: 'wire' }],
    },
    {
      id: 'builtin:front-back',
      name: 'Front & back',
      builtin: true,
      lanes: [...grid(2, 1, 'g1', 'frontend', 440, 0, 'f'), ...grid(2, 1, 'g2', 'backend', 1240, 0, 'k')],
      groups: [{ key: 'g1', name: 'Frontend', role: 'frontend', color: 'violet' }, { key: 'g2', name: 'Backend', role: 'backend', color: 'teal' }],
      nodes: [{ key: 'o1', kind: 'orchestrator', x: 0, y: 0, w: 380, h: 440, data: { title: 'Orchestrator' } }],
      wires: [{ from: 'o1', to: 'g1', kind: 'wire' }, { from: 'o1', to: 'g2', kind: 'wire' }],
    },
    { id: 'builtin:review-trio', name: 'Review trio', builtin: true, lanes: grid(3, 3, 'g1', 'reviewers'), groups: [{ key: 'g1', name: 'Reviewers', role: 'reviewers', color: 'rose' }], nodes: [], wires: [] },
    {
      id: 'builtin:triage',
      name: 'Triage',
      builtin: true,
      lanes: [...grid(2, 2, 'g1', 'testers', 0, 520, 't'), { key: 'r1', role: 'reviewers', x: 1600, y: 520, w: 720, h: 440 }],
      groups: [{ key: 'g1', name: 'Testers', role: 'testers', color: 'amber' }],
      nodes: [{ key: 'k1', kind: 'kanban', x: 0, y: 0, w: 1000, h: 440, data: { title: 'Triage' } }],
      wires: [],
    },
    { id: 'builtin:preview-lab', name: 'Preview lab', builtin: true, lanes: grid(3, 3, 'g1'), groups: [{ key: 'g1', name: 'Lab', role: 'frontend', color: 'violet' }], nodes: [{ key: 'w1', kind: 'web', x: 0, y: 520, w: 1000, h: 600, data: { url: 'http://localhost:5173/' } }], wires: [] },
    { id: 'builtin:docs-crew', name: 'Docs crew', builtin: true, lanes: grid(2, 2, 'g1', 'docs'), groups: [{ key: 'g1', name: 'Docs', role: 'docs', color: 'sage' }], nodes: [{ key: 'c1', kind: 'checklist', x: 1560, y: 0, w: 300, h: 340, data: { title: 'Docs to write', items: [] } }], wires: [] },
    { id: 'builtin:solo', name: 'Solo', builtin: true, lanes: grid(1, 1, 'g1'), groups: [{ key: 'g1', name: 'Solo', role: 'general', color: 'sky' }], nodes: [{ key: 'c1', kind: 'checklist', x: 880, y: 0, w: 420, h: 440, data: { title: 'To do', items: [] } }], wires: [] },
    { id: 'builtin:sextet', name: 'Sextet', builtin: true, lanes: grid(6, 3, 'g1'), groups: [{ key: 'g1', name: 'Sextet', role: 'general', color: 'sky' }], nodes: [], wires: [] },
    {
      id: 'builtin:three-trios',
      name: 'Three trios',
      builtin: true,
      lanes: [...grid(3, 3, 'g1', undefined, 0, 0, 'a'), ...grid(3, 3, 'g2', undefined, 0, 576, 'b'), ...grid(3, 3, 'g3', undefined, 0, 1152, 'c')],
      groups: [{ key: 'g1', name: 'Team A', role: 'general', color: 'sky' }, { key: 'g2', name: 'Team B', role: 'general', color: 'sky' }, { key: 'g3', name: 'Team C', role: 'general', color: 'sky' }],
      nodes: [{ key: 'o1', kind: 'orchestrator', x: 2400, y: 0, w: 420, h: 520, data: { title: 'Orchestrator' } }],
      wires: [{ from: 'o1', to: 'g1', kind: 'wire' }, { from: 'o1', to: 'g2', kind: 'wire' }, { from: 'o1', to: 'g3', kind: 'wire' }],
    },
    {
      id: 'builtin:lead-crew',
      name: 'Lead & crew',
      builtin: true,
      lanes: grid(4, 2, 'g1'),
      groups: [{ key: 'g1', name: 'Crew', role: 'general', color: 'sky' }],
      nodes: [{ key: 'o1', kind: 'orchestrator', x: 1640, y: 0, w: 420, h: 520, data: { title: 'Orchestrator' } }],
      wires: [{ from: 'o1', to: 'g1', kind: 'wire' }],
    },
    {
      id: 'builtin:full-stack',
      name: 'Full stack',
      builtin: true,
      lanes: [...grid(2, 2, 'g1', 'frontend', 0, 0, 'f'), ...grid(2, 2, 'g2', 'backend', 1640, 0, 'b'), ...grid(2, 2, 'g3', 'testers', 0, 576, 't'), ...grid(2, 2, 'g4', 'reviewers', 1640, 576, 'r')],
      groups: [{ key: 'g1', name: 'Frontend', role: 'frontend', color: 'violet' }, { key: 'g2', name: 'Backend', role: 'backend', color: 'teal' }, { key: 'g3', name: 'Testers', role: 'testers', color: 'amber' }, { key: 'g4', name: 'Review', role: 'reviewers', color: 'rose' }],
      nodes: [{ key: 'o1', kind: 'orchestrator', x: 3280, y: 0, w: 420, h: 520, data: { title: 'Orchestrator' } }],
      wires: [{ from: 'o1', to: 'g1', kind: 'wire' }, { from: 'o1', to: 'g2', kind: 'wire' }, { from: 'o1', to: 'g3', kind: 'wire' }, { from: 'o1', to: 'g4', kind: 'wire' }],
    },
    { id: 'builtin:code-pair', name: 'Code & pair', builtin: true, lanes: grid(2, 1, 'g1', undefined, 1360), groups: [{ key: 'g1', name: 'Pair', role: 'general', color: 'sky' }], nodes: [{ key: 'e1', kind: 'editor', x: 0, y: 0, w: 1240, h: 780, data: {} }], wires: [] },
    {
      id: 'builtin:web-builder',
      name: 'Web builder',
      builtin: true,
      lanes: [...grid(1, 1, 'g1', 'frontend', 0, 0, 'f'), ...grid(1, 1, 'g2', 'backend', 880, 0, 'b')],
      groups: [{ key: 'g1', name: 'Frontend', role: 'frontend', color: 'violet' }, { key: 'g2', name: 'Backend', role: 'backend', color: 'teal' }],
      nodes: [{ key: 'e1', kind: 'editor', x: 0, y: 576, w: 1240, h: 780, data: {} }, { key: 'w1', kind: 'web', x: 1360, y: 576, w: 960, h: 780, data: { url: 'http://localhost:5173/' } }],
      wires: [],
    },
    {
      id: 'builtin:bug-hunt',
      name: 'Bug hunt',
      builtin: true,
      lanes: [...grid(2, 2, 'g1', 'testers', 0, 0, 't'), ...grid(2, 2, 'g2', 'general', 0, 576, 'x')],
      groups: [{ key: 'g1', name: 'Testers', role: 'testers', color: 'amber' }, { key: 'g2', name: 'Fixers', role: 'general', color: 'sky' }],
      nodes: [{ key: 'o1', kind: 'orchestrator', x: 1640, y: 500, w: 420, h: 520, data: { title: 'Orchestrator' } }],
      wires: [{ from: 'o1', to: 'g1', kind: 'wire' }, { from: 'o1', to: 'g2', kind: 'wire' }],
    },
    {
      id: 'builtin:security-audit',
      name: 'Security audit',
      builtin: true,
      lanes: [...grid(2, 2, 'g1', 'security', 0, 0, 's'), ...grid(1, 1, 'g2', 'reviewers', 0, 576, 'r')],
      groups: [{ key: 'g1', name: 'Security', role: 'security', color: 'rose' }, { key: 'g2', name: 'Review', role: 'reviewers', color: 'rose' }],
      nodes: [{ key: 'c1', kind: 'checklist', x: 1640, y: 0, w: 420, h: 440, data: { title: 'Audit', items: [] } }],
      wires: [],
    },
    { id: 'builtin:design-studio', name: 'Design studio', builtin: true, lanes: grid(2, 2, 'g1', 'design'), groups: [{ key: 'g1', name: 'Design', role: 'design', color: 'violet' }], nodes: [{ key: 'w1', kind: 'web', x: 0, y: 576, w: 1280, h: 800, data: { url: 'http://localhost:5173/' } }], wires: [] },
    {
      id: 'builtin:release-crew',
      name: 'Release crew',
      builtin: true,
      lanes: [...grid(2, 2, 'g1', 'general', 0, 0, 'b'), ...grid(1, 1, 'g2', 'testers', 0, 576, 't'), ...grid(1, 1, 'g3', 'docs', 880, 576, 'd')],
      groups: [{ key: 'g1', name: 'Builders', role: 'general', color: 'sky' }, { key: 'g2', name: 'Testers', role: 'testers', color: 'amber' }, { key: 'g3', name: 'Docs', role: 'docs', color: 'sage' }],
      nodes: [{ key: 'o1', kind: 'orchestrator', x: 1760, y: 0, w: 420, h: 520, data: { title: 'Orchestrator' } }],
      wires: [{ from: 'o1', to: 'g1', kind: 'wire' }, { from: 'o1', to: 'g2', kind: 'wire' }, { from: 'o1', to: 'g3', kind: 'wire' }],
    },
    { id: 'builtin:research-desk', name: 'Research desk', builtin: true, lanes: grid(2, 2, 'g1'), groups: [{ key: 'g1', name: 'Research', role: 'general', color: 'sky' }], nodes: [{ key: 'm1', kind: 'memory', x: 1640, y: 0, w: 300, h: 196, data: { scope: 'project' } }], wires: [] },
  ];
  const deployLayout = async (wid: string, req: DeployRequest) => {
    const l = [...BUILTIN, ...saved].find((x) => x.id === req.layoutId);
    if (!l) fail(404, 'Layout not found.', 'layout_not_found');
    const items = [...(l!.lanes ?? []), ...(l!.nodes ?? [])];
    const minX = Math.min(...items.map((i) => i.x));
    const minY = Math.min(...items.map((i) => i.y));
    const maxX = Math.max(...items.map((i) => i.x + i.w));
    const maxY = Math.max(...items.map((i) => i.y + i.h));
    const b = await board(wid);
    const at = req.at ?? { x: Math.max(0, ...(b.nodes ?? []).map((n) => n.x + n.w)) + 80 + (maxX - minX) / 2, y: (maxY - minY) / 2 };
    const dx = at.x - (minX + maxX) / 2;
    const dy = at.y - (minY + maxY) / 2;
    const project = Object.values(store.getState().projects).find((p) => p.workspaces?.some((w) => w.id === wid));
    const made: Session[] = [];
    const keyNode = new Map<string, string>();
    for (const lane of l!.lanes ?? []) {
      const o = req.laneOverrides?.[lane.key] ?? {};
      const machineId = o.machineId ?? project?.machineId ?? 'local';
      const agent = o.agent || req.fill?.agent || lane.agent || (await defaultAgent(machineId, lane.role));
      const s = await hub<Session>('POST', '/api/sessions', { workspaceId: wid, machineId, preset: agent, agent, profileId: o.profileId, cols: 90, rows: 22, node: { x: Math.round(lane.x + dx), y: Math.round(lane.y + dy), w: lane.w, h: lane.h } });
      store.upsertSession(s);
      made.push(s);
    }
    await sleep(300);
    const after = await board(wid);
    (l!.lanes ?? []).forEach((lane, i) => {
      const n = nodeOfSession(after, made[i].id);
      if (n) keyNode.set(lane.key, n.id);
    });
    const list: Op[] = [];
    for (const g of l!.groups ?? []) {
      const members = (l!.lanes ?? []).filter((x) => x.group === g.key).map((x) => keyNode.get(x.key)).filter((x): x is string => !!x);
      const rects = members.map((m) => (after.nodes ?? []).find((n) => n.id === m)!).map((n) => ({ x: n.x, y: n.y, w: n.w, h: n.h }));
      const f = frameOf(rects);
      if (!f) continue;
      const id = mk('n');
      keyNode.set(g.key, id);
      const used = new Set((after.nodes ?? []).filter((n) => n.kind === 'group').map((n) => norm(readGroup(n).name)));
      let name = g.name;
      for (let i = 2; used.has(norm(name)); i++) name = `${g.name} ${i}`;
      list.push({ op: 'put', node: { id, kind: 'group', ...f, data: { name, role: g.role, color: g.color, members } } }, { op: 'z', id, to: 'back' });
    }
    for (const n of l!.nodes ?? []) {
      const id = mk('n');
      keyNode.set(n.key, id);
      const data = n.kind === 'kanban' ? { title: (n.data?.title as string) ?? 'Kanban', columns: ['Backlog', 'To do', 'In progress', 'Done'].map((t, i) => ({ id: `c${i}${rnd().slice(0, 4)}`, title: t, cards: [] })) } : (n.data ?? {});
      list.push({ op: 'put', node: { id, kind: n.kind, x: Math.round(n.x + dx), y: Math.round(n.y + dy), w: n.w, h: n.h, data } });
    }
    for (const w of l!.wires ?? []) {
      const s = keyNode.get(w.from);
      const t = keyNode.get(w.to);
      if (s && t) list.push({ op: 'putEdge', edge: { id: mk('e'), source: s, target: t, kind: w.kind, data: w.kind === 'forward' ? { mode: 'digest', trigger: 'turn' } : {} } });
    }
    if (list.length) await ops(wid, list);
    return { sessions: made, nodeIds: [...keyNode.values()] };
  };
  const saveLayout = async (body: { name?: string; workspaceId: string; nodeIds?: string[] }) => {
    const b = await board(body.workspaceId);
    const take = new Set(body.nodeIds?.length ? body.nodeIds : (b.nodes ?? []).map((n) => n.id));
    for (const n of b.nodes ?? []) if (n.kind === 'group' && take.has(n.id)) readGroup(n).members.forEach((m) => take.add(m));
    const nodes = (b.nodes ?? []).filter((n) => take.has(n.id));
    const groupOfNode = new Map<string, string>();
    for (const g of nodes.filter((n) => n.kind === 'group')) for (const m of readGroup(g).members) groupOfNode.set(m, g.id);
    const lanes = nodes
      .filter((n) => n.kind === 'terminal')
      .map((n, i) => ({ key: `l${i + 1}`, id: n.id, agent: sessions()[String(n.data.sessionId)]?.agent, group: groupOfNode.get(n.id), x: n.x, y: n.y, w: n.w, h: n.h }));
    const keep = ['orchestrator', 'kanban', 'note', 'text', 'checklist', 'memory'];
    const others = nodes.filter((n) => keep.includes(n.kind)).map((n, i) => ({ key: `n${i + 1}`, id: n.id, kind: n.kind, x: n.x, y: n.y, w: n.w, h: n.h, data: n.kind === 'kanban' ? { title: n.data.title } : n.kind === 'checklist' ? { title: n.data.title, items: [] } : { ...n.data, lastPush: undefined } }));
    const key = new Map<string, string>([...lanes.map((l) => [l.id, l.key] as [string, string]), ...others.map((o) => [o.id, o.key] as [string, string])]);
    const groups = nodes.filter((n) => n.kind === 'group').map((g) => {
      key.set(g.id, g.id);
      const d = readGroup(g);
      return { key: g.id, name: d.name, role: d.role, color: d.color };
    });
    const wires = (b.edges ?? []).filter((e) => key.has(e.source) && key.has(e.target)).map((e) => ({ from: key.get(e.source)!, to: key.get(e.target)!, kind: e.kind }));
    let name = body.name?.trim() || '';
    for (let i = 1; !name || saved.some((x) => x.name === name); i++) name = body.name?.trim() && i === 1 ? body.name.trim() : `Set ${i}`;
    const l: Layout = { id: mk('ly'), name, builtin: false, createdAt: iso(), lanes: lanes.map(({ id: _i, ...r }) => r), groups, nodes: others.map(({ id: _i, ...r }) => r), wires };
    saved.push(l);
    persist();
    emit({ type: 'layouts' });
    return l;
  };

  // ---- router ----
  const routes: [HttpMethod, RegExp, (m: RegExpMatchArray, body: unknown) => unknown | Promise<unknown>][] = [
    ['POST', /^\/api\/sessions\/([^/]+)\/deliveries$/, (m, b) => {
      const body = b as { text: string; source?: string; guard?: string; deadlineSec?: number };
      const s = sessions()[m[1]];
      if (body.guard === 'dictation' && (!s || s.agentState !== 'ready')) fail(409, 'The agent is busy.', 'target_busy');
      return deliver(m[1], body.text, body.source ?? 'api', body.deadlineSec ? { deadline: String(body.deadlineSec) } : {});
    }],
    ['GET', /^\/api\/sessions\/([^/]+)\/deliveries$/, (m) => ({ items: [...deliveries.values()].filter((d) => d.sessionId === m[1]).slice(-40) })],
    ['GET', /^\/api\/deliveries$/, () => ({ items: [...deliveries.values()].filter((d) => ['queued', 'waiting', 'typing'].includes(d.state)) })],
    ['DELETE', /^\/api\/deliveries\/([^/]+)$/, (m) => {
      const d = deliveries.get(m[1]);
      if (!d) fail(404, 'Not found.');
      if (d!.state === 'typing' || d!.state === 'sent') fail(409, 'Already typed.', 'already_sent');
      setDelivery(d!, { state: 'cancelled' });
      return { ok: true };
    }],
    ['POST', /^\/api\/projects\/([^/]+)\/commands$/, (m, b) => command(m[1], b as CommandRequest)],
    ['GET', /^\/api\/planner$/, () => ({ available: false, reason: 'claude_missing' })],
    ['POST', /^\/api\/confirmations\/([^/]+)$/, (m, b) => decide(m[1], (b as { decision: string }).decision, (b as { choice?: string }).choice)],
    ['GET', /^\/api\/threads\/([^/]+)\/messages/, (m) => ({ items: (threads[decodeURIComponent(m[1])] ?? []).slice(-50), hasMore: (threads[decodeURIComponent(m[1])] ?? []).length > 50 })],
    ['DELETE', /^\/api\/threads\/([^/]+)$/, (m) => {
      threads[m[1]] = [];
      persist();
      emit({ type: 'threadCleared', threadId: m[1] });
      return { ok: true };
    }],
    ['GET', /^\/api\/threads\/([^/]+)\/orders/, (m) => ({ items: [...orders.values()].filter((o) => o.threadId === m[1]) })],
    ['GET', /^\/api\/orders\/([^/]+)$/, (m) => orders.get(m[1]) ?? fail(404, 'Not found.')],
    ['POST', /^\/api\/orders\/([^/]+)\/cancel$/, (m) => cancelOrder(m[1])],
    ['POST', /^\/api\/workspaces\/([^/]+)\/spawn$/, (m, b) => spawn(m[1], b as SpawnRequest)],
    ['POST', /^\/api\/sessions\/([^/]+)\/(pause|resume)$/, (m) => {
      const s = sessions()[m[1]];
      if (!s) fail(404, 'Not found.');
      if (m[2] === 'pause') {
        if (s!.agentKind === 'shell' || s!.agentState === 'shell') fail(409, 'Not an agent.', 'not_agent');
        paused.set(s!.id, iso());
      } else paused.delete(s!.id);
      refresh(s!.id);
      return augment(sessions()[s!.id]);
    }],
    ['POST', /^\/api\/workspaces\/([^/]+)\/(pause|resume)-all$/, (m) => {
      const list = agentsIn(m[1]);
      for (const s of list) {
        if (m[2] === 'pause') paused.set(s.id, iso());
        else paused.delete(s.id);
        refresh(s.id);
      }
      return { sessions: list.map((s) => s.id) };
    }],
    ['POST', /^\/api\/sessions\/([^/]+)\/handoff$/, (m, b) => startHandoff(m[1], b as { agent: string })],
    ['GET', /^\/api\/projects\/([^/]+)\/forwards$/, (m) => ({ items: [...forwards.values()].filter((f) => f.projectId === m[1]) })],
    ['DELETE', /^\/api\/forwards\/([^/]+)$/, (m) => {
      const f = forwards.get(m[1]);
      if (f) {
        f.state = 'cancelled';
        emit({ type: 'forward', forward: f });
      }
      return { ok: true };
    }],
    ['GET', /^\/api\/loops\/templates$/, () => []],
    ['GET', /^\/api\/projects\/([^/]+)\/loops$/, (m) => Object.values(loops).filter((l) => l.projectId === m[1])],
    ['POST', /^\/api\/projects\/([^/]+)\/loops$/, (m, b) => createLoop(m[1], b as LoopInput)],
    ['POST', /^\/api\/loops\/stop-all$/, () => {
      const run = Object.values(loops).filter((l) => l.state?.status === 'running');
      run.forEach((l) => stopLoop(l, 'user', 'stopped'));
      return { stopped: run.length };
    }],
    ['PATCH', /^\/api\/loops\/([^/]+)$/, (m, b) => {
      const l = loops[m[1]] ?? fail(404, 'Not found.');
      const n = { ...l, ...(b as Partial<LoopDef>) } as LoopDef;
      void refreshMembers(n);
      return putLoop(n);
    }],
    ['DELETE', /^\/api\/loops\/([^/]+)$/, (m) => {
      delete loops[m[1]];
      persist();
      emit({ type: 'loopRemoved', id: m[1] });
      return { ok: true };
    }],
    ['POST', /^\/api\/loops\/([^/]+)\/(start|stop|run-now)$/, async (m) => {
      const l = loops[m[1]] ?? fail(404, 'Not found.');
      if (m[2] === 'stop') return stopLoop(l, 'user', 'stopped');
      if (m[2] === 'start')
        return putLoop({ ...l, enabled: true, state: { status: 'running', runs: 0, startedAt: iso(), nextRunAt: l.trigger.kind === 'interval' ? iso(Date.now() + l.trigger.everySec * 1000) : undefined } });
      await runLoop(l);
      return loops[l.id];
    }],
    ['GET', /^\/api\/loops\/([^/]+)\/history/, (m) => ({ items: loopHist[m[1]] ?? [] })],
    ['GET', /^\/api\/projects\/([^/]+)\/memory$/, (m) => ({ project: memDoc(m[1]), global: memDoc('global') })],
    ['PUT', /^\/api\/projects\/([^/]+)\/memory$/, (m, b) => {
      const body = b as { scope: string; content: string; ifMtime?: string };
      const key = body.scope === 'global' ? 'global' : m[1];
      if (body.ifMtime && memDoc(key).mtime && body.ifMtime !== memDoc(key).mtime) fail(409, 'Changed elsewhere.', 'mtime_mismatch');
      memory[key] = { content: body.content, mtime: iso() };
      persist();
      emit({ type: 'memory', projectId: m[1], scope: body.scope, mtime: memory[key].mtime });
      return memory[key];
    }],
    ['POST', /^\/api\/projects\/([^/]+)\/memory\/scan$/, (m) => {
      const p = store.getState().projects[m[1]];
      const j: Job = { id: mk('jb'), kind: 'runner', purpose: 'memory-scan', projectId: m[1], state: 'running', progress: { phase: 'thinking', text: 'Reading the project…' }, createdAt: iso(), startedAt: iso() };
      jobs.set(j.id, j);
      emit({ type: 'job', job: j });
      window.setTimeout(() => {
        const facts: Fact[] = [
          { kind: 'Structure', text: `The project lives at ${p?.path ?? 'its folder'}; source code is under src/.` },
          { kind: 'Context', text: 'README.md describes the project in one paragraph.' },
          { kind: 'Commands', text: 'Run the tests with `npm test`.' },
          { kind: 'Conventions', text: 'TypeScript, two-space indentation, single quotes.' },
          { kind: 'Gotchas', text: 'The app entry point is src/app.ts; it exports constants only.' },
          { kind: 'Architecture', text: 'A small single-module app without a backend.' },
        ];
        const done: Job = { ...j, state: 'done', finishedAt: iso(), elapsedMs: 1800, progress: { phase: 'finishing', text: 'Done' }, result: { facts } };
        jobs.set(j.id, done);
        emit({ type: 'job', job: done });
      }, 1800);
      return j;
    }],
    ['POST', /^\/api\/projects\/([^/]+)\/memory\/accept$/, (m, b) => {
      const body = b as { scope: string; facts: Fact[] };
      const key = body.scope === 'global' ? 'global' : m[1];
      let content = memDoc(key).content;
      for (const f of body.facts) {
        const bullet = `- ${f.text}`;
        if (content.includes(bullet)) continue;
        const head = `## ${f.kind}`;
        if (content.includes(head)) content = content.replace(head, `${head}\n${bullet}`);
        else content = `${content.trimEnd()}\n\n${head}\n${bullet}\n`.trimStart();
      }
      memory[key] = { content, mtime: iso() };
      persist();
      emit({ type: 'memory', projectId: m[1], scope: body.scope, mtime: memory[key].mtime });
      return memory[key];
    }],
    ['POST', /^\/api\/projects\/([^/]+)\/memory\/push$/, async (m, b) => {
      const body = b as { memoryNodeId?: string; targets?: string[] };
      let targets: Session[] = [];
      if (body.targets?.length) targets = body.targets.map((id) => sessions()[id]).filter(Boolean);
      else if (body.memoryNodeId) {
        const wid = Object.values(sessions()).find((s) => s.projectId === m[1])?.workspaceId ?? '';
        const bd = await board(wid);
        const ids = directedTerminals(bd.nodes ?? [], bd.edges ?? [], body.memoryNodeId);
        targets = ids.map((id) => (bd.nodes ?? []).find((n) => n.id === id)).map((n) => (n ? sessions()[String(n.data.sessionId)] : undefined)).filter((x): x is Session => !!x);
      }
      return pushMemory(m[1], targets);
    }],
    ['GET', /^\/api\/jobs\/([^/]+)$/, (m) => jobs.get(m[1]) ?? fail(404, 'Job not found.', 'job_not_found')],
    ['POST', /^\/api\/jobs\/([^/]+)\/cancel$/, (m) => {
      const j = jobs.get(m[1]) ?? fail(404, 'Job not found.', 'job_not_found');
      const n = { ...(j as Job), state: 'cancelled' };
      jobs.set(n.id, n);
      emit({ type: 'job', job: n });
      return n;
    }],
    ['GET', /^\/api\/layouts$/, () => [...BUILTIN, ...saved]],
    ['POST', /^\/api\/layouts$/, (_m, b) => saveLayout(b as { workspaceId: string })],
    ['PATCH', /^\/api\/layouts\/([^/]+)$/, (m, b) => {
      const l = saved.find((x) => x.id === m[1]) ?? fail(403, 'Built-in layouts cannot change.', 'layout_builtin');
      (l as Layout).name = (b as { name: string }).name;
      persist();
      emit({ type: 'layouts' });
      return l;
    }],
    ['DELETE', /^\/api\/layouts\/([^/]+)$/, (m) => {
      const i = saved.findIndex((x) => x.id === m[1]);
      if (i < 0) fail(403, 'Built-in layouts cannot be deleted.', 'layout_builtin');
      saved.splice(i, 1);
      persist();
      emit({ type: 'layouts' });
      return { ok: true };
    }],
    ['POST', /^\/api\/workspaces\/([^/]+)\/deploy$/, (m, b) => deployLayout(m[1], b as DeployRequest)],
  ];

  // eslint-disable-next-line no-console
  console.info('[nextboard] orchestration mock is ON (localStorage nb.orchMock). Phase 6 endpoints are served in the browser.');

  return async <T,>(method: HttpMethod, path: string, body?: unknown): Promise<T> => {
    const p = path.split('?')[0];
    for (const [m, re, fn] of routes) {
      if (m !== method) continue;
      const hit = p.match(re);
      if (hit) return (await fn(hit, body)) as T;
    }
    return hub<T>(method, path, body);
  };
}
