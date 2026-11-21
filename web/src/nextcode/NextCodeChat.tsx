// A NextCode console (docs/API-agents.md §30): no terminal, the conversation drawn as a chat from
// the hub's stream. Three layouts of the same chat: `tab` (a Code view tab: wide, with a toolbar
// for the model, the engine, the permission mode and the usage), `card` (a board card: tools as
// rows, the model chip in the composer's bar) and `phone` (narrow, buttons the width of the
// screen). The console's state on the live line comes from the session (agentState, activity) as
// for any agent. Files dropped anywhere on the chat are attached to the next message.

import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useAgent } from '../agents/catalog';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { useFileDrop } from '../lib/fileDrop';
import type { Agent, Session } from '../lib/types';
import { useSession } from '../store';
import type { ModelChange } from './api';
import { useChatAttachments } from './ChatAttachments';
import { Composer } from './Composer';
import { engineOf } from './engines';
import { costText, elapsed, liveLine, modeOf, modesFor, rateLimitText } from './format';
import { ChatAccount, ChildItems, ChildStreams, ItemView, NcMark, StreamView } from './Items';
import { groupItems, groupStreams, type Stream } from './reducer';
import { ModelMenu } from './ModelMenu';
import { RequestCard } from './RequestCard';
import type { Item, PermissionMode, Status } from './types';
import { useNextCode, type NextCodeChatController } from './useNextCode';
import '../styles/nextcode.css';

export type NextCodeVariant = 'tab' | 'card' | 'phone';

export interface NextCodeChatProps {
  sessionId: string;
  variant: NextCodeVariant;
  /** The chat from useNextCode, when the caller needs it too (the board card's header shows its socket). */
  chat?: NextCodeChatController;
  /** Bumped to give the composer the keyboard (the Code view's pane focus). */
  focusTick?: number;
  className?: string;
}

export function NextCodeChat(props: NextCodeChatProps) {
  return props.chat ? <ChatView {...props} chat={props.chat} /> : <OwnChat {...props} />;
}

function OwnChat(props: NextCodeChatProps) {
  const chat = useNextCode(props.sessionId);
  return <ChatView {...props} chat={chat} />;
}

/** The entries of the main conversation: the owner's messages, and the agent's runs between them. */
type Row = { t: 'item'; item: Item } | { t: 'agent'; key: string; items: Item[] };

function rowsOf(top: Item[]): Row[] {
  const rows: Row[] = [];
  for (const it of top) {
    if (it.kind === 'user' || it.kind === 'notice' || it.kind === 'error') {
      rows.push({ t: 'item', item: it });
      continue;
    }
    const last = rows[rows.length - 1];
    if (last?.t === 'agent') last.items.push(it);
    else rows.push({ t: 'agent', key: it.id, items: [it] });
  }
  return rows;
}

const NO_STREAMS: Stream[] = [];

/** One run of the agent's work: its mark in the wide view, then its text, thinking and tools. */
const AgentRun = memo(function AgentRun({ items, streams, compact, waitingFor }: { items: Item[]; streams: Stream[]; compact: boolean; waitingFor: Set<string> }) {
  const body = (
    <div className="nc-agent-body">
      {items.map((it) => (
        <ItemView key={it.id} item={it} compact={compact} waitingFor={waitingFor} />
      ))}
      {streams.map((s) => (
        <StreamView key={s.id} stream={s} compact={compact} />
      ))}
    </div>
  );
  if (compact) return body;
  return (
    <div className="nc-agent">
      <NcMark small />
      {body}
    </div>
  );
});

/** Ticks every second while `on`. */
function useNow(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    setNow(Date.now());
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [on]);
  return now;
}

interface ToolbarProps {
  /** "claude" | "codex" | "api". */
  engine: string;
  status: Status | null;
  mode: PermissionMode;
  onMode: (m: PermissionMode) => void;
  session: Session | undefined;
  agent: Agent | undefined;
  disabled: boolean;
  onModel: (c: ModelChange) => Promise<boolean>;
}

