// Creating tiles at a board position (tools, insert rail, right-click menu).

import type { BoardNode, NodeLayout } from '../lib/types';
import type { BoardController } from './controller';
import { normalizeLine } from './geometry';
import { DEFAULT_SIZE, defaultKanban, defaultShape, lineStyleDefaults, monthOf, type Pt, type ShapeName } from './kinds';
import { INK_COLORS, NOTE_COLORS } from './palette';
import type { UiStore } from './ui';

export type InsertKind = 'note' | 'text' | 'checklist' | 'kanban' | 'calendar' | 'uptime' | 'credentials' | ShapeName | 'line' | 'arrow' | 'orchestrator' | 'memory';

type At = { x: number; y: number };

const centred = (at: At, w: number, h: number): NodeLayout => ({ x: at.x - w / 2, y: at.y - h / 2, w, h });

let noteColor = 0;

/** Create a tile centred on `at` (or filling `rect`); text-like tiles open in edit mode. */
export function createTile(ctl: BoardController, ui: UiStore, kind: InsertKind, at: At, rect?: NodeLayout): BoardNode {
  let node: BoardNode;
  switch (kind) {
    case 'note': {
      const s = DEFAULT_SIZE.note;
      node = ctl.make('note', rect ?? centred(at, s.w, s.h), { text: '', color: NOTE_COLORS[noteColor++ % NOTE_COLORS.length] });
      break;
    }
    case 'text': {
      const s = DEFAULT_SIZE.text;
      node = ctl.make('text', rect ?? { x: at.x - 8, y: at.y - s.h / 2, w: s.w, h: s.h }, {
        text: '',
        font: 'sans',
        size: 'm',
        align: 'left',
        color: INK_COLORS[0].value,
      });
      break;
    }
    case 'checklist': {
      const s = DEFAULT_SIZE.checklist;
      node = ctl.make('checklist', rect ?? centred(at, s.w, s.h), { title: 'Checklist', items: [] });
      break;
    }
    case 'kanban': {
      const s = DEFAULT_SIZE.kanban;
      node = ctl.make('kanban', rect ?? centred(at, s.w, s.h), { ...defaultKanban() });
      break;
    }
    case 'calendar': {
      const s = DEFAULT_SIZE.calendar;
      node = ctl.make('calendar', rect ?? centred(at, s.w, s.h), { title: 'Calendar', month: monthOf(new Date()), events: [] });
      break;
    }
    case 'uptime': {
      const s = DEFAULT_SIZE.uptime;
      node = ctl.make('uptime', rect ?? centred(at, s.w, s.h), { title: 'Uptime' });
      break;
    }
    case 'credentials': {
      const s = DEFAULT_SIZE.credentials;
      node = ctl.make('credentials', rect ?? centred(at, s.w, s.h), { title: 'Credentials' });
      break;
    }
    case 'orchestrator': {
      const s = DEFAULT_SIZE.orchestrator;
      const used = new Set(ctl.nodes.filter((n) => n.kind === 'orchestrator').map((n) => String(n.data.title ?? '')));
      let title = 'Orchestrator';
      for (let i = 2; used.has(title) && i < 100; i++) title = `Orchestrator ${i}`;
      node = ctl.make('orchestrator', rect ?? centred(at, s.w, s.h), { title });
      break;
    }
    case 'memory': {
      const s = DEFAULT_SIZE.memory;
      node = ctl.make('memory', rect ?? centred(at, s.w, s.h), { scope: 'project' });
      break;
    }
    case 'line':
    case 'arrow': {
      const p0: Pt = rect ? [rect.x, rect.y] : [at.x - 100, at.y];
      const p2: Pt = rect ? [rect.x + rect.w, rect.y + rect.h] : [at.x + 100, at.y];
      return createLine(ctl, kind === 'arrow', p0, p2);
    }
    default: {
      const s = DEFAULT_SIZE.shape;
      node = ctl.make('shape', rect ?? centred(at, kind === 'diamond' ? 160 : s.w, kind === 'diamond' ? 160 : s.h), { ...defaultShape(kind) });
    }
  }
  // note/text: creation and the first typing are one undo step (an empty text box vanishes)
  ctl.apply([{ op: 'put', node }], { merge: kind === 'note' || kind === 'text' ? `text:${node.id}` : undefined });
  ctl.select([node.id]);
  if (kind === 'note' || kind === 'text') ui.set({ editing: node.id });
  return node;
}

export function createLine(ctl: BoardController, arrow: boolean, p0: Pt, p2: Pt): BoardNode {
  const mid: Pt = [(p0[0] + p2[0]) / 2, (p0[1] + p2[1]) / 2];
  const { rect, points } = normalizeLine([p0, mid, p2]);
  const node = ctl.make('line', rect, { ...lineStyleDefaults(), points, arrowStart: false, arrowEnd: arrow });
  ctl.apply([{ op: 'put', node }]);
  ctl.select([node.id]);
  return node;
}
