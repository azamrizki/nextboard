// Pointer layer over the canvas while a tool other than Select is active:
//   pen      — freehand stroke → one ink node per stroke
//   eraser   — removes strokes it passes over; a click removes the topmost tile (never a
//              console, an orchestrator card or a group frame)
//   shapes   — click to drop, drag to size; line/arrow drag from tip to tip (Shift = 15°)
//   others   — click to place (note, text, checklist, kanban, calendar, terminal, image)
// Wheel still pans / zooms (Ctrl/⌘ + wheel) while the layer is on top.

import { useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent, type ReactNode, type WheelEvent } from 'react';
import { useReactFlow, useStore } from '@xyflow/react';
import type { Op } from '../lib/types';
import type { BoardController } from './controller';
import { createLine, createTile, type InsertKind } from './create';
import { hitNode, snapAngle } from './geometry';
import type { Pt } from './kinds';
import { inkPath, loadFreehand, useFreehand } from './lazy';
import { invertOps } from './ops';
import { STICKY_TOOLS, type Tool, type UiStore } from './ui';
import { inkColor } from './palette';

interface DrawLayerProps {
  tool: Tool;
  ctl: BoardController;
  ui: UiStore;
  penColor: string;
  penWidth: number;
  /** Place a terminal of the armed preset centred on a board point. */
  onSpawn: (at: { x: number; y: number }) => void;
  /** Open the image picker for a board point. */
  onImage: (at: { x: number; y: number }) => void;
  onContextMenu: (clientX: number, clientY: number) => void;
}

type Gesture =
  | { t: 'pen'; points: [number, number, number][] }
  | { t: 'erase'; before: ReturnType<BoardController['sync']['getState']>; erased: Set<string>; moved: boolean; start: Pt }
  | { t: 'shape'; start: Pt; cur: Pt }
  | null;

const SHAPE_TOOLS = new Set<Tool>(['rect', 'ellipse', 'diamond', 'line', 'arrow']);

