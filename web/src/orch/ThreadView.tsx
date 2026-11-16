// Thread rendering (docs/API-orchestration.md §4, D3/D15): orders, plan notes with live
// per-target progress, team reports with "See output", confirmations with a countdown,
// hub replies and notes, and a mission's card, team messages and summary (MissionCard).
// Shared by the command-bar drawer and orchestrator cards.

import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { OutputViewer } from '../agents/OutputViewer';
import { jumpToSession } from '../agents/jump';
import { Button } from '../components/Button';
import { Icon, type IconName } from '../components/Icon';
import { toast } from '../components/Toast';
import { formatCountdown, formatDateTime } from '../lib/format';
import { decide, orchError } from './actions';
import { orchApi } from './api';
import { AttachedFiles, splitAttached } from './Attachments';
import { useNow } from './hooks';
import { orchStore, useActiveMission, useOrch, useThread } from './store';
import { useMarkdown } from '../board/lazy';
import { MarkdownView } from '../studio/MarkdownView';
import { messageAuthor, messageText, useLeadName } from './lead';
import { CopyButton, LeadAvatar } from './OrderBits';
import { MissionCard, MissionMessage, MissionPlan, TeamRow, TeamRun } from './MissionCard';
import { missionMarks, missionTaskText, threadItems } from './mission';
import { ORDER_DONE, type Confirmation, type Order, type OrderTarget, type ThreadMessage } from './types';

const TARGET_LABEL: Record<string, string> = {
  queued: 'Queued',
  delivered: 'Delivered',
  working: 'Working',
  needsYou: 'Needs you',
  finished: 'Finished',
  failed: 'Failed',
  timeout: 'No reply',
  cancelled: 'Cancelled',
  skipped: 'Skipped',
};
const TARGET_TONE: Record<string, string> = {
  queued: 'grey',
  delivered: 'info',
  working: 'success',
  needsYou: 'needs',
  finished: 'info',
  failed: 'danger',
  timeout: 'warning',
  cancelled: 'grey',
  skipped: 'grey',
};

