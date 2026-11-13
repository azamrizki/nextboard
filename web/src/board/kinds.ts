// Node kinds of board v2 (docs/API-board.md §2 "Kind dan data"): typed views of
// `node.data`, defensive readers (data may come from other / newer clients) and the
// defaults used when a tile is created.

import { itemId } from '../lib/ids';
import type { BoardNode } from '../lib/types';
import { GROUP_COLORS, ROLE_COLOR, ROLES, type GroupData, type MemoryBlock, type MemoryNodeData, type OrchestratorData } from '../orch/types';
import { INK_COLORS, NOTE_COLORS, TAG_COLORS, type NoteColor, type TagColor } from './palette';

export const KNOWN_KINDS = [
  'terminal',
  'note',
  'text',
  'checklist',
  'kanban',
  'calendar',
  'shape',
  'line',
  'ink',
  'image',
  'file',
  // Phase 6 (docs/API-orchestration.md §1): understood by the hub
  'group',
  'orchestrator',
  'memory',
  // Phase 7 (docs/API-studio.md §4.7)
  'web',
  'weblog',
  'link',
  'branches',
  'editor',
  'document',
  // docs/API-gui.md
  'app',
  // docs/API-uptime.md
  'uptime',
  // docs/API-vault.md
  'credentials',
] as const;
export type Kind = (typeof KNOWN_KINDS)[number];

export function isKnownKind(k: string): k is Kind {
  return (KNOWN_KINDS as readonly string[]).includes(k);
}

// ---- small readers ----

const str = (v: unknown, d = ''): string => (typeof v === 'string' ? v : d);
const num = (v: unknown, d: number): number => (typeof v === 'number' && Number.isFinite(v) ? v : d);
const bool = (v: unknown): boolean => v === true;
function oneOf<T extends string>(v: unknown, list: readonly T[], d: T): T {
  return typeof v === 'string' && (list as readonly string[]).includes(v) ? (v as T) : d;
}
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const obj = (v: unknown): Record<string, unknown> => (v && typeof v === 'object' ? (v as Record<string, unknown>) : {});

// ---- text styling (text, shape) ----

export const FONTS = ['sans', 'serif', 'mono', 'hand'] as const;
export type FontName = (typeof FONTS)[number];
export const SIZES = ['s', 'm', 'l', 'xl'] as const;
export type SizeName = (typeof SIZES)[number];
export const ALIGNS = ['left', 'center', 'right'] as const;
export type AlignName = (typeof ALIGNS)[number];

export const FONT_LABEL: Record<FontName, string> = { sans: 'Sans', serif: 'Serif', mono: 'Mono', hand: 'Hand' };
export const SIZE_PX: Record<SizeName, number> = { s: 14, m: 20, l: 28, xl: 40 };

// ---- note ----

export interface NoteData {
  text: string;
  color: NoteColor;
}
export function readNote(n: BoardNode): NoteData {
  return { text: str(n.data.text), color: oneOf(n.data.color, NOTE_COLORS, 'yellow') };
}

// ---- text ----

export interface TextData {
  text: string;
  font: FontName;
  size: SizeName;
  align: AlignName;
  color: string;
}
export function readText(n: BoardNode): TextData {
  return {
    text: str(n.data.text),
    font: oneOf(n.data.font, FONTS, 'sans'),
    size: oneOf(n.data.size, SIZES, 'm'),
    align: oneOf(n.data.align, ALIGNS, 'left'),
    color: str(n.data.color, INK_COLORS[0].value),
  };
}

// ---- checklist ----

export interface CheckItem extends TaskFields {
  id: string;
  text: string;
  done: boolean;
}
export interface ChecklistData {
  title: string;
  items: CheckItem[];
}
export function readChecklist(n: BoardNode): ChecklistData {
  return {
    title: str(n.data.title, 'Checklist'),
    items: arr(n.data.items).map((v) => {
      const o = obj(v);
      return { id: str(o.id) || itemId(), text: str(o.text), done: bool(o.done), ...taskFields(o) };
    }),
  };
}

// ---- tasks of a lead (docs/API-orchestration.md §3.10): written by the hub, kept when a tile saves ----

