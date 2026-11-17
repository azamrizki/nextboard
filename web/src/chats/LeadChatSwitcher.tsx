// The lead's chats in a thread's header (mockup 3): "<chat title> ▾" opens the list of chats with
// the lead in this thread (the current one marked, the others with Resume) and + New chat. The
// command bar's list also holds the chats of the board's orchestrator cards: resuming one shows
// that card. Every lead thread has it: the command bar, an orchestrator card, the editor's
// orchestrator pane (docs/DESIGN.md §3.5 Chats).

import { useRef, useState } from 'react';
import { isCode } from '../api';
import { useOrch } from '../orch/store';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Popover } from '../components/Popover';
import { toast } from '../components/Toast';
import { orchError } from '../orch/actions';
import { useMediaQuery } from '../lib/useMediaQuery';
import { LeadChatRow } from './LeadChatRow';
import { leadChatTitle, sortLeadChats, spoken } from './helpers';
import { chatsStore, useLeadChats } from './store';
import type { LeadChat } from './types';
import '../styles/chats.css';

/** Another thread whose chats the list shows too: an orchestrator card's. */
export interface OtherThread {
  id: string;
  /** The card's name. */
  card: string;
}

const busyText = (lead: string) => `${lead} is still answering in this chat. Start a new one when the answer is in.`;

/** Why a lead chat could not be changed or deleted, in words. */
export function leadChatError(err: unknown, lead: string): string {
  if (isCode(err, 'chat_active')) return 'This is the chat in use. Resume another one first, then delete this one.';
  if (isCode(err, 'chat_mission')) return 'A mission that has not ended writes to this chat. Stop it or let it finish first.';
  if (isCode(err, 'lead_busy')) return `${lead} is still answering in this chat. Try again when the answer is in.`;
  if (isCode(err, 'bad_title')) return 'The title is empty.';
  return orchError(err);
}

/** + New chat: the chat in use is kept, the lead starts fresh (the project memory stays). */
export async function newLeadChat(threadId: string, lead: string): Promise<boolean> {
  try {
    await chatsStore.newLeadChat(threadId);
    return true;
  } catch (err) {
    toast.error(isCode(err, 'lead_busy') ? busyText(lead) : `Could not start a new chat: ${orchError(err)}`);
    return false;
  }
}

/** Resume one of a thread's chats (the hub writes the divider that says what the lead has of it). */
export async function resumeLeadChat(threadId: string, chatId: string, lead = 'The lead'): Promise<boolean> {
  try {
    await chatsStore.activate(threadId, chatId);
    return true;
  } catch (err) {
    toast.error(isCode(err, 'lead_busy') ? `${lead} is still answering in this thread. Resume the chat when the answer is in.` : `Could not resume the chat: ${orchError(err)}`);
    return false;
  }
}

/** The lead is answering in this thread (a command planning): no new chat or resume until it is done. */
export function useLeadThinking(threadId: string): boolean {
  return useOrch((s) => {
    for (const id in s.pending) if (s.pending[id].threadId === threadId && s.pending[id].state === 'planning') return true;
    return false;
  });
}

/** The tooltip of what waits for the lead's answer. */
export const thinkingText = (lead: string) => `${lead} is still answering. Wait for the answer.`;

interface Props {
  threadId: string;
  lead: string;
  /** "Shop", or the card's name: the list's heading. */
  scope: string;
  /** Orchestrator cards on the board (the command bar's list only). */
  others?: OtherThread[];
  /** Show an orchestrator card's thread (after one of its chats was resumed). */
  onOpenOther?: (threadId: string) => void;
  className?: string;
}

/** "+ New chat" in a thread's header; an icon only where the header is narrow. */
export function NewChatButton({ threadId, lead, iconOnly }: { threadId: string; lead: string; iconOnly?: boolean }) {
  const { status } = useLeadChats(threadId);
  const [busy, setBusy] = useState(false);
  const narrow = useMediaQuery('(max-width: 719px)');
  const thinking = useLeadThinking(threadId);
  if (status === 'unsupported' || status === 'error') return null;
  const icon = iconOnly ?? narrow;
  return (
    <Button
      size="sm"
      variant="ghost"
      icon="plus"
      iconOnly={icon}
      className="nodrag lead-chat-new"
      loading={busy}
      disabled={thinking}
      aria-label="New chat"
      title={thinking ? thinkingText(lead) : `A new chat with ${lead}. This one is kept and can be resumed.`}
      onClick={() => {
        setBusy(true);
        void newLeadChat(threadId, lead).finally(() => setBusy(false));
      }}
    >
      {icon ? null : 'New chat'}
    </Button>
  );
}