function duration(ms?: number): string {
  if (!ms || ms < 0) return '';
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/** Excerpts are plain text cut from Markdown: drop heading marks and code fences. */
const plain = (t: string) => t.replace(/(^|\s)#{1,6}\s/g, '$1').replace(/```\w*/g, '').replace(/\*\*(.+?)\*\*/g, '$1').trim();

const timeOf = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
};

export function OrderProgress({ order }: { order: Order }) {
  const targets = order.targets ?? [];
  const done = targets.filter((t) => ORDER_DONE.has(t.state)).length;
  const active = done < targets.length;
  const [busy, setBusy] = useState(false);
  const cancel = async () => {
    setBusy(true);
    try {
      orchStore.upsertOrder(await orchApi.cancelOrder(order.id));
    } catch (err) {
      toast.error(`Could not cancel: ${orchError(err)}`);
    } finally {
      setBusy(false);
    }
  };
  if (!targets.length) return null;
  return (
    <div className="order-progress">
      <div className="order-bar" role="progressbar" aria-valuemin={0} aria-valuemax={targets.length} aria-valuenow={done} aria-label={`${done} of ${targets.length} done`}>
        <span style={{ width: `${(done / targets.length) * 100}%` }} />
      </div>
      <ul className="order-targets">
        {targets.map((t) => (
          <TargetChip key={t.sessionId} t={t} />
        ))}
      </ul>
      {active && (
        <Button size="sm" variant="ghost" icon="close" loading={busy} onClick={() => void cancel()} title="Drop what has not been typed yet">
          Cancel order
        </Button>
      )}
    </div>
  );
}

function TargetChip({ t }: { t: OrderTarget }) {
  const tone = TARGET_TONE[t.state] ?? 'grey';
  return (
    <li>
      <button type="button" className={`target-chip tone-${tone}`} onClick={() => jumpToSession(t.sessionId)} title={t.error || `Show @${t.handle}`}>
        <span className="target-handle">@{t.handle}</span>
        <span className="target-state">{TARGET_LABEL[t.state] ?? t.state}</span>
      </button>
    </li>
  );
}

export function ConfirmationCard({ c, compact }: { c: Confirmation; compact?: boolean }) {
  const proposal = c.kind === 'proposal';
  // a mission's approval, a limit or a held step: not urgent either, and its plan is the mission's
  const missionKind = c.kind === 'mission';
  const mission = useOrch((s) => {
    if (!missionKind) return undefined;
    if (c.missionId && s.missions[c.missionId]) return s.missions[c.missionId];
    for (const id in s.missions) if (s.missions[id].confirmationId === c.id) return s.missions[id];
    return undefined;
  });
  const showPlan = missionKind && !!mission && c.state === 'pending' && mission.state === 'awaiting_approval';
  const now = useNow(500, c.state === 'pending');
  const left = Date.parse(c.expiresAt) - now;
  const expired = c.state === 'pending' && left <= 0;
  const [busy, setBusy] = useState('');
  const act = async (optionId: string) => {
    setBusy(optionId);
    const cancel = optionId === 'cancel';
    const choice = !cancel && optionId !== 'confirm' ? optionId : c.kind === 'didYouMean' ? c.candidates?.[0]?.sessionId : undefined;
    await decide(c, cancel ? 'cancel' : 'confirm', choice);
    setBusy('');
  };
  const danger = c.kind === 'destructive';
  const state = expired ? 'expired' : c.state;
  return (
    <div className={`confirm-card kind-${c.kind} ${state !== 'pending' ? 'is-decided' : ''} ${compact ? 'is-compact' : ''}`} role="group" aria-label="Confirmation">
      <div className="confirm-prompt">
        <Icon name={proposal ? 'workflow' : missionKind ? 'flag' : danger ? 'alert' : c.kind === 'loop' ? 'loop' : 'info'} />
        <span>{c.prompt}</span>
        {proposal && !!c.plan?.length && <CopyButton text={c.plan.map((p) => `@${p.handle}: ${p.text}`).join('\n')} label="Copy the plan" className="confirm-copy" />}
        {showPlan && <CopyButton text={(mission.tasks ?? []).map((t) => `@${t.handle} · ${t.title}: ${t.text}`).join('\n')} label="Copy the plan" className="confirm-copy" />}
      </div>
      {showPlan && <MissionPlan mission={mission} />}
      {(proposal || (missionKind && !showPlan && c.state === 'pending')) && !!c.plan?.length && (
        <ol className="plan-list">
          {c.plan.map((p, i) => (
            <li key={i}>
              {p.handle && <span className="plan-handle">@{p.handle}</span>}
              <FoldText className="plan-text" text={p.text} fold />
            </li>
          ))}
        </ol>
      )}
      {c.candidates && c.candidates.length > 0 && (
        <div className="confirm-cands">
          {c.candidates.map((x) => (
            <span key={x.sessionId} className="cand-chip">
              @{x.handle}
            </span>
          ))}
        </div>
      )}
      {state === 'pending' ? (
        <div className="confirm-actions">
          {!proposal && !missionKind && (
            <span className="confirm-timer tabular" aria-live="off" title="Answer before it expires">
              <Icon name="clock" />
              {formatCountdown(left)}
            </span>
          )}
          <span className="spacer" />
          {[...c.options].sort((a, b) => (a.id === 'cancel' ? -1 : b.id === 'cancel' ? 1 : 0)).map((o) => (
            <Button
              key={o.id}
              size="sm"
              variant={o.id === 'cancel' ? 'secondary' : danger ? 'danger' : 'primary'}
              loading={busy === o.id}
              disabled={!!busy && busy !== o.id}
              onClick={() => void act(o.id)}
            >
              {o.label}
            </Button>
          ))}
        </div>
      ) : (
        <div className="confirm-state">
          <Icon name={state === 'confirmed' ? 'check' : 'close'} />
          {state === 'confirmed' ? (proposal ? 'Agreed' : missionKind ? 'Approved' : 'Confirmed') : state === 'cancelled' ? (proposal ? 'Not sent' : 'Cancelled') : 'Expired — nothing was done'}
          {c.decidedBy && c.decidedBy !== 'admin' ? ' on another device' : ''}
        </div>
      )}
    </div>
  );
}

const SOURCE_ICON: Record<string, IconName> = { voice: 'message', loop: 'loop', api: 'link' };

/** Longer than this, an order in a compact thread starts folded. */
const FOLD_CHARS = 240;

function FoldText({ text, fold, className }: { text: string; fold?: boolean; className: string }) {
  const long = !!fold && text.length > FOLD_CHARS;
  const [open, setOpen] = useState(false);
  return (
    <>
      <span className={`${className} ${long && !open ? 'clamp-3' : ''}`}>{text}</span>
      {long && (
        <button type="button" className="link-btn plan-more" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? 'Show less' : 'Show all'}
        </button>
      )}
    </>
  );
}