export interface TaskFields {
  assignee?: string;
  /** The part of the project the task is about (ROADMAP #49): handed out to its consoles. */
  part?: string;
  sessionId?: string;
  orderId?: string;
  state?: string;
  error?: string;
}

function taskFields(o: Record<string, unknown>): TaskFields {
  const out: TaskFields = {};
  for (const k of ['assignee', 'sessionId', 'orderId', 'state', 'error'] as const) {
    if (typeof o[k] === 'string' && o[k]) out[k] = o[k];
  }
  return out;
}

// ---- kanban ----

export const PRIORITIES = ['low', 'med', 'high'] as const;
export type Priority = (typeof PRIORITIES)[number];
export const PRIORITY_LABEL: Record<Priority, string> = { low: 'Low', med: 'Medium', high: 'High' };

export interface KanbanCard extends TaskFields {
  id: string;
  title: string;
  notes?: string;
  due?: string;
  priority?: Priority;
  done?: boolean;
}
export interface KanbanColumn {
  id: string;
  title: string;
  color?: TagColor;
  cards: KanbanCard[];
}
export interface KanbanData {
  title: string;
  columns: KanbanColumn[];
}
export function readKanban(n: BoardNode): KanbanData {
  return {
    title: str(n.data.title, 'Kanban'),
    columns: arr(n.data.columns).map((v) => {
      const c = obj(v);
      return {
        id: str(c.id) || itemId(),
        title: str(c.title, 'Column'),
        color: c.color ? oneOf(c.color, TAG_COLORS, 'sky') : undefined,
        cards: arr(c.cards).map((cv) => {
          const k = obj(cv);
          const card: KanbanCard = { id: str(k.id) || itemId(), title: str(k.title), ...taskFields(k) };
          if (typeof k.notes === 'string' && k.notes) card.notes = k.notes;
          if (typeof k.due === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(k.due)) card.due = k.due;
          if (typeof k.priority === 'string' && (PRIORITIES as readonly string[]).includes(k.priority))
            card.priority = k.priority as Priority;
          if (k.done === true) card.done = true;
          return card;
        }),
      };
    }),
  };
}

// ---- calendar ----

export interface CalEvent {
  id: string;
  title: string;
  date: string;
  time?: string;
  notes?: string;
  color?: TagColor;
}
export interface CalendarData {
  title: string;
  month: string;
  events: CalEvent[];
}
export function monthOf(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
}
export function readUptime(n: BoardNode): { title: string } {
  return { title: str(n.data.title, 'Uptime') };
}

/** Only a title: what the card shows is in the vault, never on the board. */
export function readCredentials(n: BoardNode): { title: string } {
  return { title: str(n.data.title, 'Credentials') };
}

export function readCalendar(n: BoardNode): CalendarData {
  const month = str(n.data.month);
  return {
    title: str(n.data.title, 'Calendar'),
    month: /^\d{4}-\d{2}$/.test(month) ? month : monthOf(new Date()),
    events: arr(n.data.events)
      .map((v) => {
        const e = obj(v);
        const ev: CalEvent = { id: str(e.id) || itemId(), title: str(e.title), date: str(e.date) };
        if (typeof e.time === 'string' && /^\d{2}:\d{2}$/.test(e.time)) ev.time = e.time;
        if (typeof e.notes === 'string' && e.notes) ev.notes = e.notes;
        if (e.color) ev.color = oneOf(e.color, TAG_COLORS, 'sky');
        return ev;
      })
      .filter((e) => /^\d{4}-\d{2}-\d{2}$/.test(e.date)),
  };
}

// ---- shape & line (rough.js) ----

export const SHAPES = ['rect', 'ellipse', 'diamond'] as const;
export type ShapeName = (typeof SHAPES)[number];
export const STROKE_WIDTHS = [1, 2, 4] as const;
export const STROKE_STYLES = ['solid', 'dashed', 'dotted'] as const;
export type StrokeStyle = (typeof STROKE_STYLES)[number];
export const FILL_STYLES = ['hachure', 'cross-hatch', 'solid'] as const;
export type FillStyle = (typeof FILL_STYLES)[number];