function Toolbar({ engine, status, mode, onMode, session, agent, disabled, onModel }: ToolbarProps) {
  const cost = costText(status?.costUsd);
  const limit = rateLimitText(status?.rateLimit, engine);
  const modes = modesFor(engine);
  const usage = status?.usage;
  const tokens = usage ? usage.input + usage.output + usage.cacheRead + usage.cacheWrite : 0;
  // the arrows move the choice (DESIGN.md §3.2)
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    e.preventDefault();
    const i = modes.findIndex((m) => m.value === mode);
    const next = modes[(i + (e.key === 'ArrowRight' ? 1 : modes.length - 1)) % modes.length];
    onMode(next.value);
    const btn = e.currentTarget.querySelector<HTMLButtonElement>(`[data-mode="${next.value}"]`);
    btn?.focus();
  };
  return (
    <div className="nc-toolbar">
      <ModelMenu session={session} agent={agent} running={status?.model} engine={engine} disabled={disabled} onChange={onModel} />
      <div className="segmented sm nc-modes" role="radiogroup" aria-label="Permission mode" onKeyDown={onKey}>
        {modes.map((m) => (
          <button
            key={m.value}
            type="button"
            role="radio"
            data-mode={m.value}
            aria-checked={m.value === mode}
            tabIndex={m.value === mode ? 0 : -1}
            className={`segmented-item ${m.value === 'bypassPermissions' ? 'nc-mode-danger' : ''}`}
            title={m.title}
            onClick={() => m.value !== mode && onMode(m.value)}
          >
            {m.label}
          </button>
        ))}
      </div>
      <div className="nc-usage">
        {tokens > 0 && <span title={`Last turn: ${usage!.input} in, ${usage!.output} out, ${usage!.cacheRead} read from the cache, ${usage!.cacheWrite} written to it`}>{tokens >= 1000 ? `${(tokens / 1000).toFixed(tokens >= 10_000 ? 0 : 1)}k tokens` : `${tokens} tokens`}</span>}
        {cost && <span title="What this conversation has cost so far">{cost}</span>}
        {limit && <span className={/reached/.test(limit) ? 'is-bad' : ''}>{limit}</span>}
      </div>
    </div>
  );
}

