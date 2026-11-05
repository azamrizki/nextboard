// Line / arrow: rough.js path through three points (tips + middle bend knob).
// Drag a tip to move it (Shift snaps to 15°), drag the middle knob to bend,
// double-click the middle knob to straighten. Only the stroke takes clicks. A wire (a diagram
// arrow in the wiring style) stays straight stretches: its tips move, and a knob on each
// stretch between two corners pushes that stretch sideways.

import { memo, useMemo, useRef, type PointerEvent as ReactPointerEvent } from 'react';
import { useReactFlow, type NodeProps } from '@xyflow/react';
import type { FlowNode } from '../flow';
import { isStraight, moveWireStretch, moveWireTip, normalizeLine, normalizeRoute, onRoute, snapAngle } from '../geometry';
import { cleanRoute } from '../../studio/diagrams/wires';
import { readLine, type Pt } from '../kinds';
import { useRough } from '../lazy';
import { arrowHeads, dashArray, drawLine, lineDrawnD } from '../rough';
import { useBoardEnv } from '../ui';
import { rectOf } from '../controller';
import { inkColor } from '../palette';

type Knob = 0 | 1 | 2;

function LineTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const n = data.node;
  const d = readLine(n);
  const gen = useRough();
  const rf = useReactFlow();
  const { ctl } = useBoardEnv();
  const start = useRef<{ rect: ReturnType<typeof rectOf>; points: [Pt, Pt, Pt] } | null>(null);

  const key = JSON.stringify([d.points, d.route, d.stroke, d.strokeWidth, d.strokeStyle, d.roughness, d.arrowStart, d.arrowEnd, d.seed]);
  const paths = useMemo(() => (gen ? drawLine(gen, d) : null), [gen, key]);
  const dash = dashArray(d.strokeStyle, d.strokeWidth);
  const pathD = lineDrawnD(d);

  const abs = (p: Pt): Pt => [p[0] + n.x, p[1] + n.y];

  const dragKnob = (e: ReactPointerEvent, k: Knob) => {
    if (e.button !== 0) return;
    e.stopPropagation();
    e.preventDefault();
    const node = ctl.node(id);
    if (!node) return;
    const d0 = readLine(node);
    const pts0 = d0.points.map((p) => [p[0] + node.x, p[1] + node.y] as Pt) as [Pt, Pt, Pt];
    const straight = isStraight(pts0[0], pts0[1], pts0[2]);
    start.current = { rect: rectOf(node), points: d0.points };
    let moved = false;
    const move = (ev: PointerEvent) => {
      let p: Pt = (() => {
        const f = rf.screenToFlowPosition({ x: ev.clientX, y: ev.clientY });
        return [f.x, f.y];
      })();
      const next = pts0.slice() as [Pt, Pt, Pt];
      if (k === 1) {
        next[1] = p;
      } else {
        if (ev.shiftKey) p = snapAngle(k === 0 ? pts0[2] : pts0[0], p);
        next[k] = p;
        if (straight) next[1] = [(next[0][0] + next[2][0]) / 2, (next[0][1] + next[2][1]) / 2];
      }
      const { rect, points } = normalizeLine(next);
      moved = true;
      ctl.transient([{ op: 'patch', id, set: { ...rect, data: { points } } }]);
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      const s = start.current;
      start.current = null;
      const after = ctl.node(id);
      if (!moved || !s || !after) return;
      ctl.record(
        [{ op: 'patch', id, set: { ...s.rect, data: { points: s.points } } }],
        [{ op: 'patch', id, set: { ...rectOf(after), data: { points: readLine(after).points } } }],
      );
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  /** Drags a tip of a wire (stretch -1: the first, -2: the last) or the stretch from point `stretch`. */
  const dragWire = (e: ReactPointerEvent, stretch: number) => {
    if (e.button !== 0) return;
    e.stopPropagation();
    e.preventDefault();
    const node = ctl.node(id);
    if (!node) return;
    const d0 = readLine(node);
    if (!d0.route) return;
    const abs0 = d0.route.map((p) => [p[0] + node.x, p[1] + node.y] as Pt);
    const dots0 = d0.dots.map((p) => [p[0] + node.x, p[1] + node.y] as Pt);
    const before = { ...rectOf(node), data: { points: d0.points, route: d0.route, dots: d0.dots } };
    const shaped = (ev: PointerEvent) => {
      const f = rf.screenToFlowPosition({ x: ev.clientX, y: ev.clientY });
      const p: Pt = [Math.round(f.x), Math.round(f.y)];
      const abs = stretch === -1 ? moveWireTip(abs0, false, p) : stretch === -2 ? moveWireTip(abs0, true, p) : moveWireStretch(abs0, stretch, p);
      const route = cleanRoute(abs);
      const { rect, points, route: rel, dots } = normalizeRoute(route.length >= 2 ? route : abs, dots0.filter((q) => onRoute(q, abs)));
      return { ...rect, data: { points, route: rel, dots } };
    };
    let moved = false;
    const move = (ev: PointerEvent) => {
      moved = true;
      ctl.transient([{ op: 'patch', id, set: shaped(ev) }]);
    };
    const up = (ev: PointerEvent) => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      if (moved) ctl.record([{ op: 'patch', id, set: before }], [{ op: 'patch', id, set: shaped(ev) }]);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const straighten = () => {
    const p = d.points.map(abs) as [Pt, Pt, Pt];
    p[1] = [(p[0][0] + p[2][0]) / 2, (p[0][1] + p[2][1]) / 2];
    const { rect, points } = normalizeLine(p);
    ctl.patch(id, { ...rect, data: { points } });
  };

  const isArrow = d.arrowStart || d.arrowEnd;
  return (
    <div className={`tile tile-line ${selected ? 'selected' : ''}`} role="group" aria-label={isArrow ? 'Arrow' : 'Line'}>
      <svg className="line-svg" width={n.w} height={n.h} style={{ opacity: d.opacity / 100 }} aria-hidden="true">
        {paths
          ? paths.map((p, i) => (
              <path
                key={i}
                d={p.d}
                stroke={inkColor(p.stroke)}
                strokeWidth={p.strokeWidth}
                fill="none"
                strokeDasharray={p.outline && i === 0 ? dash : undefined}
                // wires of one source lie on each other: blended edges would make a shared stretch look heavier
                shapeRendering={d.route && d.roughness === 0 && i === 0 ? 'crispEdges' : undefined}
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            ))
          : (
              <>
                <path d={pathD} stroke={inkColor(d.stroke)} strokeWidth={d.strokeWidth} fill="none" strokeDasharray={dash} />
                {arrowHeads(d).map((h, i) => (
                  <polyline key={i} points={h.map((q) => q.join(',')).join(' ')} stroke={inkColor(d.stroke)} strokeWidth={d.strokeWidth} fill="none" />
                ))}
              </>
            )}
        <path className="line-hit" d={pathD} stroke="transparent" strokeWidth={Math.max(16, d.strokeWidth + 14)} fill="none" />
        {selected && <path className="line-sel" d={pathD} fill="none" />}
        {d.dots.map((p, i) => (
          <circle key={i} cx={p[0]} cy={p[1]} r={d.strokeWidth + 2} fill={inkColor(d.stroke)} />
        ))}
      </svg>
      {selected && d.route && (
        <>
          {[0, d.route.length - 1].map((at) => (
            <button
              key={at}
              type="button"
              tabIndex={-1}
              className="line-knob nodrag tip"
              style={{ left: d.route![at][0], top: d.route![at][1] }}
              aria-label={at === 0 ? 'Start point' : 'End point'}
              title="Drag to move"
              onPointerDown={(e) => dragWire(e, at === 0 ? -1 : -2)}
            />
          ))}
          {d.route.slice(1, -2).map((p, i) => (
            <button
              key={i}
              type="button"
              tabIndex={-1}
              className="line-knob nodrag mid"
              style={{ left: (p[0] + d.route![i + 2][0]) / 2, top: (p[1] + d.route![i + 2][1]) / 2 }}
              aria-label="Move this stretch"
              title="Drag to move this stretch"
              onPointerDown={(e) => dragWire(e, i + 1)}
            />
          ))}
        </>
      )}
      {selected &&
        !d.route &&
        ([0, 1, 2] as Knob[]).map((k) => (
          <button
            key={k}
            type="button"
            tabIndex={-1}
            className={`line-knob nodrag ${k === 1 ? 'mid' : 'tip'}`}
            style={{ left: d.points[k][0], top: d.points[k][1] }}
            aria-label={k === 1 ? 'Bend (double-click to straighten)' : k === 0 ? 'Start point' : 'End point'}
            title={k === 1 ? 'Drag to bend · double-click to straighten' : 'Drag to move · Shift snaps to 15°'}
            onPointerDown={(e) => dragKnob(e, k)}
            onDoubleClick={(e) => {
              e.stopPropagation();
              if (k === 1) straighten();
            }}
          />
        ))}
    </div>
  );
}

export const LineTile = memo(LineTileImpl);
