// Header chip with the number of consoles really processing across all projects: agents in
// a turn and shells with a command running. Opens a list (needs-you first) to jump to any of
// them; the idle ones stay out of it until asked for.

import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import { jumpToSession } from '../agents/jump';
import { agentPill, isActive, isIdle } from '../agents/state';
import type { Session } from '../lib/types';
import { useAsleepCount, useMachineNameOf, useProjects, useRunningCount, useRunningSessions } from '../store';
import { Icon } from './Icon';

const RANK: Record<string, number> = { needsYou: 0, error: 1, working: 2, starting: 3, reconnecting: 3, connecting: 5, legacy: 5, ready: 6, shell: 6, exited: 7 };
/** A shell running a command stands with the agents at work. */
const rankOf = (s: Session) => (isActive(s) ? RANK.working : (RANK[agentPill(s).state] ?? 9));
const needsYouState = (s: Session) => s.agentState === 'needsYou';

function summaryOf(needsYou: number, active: number, idle: number, asleep: number): string[] {
  return [
    needsYou ? `${needsYou} need${needsYou === 1 ? 's' : ''} you` : '',
    active ? `${active} active` : '',
    idle ? `${idle} idle` : '',
    asleep ? `${asleep} asleep` : '',
  ].filter(Boolean);
}

export function LiveChip() {
  // counts only: the list and its groups are made while the popover is open
  const active = useRunningCount(isActive);
  const idle = useRunningCount(isIdle);
  const needsYou = useRunningCount(needsYouState);
  const asleep = useAsleepCount();
  const [open, setOpen] = useState(false);
  const [showIdle, setShowIdle] = useState(false);
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const pop = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (!open) return;
    const r = btn.current?.getBoundingClientRect();
    // keep the popover on screen: right-aligned to the chip, but never past the left edge
    const w = Math.min(380, window.innerWidth - 32);
    if (r) setPos({ top: r.bottom + 8, right: Math.max(8, Math.min(window.innerWidth - r.right, window.innerWidth - w - 8)) });
  }, [open]);

  useEffect(() => {
    if (!open) return;
    pop.current?.querySelector<HTMLElement>('button')?.focus();
    const down = (e: MouseEvent) => {
      if (!pop.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false);
        btn.current?.focus();
      }
    };
    document.addEventListener('pointerdown', down, true);
    document.addEventListener('keydown', key);
    return () => {
      document.removeEventListener('pointerdown', down, true);
      document.removeEventListener('keydown', key);
    };
  }, [open]);

  const summary = summaryOf(needsYou, active, idle, asleep);

  return (
    <>
      <button
        ref={btn}
        type="button"
        className={`live-chip ${active ? 'has-active' : ''}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`${active} active console${active === 1 ? '' : 's'}${summary.length ? `: ${summary.join(', ')}` : ''}`}
        title={summary.length ? summary.join(' · ') : 'No consoles open'}
        onClick={() => setOpen((o) => !o)}
      >
        <span className={`dot ${active ? 'dot-busy' : 'dot-grey'}`} aria-hidden="true" />
        <span className="live-chip-count">
          <span className="tabular">{active}</span>
          <span className="live-chip-label">active</span>
        </span>
        {idle > 0 && <span className="live-chip-sub tabular">· {idle} idle</span>}
        {asleep > 0 && (
          <span className="live-chip-sub tabular" title="Stopped to free memory; they resume when opened or sent work">
            · {asleep} asleep
          </span>
        )}
      </button>
      {open &&
        createPortal(
          <LivePopover popRef={pop} pos={pos} showIdle={showIdle} onShowIdle={setShowIdle} onClose={() => setOpen(false)} />,
          document.body,
        )}
    </>
  );
}

/** The open list: the running sessions grouped by project / workspace, needs-you first in each. */
function LivePopover({
  popRef,
  pos,
  showIdle,
  onShowIdle,
  onClose,
}: {
  popRef: RefObject<HTMLDivElement | null>;
  pos: { top: number; right: number } | null;
  showIdle: boolean;
  onShowIdle: (v: (cur: boolean) => boolean) => void;
  onClose: () => void;
}) {
  const running = useRunningSessions();
  const asleep = useAsleepCount();
  const projects = useProjects();
  const machineName = useMachineNameOf();

  const active = running.filter(isActive).length;
  const idle = running.filter(isIdle).length;
  const needsYou = running.filter(needsYouState).length;
  const summary = summaryOf(needsYou, active, idle, asleep);
  const listed = showIdle ? running : running.filter((s) => !isIdle(s));

  const groups = new Map<string, { title: string; sub: string; list: Session[]; pid?: string; wid?: string }>();
  for (const s of listed) {
    const p = projects.find((x) => x.id === s.projectId);
    const w = p?.workspaces?.find((x) => x.id === s.workspaceId);
    const key = `${s.projectId ?? ''}/${s.workspaceId ?? ''}`;
    const g = groups.get(key) ?? { title: p?.name ?? 'Other', sub: w?.name ?? '', list: [], pid: p?.id, wid: w?.id };
    g.list.push(s);
    groups.set(key, g);
  }
  for (const g of groups.values()) g.list.sort((a, b) => rankOf(a) - rankOf(b));

  const jump = (s: Session) => {
    onClose();
    jumpToSession(s.id);
  };

  const onListKey = (e: ReactKeyboardEvent) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const items = Array.from(popRef.current?.querySelectorAll<HTMLButtonElement>('button.live-item') ?? []);
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    items[(i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus();
  };

  return (
    <div
      ref={popRef}
      className="popover live-pop"
      role="dialog"
      aria-label="Consoles"
      style={pos ? { top: pos.top, right: pos.right } : { visibility: 'hidden' }}
      onKeyDown={onListKey}
    >
      <div className="popover-head">
        <Icon name="terminal" />
        <h2>Consoles</h2>
      </div>
      {summary.length > 0 && <p className="live-summary">{summary.join(' · ')}</p>}
      {running.length === 0 && <p className="text-3 fs-sm">No consoles open. Start one from a project board.</p>}
      {running.length > 0 && listed.length === 0 && <p className="text-3 fs-sm">Nothing is working right now.</p>}
      {[...groups.values()].map((g) => (
        <section key={`${g.pid}/${g.wid}`} className="live-group">
          <h3 className="section-title">
            {g.title}
            {g.sub && <span className="live-sub"> · {g.sub}</span>}
          </h3>
          <ul className="plain">
            {g.list.map((s) => {
              const st = agentPill(s);
              return (
                <li key={s.id}>
                  <button type="button" className="live-item" disabled={!g.pid} onClick={() => jump(s)} title={st.detail}>
                    <span className={`dot dot-${st.dot}`} aria-hidden="true" />
                    <span className="truncate live-name">{s.handle ? `@${s.handle}` : s.name}</span>
                    <span className={`agent-pill tone-${st.tone} live-pill`}>{st.label}</span>
                    <span className="live-meta truncate">{st.activity || machineName(s)}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        </section>
      ))}
      {idle > 0 && (
        <button type="button" className="live-idle-toggle" aria-expanded={showIdle} onClick={() => onShowIdle((v) => !v)}>
          <Icon name={showIdle ? 'chevronUp' : 'chevronDown'} />
          {showIdle ? 'Hide idle' : `Show ${idle} idle`}
        </button>
      )}
    </div>
  );
}