function ChatView({ sessionId, variant, chat, focusTick, className = '' }: NextCodeChatProps & { chat: NextCodeChatController }) {
  const session = useSession(sessionId);
  const agent = useAgent(session ? session.agent || session.preset : undefined);
  const att = useChatAttachments(sessionId);
  const { state } = chat;
  const compact = variant !== 'tab';
  const status = state.status;
  const mode = modeOf(status);
  const engine = status?.engine || engineOf(agent);
  // an API console has no login: a refused key is the provider's
  const account = useMemo(() => ({ engine, onProvider: engine === 'api' || !!session?.providerId }), [engine, session?.providerId]);

  const tree = useMemo(() => groupItems(state.items), [state.items]);
  const streams = useMemo(() => groupStreams(state.streams, tree.ids), [state.streams, tree.ids]);
  const rows = useMemo(() => rowsOf(tree.top), [tree.top]);
  const waitingFor = useMemo(() => new Set(state.requests.map((r) => r.toolUseId ?? '').filter(Boolean)), [state.requests]);

  const who = session ? (session.handle ? `@${session.handle}` : session.name) : 'NextCode';
  const live = liveLine(session?.agentState, session?.activity, status, chat.conn === 'open');
  const since = live.timed ? (session?.agentState === 'needsYou' ? session.stateSince : status?.turnSince) : undefined;
  const now = useNow(!!since);
  const ended = session?.status === 'exited';
  const working = !!status?.working;
  // files dropped anywhere on the chat go with the next message
  const dropRef = useFileDrop<HTMLDivElement>(ended ? null : att.add, 'Drop to attach to your message');

  // ---- scrolling: stay at the end while the end is in sight ----
  const scrollRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [away, setAway] = useState(false);
  // older items go in front: the view keeps what it showed where it was
  const fromEnd = useRef<number | null>(null);

  const follow = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    if (fromEnd.current !== null) {
      el.scrollTop = el.scrollHeight - fromEnd.current;
      return;
    }
    if (stick.current) el.scrollTop = el.scrollHeight;
  }, []);

  useEffect(() => {
    const box = contentRef.current;
    if (!box || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => follow());
    ro.observe(box);
    return () => ro.disconnect();
  }, [follow]);

  // a new request needs the owner: it comes into sight
  const asks = state.requests.length;
  const prevAsks = useRef(asks);
  useLayoutEffect(() => {
    if (asks > prevAsks.current) stick.current = true;
    prevAsks.current = asks;
    follow();
  }, [state.items, state.streams, asks, follow]);

  const firstId = state.items[0]?.id;
  useLayoutEffect(() => {
    if (fromEnd.current === null) return;
    follow();
    fromEnd.current = null;
  }, [firstId, follow]);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const atEnd = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    stick.current = atEnd;
    setAway(!atEnd);
  };

  const toEnd = () => {
    const el = scrollRef.current;
    stick.current = true;
    setAway(false);
    if (el) el.scrollTop = el.scrollHeight;
  };

  const older = async () => {
    const el = scrollRef.current;
    if (el) fromEnd.current = el.scrollHeight - el.scrollTop;
    await chat.loadOlder();
    // nothing came (it failed): do not hold the next change to this place
    window.setTimeout(() => {
      fromEnd.current = null;
    }, 1000);
  };

  const send = async (text: string) => {
    const ok = await chat.send(text);
    if (ok) toEnd();
    return ok;
  };

  const act = useMemo(() => ({ answer: chat.answer, setMode: chat.setMode }), [chat.answer, chat.setMode]);
  const lastRow = rows[rows.length - 1];
  const tailStreams = streams.top;
  const empty = state.loaded && !state.items.length && !state.streams.length && !state.requests.length;

  return (
    <ChatAccount.Provider value={account}>
      <div ref={dropRef} className={`nc-chat is-${variant} ${className}`} data-session={sessionId}>
        {variant === 'tab' && <Toolbar engine={engine} status={status} mode={mode} onMode={(m) => void chat.setMode(m)} session={session} agent={agent} disabled={ended} onModel={chat.setModel} />}
        <div className="nc-scroll-wrap">
          <div className="nc-scroll" ref={scrollRef} onScroll={onScroll} tabIndex={0} aria-label={`Conversation with ${who}`}>
            <div className="nc-col" ref={contentRef}>
              {state.more && (
                <Button size="sm" variant="ghost" icon="arrowUp" className="nc-older" loading={chat.loadingOlder} onClick={() => void older()}>
                  Show earlier messages
                </Button>
              )}
              {!state.loaded &&
                (chat.problem ? (
                  <div className="alert" role="alert">
                    <Icon name="alertCircle" />
                    <span>{chat.problem}</span>
                  </div>
                ) : (
                  <div className="nc-loading" role="status">
                    <span className="spinner" aria-hidden="true" /> Opening the chat…
                  </div>
                ))}
              {empty && (
                <div className="nc-empty">
                  <NcMark />
                  <p className="nc-empty-title">{who} is ready</p>
                  <p className="nc-quiet">{compact ? 'Ask for a change, a review or an explanation.' : 'Ask for a change, a review or an explanation. It works in this console’s folder and asks before it does anything your rules do not allow.'}</p>
                </div>
              )}
              <ChildItems.Provider value={tree.children}>
                <ChildStreams.Provider value={streams.children}>
                  {rows.map((r, i) =>
                    r.t === 'item' ? (
                      <ItemView key={r.item.id} item={r.item} compact={compact} waitingFor={waitingFor} />
                    ) : (
                      <AgentRun key={r.key} items={r.items} streams={i === rows.length - 1 ? tailStreams : NO_STREAMS} compact={compact} waitingFor={waitingFor} />
                    ),
                  )}
                  {tailStreams.length > 0 && lastRow?.t !== 'agent' && <AgentRun items={[]} streams={tailStreams} compact={compact} waitingFor={waitingFor} />}
                </ChildStreams.Provider>
              </ChildItems.Provider>
              {state.requests.map((r) => (
                <RequestCard key={r.id} req={r} act={act} compact={compact} stacked={variant === 'phone'} />
              ))}
            </div>
          </div>
          {away && (
            <button type="button" className={`nc-jump ${state.requests.length ? 'is-needs' : ''}`} onClick={toEnd}>
              <Icon name="arrowDown" />
              {state.requests.length ? 'Needs you' : 'Latest'}
            </button>
          )}
        </div>
        <div className="nc-foot">
          <div className="nc-col">
            <div className="nc-live">
              <span className={`dot dot-${live.dot}`} aria-hidden="true" />
              {/* the state is announced when it changes; the ticking time is not */}
              <span className="nc-live-text truncate" title={live.text} aria-live="polite">
                {live.text}
              </span>
              {since && <span className="nc-live-time">{elapsed(now - Date.parse(since))}</span>}
            </div>
            {status?.error && !working && (
              <p className="nc-live-err" title={status.error}>
                {status.error}
              </p>
            )}
            <Composer
              sessionId={sessionId}
              who={who}
              variant={variant}
              working={working}
              stopping={!!status?.stopping}
              disabled={ended}
              commands={status?.commands ?? []}
              mode={mode}
              onMode={(m) => void chat.setMode(m)}
              onSend={send}
              onStop={() => void chat.interrupt()}
              focusTick={focusTick}
              model={variant !== 'tab' && <ModelMenu session={session} agent={agent} running={status?.model} engine={engine} compact disabled={ended} onChange={chat.setModel} />}
            />
          </div>
        </div>
      </div>
    </ChatAccount.Provider>
  );
}
