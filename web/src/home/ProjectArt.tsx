// A project's cover and icon as the cards show them (DESIGN.md §3.11): the box has its size before
// the picture loads, so nothing moves when it arrives, and a picture that cannot load leaves the
// quiet fallback (a gradient in the project's tone, the monogram).

import { useState, type CSSProperties } from 'react';
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

const LABELS = 'prod|production|dev|develop|development|staging|stage|stg|uat|qa|test|testing|local|demo|sandbox|preprod|beta|live';
const SEP = '[-–—:|/_.]';
const LABEL_RE = new RegExp(
  `^\\s*[\\[(](${LABELS})[\\])]\\s*${SEP}*\\s*|^\\s*(${LABELS})\\s*${SEP}+\\s*|\\s*${SEP}+\\s*(${LABELS})\\s*$|\\s*[\\[(](${LABELS})[\\])]\\s*$`,
  'i',
);

/**
 * The project's name as a cover carries it: without working labels such as "PROD - " or
 * "(staging)", at most 40 characters (the same rule as the hub's art.DisplayName).
 */
export function coverTitle(name: string): string {
  let s = name.replace(/[\u0000-\u001f\u007f]/g, ' ').replace(/\s+/g, ' ').trim();
  for (let prev = ''; s !== prev; ) {
    prev = s;
    s = s.replace(LABEL_RE, '').trim();
  }
  s = s.replace(/["“”`]/g, '');
  return Array.from(s).slice(0, 40).join('').trim();
}

const ACCENT = /^#[0-9A-Fa-f]{6}$/;

export function ProjectCover({ project: p, className = '' }: { project: Project; className?: string }) {
  const id = p.cover?.assetId;
  const [failed, setFailed] = useState('');
  const shown = !!id && failed !== id;
  // A cover made as a background gets the name set over it here, always in the same type and
  // place, and it follows a rename (DESIGN.md §3.11 Cover).
  const title = shown && p.cover?.title ? coverTitle(p.name) : '';
  const accent = p.cover?.accent && ACCENT.test(p.cover.accent) ? p.cover.accent : undefined;
  return (
    <div className={`project-cover tone-${monogramTone(p.id)} ${title ? 'has-title' : ''} ${className}`} aria-hidden="true">
      {shown && <Picture key={id} id={id} onFail={() => setFailed(id)} />}
      {title && (
        <div className="cover-frame" style={accent ? ({ '--cover-accent': accent } as CSSProperties) : undefined}>
          <span className="cover-title">{title}</span>
          {p.cover?.tagline && <span className="cover-tagline">{p.cover.tagline}</span>}
        </div>
      )}
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
