// A project's cover and icon as the cards show them (DESIGN.md §3.11): the box has its size before
// the picture loads, so nothing moves when it arrives, and a picture that cannot load leaves the
// quiet fallback (a gradient in the project's tone, the monogram).

import { useState } from 'react';
import { assetUrl } from '../api';
import type { Project } from '../lib/types';

/** One or two capitals from the name: "demo-backoffice" → DB, "nextboard" → N. */
export function projectMonogram(name: string): string {
  const words = name.split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  const first = (w: string | undefined) => (w ? Array.from(w)[0].toUpperCase() : '');
  return first(words[0]) + first(words[1]) || '?';
}

const MONOGRAM_TONES = 6;

/** By id, so a project keeps its colour when renamed. */
export function monogramTone(id: string): number {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return h % MONOGRAM_TONES;
}

/** The picture fades in once it is there; the box around it never changes size. */
function Picture({ id, onFail }: { id: string; onFail: () => void }) {
  const [loaded, setLoaded] = useState(false);
  return <img src={assetUrl(id)} alt="" decoding="async" loading="lazy" draggable={false} className={loaded ? 'is-loaded' : ''} onLoad={() => setLoaded(true)} onError={onFail} />;
}

export function ProjectCover({ project: p, className = '' }: { project: Project; className?: string }) {
  const id = p.cover?.assetId;
  const [failed, setFailed] = useState('');
  return (
    <div className={`project-cover tone-${monogramTone(p.id)} ${className}`} aria-hidden="true">
      {id && failed !== id && <Picture key={id} id={id} onFail={() => setFailed(id)} />}
    </div>
  );
}

export function ProjectIcon({ project: p, className = '' }: { project: Project; className?: string }) {
  const id = p.icon?.assetId;
  const [failed, setFailed] = useState('');
  if (id && failed !== id) {
    return (
      <span className={`project-mono project-icon ${className}`} aria-hidden="true">
        <Picture key={id} id={id} onFail={() => setFailed(id)} />
      </span>
    );
  }
  return (
    <span className={`project-mono tone-${monogramTone(p.id)} ${className}`} aria-hidden="true">
      {projectMonogram(p.name)}
    </span>
  );
}
