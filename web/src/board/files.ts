// Turning files into tiles: machine paths (File from project, folder cards, drops copied
// into the project) and browser files (drops / paste / image picker).

import { api, assetUrl, errorMessage, fsRawUrl } from '../api';
import { toast } from '../components/Toast';
import type { BoardNode, FsStat } from '../lib/types';
import type { BoardController } from './controller';
import {
  baseName,
  DEFAULT_SIZE,
  fitImage,
  isImagePath,
  isMarkdownPath,
  isProbablyText,
  SNAPSHOT_MAX,
  type FileData,
  type FileSnapshot,
  type FileView,
} from './kinds';

type At = { x: number; y: number };

export function loadImageSize(url: string): Promise<{ w: number; h: number }> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve({ w: img.naturalWidth, h: img.naturalHeight });
    img.onerror = () => reject(new Error('The image could not be loaded'));
    img.src = url;
  });
}

function rectAt(at: At, w: number, h: number, centre: boolean) {
  return centre ? { x: at.x - w / 2, y: at.y - h / 2, w, h } : { x: at.x, y: at.y, w, h };
}

export function snapshotOf(content: string, size: number, mtime: string, truncated: boolean): FileSnapshot {
  const cut = content.length > SNAPSHOT_MAX;
  return { content: cut ? content.slice(0, SNAPSHOT_MAX) : content, size, mtime, truncated: truncated || cut };
}

/** Node for a path on a machine (not added yet). `centre` = `at` is the node centre. */
export async function nodeForPath(
  ctl: BoardController,
  machineId: string,
  path: string,
  at: At,
  opts: { dir?: boolean; centre?: boolean } = {},
): Promise<BoardNode | null> {
  const centre = opts.centre ?? true;
  if (opts.dir) {
    const s = DEFAULT_SIZE.folder;
    const data: FileData = { machineId, path, view: 'folder', snapshot: null };
    return ctl.make('file', rectAt(at, s.w, s.h, centre), { ...data });
  }
  if (isImagePath(path)) {
    try {
      const nat = await loadImageSize(fsRawUrl(machineId, path));
      const size = fitImage(nat.w, nat.h);
      return ctl.make('image', rectAt(at, size.w, size.h, centre), {
        src: { machineId, path },
        alt: baseName(path),
        naturalW: nat.w,
        naturalH: nat.h,
      });
    } catch {
      // fall through to a generic file card
    }
  }
  let view: FileView = isMarkdownPath(path) ? 'markdown' : isProbablyText(path) ? 'code' : 'file';
  let snapshot: FileSnapshot | null = null;
  if (view === 'code') {
    try {
      const r = await api.fsRead(machineId, path, 800_000);
      if (r.binary) view = 'file';
      else snapshot = snapshotOf(r.content, r.size, r.mtime, r.truncated);
    } catch (err) {
      toast.error(`Could not read ${baseName(path)}: ${errorMessage(err)}`);
      return null;
    }
  }
  const s = DEFAULT_SIZE[view];
  return ctl.make('file', rectAt(at, s.w, s.h, centre), { machineId, path, view, ...(snapshot ? { snapshot } : {}) });
}

export async function addPath(ctl: BoardController, machineId: string, path: string, at: At, opts: { dir?: boolean; centre?: boolean } = {}) {
  const n = await nodeForPath(ctl, machineId, path, at, opts);
  if (n) ctl.addNodes([n]);
  return n;
}

// ---- browser files ----

export const ASSET_TYPES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);
export const MAX_UPLOAD = 20 * 1024 * 1024;

export function fileToBase64(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => {
      const s = String(r.result);
      resolve(s.slice(s.indexOf(',') + 1));
    };
    r.onerror = () => reject(r.error ?? new Error('Could not read the file'));
    r.readAsDataURL(file);
  });
}

/** Upload an image blob as a hub asset and make an image node for it. */
export async function imageNodeFromBlob(ctl: BoardController, blob: Blob, at: At, alt = '', centre = true): Promise<BoardNode | null> {
  if (!ASSET_TYPES.has(blob.type)) {
    toast.error('Only PNG, JPEG, GIF and WebP images can be added to the board.');
    return null;
  }
  if (blob.size > MAX_UPLOAD) {
    toast.error('Images larger than 20 MB cannot be uploaded.');
    return null;
  }
  try {
    const a = await api.uploadAsset(blob, blob.type);
    let nat = { w: a.w, h: a.h };
    if (!(nat.w > 0 && nat.h > 0)) nat = await loadImageSize(assetUrl(a.id)).catch(() => ({ w: 460, h: 300 }));
    const size = fitImage(nat.w, nat.h);
    return ctl.make('image', rectAt(at, size.w, size.h, centre), { src: { asset: a.id }, alt, naturalW: nat.w, naturalH: nat.h });
  } catch (err) {
    toast.error(`Could not upload the image: ${errorMessage(err)}`);
    return null;
  }
}

/** A text file from the browser kept on the board as a snapshot card (no path on a machine). */
export async function snapshotNodeFromFile(ctl: BoardController, file: File, at: At): Promise<BoardNode | null> {
  const text = await file.text();
  const s = DEFAULT_SIZE.code;
  return ctl.make('file', rectAt(at, s.w, s.h, true), {
    machineId: '',
    path: file.name,
    view: 'code',
    snapshot: snapshotOf(text, file.size, new Date(file.lastModified || Date.now()).toISOString(), false),
  });
}

export function looksLikeText(file: File): boolean {
  if (file.type.startsWith('text/')) return true;
  if (/json|xml|javascript|typescript|yaml|toml|sql|x-sh/.test(file.type)) return true;
  return !file.type && isProbablyText(file.name);
}

/**
 * The files a paste carries, none when it is text. A spreadsheet puts a picture of its cells
 * next to their text, and that is text; a file manager puts the file next to its name, and that
 * is a file. A folder only arrives as an empty name, so it is refused here.
 */
export function clipboardFiles(dt: DataTransfer | null): File[] {
  if (!dt?.files.length) return [];
  const files: File[] = [];
  for (const item of Array.from(dt.items)) {
    if (item.kind !== 'file') continue;
    const file = item.getAsFile();
    if (item.webkitGetAsEntry?.()?.isDirectory) toast.error(`${file?.name ?? 'A folder'} is a folder. Copy the files inside it instead.`);
    else if (file) files.push(file);
  }
  const text = dt.getData('text/plain').trim();
  if (!text || files.some((f) => !f.type.startsWith('image/'))) return files;
  return files.some((f) => text.endsWith(f.name)) ? files : [];
}

const SLOW_PASTE = 2 * 1024 * 1024;

/** Send pasted files one at a time, in order. What fails is said and left out. */
export async function sendPasted(files: File[], send: (file: File) => Promise<FsStat | null>): Promise<FsStat[]> {
  const size = files.reduce((n, f) => n + f.size, 0);
  if (size > SLOW_PASTE) toast.info(files.length === 1 ? `Uploading ${files[0].name}…` : `Uploading ${files.length} files…`);
  const sent: FsStat[] = [];
  for (const file of files) {
    if (file.size > MAX_UPLOAD) {
      toast.error(`${file.name} is larger than 20 MB.`);
      continue;
    }
    try {
      const st = await send(file);
      if (st) sent.push(st);
    } catch (err) {
      toast.error(`Could not paste ${file.name}: ${errorMessage(err)}`);
    }
  }
  return sent;
}
