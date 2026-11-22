// The header's This PC pill (docs/API.md, Host resources): CPU load and the hottest sensor at a
// glance, amber or red past the owner's thresholds; a click opens the details. A tablet held
// upright shows only the ring (CSS), and opens the details as a sheet, as a phone does from
// its "…" menu. The Code view's status bar has its own quiet line of the same.

import { useRef, useState, useSyncExternalStore } from 'react';
import { Modal } from '../components/Modal';
import { Popover } from '../components/Popover';
import { useLayout } from '../lib/layout';
import type { HostSnapshot } from '../lib/types';
import { HostPanel } from './HostPanel';
import { levelOf, memPercent, useHost } from './store';

/** A ring of a percentage. */
export function Ring({ pct, className }: { pct: number; className?: string }) {
  const r = 6;
  const len = 2 * Math.PI * r;
  return (
    <svg className={`host-ring ${className ?? ''}`} viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r={r} className="host-ring-track" />
      <circle cx="8" cy="8" r={r} className="host-ring-value" strokeDasharray={`${(len * Math.max(0, Math.min(100, pct))) / 100} ${len}`} transform="rotate(-90 8 8)" />
    </svg>
  );
}

/** What the pill says, for its title and screen readers. */
export function hostSummary(s: HostSnapshot): string {
  const parts = [`CPU ${Math.round(s.cpu.percent)}%`, `memory ${Math.round(memPercent(s))}%`];
  if (s.hottest) parts.push(`${s.hottest.label} ${Math.round(s.hottest.c)} °C`);
  const head = s.level === 'hot' ? 'This PC runs hot' : s.level === 'warn' ? 'This PC runs warm' : 'This PC';
  return `${head}: ${parts.join(', ')}`;
}

// a tablet held upright: the pill is a ring and the details a sheet (as the project switcher)
const portrait = typeof window !== 'undefined' ? window.matchMedia('(orientation: portrait)') : null;
function usePortrait(): boolean {
  return useSyncExternalStore(
    (fn) => {
      portrait?.addEventListener('change', fn);
      return () => portrait?.removeEventListener('change', fn);
    },
    () => !!portrait?.matches,
  );
}

/** The details in a sheet (a phone, a tablet upright). */
export function HostSheet({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="This PC" icon="monitor" onClose={onClose} size="md" className="host-sheet">
      <HostPanel compact consoles={2} onLeave={onClose} head="facts" />
    </Modal>
  );
}

export function HostPill({ where = 'header' }: { where?: 'header' | 'status' }) {
  const s = useHost();
  const { layout } = useLayout();
  const upright = usePortrait();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLButtonElement>(null);
  if (!s?.supported) return null;
  const level = levelOf(s);
  const cpu = Math.round(s.cpu.percent);
  const hottest = s.hottest ? Math.round(s.hottest.c) : null;
  const summary = hostSummary(s);
  const sheet = layout === 'phone' || (layout === 'tablet' && upright);
  const details = open ? (
    sheet ? (
      <HostSheet onClose={() => setOpen(false)} />
    ) : (
      <Popover anchor={ref} onClose={() => setOpen(false)} label="This PC" placement={where === 'status' ? 'above' : 'below'} className="host-popover" width={480}>
        <HostPanel onLeave={() => setOpen(false)} />
      </Popover>
    )
  ) : null;

  if (where === 'status') {
    const gpu = s.gpus?.find((g) => g.busy != null)?.busy;
    return (
      <>
        <button ref={ref} type="button" className={`wb-status-item host-status lvl-${level} ${open ? 'is-open' : ''}`} title={summary} aria-expanded={open} aria-haspopup="dialog" onClick={() => setOpen(!open)}>
          CPU <b className="tabular">{cpu}%</b> · RAM{' '}
          <b className="tabular">
            {(s.memory.used / 2 ** 30).toFixed(1)}/{Math.round(s.memory.total / 2 ** 30)} G
          </b>
          {gpu != null && (
            <>
              {' '}
              · GPU <b className="tabular">{Math.round(gpu)}%</b>
            </>
          )}
          {hottest != null && (
            <>
              {' '}
              · <b className="tabular">{hottest}°</b>
            </>
          )}
        </button>
        {details}
      </>
    );
  }

  return (
    <>
      <button ref={ref} type="button" className={`host-pill lvl-${level} ${open ? 'is-open' : ''}`} title={summary} aria-label={`${summary}. Open This PC`} aria-expanded={open} aria-haspopup="dialog" onClick={() => setOpen(!open)}>
        <Ring pct={cpu} />
        <span className="host-pill-num tabular">{cpu}%</span>
        {hottest != null && (
          <>
            <span className="host-pill-sep host-pill-num" aria-hidden="true" />
            <span className="host-pill-temp host-pill-num tabular">{hottest}°</span>
          </>
        )}
      </button>
      {details}
    </>
  );
}