/** What the lead says: Claude writes Markdown. The text alone until the renderer has loaded. */
function LeadText({ text }: { text: string }) {
  const md = useMarkdown();
  if (!md) return <span className="msg-text">{text}</span>;
  return <MarkdownView className="msg-text msg-md" text={text} onLink={() => true} />;
}

/** What a thread lets you do with a message; a thread without it can only copy. */
export interface ThreadActions {
  reply: (m: ThreadMessage, toLead: boolean) => void;
  /** Put the text back in the box, to change and send again. */
  reuse: (text: string) => void;
  /**
   * Send text as an answer to m, the way this thread sends orders: the agent's next result comes
   * back here. The output window's reply box uses it.
   */
  answer?: (m: ThreadMessage, text: string) => Promise<unknown>;
}

/** A message's actions; they show when the pointer is on the message. */
function MsgActions({ m, lead, actions }: { m: ThreadMessage; lead: string; actions?: ThreadActions }) {
  const text = messageText(m);
  const fromAgent = !!m.handle || m.plan?.length === 1;
  return (
    <span className="msg-actions">
      {actions && m.role === 'user' && (
        <button type="button" className="msg-act" aria-label="Edit and send again" title="Edit and send again" onClick={() => actions.reuse(m.text)}>
          <Icon name="pencil" />
        </button>
      )}
      {actions && m.role !== 'user' && (
        <button type="button" className="msg-act" aria-label={fromAgent ? `Reply to ${messageAuthor(m, lead)}` : 'Reply'} title={fromAgent ? `Reply to ${messageAuthor(m, lead)}` : 'Reply'} onClick={() => actions.reply(m, false)}>
          <Icon name="reply" />
        </button>
      )}
      {actions && fromAgent && (
        <button type="button" className="msg-act" aria-label={`Ask ${lead} about this`} title={`Ask ${lead} about this`} onClick={() => actions.reply(m, true)}>
          <Icon name="sparkle" />
        </button>
      )}
      {m.sessionId && (
        <button type="button" className="msg-act" aria-label="Show this console" title="Show this console" onClick={() => jumpToSession(m.sessionId!)}>
          <Icon name="target" />
        </button>
      )}
      {text && <CopyButton text={text} />}
    </span>
  );
}

/** After a hub restart: the order stays where it was until a person sends it again. */
function ResendOrder({ orderId }: { orderId: string }) {
  const order = useOrch((s) => s.orders[orderId]);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    // Only active orders load with the thread, and this one has ended.
    if (!order) void orchApi.order(orderId).then((o) => orchStore.upsertOrder(o)).catch(() => undefined);
  }, [order, orderId]);
  const missed = (order?.targets ?? []).filter((t) => t.undelivered);
  if (!missed.length) return null;
  const send = async () => {
    setBusy(true);
    try {
      orchStore.upsertOrder(await orchApi.resendOrder(orderId));
    } catch (err) {
      toast.error(`Could not send: ${orchError(err)}`);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Button size="sm" variant="ghost" icon="send" loading={busy} onClick={() => void send()} title={`Send this order to ${missed.map((t) => `@${t.handle}`).join(', ')}`}>
      Send again
    </Button>
  );
}

function MessageViewImpl({ m, onOutput, compact, lead, actions, onGoTo, mark = '' }: { m: ThreadMessage; onOutput: (m: ThreadMessage) => void; compact?: boolean; lead: string; actions?: ThreadActions; onGoTo: (id: string) => void; mark?: string }) {
  if (m.kind === 'team') return <TeamRow m={m} lead={lead} />;
  if (m.kind === 'mission') return <MissionMessage m={m} lead={lead} first={mark.includes('first')} last={mark.includes('last')} />;
  return <PlainMessage m={m} onOutput={onOutput} compact={compact} lead={lead} actions={actions} onGoTo={onGoTo} />;
}

