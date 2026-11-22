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
 * Whether a paste is a document's text with a picture of it beside: cells from a spreadsheet,
 * a part of a page from an office suite. Its text is what is meant, not the picture. A
 * picture copied from a chat (Telegram puts its caption or link beside it), a browser or a
 * screenshot tool is meant as a picture.
 */
function documentText(dt: DataTransfer, text: string): boolean {
  if (text.includes('\t')) return true; // cells
  const html = dt.getData('text/html');
  return /<table[\s>]/i.test(html) || /urn:schemas-microsoft-com:office|content="(LibreOffice|OpenOffice|Microsoft (Excel|Word|PowerPoint))|ProgId/i.test(html);
}

/**
 * The files a paste carries, none when it is text. A spreadsheet puts a picture of its cells
 * next to their text, and that is text; a file manager puts the file next to its name, and an
 * app a picture next to a caption or a link: those are files. A folder only arrives as an
 * empty name, so it is refused here. The files are read from the items first: some browsers
 * (WebKit) put a pasted picture there and not in files.
 */
export function clipboardFiles(dt: DataTransfer | null): File[] {
  if (!dt) return [];
  const files: File[] = [];
  let items = 0;
  for (const item of Array.from(dt.items ?? [])) {
    if (item.kind !== 'file') continue;
    items++;
    const file = item.getAsFile();
    if (item.webkitGetAsEntry?.()?.isDirectory) toast.error(`${file?.name ?? 'A folder'} is a folder. Copy the files inside it instead.`);
    else if (file) files.push(file);
  }
  if (!items) files.push(...Array.from(dt.files ?? []));
  if (!files.length) return [];
  const text = dt.getData('text/plain').trim();
  if (!text || files.some((f) => !f.type.startsWith('image/'))) return files;
  // pictures with text: a file manager's names, or a picture with its caption, are the files;
  // a document's text with a picture of it is the text
  if (files.some((f) => text.endsWith(f.name))) return files;
  return documentText(dt, text) ? [] : files;
}

const SLOW_PASTE = 2 * 1024 * 1024;

/** The pictures agents read (Claude Code and Codex: PNG, JPEG, GIF and WebP). */
const AGENT_IMAGE = /^image\/(png|jpeg|gif|webp)$/;

/**
 * A pasted picture as an agent can read it. One of a type agents do not read becomes a PNG:
 * a Mac's clipboard gives Safari a TIFF, and there are BMP and HEIC too. One over the upload
 * limit becomes a JPEG: a browser turns a copied photo into a PNG many times its size. What
 * the browser cannot draw stays as it is.
 */
export async function forAgent(file: File): Promise<File> {
  if (!file.type.startsWith('image/') || file.type === 'image/svg+xml' || file.type === 'image/gif') return file;
  const odd = !AGENT_IMAGE.test(file.type);
  const big = file.size > MAX_UPLOAD;
  if (!odd && !big) return file;
  try {
    const bmp = await createImageBitmap(file);
    const canvas = document.createElement('canvas');
    canvas.width = bmp.width;
    canvas.height = bmp.height;
    canvas.getContext('2d')?.drawImage(bmp, 0, 0);
    bmp.close();
    const encode = (type: string) => new Promise<Blob | null>((done) => canvas.toBlob(done, type, 0.9));
    let blob = big ? null : await encode('image/png');
    if (!blob || blob.size > MAX_UPLOAD) blob = await encode('image/jpeg');
    if (!blob) return file;
    const ext = blob.type === 'image/png' ? 'png' : 'jpg';
    return new File([blob], `${file.name.replace(/\.[^./]+$/, '') || 'image'}.${ext}`, { type: blob.type, lastModified: file.lastModified });
  } catch {
    return file;
  }
}

/**
 * Send pasted files one at a time, in order. What fails is said and left out. For an agent
 * (forAgent), a picture is first made one it can read.
 */
export async function sendPasted(files: File[], send: (file: File) => Promise<FsStat | null>, opts?: { forAgent?: boolean }): Promise<FsStat[]> {
  const size = files.reduce((n, f) => n + f.size, 0);
  if (size > SLOW_PASTE) toast.info(files.length === 1 ? `Uploading ${files[0].name}…` : `Uploading ${files.length} files…`);
  const sent: FsStat[] = [];
  for (const pasted of files) {
    const file = opts?.forAgent ? await forAgent(pasted) : pasted;
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

/**
 * A paste into an attachments field that brought neither text nor a file: it says so, with
 * the kinds of data the browser was given, instead of nothing happening.
 */
export function noteEmptyPaste(dt: DataTransfer | null) {
  if (!dt || dt.getData('text/plain').trim() || dt.getData('text/html') || dt.files?.length) return;
  const types = Array.from(dt.types ?? []).filter((t) => t !== 'Files');
  toast.info(
    types.length
      ? `Nothing to paste: the clipboard holds ${types.join(', ')}, which this browser cannot read as a picture or a file.`
      : 'Nothing to paste: the browser was given no text, picture or file. Copy the picture again, or save it and paste the file.',
  );
}