export interface StrokeData {
  stroke: string;
  strokeWidth: number;
  strokeStyle: StrokeStyle;
  roughness: number;
  opacity: number;
  seed: number;
}
export interface ShapeData extends StrokeData {
  /** Opaque like the board: what runs behind it, such as an arrow, does not cross its text. */
  backed?: boolean;
  shape: ShapeName;
  fill: string;
  fillStyle: FillStyle;
  edges: 'sharp' | 'round';
  text: string;
  font: FontName;
  size: SizeName;
  align: AlignName;
}
function readStroke(d: Record<string, unknown>): StrokeData {
  return {
    stroke: str(d.stroke, INK_COLORS[0].value),
    strokeWidth: num(d.strokeWidth, 2),
    strokeStyle: oneOf(d.strokeStyle, STROKE_STYLES, 'solid'),
    roughness: Math.max(0, Math.min(2, num(d.roughness, 1))),
    opacity: Math.max(10, Math.min(100, num(d.opacity, 100))),
    seed: Math.floor(num(d.seed, 1)),
  };
}
export function readShape(n: BoardNode): ShapeData {
  return {
    backed: bool(n.data.backed),
    ...readStroke(n.data),
    shape: oneOf(n.data.shape, SHAPES, 'rect'),
    fill: str(n.data.fill, 'transparent'),
    fillStyle: oneOf(n.data.fillStyle, FILL_STYLES, 'hachure'),
    edges: n.data.edges === 'sharp' ? 'sharp' : 'round',
    text: str(n.data.text),
    font: oneOf(n.data.font, FONTS, 'hand'),
    size: oneOf(n.data.size, SIZES, 'm'),
    align: oneOf(n.data.align, ALIGNS, 'center'),
  };
}

export type Pt = [number, number];
export interface LineData extends StrokeData {
  points: [Pt, Pt, Pt];
  /** A wire (diagram arrows): straight stretches through these points, instead of the curve through `points`. */
  route: Pt[] | null;
  /** Where a wire and another from the same source part ways. */
  dots: Pt[];
  arrowStart: boolean;
  arrowEnd: boolean;
}
const readPts = (v: unknown): Pt[] =>
  arr(v).map((p) => {
    const a = arr(p);
    return [num(a[0], 0), num(a[1], 0)] as Pt;
  });
export function readLine(n: BoardNode): LineData {
  const pts = readPts(n.data.points);
  const route = readPts(n.data.route);
  const p0 = pts[0] ?? [0, 0];
  const p2 = pts[pts.length - 1] ?? [n.w, n.h];
  const p1 = pts.length >= 3 ? pts[1] : ([(p0[0] + p2[0]) / 2, (p0[1] + p2[1]) / 2] as Pt);
  return { ...readStroke(n.data), points: [p0, p1, p2], route: route.length >= 2 ? route : null, dots: readPts(n.data.dots), arrowStart: bool(n.data.arrowStart), arrowEnd: bool(n.data.arrowEnd) };
}

// ---- ink ----

export interface InkData {
  points: [number, number, number][];
  color: string;
  width: number;
}
export function readInk(n: BoardNode): InkData {
  return {
    points: arr(n.data.points).map((p) => {
      const a = arr(p);
      return [num(a[0], 0), num(a[1], 0), num(a[2], 0.5)];
    }),
    color: str(n.data.color, INK_COLORS[0].value),
    width: Math.max(1, Math.min(16, num(n.data.width, 4))),
  };
}

// ---- image ----

export type ImageSrc = { asset: string } | { machineId: string; path: string };
export interface ImageData {
  src: ImageSrc | null;
  alt: string;
  naturalW: number;
  naturalH: number;
}
export function readImage(n: BoardNode): ImageData {
  const s = obj(n.data.src);
  const src: ImageSrc | null =
    typeof s.asset === 'string'
      ? { asset: s.asset }
      : typeof s.path === 'string' && typeof s.machineId === 'string'
        ? { machineId: s.machineId, path: s.path }
        : null;
  return { src, alt: str(n.data.alt), naturalW: num(n.data.naturalW, n.w), naturalH: num(n.data.naturalH, n.h) };
}