export function LeadChatSwitcher({ threadId, lead, scope, others = [], onOpenOther, className = '' }: Props) {
  const { items, active, status } = useLeadChats(threadId);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState('');
  const ref = useRef<HTMLButtonElement>(null);
  const thinking = useLeadThinking(threadId);
  // a hub from before lead chats keeps one chat per thread: nothing to switch
  if (status === 'unsupported' || status === 'error') return null;
  const current = items.find((c) => c.id === active);
  const title = current ? leadChatTitle(current) : status === 'ready' ? 'New chat' : '…';

  const startNew = async () => {
    setBusy('new');
    const ok = await newLeadChat(threadId, lead);
    setBusy('');
    if (ok) setOpen(false);
  };
  const resume = async (tid: string, c: LeadChat) => {
    setBusy(c.id);
    const ok = await resumeLeadChat(tid, c.id, lead);
    setBusy('');
    if (!ok) return;
    setOpen(false);
    if (tid !== threadId) onOpenOther?.(tid);
  };

  return (
    <span className={`lead-chat-switch ${className}`}>
      <button
        ref={ref}
        type="button"
        className="lead-chat-btn nodrag"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`Chat with ${lead}: ${title}. Other chats`}
        title={`${title} · chats with ${lead}`}
        onClick={() => setOpen((o) => !o)}
      >
        <span className="truncate">{title}</span>
        <Icon name="chevronDown" />
      </button>
      {open && (
        <Popover anchor={ref} onClose={() => setOpen(false)} label={`Chats with ${lead}`} placement="below" align="start" width={Math.min(420, window.innerWidth - 16)} className="lead-chat-pop">
          <div className="lead-chat-list nowheel">
            <h3 className="section-title">
              Chats with {lead} · {scope}
            </h3>
            {status !== 'ready' && !items.length && (
              <div className="list-msg" role="status">
                <span className="spinner" aria-hidden="true" /> Loading…
              </div>
            )}
            <ul className="plain chat-list">
              {sortLeadChats(items.filter((c) => c.id === active || spoken(c)), active).map((c) => (
                <LeadChatRow key={c.id} chat={c} current={c.id === active} busy={busy === c.id} blocked={thinking ? thinkingText(lead) : undefined} onResume={() => void resume(threadId, c)} />
              ))}
              {others.map((t) => (
                <OtherThreadRows key={t.id} thread={t} busy={busy} onResume={(c) => void resume(t.id, c)} />
              ))}
            </ul>
            <button type="button" className="lead-chat-newrow" disabled={busy === 'new' || thinking} title={thinking ? thinkingText(lead) : undefined} onClick={() => void startNew()}>
              <Icon name="plus" />
              <span className="lead-chat-newtext">
                <strong>New chat</strong>
                <span>This chat is kept; {lead} starts fresh, the project memory stays.</span>
              </span>
            </button>
          </div>
        </Popover>
      )}
    </span>
  );
}

/** An orchestrator card's chats in the command bar's list: its current one opens the card. */
function OtherThreadRows({ thread, busy, onResume }: { thread: OtherThread; busy: string; onResume: (c: LeadChat) => void }) {
  const { items, active } = useLeadChats(thread.id);
  const thinking = useLeadThinking(thread.id);
  return (
    <>
      {sortLeadChats(items, active)
        .filter(spoken)
        .map((c) => (
          <LeadChatRow key={c.id} chat={{ ...c, title: `${thread.card} · ${leadChatTitle(c)}` }} current={false} card={thread.card} busy={busy === c.id} blocked={thinking && c.id !== active ? thinkingText(thread.card) : undefined} onResume={() => onResume(c)} />
        ))}
    </>
  );
}
