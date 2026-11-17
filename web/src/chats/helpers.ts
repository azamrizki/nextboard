// What the chat lists show, worked out from the hub's views: day groups, the place chip, the
// action a row offers, the handle clash and the swap rules. Pure, so the tests read like the
// mockups (/home/…/mockups/resume-chat).

import { parseTime } from '../lib/format';
import { projectParts } from '../lib/parts';
import { ROOT_VALUE } from '../lib/placement';
import type { Project, Session } from '../lib/types';
import type { ThreadMessage } from '../orch/types';
import type { Chat, ChatChanges, ChatView, HandleTaken, LeadChat } from './types';

// ---- days ---------------------------------------------------------------------------

/** "Today", "Yesterday", "Earlier this week" (2–6 days) or "Earlier", by the calendar day. */
export function dayGroup(iso: string | null | undefined, now = new Date()): string {
  const d = parseTime(iso);
  if (!d) return 'Earlier';
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((start(now) - start(d)) / 86400000);
  if (days <= 0) return 'Today';
  if (days === 1) return 'Yesterday';
  if (days < 7) return 'Earlier this week';
  return 'Earlier';
}

/** Runs of the same day group, in the order given (newest first in, newest group first out). */
export function groupByDay<T>(items: T[], at: (t: T) => string, now = new Date()): { title: string; items: T[] }[] {
  const out: { title: string; items: T[] }[] = [];
  for (const it of items) {
    const g = dayGroup(at(it), now);
    const last = out[out.length - 1];
    if (last?.title === g) last.items.push(it);
    else out.push({ title: g, items: [it] });
  }
  return out;
}

/** Nanoseconds past the millisecond of an RFC 3339 time ("…57.248371337+07:00" → 371337). */
function subMs(iso: string): number {
  const f = /\.(\d+)/.exec(iso)?.[1] ?? '';
  return Number((f + '000000000').slice(3, 9));
}

/** a is strictly later than b (the hub's times carry nanoseconds; Date keeps milliseconds). */
export function laterThan(a: string | null | undefined, b: string | null | undefined): boolean {
  const ta = a ? Date.parse(a) : NaN;
  const tb = b ? Date.parse(b) : NaN;
  if (Number.isNaN(ta)) return false;
  if (Number.isNaN(tb)) return true;
  return ta !== tb ? ta > tb : subMs(a!) > subMs(b!);
}

/** Newest first: the last message, then the start. */
export const byLast = (a: Pick<Chat, 'lastAt' | 'startedAt' | 'id'>, b: Pick<Chat, 'lastAt' | 'startedAt' | 'id'>) =>
  (b.lastAt || b.startedAt || '').localeCompare(a.lastAt || a.startedAt || '') || a.id.localeCompare(b.id);

export const msgsLabel = (n: number) => `${n} ${n === 1 ? 'msg' : 'msgs'}`;

// ---- place --------------------------------------------------------------------------

/** The value a chat's place has in the place filters: ROOT_VALUE, a part id, or "" (no part). */
export function chatPlaceValue(c: Pick<Chat, 'root' | 'partId'>): string {
  if (c.root) return ROOT_VALUE;
  return c.partId ?? '';
}

export interface PlaceChip {
  label: string;
  /** Machine and folder, for the tooltip. */
  title: string;
  kind: 'root' | 'part' | 'none' | 'orphan';
}

/**
 * "Root · all parts", "api · devbox" (the part and the machine the work is on: a server part's,
 * though its CLI ran on This PC), or "legacy · part removed" for a chat that cannot go back.
 */
export function placeChip(c: ChatView, project: Project | undefined, machineName: (id: string) => string): PlaceChip {
  const folder = c.remoteCwd || c.cwd;
  const part = c.partId && project ? projectParts(project).find((p) => p.id === c.partId) : undefined;
  const machineId = c.remoteMachineId || part?.machineId || c.machineId;
  const machine = machineName(machineId) || c.machineName || machineId;
  const title = `${folder}${machine ? ` on ${machine}` : ''}`;
  const partName = part?.name || c.partName || (c.placeLabel && c.placeLabel !== 'Root' ? c.placeLabel : '');
  if (c.status === 'orphan') {
    const gone = c.orphanReason ? c.orphanReason === 'part_removed' : !!c.partId && !!project && !part;
    return { label: `${partName || (c.root ? 'Root' : 'folder')} · ${gone ? 'part removed' : 'folder missing'}`, title, kind: 'orphan' };
  }
  if (c.root) return { label: 'Root · all parts', title, kind: 'root' };
  if (partName) return { label: `${partName} · ${machine}`, title, kind: 'part' };
  return { label: machine || 'This PC', title, kind: 'none' };
}

