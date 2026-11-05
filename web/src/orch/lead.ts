// The lead agent (docs/API-orchestration.md §3.9): the orchestrator as a person. The hub has
// one; a card named by the owner is its own.

import { useEffectiveSettings } from '../agents/settings';
import type { Settings } from '../lib/types';
import type { ThreadMessage } from './types';

const DEFAULT_TITLE = /^Orchestrator( \d+)?$/;
const FALLBACK = 'Orchestrator';

export function leadNameOf(s: Settings, cardTitle?: string): string {
  const t = cardTitle?.trim();
  return t && !DEFAULT_TITLE.test(t) ? t : s.lead?.name || FALLBACK;
}

export function useLeadName(cardTitle?: string): string {
  return leadNameOf(useEffectiveSettings(), cardTitle);
}

export function useTalkFirst(): boolean {
  return useEffectiveSettings().lead?.talkFirst !== false;
}

/** An answer being written to an earlier message of the thread. */
export interface ReplyDraft {
  id: string;
  /** Goes to this agent; none = to the lead. */
  handle?: string;
  /** For the chip above the box. */
  label: string;
  excerpt: string;
}

/** What a message says, as plain text. */
export function messageText(m: ThreadMessage): string {
  if (m.plan?.length) return m.plan.map((p) => `@${p.handle}: ${p.text}`).join('\n');
  return m.text || m.report?.excerpt || '';
}

/** Who a message is from, by the name the thread shows. */
export function messageAuthor(m: ThreadMessage, lead: string): string {
  if (m.role === 'user') return 'You';
  if (m.handle) return `@${m.handle}`;
  if (m.plan?.length === 1) return `@${m.plan[0].handle}`;
  return lead;
}

export function replyDraftOf(m: ThreadMessage, lead: string, toLead = false): ReplyDraft {
  const handle = toLead ? undefined : (m.handle ?? (m.plan?.length === 1 ? m.plan[0].handle : undefined));
  const text = m.report?.excerpt || (m.plan?.length ? m.plan[0].text : m.text);
  const author = messageAuthor(m, lead);
  const label = handle ? `To @${handle}` : author === lead ? `To ${lead}` : `Asking ${lead} about ${author}`;
  return { id: m.id, handle, label, excerpt: text.replace(/\s+/g, ' ').slice(0, 200) };
}

/** The text to send: an answer to an agent's message goes to that agent. */
export function replyText(text: string, reply: ReplyDraft | null): string {
  return reply?.handle && !text.startsWith('@') ? `@${reply.handle} ${text}` : text;
}
