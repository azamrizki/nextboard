// A project's cover and icon (API-board §1): how an uploaded picture is fitted before it goes to
// the asset store, and the "Create with AI" jobs that outlive the dialog that started them.

import type { ArtKind } from '../lib/types';
import type { Job } from '../orch/types';

/** The biggest a picture is kept: the cover is shown across a card, the icon at 36–44 px. */
export const ART_MAX = { cover: { w: 1600, h: 1200 }, icon: { w: 256, h: 256 } } as const;

/** What the asset store takes as it is (docs/API-board.md §5). */
export const ART_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];

export interface Fit {
  /** The part of the picture kept, in its own pixels. */
  sx: number;
  sy: number;
  sw: number;
  sh: number;
  /** The size it is drawn at. */
  w: number;
  h: number;
  /** Nothing to crop or shrink: the file can go as it is. */
  same: boolean;
}

/**
 * The cover keeps its shape and shrinks to fit 1600×1200. The icon is shown square, so the middle
 * square is kept (what the card would show anyway) and shrunk to 256 px.
 */
export function fitSize(w: number, h: number, kind: ArtKind): Fit {
  const max = ART_MAX[kind];
  let sx = 0;
  let sy = 0;
  let sw = w;
  let sh = h;
  if (kind === 'icon' && w !== h) {
    const side = Math.min(w, h);
    sx = Math.floor((w - side) / 2);
    sy = Math.floor((h - side) / 2);
    sw = side;
    sh = side;
  }
  const scale = Math.min(1, max.w / sw, max.h / sh);
  const out = { w: Math.max(1, Math.round(sw * scale)), h: Math.max(1, Math.round(sh * scale)) };
  return { sx, sy, sw, sh, ...out, same: sx === 0 && sy === 0 && sw === w && sh === h && scale === 1 };
}

/** The picture as it goes to the hub: as it is when it already fits and is a type the store takes. */
export async function fitImage(file: Blob, kind: ArtKind): Promise<Blob> {
  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(file);
  } catch {
    throw new Error('This picture cannot be read. Use a PNG, JPEG, GIF or WebP image.');
  }
  try {
    const fit = fitSize(bitmap.width, bitmap.height, kind);
    if (fit.same && ART_TYPES.includes(file.type)) return file;
    const canvas = document.createElement('canvas');
    canvas.width = fit.w;
    canvas.height = fit.h;
    const ctx = canvas.getContext('2d');
    if (!ctx) throw new Error('This browser cannot resize the picture.');
    ctx.imageSmoothingQuality = 'high';
    ctx.drawImage(bitmap, fit.sx, fit.sy, fit.sw, fit.sh, 0, 0, fit.w, fit.h);
    const encode = (type: string, quality?: number) => new Promise<Blob | null>((done) => canvas.toBlob(done, type, quality));
    // WebP keeps an icon's transparency and is small; a browser without it gives PNG back
    const webp = await encode('image/webp', 0.9);
    if (webp?.type === 'image/webp') return webp;
    const fallback = kind === 'cover' ? await encode('image/jpeg', 0.88) : await encode('image/png');
    if (!fallback) throw new Error('This browser cannot resize the picture.');
    return fallback;
  } finally {
    bitmap.close();
  }
}

// ---- Create with AI: the jobs of this page, by project and kind ---------------------------------

export const artKey = (projectId: string, kind: ArtKind) => `${projectId}:${kind}`;

/** A job that is still going: it keeps going when the dialog closes, and shows when it opens again. */
const running = new Map<string, Job>();

export const artJobs = {
  get: (projectId: string, kind: ArtKind) => running.get(artKey(projectId, kind)),
  set: (projectId: string, kind: ArtKind, job: Job) => running.set(artKey(projectId, kind), job),
  drop: (projectId: string, kind: ArtKind) => running.delete(artKey(projectId, kind)),
};

export const jobOver = (j: Job | undefined) => !!j && ['done', 'failed', 'cancelled'].includes(j.state);

/** What a job's progress says, in a few words. */
export function jobText(j: Job): string {
  if (j.state === 'queued') return 'Waiting to start…';
  const t = j.progress?.text || j.progress?.phase || '';
  const pct = typeof j.progress?.pct === 'number' ? ` ${Math.round(j.progress.pct)}%` : '';
  return `${t || 'Making the image…'}${pct}`;
}