// ---- status and what a row offers -----------------------------------------------------

export type ChatAction = 'show' | 'wake' | 'resume' | 'orphan';

/** live → Show (focus the console), asleep → Wake, closed → Resume, orphan → Transcript / Continue elsewhere. */
export function chatAction(c: Pick<ChatView, 'status' | 'sessionId'>): ChatAction {
  switch (c.status) {
    case 'live':
      return c.sessionId ? 'show' : 'resume';
    case 'asleep':
      return c.sessionId ? 'wake' : 'resume';
    case 'orphan':
      return 'orphan';
    default:
      return 'resume';
  }
}

/** The handle of the console holding it now (it may have been renamed since the chat recorded it). */
export function holderHandle(c: Pick<ChatView, 'sessionId' | 'handle'>, sessions: Record<string, Pick<Session, 'handle'> | undefined>): string {
  return (c.sessionId && sessions[c.sessionId]?.handle) || c.handle;
}

/** "live in @Arkan", "asleep in @Daffa", "closed", "part removed". */
export function statusText(c: ChatView, sessions: Record<string, Pick<Session, 'handle'> | undefined>): string {
  const h = holderHandle(c, sessions);
  switch (c.status) {
    case 'live':
      return h ? `live in @${h}` : 'live';
    case 'asleep':
      return h ? `asleep in @${h}` : 'asleep';
    case 'orphan':
      return 'cannot resume here';
    default:
      return 'closed';
  }
}

// ---- filters ------------------------------------------------------------------------

export interface ChatFilter {
  q?: string;
  agent?: string;
  /** "" = all places, ROOT_VALUE, or a part id. */
  place?: string;
  /** Ids the hub found for q in the transcripts (beyond the titles). */
  found?: ReadonlySet<string>;
}

export function filterChats<T extends ChatView>(items: T[], f: ChatFilter): T[] {
  const q = (f.q ?? '').trim().toLowerCase();
  return items.filter((c) => {
    if (c.archived) return false;
    if (f.agent && c.agent !== f.agent) return false;
    if (f.place && chatPlaceValue(c) !== f.place) return false;
    if (q && !c.title.toLowerCase().includes(q) && !f.found?.has(c.id)) return false;
    return true;
  });
}

// ---- the old handle, and swapping into a console ------------------------------------

/** The hub's reasons (internal/chats SwapReason), and "gone": no such console here. */
export type SwapReason = '' | 'busy' | 'other_folder' | 'other_agent' | 'gone';

/** A folder as Go's path.Clean has it: no trailing slash, no "." or "..", no doubled slashes. */
export function cleanPath(p: string): string {
  if (!p) return '.';
  const rooted = p.startsWith('/');
  const out: string[] = [];
  for (const seg of p.split('/')) {
    if (!seg || seg === '.') continue;
    if (seg === '..') {
      if (out.length && out[out.length - 1] !== '..') out.pop();
      else if (!rooted) out.push('..');
      continue;
    }
    out.push(seg);
  }
  const joined = out.join('/');
  return rooted ? `/${joined}` : joined || '.';
}

const same = (a: string | undefined, b: string | undefined) => (a ?? '') === (b ?? '');

/**
 * Whether a chat can move into this console, as the hub decides it (chats.SwapReason, which
 * this mirrors rule for rule): the same agent and login, in the same project, machine and folder
 * (and server, and root or not), idle at its agent's prompt or at its shell, or its agent ended.
 * A sign-in console runs "another agent"; asleep, paused, a delivery queued or an agent typed by
 * hand is busy.
 */
export function swapCheck(c: Chat, s: Session | undefined): SwapReason {
  if (!s) return 'gone';
  if (s.agentKind !== 'agent' || s.signIn || !same(s.agent, c.agent) || !same(s.profileId, c.profileId) || !same(s.providerId, c.providerId)) return 'other_agent';
  if (!same(s.projectId, c.projectId) || !same(s.machineId, c.machineId) || cleanPath(s.cwd ?? '') !== cleanPath(c.cwd ?? '') || !same(s.remoteMachineId, c.remoteMachineId) || !!s.root !== !!c.root) {
    return 'other_folder';
  }
  if (s.status !== 'running' || s.paused || (s.queue ?? 0) > 0 || s.manualAgent) return 'busy';
  return s.agentState === 'ready' || s.agentState === 'shell' || s.agentState === 'exited' ? '' : 'busy';
}

/** The hub's 409 not_swappable for a console, and the console as it was then. */
export interface Refusal {
  reason: string;
  stamp: string;
}

