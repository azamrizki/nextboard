// Link graph of the markdown vault (E5): wiki and markdown links between notes, orphans
// included. d3-force lays it out once (no animation); drag the background to pan, wheel
// to zoom, 0 fits, arrows pan, + / − zoom; a spacing slider; click a note to open it.

import { forceCenter, forceCollide, forceLink, forceManyBody, forceSimulation, forceX, forceY, type SimulationLinkDatum, type SimulationNodeDatum } from 'd3-force';
import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent, type WheelEvent } from 'react';
import type { MdGraph } from '../types';

interface N extends SimulationNodeDatum {
  id: string;
  title: string;
  deg: number;
  orphan: boolean;
}
type L = SimulationLinkDatum<N> & { kind: string };

export default function Graph({ graph, current, onOpen }: { graph: MdGraph; current: string | null; onOpen: (rel: string) => void }) {
  const [spacing, setSpacing] = useState(60);
  const box = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 800, h: 560 });
  const [view, setView] = useState({ x: 0, y: 0, k: 1 });
  const drag = useRef<{ x: number; y: number; vx: number; vy: number } | null>(null);

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setSize({ w: el.clientWidth, h: el.clientHeight }));
    ro.observe(el);
    setSize({ w: el.clientWidth, h: el.clientHeight });
    return () => ro.disconnect();
  }, []);

  const laid = useMemo(() => {
    const nodes: N[] = (graph.nodes ?? []).map((n) => ({ id: n.id, title: n.title || n.id, deg: n.in + n.out, orphan: n.orphan }));
    const ids = new Set(nodes.map((n) => n.id));
    const links: L[] = (graph.edges ?? []).filter((e) => ids.has(e.from) && ids.has(e.to)).map((e) => ({ source: e.from, target: e.to, kind: e.kind }));
    const sim = forceSimulation<N>(nodes)
      .force('link', forceLink<N, L>(links).id((n) => n.id).distance(spacing * 1.4).strength(0.6))
      .force('charge', forceManyBody().strength(-spacing * 4))
      .force('collide', forceCollide<N>((n) => 10 + Math.sqrt(n.deg) * 3))
      .force('x', forceX(0).strength(0.04))
      .force('y', forceY(0).strength(0.06))
      .force('center', forceCenter(0, 0))
      .stop();
    for (let i = 0; i < 300; i++) sim.tick();
    return { nodes, links };
  }, [graph, spacing]);

  const fit = () => {
    const ns = laid.nodes;
    if (!ns.length) return;
    const xs = ns.map((n) => n.x ?? 0);
    const ys = ns.map((n) => n.y ?? 0);
    const w = Math.max(...xs) - Math.min(...xs) + 160;
    const h = Math.max(...ys) - Math.min(...ys) + 80;
    const k = Math.max(0.2, Math.min(1.25, Math.min(size.w / w, size.h / h)));
    setView({ k, x: -((Math.max(...xs) + Math.min(...xs)) / 2) * k, y: -((Math.max(...ys) + Math.min(...ys)) / 2) * k });
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(fit, [laid, size.w, size.h]);

  const onWheel = (e: WheelEvent) => {
    const k = Math.max(0.2, Math.min(3, view.k * (e.deltaY < 0 ? 1.12 : 1 / 1.12)));
    setView((v) => ({ ...v, k, x: (v.x * k) / v.k, y: (v.y * k) / v.k }));
  };
  const onDown = (e: PointerEvent<SVGSVGElement>) => {
    if ((e.target as Element).closest('.g-node')) return;
    drag.current = { x: e.clientX, y: e.clientY, vx: view.x, vy: view.y };
    e.currentTarget.setPointerCapture(e.pointerId);
  };
  const onMove = (e: PointerEvent<SVGSVGElement>) => {
    const d = drag.current;
    if (d) setView((v) => ({ ...v, x: d.vx + e.clientX - d.x, y: d.vy + e.clientY - d.y }));
  };
  const onKey = (e: KeyboardEvent) => {
    const step = 60;
    if (e.key === '0') fit();
    else if (e.key === '+' || e.key === '=') setView((v) => ({ ...v, k: Math.min(3, v.k * 1.2), x: v.x * 1.2, y: v.y * 1.2 }));
    else if (e.key === '-') setView((v) => ({ ...v, k: Math.max(0.2, v.k / 1.2), x: v.x / 1.2, y: v.y / 1.2 }));
    else if (e.key === 'ArrowLeft') setView((v) => ({ ...v, x: v.x + step }));
    else if (e.key === 'ArrowRight') setView((v) => ({ ...v, x: v.x - step }));
    else if (e.key === 'ArrowUp') setView((v) => ({ ...v, y: v.y + step }));
    else if (e.key === 'ArrowDown') setView((v) => ({ ...v, y: v.y - step }));
    else return;
    e.preventDefault();
  };

  const tx = size.w / 2 + view.x;
  const ty = size.h / 2 + view.y;
  return (
    <div className="md-graph">
      <div className="md-graph-bar">
        <span className="fs-xs text-3">
          {laid.nodes.length} notes · {laid.links.length} links{graph.unresolved ? ` · ${graph.unresolved} unresolved` : ''}
        </span>
        <span className="spacer" />
        <label className="fs-xs text-2 md-spacing">
          Spacing
          <input type="range" min={30} max={140} value={spacing} onChange={(e) => setSpacing(Number(e.target.value))} />
        </label>
        <button type="button" className="btn btn-ghost btn-sm" onClick={fit}>
          Fit (0)
        </button>
      </div>
      <div className="md-graph-canvas" ref={box} tabIndex={0} role="application" aria-label="Link graph: arrows pan, plus and minus zoom, 0 fits" onKeyDown={onKey}>
        <svg width={size.w} height={size.h} onWheel={onWheel} onPointerDown={onDown} onPointerMove={onMove} onPointerUp={() => (drag.current = null)}>
          <g transform={`translate(${tx},${ty}) scale(${view.k})`}>
            {laid.links.map((l, i) => {
              const s = l.source as N;
              const t = l.target as N;
              return <line key={i} className={`g-link ${l.kind === 'wiki' ? 'is-wiki' : ''}`} x1={s.x} y1={s.y} x2={t.x} y2={t.y} />;
            })}
            {laid.nodes.map((n) => (
              <g
                key={n.id}
                className={`g-node ${n.orphan ? 'is-orphan' : ''} ${current === n.id ? 'is-current' : ''}`}
                transform={`translate(${n.x},${n.y})`}
                role="button"
                tabIndex={-1}
                aria-label={`Open ${n.title}`}
                onClick={() => onOpen(n.id)}
              >
                <circle r={5 + Math.min(10, Math.sqrt(n.deg) * 2.5)} />
                <text y={-(9 + Math.min(10, Math.sqrt(n.deg) * 2.5))}>{n.title.length > 28 ? `${n.title.slice(0, 27)}…` : n.title}</text>
              </g>
            ))}
          </g>
        </svg>
        {!laid.nodes.length && <p className="list-msg md-graph-empty">No markdown notes in this project yet.</p>}
      </div>
    </div>
  );
}