/** Longest side 460 px, aspect ratio kept (default size). */
export function fitImage(w: number, h: number, longest = 460): { w: number; h: number } {
  if (!(w > 0 && h > 0)) return { w: longest, h: Math.round(longest * 0.66) };
  const k = longest / Math.max(w, h);
  return { w: Math.max(16, Math.round(w * k)), h: Math.max(16, Math.round(h * k)) };
}

// ---- file ----

export const FILE_VIEWS = ['code', 'markdown', 'file', 'folder'] as const;
export type FileView = (typeof FILE_VIEWS)[number];
export interface FileSnapshot {
  content: string;
  size: number;
  mtime: string;
  truncated: boolean;
}
export interface FileData {
  machineId: string;
  path: string;
  view: FileView;
  snapshot: FileSnapshot | null;
}
export function readFile(n: BoardNode): FileData {
  const s = n.data.snapshot ? obj(n.data.snapshot) : null;
  return {
    machineId: str(n.data.machineId),
    path: str(n.data.path),
    view: oneOf(n.data.view, FILE_VIEWS, 'file'),
    snapshot: s
      ? { content: str(s.content), size: num(s.size, 0), mtime: str(s.mtime), truncated: bool(s.truncated) }
      : null,
  };
}

export function baseName(path: string): string {
  const p = path.replace(/\/+$/, '');
  const i = p.lastIndexOf('/');
  return i >= 0 ? p.slice(i + 1) || p : p;
}
export function dirName(path: string): string {
  const p = path.replace(/\/+$/, '');
  const i = p.lastIndexOf('/');
  return i > 0 ? p.slice(0, i) : '/';
}
export function joinPath(dir: string, name: string): string {
  return dir.endsWith('/') ? dir + name : `${dir}/${name}`;
}
export function extOf(path: string): string {
  const b = baseName(path);
  const i = b.lastIndexOf('.');
  return i > 0 ? b.slice(i + 1).toLowerCase() : '';
}

const IMAGE_EXT = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'avif']);
const MD_EXT = new Set(['md', 'markdown', 'mdx']);
const BINARY_EXT = new Set([
  'pdf', 'zip', 'gz', 'tgz', 'xz', 'bz2', '7z', 'rar', 'jar', 'exe', 'dll', 'so', 'dylib', 'bin', 'o', 'a', 'class',
  'wasm', 'mp3', 'mp4', 'mov', 'avi', 'mkv', 'wav', 'flac', 'ogg', 'webm', 'ttf', 'otf', 'woff', 'woff2', 'ico',
  'psd', 'sketch', 'fig', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'sqlite', 'db', 'iso', 'dmg', 'svg',
]);
export const isImagePath = (p: string) => IMAGE_EXT.has(extOf(p));
export const isMarkdownPath = (p: string) => MD_EXT.has(extOf(p));
export const isProbablyText = (p: string) => !IMAGE_EXT.has(extOf(p)) && !BINARY_EXT.has(extOf(p));

/** Snapshot size stored in a node (hub limit: 256 KB of data per node). */
export const SNAPSHOT_MAX = 200_000;
/** Code preview is cut after this many lines. */
export const PREVIEW_LINES = 5000;

// ---- defaults ----

export const DEFAULT_SIZE: Record<string, { w: number; h: number }> = {
  note: { w: 220, h: 200 },
  text: { w: 260, h: 56 },
  checklist: { w: 300, h: 340 },
  kanban: { w: 1000, h: 540 },
  calendar: { w: 600, h: 500 },
  shape: { w: 200, h: 130 },
  code: { w: 560, h: 420 },
  markdown: { w: 560, h: 480 },
  file: { w: 320, h: 132 },
  folder: { w: 320, h: 420 },
  orchestrator: { w: 380, h: 460 },
  memory: { w: 320, h: 300 },
  web: { w: 960, h: 640 },
  weblog: { w: 460, h: 380 },
  link: { w: 320, h: 112 },
  branches: { w: 320, h: 420 },
  editor: { w: 1240, h: 780 },
  document: { w: 560, h: 640 },
  app: { w: 1200, h: 800 },
  uptime: { w: 1000, h: 640 },
  credentials: { w: 960, h: 640 },
};

export function randomSeed(): number {
  return Math.floor(Math.random() * 2 ** 31);
}

