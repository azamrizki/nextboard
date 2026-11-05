// Resume picker (docs/API-agents.md §8.2 Conversation, C10): conversations of this
// agent in the console's folder, newest first, grouped by day. Resume into this console
// (when it is at the shell prompt) or into a new console next to it.

import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { api, errorMessage, isCode } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { formatDateTime, parseTime, timeAgo } from '../lib/format';
import type { Agent, Conversation, Session } from '../lib/types';
import { store } from '../store';
import { jumpToSession } from './jump';

interface ResumePickerProps {
  session: Session;
  agent: Agent | undefined;
  onClose: () => void;
  /** Resume into this console (only offered at the shell prompt). */
  onResumeHere: (c: Conversation) => Promise<void>;
  /** Start a new console of the same agent that resumes it. */
  onResumeNew: (c: Conversation) => Promise<void>;
}

function dayGroup(iso: string, now = new Date()): string {
  const d = parseTime(iso);
  if (!d) return 'Older';
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((start(now) - start(d)) / 86400000);
  if (days <= 0) return 'Today';
  if (days === 1) return 'Yesterday';
  if (days < 7) return 'Earlier this week';
  return 'Older';
}

function sizeLabel(b: number): string {
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${Math.round(b / 1024)} KB`;
  return `${(b / 1024 / 1024).toFixed(1)} MB`;
}

export function ResumePicker({ session, agent, onClose, onResumeHere, onResumeNew }: ResumePickerProps) {
  const [items, setItems] = useState<Conversation[] | null>(null);
  const [error, setError] = useState('');
  const [truncated, setTruncated] = useState(false);
  const [filter, setFilter] = useState('');
  const [selected, setSelected] = useState('');
  const [busy, setBusy] = useState<'' | 'here' | 'new'>('');
  const listRef = useRef<HTMLDivElement>(null);
  const atShell = session.agentState === 'shell' && session.status === 'running';
  // Phase 5 restart with agentSessionId: a running console can switch conversations too
  const running = session.status === 'running' && !atShell;
  const [confirmSwitch, setConfirmSwitch] = useState(false);

  useEffect(() => {
    const ac = new AbortController();
    api
      .conversations(session.machineId, { agent: session.agent || session.preset, cwd: session.cwd, profileId: session.profileId || undefined, limit: 100 }, ac.signal)
      .then((r) => {
        const list = (r.items ?? []).slice().sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
        setItems(list);
        setTruncated(r.truncated);
        setSelected((cur) => cur || list.find((c) => !c.attachedTo || c.attachedTo === session.id)?.agentSessionId || '');
      })
      .catch((err) => {
        if (ac.signal.aborted) return;
        setError(isCode(err, 'no_transcript') ? `${agent?.name ?? 'This agent'} keeps no transcripts NextBoard can read.` : errorMessage(err));
      });
    return () => ac.abort();
  }, [session.machineId, session.agent, session.preset, session.cwd, session.profileId, session.id, agent?.name]);

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return (items ?? []).filter((c) => !q || c.title.toLowerCase().includes(q) || c.agentSessionId.includes(q));
  }, [items, filter]);

  const groups = useMemo(() => {
    const out: { title: string; list: Conversation[] }[] = [];
    for (const c of shown) {
      const g = dayGroup(c.updatedAt);
      const last = out[out.length - 1];
      if (last?.title === g) last.list.push(c);
      else out.push({ title: g, list: [c] });
    }
    return out;
  }, [shown]);

  const current = shown.find((c) => c.agentSessionId === selected) ?? null;
  const otherOwner = current?.attachedTo && current.attachedTo !== session.id ? store.getState().sessions[current.attachedTo] : undefined;
  const isHere = !!current && current.attachedTo === session.id;

  const run = async (where: 'here' | 'new', confirmed = false) => {
    if (!current || busy) return;
    if (where === 'here' && running && !confirmed && (session.agentState === 'working' || session.agentState === 'needsYou')) {
      setConfirmSwitch(true);
      return;
    }
    setBusy(where);
    try {
      await (where === 'here' ? onResumeHere(current) : onResumeNew(current));
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy('');
    }
  };

  const onListKey = (e: KeyboardEvent) => {
    if (!shown.length) return;
    const i = shown.findIndex((c) => c.agentSessionId === selected);
    let j = i;
    if (e.key === 'ArrowDown') j = Math.min(shown.length - 1, i + 1);
    else if (e.key === 'ArrowUp') j = Math.max(0, i - 1);
    else if (e.key === 'Home') j = 0;
    else if (e.key === 'End') j = shown.length - 1;
    else if (e.key === 'Enter') {
      e.preventDefault();
      if (!otherOwner && !isHere) void run(atShell || running ? 'here' : 'new');
      return;
    } else return;
    e.preventDefault();
    const id = shown[j].agentSessionId;
    setSelected(id);
    listRef.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'nearest' });
  };

  const handle = session.handle ? `@${session.handle}` : session.name;

  return (
    <Modal
      title="Pick a conversation"
      icon="history"
      size="md"
      className="resume-modal"
      onClose={onClose}
      dismissable={!busy}
      footer={
        otherOwner ? (
          <Button variant="primary" icon="arrowRight" onClick={() => (onClose(), jumpToSession(otherOwner.id))}>
            Show @{otherOwner.handle ?? otherOwner.name}
          </Button>
        ) : (
          <>
            <Button variant="secondary" icon="plus" onClick={() => void run('new')} loading={busy === 'new'} disabled={!current || isHere || (!!busy && busy !== 'new')}>
              Resume in new console
            </Button>
            <Button
              variant="primary"
              icon="resume"
              onClick={() => void run('here')}
              loading={busy === 'here'}
              disabled={!current || !(atShell || running) || isHere || (!!busy && busy !== 'here')}
              title={atShell ? undefined : running ? `Restarts the agent in ${handle} on this conversation (the scrollback stays)` : 'Start the console first, or resume in a new console.'}
            >
              Resume in {handle}
            </Button>
          </>
        )
      }
    >
      <div className="stack resume-picker">
        <p className="picker-meta truncate" title={session.cwd}>
          {agent?.name ?? session.preset} in <span className="mono">{session.cwd}</span>
        </p>
        <div className="input-group has-lead">
          <span className="ig-lead">
            <Icon name="search" />
          </span>
          <input
            className="control"
            placeholder="Filter by title"
            aria-label="Filter conversations"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            onKeyDown={onListKey}
          />
        </div>
        {running && !otherOwner && (
          <p className="fs-xs text-3">
            The agent is running in {handle}. Resuming here restarts it on the picked conversation; the scrollback stays. Or resume in a new console beside it.
          </p>
        )}
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
        {!items && !error && (
          <div className="list-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Looking for conversations…
          </div>
        )}
        {items && items.length === 0 && !error && (
          <div className="list-msg">No conversations in this folder yet. Start one, and it shows up here.</div>
        )}
        {items && items.length > 0 && shown.length === 0 && <div className="list-msg">No conversation matches “{filter}”.</div>}
        {shown.length > 0 && (
          <div
            ref={listRef}
            className="conv-list"
            role="listbox"
            aria-label="Conversations"
            tabIndex={0}
            aria-activedescendant={selected ? `conv-${selected}` : undefined}
            onKeyDown={onListKey}
          >
            {groups.map((g) => (
              <div key={g.title} role="group" aria-label={g.title}>
                <div className="conv-group" role="presentation">
                  {g.title}
                </div>
                {g.list.map((c) => {
                  const owner = c.attachedTo ? store.getState().sessions[c.attachedTo] : undefined;
                  return (
                    <div
                      key={c.agentSessionId}
                      id={`conv-${c.agentSessionId}`}
                      data-id={c.agentSessionId}
                      role="option"
                      aria-selected={c.agentSessionId === selected}
                      className="conv-item"
                      onClick={() => setSelected(c.agentSessionId)}
                      onDoubleClick={() => {
                        setSelected(c.agentSessionId);
                        if (!c.attachedTo) void (atShell ? onResumeHere(c).then(onClose) : onResumeNew(c).then(onClose));
                        // (double-click never interrupts a running agent; use the button)
                      }}
                    >
                      <span className="conv-title truncate" title={c.title}>
                        {c.title || 'Untitled conversation'}
                      </span>
                      {c.attachedTo && (
                        <span className="badge badge-info conv-tag">
                          {c.attachedTo === session.id ? 'This console' : `Open in @${c.attachedHandle ?? owner?.handle ?? owner?.name ?? 'another console'}`}
                        </span>
                      )}
                      <span className="conv-meta tabular" title={`Started ${formatDateTime(c.startedAt)} · updated ${formatDateTime(c.updatedAt)}`}>
                        {timeAgo(c.updatedAt)} · {c.messages} message{c.messages === 1 ? '' : 's'} · {sizeLabel(c.sizeBytes)}
                      </span>
                    </div>
                  );
                })}
              </div>
            ))}
          </div>
        )}
        {truncated && <p className="fs-xs text-3">Only the newest conversations were scanned.</p>}
      </div>
      {confirmSwitch && (
        <ConfirmDialog
          title={`Switch ${handle} to this conversation?`}
          confirmLabel="Restart on this conversation"
          icon="restart"
          onClose={() => setConfirmSwitch(false)}
          onConfirm={() => run('here', true)}
        >
          <p>{handle} is {session.agentState === 'needsYou' ? 'waiting for your answer' : 'in the middle of a turn'}. Restarting stops it and resumes the picked conversation; the scrollback stays.</p>
        </ConfirmDialog>
      )}
    </Modal>
  );
}