function PlainMessage({ m, onOutput, compact, lead, actions, onGoTo }: { m: ThreadMessage; onOutput: (m: ThreadMessage) => void; compact?: boolean; lead: string; actions?: ThreadActions; onGoTo: (id: string) => void }) {
  const order = useOrch((s) => (m.orderId ? s.orders[m.orderId] : undefined));
  const conf = useOrch((s) => (m.confirmationId ? s.confirmations[m.confirmationId] : undefined));
  const time = <time className="msg-time" dateTime={m.at} title={formatDateTime(m.at)}>{timeOf(m.at)}</time>;
  const tools = <MsgActions m={m} lead={lead} actions={actions} />;

  if (m.kind === 'order' || m.role === 'user') {
    const said = splitAttached(m.text);
    return (
      <li className="msg msg-user" data-msg={m.id}>
        {m.replyTo && (
          <button type="button" className="msg-quote" title="Show the message this answers" onClick={() => onGoTo(m.replyTo!.id)}>
            <Icon name="reply" />
            <span className="truncate">
              {m.replyTo.handle && <strong>@{m.replyTo.handle} </strong>}
              {m.replyTo.excerpt}
            </span>
          </button>
        )}
        {said.files.length > 0 && <AttachedFiles files={said.files} />}
        {said.text && (
          <div className="msg-bubble">
            {m.source && SOURCE_ICON[m.source] && <Icon name={SOURCE_ICON[m.source]} className="msg-src" />}
            <span className="msg-body">
              <FoldText className="msg-text" text={said.text} fold={compact} />
            </span>
          </div>
        )}
        <div className="msg-meta">
          {tools}
          {time}
        </div>
      </li>
    );
  }
  if (m.kind === 'plan') {
    return (
      <li className="msg msg-plan" data-msg={m.id}>
        <div className="msg-head">
          <Icon name="workflow" />
          <span>{m.text || 'Plan'}</span>
          <span className="msg-meta">
            {tools}
            {time}
          </span>
        </div>
        {m.plan && m.plan.length > 0 && (
          <ol className="plan-list">
            {m.plan.map((p, i) => (
              <li key={i}>
                <span className="plan-handle">@{p.handle}</span>
                <FoldText className="plan-text" text={m.missionId ? missionTaskText(p.text) : p.text} fold={compact || !!m.missionId} />
              </li>
            ))}
          </ol>
        )}
        {order && <OrderProgress order={order} />}
      </li>
    );
  }
  if (m.kind === 'report' && m.report) {
    const r = m.report;
    const ok = r.state === 'finished';
    return (
      <li className={`msg msg-report state-${r.state}`} data-msg={m.id}>
        <div className="msg-head">
          <span className={`report-dot tone-${ok ? 'info' : r.state === 'failed' ? 'danger' : 'warning'}`} aria-hidden="true" />
          <button type="button" className="plan-handle link-btn" onClick={() => m.sessionId && jumpToSession(m.sessionId)}>
            @{m.handle}
          </button>
          <span className="report-state">
            {ok ? 'finished' : r.state === 'failed' ? 'failed' : r.state === 'timeout' ? 'did not reply' : r.state}
            {r.durationMs ? ` in ${duration(r.durationMs)}` : ''}
          </span>
          <span className="msg-meta">
            {tools}
            {time}
          </span>
        </div>
        {(r.excerpt || m.text) && <p className={`report-excerpt ${compact ? 'clamp-3' : 'clamp-6'}`}>{plain(r.excerpt || m.text)}</p>}
        {ok && (r.chars ?? 0) > 0 && (
          <Button size="sm" variant="ghost" icon="fileText" onClick={() => onOutput(m)}>
            See output · {(r.chars ?? 0).toLocaleString()} chars
          </Button>
        )}
      </li>
    );
  }
  if (m.kind === 'confirm' && conf) {
    return (
      <li className="msg msg-confirm" data-msg={m.id}>
        <ConfirmationCard c={conf} compact={compact} />
      </li>
    );
  }
  const icon: IconName = m.kind === 'error' ? 'alertCircle' : m.kind === 'note' || m.kind === 'confirm' ? 'info' : 'sparkle';
  const fromLead = m.kind === 'reply';
  return (
    <li className={`msg msg-hub kind-${m.kind}`} data-msg={m.id}>
      {fromLead && (
        <div className="msg-lead">
          <LeadAvatar name={lead} />
          <span>{lead}</span>
        </div>
      )}
      <div className="msg-hub-body">
        {fromLead ? (
          <LeadText text={m.text} />
        ) : (
          <>
            <Icon name={icon} />
            <span className="msg-text">{m.kind === 'confirm' ? `${m.text} · no longer open` : m.text}</span>
          </>
        )}
      </div>
      <div className="msg-meta">
        {time}
        {tools}
      </div>
      {order && <OrderProgress order={order} />}
      {m.resend && m.orderId && <ResendOrder orderId={m.orderId} />}
    </li>
  );
}

// A thread holds up to 500 messages and its parent re-renders on every keystroke in its box;
// with stable props (see ThreadList) only a message that changed renders again.
const MessageView = memo(MessageViewImpl);

