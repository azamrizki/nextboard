// The row of last checks. Newest on the right; slots not used yet stay empty. A failed check
// is also taller, so that it can be told apart without its colour.

import { useState } from 'react';
import type { Beat } from './api';
import { clock, millis } from './format';

const NAME = ['Down', 'Up', 'Pending'] as const;
const CLASS = ['is-down', 'is-up', 'is-pending'] as const;

const beatText = (b: Beat) => `${clock(b.t)} · ${NAME[b.s]}${b.ms >= 0 ? ` · ${millis(b.ms)}` : ''}${b.m ? ` · ${b.m}` : ''}`;

function summary(shown: Beat[]): string {
  if (!shown.length) return 'No checks yet';
  const count = (s: number) => shown.filter((b) => b.s === s).length;
  const parts = [`${count(1)} up`];
  if (count(0)) parts.push(`${count(0)} down`);
  if (count(2)) parts.push(`${count(2)} pending`);
  return `Last ${shown.length} checks: ${parts.join(', ')}`;
}

export function Heartbeats({ beats, count }: { beats: Beat[]; count: number }) {
  const shown = beats.slice(-count);
  return (
    <span className="up-beats" role="img" aria-label={summary(shown)}>
      {Array.from({ length: count - shown.length }, (_, i) => (
        <span key={`e${i}`} className="up-beat" />
      ))}
      {shown.map((b) => (
        <span key={b.t} className={`up-beat ${CLASS[b.s]}`} />
      ))}
    </span>
  );
}

/** The wider row of a monitor's page, which says about the check under the pointer. */
export function HeartbeatsDetail({ beats, count, idle }: { beats: Beat[]; count: number; idle: string }) {
  const shown = beats.slice(-count);
  const [hover, setHover] = useState<Beat | null>(null);
  return (
    <div className="up-beats-detail">
      <div
        className="up-beats-hit"
        onPointerDown={(e) => {
          // a finger taps (or slides) instead of hovering
          const box = e.currentTarget.getBoundingClientRect();
          const slot = Math.floor(((e.clientX - box.left) / box.width) * count) - (count - shown.length);
          setHover(shown[slot] ?? null);
        }}
        onPointerMove={(e) => {
          // a share of the width, which the board's zoom does not change
          const box = e.currentTarget.getBoundingClientRect();
          const slot = Math.floor(((e.clientX - box.left) / box.width) * count) - (count - shown.length);
          setHover(shown[slot] ?? null);
        }}
        onPointerLeave={(e) => e.pointerType !== 'touch' && setHover(null)}
      >
        <Heartbeats beats={beats} count={count} />
      </div>
      <p className="up-beats-caption truncate tabular">{hover ? beatText(hover) : idle}</p>
    </div>
  );
}
