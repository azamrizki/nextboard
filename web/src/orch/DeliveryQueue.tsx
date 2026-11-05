// Delivery queue of one console (docs/API-orchestration.md §2, §18): header badge
// "2 queued"; click lists active and recent deliveries with why they wait, and Cancel.

import { orchError } from './actions';
import { useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import { ApiError } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Popover } from '../components/Popover';
import { toast } from '../components/Toast';
import { timeAgo } from '../lib/format';
import type { Session } from '../lib/types';
import { orchApi } from './api';
import { orchStore, useOrch } from './store';
import { DELIVERY_ACTIVE, type Delivery } from './types';

const WAIT: Record<string, string> = {
  starting: 'for the agent to start',
  needsYou: 'until you answer the agent',
  working: 'for the current turn to end',
  paused: 'while the console is paused',
  reconnecting: 'for the machine to reconnect',
  humanTyping: 'while you type in the console',
  dialog: 'for the dialog on screen to close',
};
const STATE: Record<string, string> = {
  queued: 'Queued',
  waiting: 'Waiting',
  typing: 'Typing',
  sent: 'Sent, not confirmed',
  confirmed: 'Delivered',
  failed: 'Failed',
  expired: 'Expired',
  cancelled: 'Cancelled',
};
const SOURCE: Record<string, string> = {
  order: 'Order',
  loop: 'Loop',
  memory: 'Memory',
  handoff: 'Handoff',
  forward: 'Forward',
  reply: 'Reply',
  skill: 'Skill',
  dictation: 'Dictation',
  api: 'API',
};

/** Why a delivery did not go through, in plain words (hub codes; its own text otherwise). */
export function deliveryProblem(d: Delivery): string {
  switch (d.code) {
    case 'not_agent':
      return 'the agent is not in the foreground, so nothing was typed (never into a shell)';
    case 'session_gone':
      return 'the console is gone';
    case 'agent_exited':
      return 'the agent exited before it was free';
    case 'submit_aborted':
      return 'the screen changed after pasting, so Return was not pressed (the text is still in the agent’s input)';
    case 'write_failed':
      return 'the console could not be written to';
    case 'deadline':
      return 'the agent was not free before the deadline';
    case 'target_busy':
      return 'the agent is busy';
    default:
      return d.error ?? '';
  }
}

export function QueueBadge({ session }: { session: Session }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLButtonElement>(null);
  const n = session.queue ?? 0;
  if (!n && !open) return null;
  return (
    <>
      <button
        ref={ref}
        type="button"
        className="badge badge-info node-badge queue-badge nodrag"
        aria-haspopup="dialog"
        aria-expanded={open}
        title="Orders waiting for this console"
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
      >
        <Icon name="queue" />
        {n} queued
      </button>
      {open && <QueueList anchor={ref} session={session} onClose={() => setOpen(false)} />}
    </>
  );
}

function QueueList({ anchor, session, onClose }: { anchor: RefObject<HTMLButtonElement | null>; session: Session; onClose: () => void }) {
  const [error, setError] = useState('');
  const all = useOrch((s) => s.deliveries);
  const list = useMemo(
    () =>
      Object.values(all)
        .filter((d) => d.sessionId === session.id)
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
        .slice(0, 25),
    [all, session.id],
  );
  useEffect(() => {
    orchApi
      .deliveries(session.id)
      .then((l) => orchStore.setDeliveries(l))
      .catch((err) => setError(orchError(err)));
  }, [session.id]);

  const cancel = async (d: Delivery) => {
    try {
      await orchApi.cancelDelivery(d.id);
      orchStore.upsertDelivery({ ...d, state: 'cancelled' });
    } catch (err) {
      if (err instanceof ApiError && err.code === 'already_sent') toast.info('That text is already in the agent’s input; it was not removed.');
      else toast.error(`Could not cancel: ${orchError(err)}`);
    }
  };

  const active = list.filter((d) => DELIVERY_ACTIVE.has(d.state));
  const done = list.filter((d) => !DELIVERY_ACTIVE.has(d.state));
  return (
    <Popover anchor={anchor} onClose={onClose} label={`Queue of @${session.handle ?? session.name}`} className="queue-pop" width={360}>
      <div className="pop-head">
        <Icon name="queue" />
        <strong>Waiting for @{session.handle ?? session.name}</strong>
      </div>
      {error && <p className="field-error">{error}</p>}
      {active.length === 0 && <p className="list-msg">Nothing waiting. New orders are typed as soon as the agent is at its prompt.</p>}
      <ul className="queue-list">
        {[...active, ...done.slice(0, 6)].map((d) => (
          <li key={d.id} className={`queue-item state-${d.state}`}>
            <div className="queue-meta">
              <span className="cap-chip">{SOURCE[d.source] ?? d.source}</span>
              <span className="queue-state">
                {STATE[d.state] ?? d.state}
                {d.state === 'waiting' && d.waitReason ? ` ${WAIT[d.waitReason] ?? `(${d.waitReason})`}` : ''}
                {d.error || d.code ? ` — ${deliveryProblem(d)}` : ''}
              </span>
              <span className="spacer" />
              <span className="text-3 fs-xs">{timeAgo(d.createdAt)}</span>
            </div>
            <p className="queue-text clamp-2" title={d.text}>
              {d.text}
            </p>
            {(d.state === 'queued' || d.state === 'waiting') && (
              <Button size="sm" variant="ghost" icon="close" onClick={() => void cancel(d)}>
                Cancel
              </Button>
            )}
          </li>
        ))}
      </ul>
    </Popover>
  );
}