/** The hub's refusal while the console is as it was then; "" once it changed (idle again, say). */
export function refusedNow(r: Refusal | undefined, s: Session | undefined): string {
  return r && r.stamp === swapStamp(s) ? r.reason : '';
}

/** What swapCheck reads of a console: when it changes, an earlier "cannot take it" is stale. */
export function swapStamp(s: Session | undefined): string {
  if (!s) return '';
  return [s.agentKind, s.signIn, s.agent, s.profileId, s.providerId, s.projectId, s.machineId, s.cwd, s.remoteMachineId, s.root, s.status, s.paused, s.queue, s.manualAgent, s.agentState].map((v) => String(v ?? '')).join('|');
}

/** The reason in a few words, for "Into @Bintang (Codex, busy)". */
export function swapReasonShort(r: SwapReason | string): string {
  switch (r) {
    case 'busy':
      return 'busy';
    case 'other_folder':
      return 'in another folder';
    case 'other_agent':
      return 'runs another agent';
    case 'gone':
      return 'no longer running';
    default:
      return r ? r.replace(/_/g, ' ') : '';
  }
}

export function swapReasonText(r: SwapReason | string, handle: string): string {
  const who = handle ? `@${handle}` : 'That console';
  switch (r) {
    case 'busy':
      return `${who} is busy: only an idle console can take a chat.`;
    case 'other_folder':
      return `${who} works in another folder.`;
    case 'other_agent':
      return `${who} runs another agent.`;
    case 'gone':
      return `${who} is no longer running.`;
    default:
      return r ? `${who} cannot take this chat (${r}).` : '';
  }
}

/** "idle at the shell in web", "idle at its prompt": where a console in the swap choice is. */
export function idleWhere(s: Session, place: string): string {
  const at = s.agentState === 'shell' ? 'idle at the shell' : 'idle at its prompt';
  return place ? `${at} in ${place}` : at;
}

/** The other consoles in the chat's folder running its agent: what "Into @X" offers. */
export function swapCandidates(c: ChatView, sessions: Record<string, Session | undefined>): { session: Session; reason: SwapReason }[] {
  const out: { session: Session; reason: SwapReason }[] = [];
  for (const s of Object.values(sessions)) {
    if (!s || s.id === c.sessionId || s.panel) continue;
    const reason = swapCheck(c, s);
    if (reason === 'other_agent' || reason === 'other_folder' || reason === 'gone') continue;
    out.push({ session: s, reason });
  }
  return out.sort((a, b) => Number(!!a.reason) - Number(!!b.reason) || (a.session.handle ?? '').localeCompare(b.session.handle ?? ''));
}

export interface Clash {
  /** The chat's old handle. */
  taken: string;
  /** The console that holds it now. */
  by: string;
  /** That console's chat, when known. */
  byChat: string;
  /** The next free name ("" until the hub has said). */
  next: string;
  /** Whether the chat can be swapped into it, and why not. */
  reason: SwapReason | string;
}

/**
 * The chat's old handle held by another console: from the hub when it said so (409
 * handle_taken, or the chat's detail), else from the consoles this client knows (then the next
 * free name is not known until the hub answers). Whether it can swap follows that console as it
 * is now (the hub's answer was one moment's); the hub's word counts for a console unknown here.
 */
export function clashOf(c: ChatView, sessions: Record<string, Session | undefined>, hub?: HandleTaken | null): Clash | null {
  if (hub?.taken) {
    const s = sessions[hub.by];
    const reason = s ? swapCheck(c, s) : hub.swappable === true ? '' : hub.swappable === false ? hub.reason || 'busy' : 'gone';
    return { taken: hub.taken, by: hub.by, byChat: hub.byChat ?? '', next: hub.next ?? '', reason };
  }
  if (!c.handle) return null;
  const s = Object.values(sessions).find((x) => x && x.handle === c.handle && x.id !== c.sessionId);
  if (!s) return null;
  return { taken: c.handle, by: s.id, byChat: '', next: '', reason: swapCheck(c, s) };
}

/**
 * The console in a resume's or continue's answer (201: the session as POST /api/sessions has it,
 * with chatId beside it; an older shape nests it as `session`), or null.
 */
export function sessionOfResult(r: unknown): Session | null {
  if (!r || typeof r !== 'object') return null;
  const o = r as { session?: unknown; id?: unknown };
  const s = (o.session && typeof o.session === 'object' ? o.session : o) as Session;
  return typeof s.id === 'string' && s.id.startsWith('s_') ? s : null;
}

