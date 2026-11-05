// Card sizes: the display button in a card's header (DisplayButton) gives the card one of the
// device sizes. One undo step; a group follows its member. A card without a header, or several
// cards at once, have the same list in their right-click menu.

import type { MenuEntry } from '../components/Menu';
import type { BoardNode, NodeLayout, Op } from '../lib/types';
import { DEVICES, externalUrl, localTarget, readWeb } from '../web/model';
import type { BoardController } from './controller';
import { boundsOf } from './geometry';
import { frameOps, groupsTouching } from './groups';
import { DEFAULT_SIZE, readImage } from './kinds';
import { DEFAULT_H, DEFAULT_W } from './layout';

type Device = (typeof DEVICES)[number];
type Size = { w: number; h: number };

/** Drawings and text are not cards, and a group's frame follows its members. */
const NOT_CARDS = new Set(['group', 'text', 'shape', 'line', 'ink']);
const HEADER = '.tile-head, .nb-node-header, .wb-title';
/** Narrower than this a card stops working (its minW in the tile). */
const MIN_W: Record<string, number> = { editor: 560, kanban: 420, calendar: 420, uptime: 420, credentials: 360 };

/** Does this right-click's menu list the sizes? Not on a card whose header has the button. */
export function sizesInMenu(target: EventTarget | null, kind: string, count: number): boolean {
  if (NOT_CARDS.has(kind) || !(target instanceof Element)) return false;
  if (!target.closest('.react-flow__node')?.querySelector(HEADER)) return true;
  return count > 1 && !!target.closest(HEADER);
}

/** A dev-server web tile shows its page at the device's own viewport (WebTile). */
function isDevWeb(n: BoardNode): boolean {
  if (n.kind !== 'web') return false;
  const url = readWeb(n).url;
  return !(url && !localTarget(url) && externalUrl(url));
}

/** What the card adds around its page: header, toolbar, tabs, borders. */
function webChrome(id: string): Size {
  const root = document.querySelector<HTMLElement>(`.react-flow__node[data-id="${CSS.escape(id)}"]`);
  const body = root?.querySelector<HTMLElement>('.web-body');
  return root && body ? { w: root.offsetWidth - body.clientWidth, h: root.offsetHeight - body.clientHeight } : { w: 0, h: 0 };
}

/** "Fit tile" gives a card back the size it starts with. */
function startSize(n: BoardNode): Size | null {
  if (n.kind === 'terminal') return { w: DEFAULT_W, h: DEFAULT_H };
  if (n.kind === 'file') return DEFAULT_SIZE[String(n.data.view)] ?? DEFAULT_SIZE.file;
  if (n.kind === 'image') {
    const d = readImage(n);
    return d.naturalW > 0 && d.naturalH > 0 ? { w: d.naturalW, h: d.naturalH } : null;
  }
  return DEFAULT_SIZE[n.kind] ?? null;
}

function sizeFor(n: BoardNode, d: Device): Size | null {
  if (!d.w) return startSize(n);
  if (n.kind === 'image') {
    // a picture keeps its shape, inside the device's box
    const k = Math.min(d.w / n.w, d.h / n.h);
    return { w: Math.round(n.w * k), h: Math.round(n.h * k) };
  }
  if ((MIN_W[n.kind] ?? 0) > d.w) return null;
  if (!isDevWeb(n)) return { w: d.w, h: d.h };
  const c = webChrome(n.id);
  return { w: d.w + c.w, h: d.h + c.h };
}

function isCurrent(n: BoardNode, d: Device): boolean {
  if (isDevWeb(n)) return readWeb(n).device === d.id;
  const s = sizeFor(n, d);
  return !!s && s.w === n.w && s.h === n.h;
}

function applySize(ctl: BoardController, cards: BoardNode[], d: Device, reveal: (r: NodeLayout) => void) {
  const ops: Op[] = [];
  const rects = new Map<string, NodeLayout>();
  for (const n of cards) {
    const s = sizeFor(n, d);
    if (!s) continue;
    ops.push({ op: 'patch', id: n.id, set: isDevWeb(n) ? { ...s, data: { device: d.id } } : s } as Op);
    rects.set(n.id, { x: n.x, y: n.y, ...s });
  }
  ops.push(...frameOps(ctl.nodes, groupsTouching(ctl.nodes, rects.keys()), rects));
  ctl.apply(ops);
  const box = boundsOf([...rects.values()]);
  if (box) reveal(box);
}

/** The size this card has now, when it is one of the devices. */
export function currentDevice(ctl: BoardController, id: string): Device | undefined {
  const n = ctl.node(id);
  return n && !NOT_CARDS.has(n.kind) ? DEVICES.find((d) => d.w > 0 && isCurrent(n, d)) : undefined;
}

/** Fit tile, the displays and the phones; nothing when none of these nodes is a card. */
export function deviceEntries(ctl: BoardController, ids: string[], reveal: (r: NodeLayout) => void): MenuEntry[] {
  const cards = ctl.nodes.filter((n) => ids.includes(n.id) && !NOT_CARDS.has(n.kind));
  if (!cards.length) return [];
  const row = (d: Device): MenuEntry => ({
    label: d.label,
    icon: d.group === 'fit' ? 'fit' : d.group === 'phone' ? 'phone' : 'monitor',
    meta: d.w ? `${d.w} × ${d.h}` : undefined,
    checked: cards.every((n) => isCurrent(n, d)),
    disabled: !cards.some((n) => sizeFor(n, d)),
    onSelect: () => applySize(ctl, cards, d, reveal),
  });
  const of = (group: Device['group']) => DEVICES.filter((d) => d.group === group).map(row);
  return [...of('fit'), { type: 'heading', label: 'Displays' }, ...of('display'), { type: 'heading', label: 'Phones' }, ...of('phone')];
}