export function defaultKanban(): KanbanData {
  return {
    title: 'Kanban',
    columns: [
      { id: itemId(), title: 'Backlog', color: 'violet', cards: [] },
      { id: itemId(), title: 'To do', color: 'sky', cards: [] },
      { id: itemId(), title: 'In progress', color: 'amber', cards: [] },
      { id: itemId(), title: 'Done', color: 'sage', cards: [] },
    ],
  };
}

export function defaultShape(shape: ShapeName): ShapeData {
  return {
    shape,
    stroke: INK_COLORS[0].value,
    strokeWidth: 2,
    strokeStyle: 'solid',
    roughness: 1,
    opacity: 100,
    seed: randomSeed(),
    fill: 'transparent',
    fillStyle: 'hachure',
    edges: 'round',
    text: '',
    font: 'hand',
    size: 'm',
    align: 'center',
  };
}

export function lineStyleDefaults(): StrokeData {
  return { stroke: INK_COLORS[0].value, strokeWidth: 2, strokeStyle: 'solid', roughness: 1, opacity: 100, seed: randomSeed() };
}

/** Human label of a node, for menus, aria labels and confirmations. */
export function nodeLabel(n: BoardNode): string {
  switch (n.kind) {
    case 'note':
      return 'Note';
    case 'text':
      return 'Text';
    case 'checklist':
      return str(n.data.title, 'Checklist');
    case 'kanban':
      return str(n.data.title, 'Kanban');
    case 'calendar':
      return str(n.data.title, 'Calendar');
    case 'uptime':
      return str(n.data.title, 'Uptime');
    case 'credentials':
      return str(n.data.title, 'Credentials');
    case 'shape':
      return ({ rect: 'Rectangle', ellipse: 'Ellipse', diamond: 'Diamond' } as Record<string, string>)[str(n.data.shape)] ?? 'Shape';
    case 'line':
      return n.data.arrowEnd || n.data.arrowStart ? 'Arrow' : 'Line';
    case 'ink':
      return 'Drawing';
    case 'image':
      return str(n.data.alt) || 'Image';
    case 'file':
      return baseName(str(n.data.path)) || 'File';
    case 'terminal':
      return 'Terminal';
    case 'group':
      return str(n.data.name, 'Group');
    case 'orchestrator':
      return str(n.data.title, 'Orchestrator');
    case 'memory':
      return 'Memory';
    case 'web':
      return str(n.data.url) ? `Web: ${str(n.data.url).replace(/^https?:\/\//, '')}` : 'Web page';
    case 'weblog':
      return 'Console log';
    case 'link':
      return str(n.data.title) || str(n.data.url) || 'Link';
    case 'branches':
      return 'Branches';
    case 'editor':
      return 'Code editor';
    case 'document':
      return str(n.data.title, 'Document');
    case 'app':
      return str(n.data.name, 'Desktop app');
    default:
      return 'Tile';
  }
}

// ---- Phase 6: group, orchestrator, memory ----

export function readGroup(n: BoardNode): GroupData {
  const role = oneOf(n.data.role, ROLES, 'general');
  return {
    name: str(n.data.name, 'Group'),
    role,
    color: oneOf(n.data.color, GROUP_COLORS, ROLE_COLOR[role]),
    members: arr(n.data.members).filter((v): v is string => typeof v === 'string'),
    syncInput: n.data.syncInput === true,
  };
}

export function readOrchestrator(n: BoardNode): OrchestratorData {
  return { title: str(n.data.title, 'Orchestrator'), personality: str(n.data.personality) };
}

export function readMemoryNode(n: BoardNode): MemoryNodeData {
  const lp = obj(n.data.lastPush);
  const list = Array.isArray(n.data.blocks) ? (n.data.blocks as MemoryBlock[]) : n.data.scope === 'global' ? [{ name: 'main' }] : [];
  return {
    blocks: list.filter((b) => b && typeof b.name === 'string' && b.name !== ''),
    manual: n.data.manual === true,
    lastPush: n.data.lastPush ? { at: str(lp.at), delivered: num(lp.delivered, 0), busy: num(lp.busy, 0), skipped: num(lp.skipped, 0) } : null,
  };
}