/** A 409 handle_taken body, read defensively. */
export function readHandleTaken(body: Record<string, unknown> | undefined): HandleTaken | null {
  if (!body || typeof body.taken !== 'string' || !body.taken) return null;
  return {
    taken: body.taken,
    by: typeof body.by === 'string' ? body.by : '',
    byHandle: typeof body.byHandle === 'string' ? body.byHandle : undefined,
    byChat: typeof body.byChat === 'string' ? body.byChat : undefined,
    next: typeof body.next === 'string' ? body.next : '',
    swappable: typeof body.swappable === 'boolean' ? body.swappable : undefined,
    reason: typeof body.reason === 'string' ? body.reason : undefined,
  };
}

/** A typed handle as the hub takes it: without the @. */
export const cleanHandle = (h: string) => h.trim().replace(/^@+/, '');

// ---- what changed since -------------------------------------------------------------

/** "6 new commits in web, 3 files changed, 2 memory facts", or "" when nothing changed. */
export function changesLine(ch: ChatChanges | null | undefined): string {
  if (!ch) return '';
  if (ch.summary) return ch.summary;
  const parts: string[] = [];
  for (const c of ch.commits ?? []) {
    const n = c.count ?? c.subjects?.length ?? 0;
    if (!n) continue;
    const where = c.part || c.partId;
    parts.push(`${n} new ${n === 1 ? 'commit' : 'commits'}${where ? ` in ${where}` : ''}`);
  }
  if (ch.files) parts.push(`${ch.files} ${ch.files === 1 ? 'file' : 'files'} changed`);
  const facts = ch.facts?.length ?? 0;
  if (facts) parts.push(`${facts} new memory ${facts === 1 ? 'fact' : 'facts'}`);
  return parts.join(', ');
}

// ---- lead chats ---------------------------------------------------------------------

/** The thread note the hub writes when a chat is resumed (source "resume"): drawn as a divider. */
export function isResumeDivider(m: Pick<ThreadMessage, 'kind' | 'role' | 'text' | 'source'>): boolean {
  if (m.source === 'resume') return true;
  return m.kind === 'note' && m.role === 'hub' && !m.source && /^Resumed —/.test(m.text);
}

/** A message of the chat the thread shows: an older hub, or a message without one, always is. */
export function inChat(m: Pick<ThreadMessage, 'chatId'>, active: string): boolean {
  return !active || !m.chatId || m.chatId === active;
}

/** The lead's chats, the current one first, then newest first. */
export function sortLeadChats(items: LeadChat[], active: string): LeadChat[] {
  return items.slice().sort((a, b) => Number(b.id === active) - Number(a.id === active) || (b.lastAt || b.createdAt).localeCompare(a.lastAt || a.createdAt));
}

/** "today", "yesterday", "2 days ago", "last week", or the date: how far back a lead chat is. */
export function daysAgo(iso: string | null | undefined, now = new Date()): string {
  const d = parseTime(iso);
  if (!d) return '';
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((start(now) - start(d)) / 86400000);
  if (days <= 0) return 'today';
  if (days === 1) return 'yesterday';
  if (days < 7) return `${days} days ago`;
  if (days < 14) return 'last week';
  return d.toLocaleDateString(undefined, d.getFullYear() === now.getFullYear() ? { month: 'short', day: 'numeric' } : { month: 'short', year: 'numeric' });
}

/** How far the command bar's closed thread was read: the chat in use then, and its message count. */
export interface Seen {
  chat: string;
  count: number;
}

/**
 * The mark after a change: an open thread is read up to its end; another chat in use (once its
 * messages are loaded) starts read, so the badge never counts the old chat's messages.
 */
export function nextSeen(seen: Seen | null, now: { open: boolean; ready: boolean; chat: string; count: number }): Seen | null {
  const mark = { chat: now.chat, count: now.count };
  if (now.open) return seen && seen.chat === mark.chat && seen.count === mark.count ? seen : mark;
  if (now.ready && (!seen || seen.chat !== now.chat)) return mark;
  return seen;
}

/** New messages since the mark, in the chat in use. */
export function unreadOf(seen: Seen | null, open: boolean, chat: string, count: number): number {
  return open || !seen || seen.chat !== chat ? 0 : Math.max(0, count - seen.count);
}

/** A chat something was said in: an empty one (New chat, never used) is not offered to go back to. */
export const spoken = (c: Pick<LeadChat, 'messages'>) => c.messages > 0;

/** "New chat" until the owner's first message names it. */
export const leadChatTitle = (c: Pick<LeadChat, 'title'>) => c.title?.trim() || 'New chat';