export function DrawLayer({ tool, ctl, ui, penColor, penWidth, onSpawn, onImage, onContextMenu }: DrawLayerProps) {
  const rf = useReactFlow();
  const transform = useStore((s) => s.transform);
  const getStroke = useFreehand(tool === 'pen');
  const [g, setG] = useState<Gesture>(null);
  const gRef = useRef<Gesture>(null);
  const set = (next: Gesture) => {
    gRef.current = next;
    setG(next);
  };

  const flow = (e: { clientX: number; clientY: number }): Pt => {
    const p = rf.screenToFlowPosition({ x: e.clientX, y: e.clientY });
    return [p.x, p.y];
  };
  const zoom = transform[2];

  const eraseAt = (p: Pt, strokesOnly: boolean) => {
    const cur = gRef.current;
    if (!cur || cur.t !== 'erase') return;
    const nodes = ctl.nodes;
    const tol = 6 / zoom;
    for (let i = nodes.length - 1; i >= 0; i--) {
      const n = nodes[i];
      // never erased (API-orchestration §1): consoles, orchestrator cards; group frames are
      // removed with Ungroup, not by wiping over their members
      if (n.kind === 'terminal' || n.kind === 'orchestrator' || n.kind === 'group' || cur.erased.has(n.id)) continue;
      const stroke = n.kind === 'ink' || n.kind === 'line';
      if (strokesOnly && !stroke) continue;
      if (hitNode(n, p, tol)) {
        cur.erased.add(n.id);
        ctl.transient([{ op: 'del', id: n.id }]);
        if (!stroke) break; // only the topmost tile
      }
    }
  };

  // Revert to Select after one placement — but only after the click that follows this
  // pointerup, so it lands on this layer and not on the pane (which would end editing).
  const finishTool = () => {
    if (!STICKY_TOOLS.has(tool)) window.setTimeout(() => ui.get().tool === tool && ui.set({ tool: 'select' }), 0);
  };

  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button === 2) return; // context menu
    if (e.button !== 0) return;
    e.preventDefault();
    (e.target as HTMLElement).setPointerCapture?.(e.pointerId);
    const p = flow(e);
    if (tool === 'pen') {
      void loadFreehand().catch(() => undefined);
      set({ t: 'pen', points: [[p[0], p[1], e.pointerType === 'pen' ? e.pressure : 0.5]] });
    } else if (tool === 'eraser') {
      set({ t: 'erase', before: ctl.sync.state, erased: new Set(), moved: false, start: p });
      eraseAt(p, true);
    } else if (SHAPE_TOOLS.has(tool)) {
      set({ t: 'shape', start: p, cur: p });
    }
  };

  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    const cur = gRef.current;
    if (!cur) return;
    const p = flow(e);
    if (cur.t === 'pen') {
      const last = cur.points[cur.points.length - 1];
      if (Math.hypot(p[0] - last[0], p[1] - last[1]) < 1.5 / zoom) return;
      set({ t: 'pen', points: [...cur.points, [p[0], p[1], e.pointerType === 'pen' ? e.pressure : 0.5]] });
    } else if (cur.t === 'erase') {
      if (Math.hypot(p[0] - cur.start[0], p[1] - cur.start[1]) > 3 / zoom) cur.moved = true;
      eraseAt(p, true);
    } else if (cur.t === 'shape') {
      let q = p;
      if ((tool === 'line' || tool === 'arrow') && e.shiftKey) q = snapAngle(cur.start, p);
      set({ ...cur, cur: q });
    }
  };

  const onPointerUp = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    const cur = gRef.current;
    set(null);
    const p = flow(e);
    if (cur?.t === 'pen') {
      commitStroke(cur.points);
      return;
    }
    if (cur?.t === 'erase') {
      if (!cur.moved && cur.erased.size === 0) eraseAt(cur.start, false);
      if (cur.erased.size) {
        const ops: Op[] = [...cur.erased].map((id) => ({ op: 'del', id }));
        ctl.record(invertOps(cur.before, ops), ops);
      }
      return;
    }
    if (cur?.t === 'shape') {
      const [x0, y0] = cur.start;
      const [x1, y1] = cur.cur;
      const dragged = Math.hypot(x1 - x0, y1 - y0) * zoom > 6;
      if (tool === 'line' || tool === 'arrow') {
        if (dragged) createLine(ctl, tool === 'arrow', cur.start, cur.cur);
        else createTile(ctl, ui, tool, { x: x0, y: y0 });
      } else {
        const rect = dragged ? { x: Math.min(x0, x1), y: Math.min(y0, y1), w: Math.abs(x1 - x0), h: Math.abs(y1 - y0) } : undefined;
        createTile(ctl, ui, tool as InsertKind, { x: x0, y: y0 }, rect && rect.w > 8 && rect.h > 8 ? rect : undefined);
      }
      finishTool();
      return;
    }
    // click-to-place tools
    const at = { x: p[0], y: p[1] };
    if (tool === 'spawn') onSpawn(at);
    else if (tool === 'image') onImage(at);
    else if (tool === 'note' || tool === 'text' || tool === 'checklist' || tool === 'kanban' || tool === 'calendar') createTile(ctl, ui, tool, at);
    finishTool();
  };

  const commitStroke = (points: [number, number, number][]) => {
    if (points.length === 0) return;
    const pts = points.length === 1 ? [points[0], [points[0][0] + 0.5, points[0][1] + 0.5, points[0][2]] as [number, number, number]] : points;
    const pad = penWidth + 4;
    const xs = pts.map((q) => q[0]);
    const ys = pts.map((q) => q[1]);
    const x = Math.floor(Math.min(...xs) - pad);
    const y = Math.floor(Math.min(...ys) - pad);
    const w = Math.ceil(Math.max(...xs) - x + pad);
    const h = Math.ceil(Math.max(...ys) - y + pad);
    const rel = pts.map((q) => [Math.round((q[0] - x) * 10) / 10, Math.round((q[1] - y) * 10) / 10, Math.round(q[2] * 100) / 100]);
    const node = ctl.make('ink', { x, y, w, h }, { points: rel, color: penColor, width: penWidth });
    ctl.addNodes([node], false);
  };

  const onWheel = (e: WheelEvent<HTMLDivElement>) => {
    const vp = rf.getViewport();
    if (e.ctrlKey || e.metaKey) {
      const factor = Math.pow(2, -e.deltaY * (e.deltaMode === 1 ? 0.05 : 0.002));
      const zoomTo = Math.max(0.2, Math.min(3, vp.zoom * factor));
      const box = (e.currentTarget as HTMLElement).getBoundingClientRect();
      const px = e.clientX - box.left;
      const py = e.clientY - box.top;
      const k = zoomTo / vp.zoom;
      void rf.setViewport({ x: px - (px - vp.x) * k, y: py - (py - vp.y) * k, zoom: zoomTo });
    } else {
      const dx = e.shiftKey && !e.deltaX ? e.deltaY : e.deltaX;
      const dy = e.shiftKey && !e.deltaX ? 0 : e.deltaY;
      void rf.setViewport({ x: vp.x - dx, y: vp.y - dy, zoom: vp.zoom });
    }
  };

  // ---- preview (board coordinates inside the viewport transform) ----
  let preview: ReactNode = null;
  if (g?.t === 'pen') {
    preview = <path d={inkPath(g.points, penWidth, getStroke)} fill={getStroke ? inkColor(penColor) : 'none'} stroke={getStroke ? 'none' : inkColor(penColor)} strokeWidth={penWidth} strokeLinecap="round" strokeLinejoin="round" />;
  } else if (g?.t === 'shape') {
    const [x0, y0] = g.start;
    const [x1, y1] = g.cur;
    if (tool === 'line' || tool === 'arrow') {
      preview = <line className="draw-preview" x1={x0} y1={y0} x2={x1} y2={y1} />;
    } else if (tool === 'ellipse') {
      preview = <ellipse className="draw-preview" cx={(x0 + x1) / 2} cy={(y0 + y1) / 2} rx={Math.abs(x1 - x0) / 2} ry={Math.abs(y1 - y0) / 2} />;
    } else if (tool === 'diamond') {
      const cx = (x0 + x1) / 2;
      const cy = (y0 + y1) / 2;
      preview = <polygon className="draw-preview" points={`${cx},${Math.min(y0, y1)} ${Math.max(x0, x1)},${cy} ${cx},${Math.max(y0, y1)} ${Math.min(x0, x1)},${cy}`} />;
    } else {
      preview = <rect className="draw-preview" x={Math.min(x0, x1)} y={Math.min(y0, y1)} width={Math.abs(x1 - x0)} height={Math.abs(y1 - y0)} />;
    }
  }

  return (
    <div
      className={`draw-layer tool-${tool}`}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={() => set(null)}
      onWheel={onWheel}
      onContextMenu={(e) => {
        e.preventDefault();
        onContextMenu(e.clientX, e.clientY);
      }}
      aria-hidden="true"
    >
      <svg className="draw-svg">
        <g transform={`translate(${transform[0]} ${transform[1]}) scale(${zoom})`} style={{ '--draw-zoom': zoom } as CSSProperties}>
          {preview}
        </g>
      </svg>
    </div>
  );
}