export function ThreadList({
  threadId,
  projectId,
  compact,
  empty,
  lead,
  actions,
}: {
  threadId: string;
  /** The project the thread is in (a card's thread id does not say): its active missions are loaded. */
  projectId?: string;
  compact?: boolean;
  empty: ReactNode;
  lead?: string;
  actions?: ThreadActions;
}) {
  const hubLead = useLeadName();
  const who = lead ?? hubLead;
  const thread = useThread(threadId);
  const mission = useActiveMission(threadId);
  const items = useMemo(() => threadItems(thread.messages), [thread.messages]);
  const marks = useMemo(() => missionMarks(thread.messages), [thread.messages]);
  const pid = projectId ?? (threadId.startsWith('p_') ? threadId : '');
  useEffect(() => {
    if (pid) void orchStore.loadMissions(pid);
  }, [pid]);
  // a primitive (command id), so the store snapshot stays stable between renders
  const pending = useOrch((s) => {
    for (const id in s.pending) if (s.pending[id].threadId === threadId && s.pending[id].state === 'planning') return id;
    return '';
  });
  const [output, setOutput] = useState<ThreadMessage | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    void orchStore.loadThread(threadId);
  }, [threadId]);

  const goTo = useCallback((id: string) => {
    const el = ref.current?.querySelector<HTMLElement>(`[data-msg="${CSS.escape(id)}"]`);
    if (!el) return toast.info('That message is further back in the thread.');
    el.scrollIntoView({ block: 'center', behavior: 'smooth' });
    el.classList.add('is-found');
    window.setTimeout(() => el.classList.remove('is-found'), 1400);
  }, []);

  // Parents build `actions` anew on every render; messages get a stand-in that stays the same
  // and calls the latest one, so memo(MessageView) can skip them.
  const actionsRef = useRef(actions);
  actionsRef.current = actions;
  const hasActions = !!actions;
  // a message only replies and reuses; the output window below takes `answer` as it is
  const msgActions = useMemo<ThreadActions | undefined>(
    () => (hasActions ? { reply: (m, toLead) => actionsRef.current?.reply(m, toLead), reuse: (t) => actionsRef.current?.reuse(t) } : undefined),
    [hasActions],
  );

  useLayoutEffect(() => {
    const el = ref.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [thread.messages.length, pending]);

  return (
    <>
      {mission && <MissionCard mission={mission} lead={who} pinned compact={compact} />}
      <div
        className={`thread-list ${compact ? 'is-compact' : ''}`}
        ref={ref}
        tabIndex={0}
        aria-label="Thread"
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        {thread.hasMore && (
          <Button size="sm" variant="ghost" icon="arrowUp" className="thread-older" onClick={() => void orchStore.loadOlder(threadId)}>
            Show older messages
          </Button>
        )}
        {thread.status === 'loading' && !thread.messages.length && (
          <div className="list-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Loading the thread…
          </div>
        )}
        {thread.status === 'unsupported' && <div className="list-msg">This hub does not keep threads yet. Update NextBoard on the hub to send orders.</div>}
        {thread.status === 'error' && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>Could not load the thread: {thread.error}</span>
          </div>
        )}
        {thread.status === 'ready' && !thread.messages.length && !pending && (typeof empty === 'string' ? <p className="thread-empty">{empty}</p> : empty)}
        <ol className="msgs" aria-live="polite">
          {items.map((it) =>
            it.kind === 'team' ? (
              <TeamRun key={it.key} msgs={it.msgs} lead={who} />
            ) : (
              <MessageView
                key={it.m.id}
                m={it.m}
                onOutput={setOutput}
                compact={compact}
                lead={who}
                actions={msgActions}
                onGoTo={goTo}
                mark={it.m.kind === 'mission' ? `${marks.first.has(it.m.id) ? 'first ' : ''}${marks.last.has(it.m.id) ? 'last' : ''}` : ''}
              />
            ),
          )}
          {pending && !thread.messages.some((m) => /still thinking/i.test(m.text)) && (
            <li className="msg msg-hub kind-thinking">
              <div className="msg-hub-body">
                <LeadAvatar name={who} />
                <span className="msg-text">{who} is thinking</span>
                <span className="lead-dots" aria-hidden="true">
                  <span />
                  <span />
                  <span />
                </span>
              </div>
            </li>
          )}
        </ol>
        {output && (
          <OutputViewer
            message={output}
            onClose={() => setOutput(null)}
            onAnswer={actions?.answer && output.role !== 'user' ? (t) => actions.answer!(output, t) : undefined}
          />
        )}
      </div>
    </>
  );
}
