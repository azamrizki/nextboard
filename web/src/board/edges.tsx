// Edges of board v2 that the hub understands (docs/API-orchestration.md §1):
//   wire    orchestrator / memory / editor / content tile → console or group (who a card
//           directs, briefs or gives its content to)
//   forward console or group → console or group (results of finished turns go on)
// Routed by wireRoute: from the border of one tile to the other, around whatever is in
// the way, so they follow tiles of any size without fixed handle positions.

import { memo, useMemo } from 'react';
import { BaseEdge, EdgeLabelRenderer, useStore, type ConnectionLineComponentProps, type Edge, type EdgeProps, type ReactFlowState } from '@xyflow/react';
import type { BoardEdge } from '../lib/types';
import { useContext } from 'react';
import { useMediaQuery } from '../lib/useMediaQuery';
import { useSessionsValue } from '../orch/hooks';
import { groupSessions, noAgentReason } from './groups';
import { BoardEnvContext } from './ui';
import { draftRoute, routeWire, type Box, type Route, type Side } from './wireRoute';

export interface EdgeData extends Record<string, unknown> {
  edge: BoardEdge;
}
export type FlowEdge = Edge<EdgeData>;

/** Marks on the canvas: a wire may cross them, it only must not vanish under a tile. */
const DRAWINGS = new Set(['shape', 'line', 'ink', 'text']);

interface TileBox extends Box {
  id: string;
  solid: boolean;
}

// One list for all edges, rebuilt once per change of the nodes.
let boxesOf: unknown = null;
let boxes: TileBox[] = [];

function selectBoxes(s: ReactFlowState): TileBox[] {
  if (s.nodes === boxesOf) return boxes;
  boxesOf = s.nodes;
  const next: TileBox[] = [];
  for (const n of s.nodes) {
    const i = s.nodeLookup.get(n.id);
    if (!i || n.hidden) continue;
    const p = i.internals.positionAbsolute;
    next.push({ id: n.id, x: p.x, y: p.y, w: i.measured?.width ?? i.width ?? 0, h: i.measured?.height ?? i.height ?? 0, solid: !DRAWINGS.has(n.type ?? '') });
  }
  const same = next.length === boxes.length && next.every((b, k) => {
    const o = boxes[k];
    return o.id === b.id && o.x === b.x && o.y === b.y && o.w === b.w && o.h === b.h;
  });
  if (!same) boxes = next;
  return boxes;
}

function useRoute(source: string, target: string): Route | null {
  const all = useStore(selectBoxes);
  return useMemo(() => {
    const s = all.find((b) => b.id === source);
    const t = all.find((b) => b.id === target);
    if (!s || !t) return null;
    return routeWire(s, t, all.filter((b) => b.solid && b.id !== source && b.id !== target));
  }, [all, source, target]);
}

const ARROW_D = 'M0 0L-10 -5L-10 5Z';

/** A line with its port at the source and its arrow at the target. */
function Link({ id, route, kind, selected, flowing, idle, interactionWidth }: { id: string; route: Route; kind: string; selected?: boolean; flowing?: boolean; idle?: string; interactionWidth: number }) {
  return (
    <g className={`nb-link nb-link-${kind} ${selected ? 'is-selected' : ''} ${flowing ? 'is-flowing' : ''} ${idle ? 'is-idle' : ''}`}>
      {idle && <title>{`${idle}: this line does nothing until one runs there.`}</title>}
      <BaseEdge id={id} path={route.path} className={`nb-edge nb-${kind}`} interactionWidth={interactionWidth} />
      <circle className="nb-edge-port" cx={route.start.x} cy={route.start.y} r={3} aria-hidden="true" />
      <path className="nb-edge-arrow" d={ARROW_D} transform={`translate(${route.tip.x} ${route.tip.y}) rotate(${route.angle})`} aria-hidden="true" />
    </g>
  );
}

const SIDE_OF: Record<string, Side> = { left: 'left', right: 'right', top: 'top', bottom: 'bottom' };

/** The wire being drawn: the look it will have, dashed until it lands on a tile. */
export function ConnectionWire({ fromX, fromY, toX, toY, fromPosition, connectionStatus }: ConnectionLineComponentProps) {
  const route = draftRoute({ x: fromX, y: fromY }, { x: toX, y: toY }, SIDE_OF[fromPosition] ?? 'right');
  return (
    <g className={`nb-link nb-link-wire nb-link-draft ${connectionStatus === 'valid' ? 'is-valid' : ''}`}>
      <path className="nb-edge" d={route.path} />
      <circle className="nb-edge-port" cx={route.start.x} cy={route.start.y} r={3} />
      <path className="nb-edge-arrow" d={ARROW_D} transform={`translate(${route.tip.x} ${route.tip.y}) rotate(${route.angle})`} />
    </g>
  );
}

/** An orchestrator's wire carries work while a console at its end is busy. */
function useWireBusy(source: string, target: string): boolean {
  const env = useContext(BoardEnvContext);
  const ctl = env?.ctl;
  // re-renders when the answer changes, not on every change of any console
  return useSessionsValue((sessions) => {
    if (!ctl || ctl.node(source)?.kind !== 'orchestrator') return false;
    const node = ctl.node(target);
    if (!node) return false;
    const list = node.kind === 'group' ? groupSessions(ctl.nodes, node) : [sessions[String(node.data.sessionId)]];
    return list.some((s) => s && s.status === 'running' && (s.agentState === 'working' || (s.queue ?? 0) > 0));
  });
}

/** Why a wire or forward does nothing right now: a plain shell at one of its ends. */
function useLineIdle(source: string, target: string): string {
  const env = useContext(BoardEnvContext);
  const ctl = env?.ctl;
  return useSessionsValue(() => {
    if (!ctl) return '';
    const from = ctl.node(source);
    const to = ctl.node(target);
    return (to && noAgentReason(ctl.nodes, to)) || (from && noAgentReason(ctl.nodes, from)) || '';
  });
}

/** A label on a shorter line would lie on the knobs of its tiles; the line's own menu stays. */
const CHIP_ROOM = 130;

const FLOW_SECONDS = 2.4;
const FLOW_ORBS = 3;

/** Light travelling down the wire: a soft halo, a fine stream and a few sparks. */
function WireFlow({ path }: { path: string }) {
  const still = useMediaQuery('(prefers-reduced-motion: reduce)');
  return (
    <g className="nb-wire-flow" aria-hidden="true">
      <path d={path} className="nb-wire-halo" />
      <path d={path} className="nb-wire-stream" />
      {!still &&
        Array.from({ length: FLOW_ORBS }, (_, i) => {
          const begin = `${(-i * FLOW_SECONDS) / FLOW_ORBS}s`;
          return (
            <g key={i} opacity="0">
              <circle r="5.5" className="nb-wire-spark-halo" />
              <circle r="2.4" className="nb-wire-spark" />
              <animateMotion dur={`${FLOW_SECONDS}s`} begin={begin} repeatCount="indefinite" path={path} />
              <animate attributeName="opacity" values="0;1;1;0" keyTimes="0;0.14;0.82;1" dur={`${FLOW_SECONDS}s`} begin={begin} repeatCount="indefinite" />
            </g>
          );
        })}
    </g>
  );
}

function WireEdgeImpl({ id, source, target, selected }: EdgeProps<FlowEdge>) {
  const route = useRoute(source, target);
  const busy = useWireBusy(source, target);
  const idle = useLineIdle(source, target);
  if (!route) return null;
  return (
    <>
      <Link id={id} route={route} kind="wire" selected={selected} flowing={busy} idle={idle} interactionWidth={18} />
      {busy && <WireFlow path={route.path} />}
    </>
  );
}

function ForwardEdgeImpl({ id, source, target, selected, data }: EdgeProps<FlowEdge>) {
  const route = useRoute(source, target);
  const env = useContext(BoardEnvContext);
  const idle = useLineIdle(source, target);
  if (!route) return null;
  const mode = data?.edge.data?.mode === 'split' ? 'split' : 'digest';
  const trigger = data?.edge.data?.trigger === 'order' ? 'orders' : 'each turn';
  return (
    <>
      <Link id={id} route={route} kind="forward" selected={selected} idle={idle} interactionWidth={18} />
      {(selected || Math.hypot(route.tip.x - route.start.x, route.tip.y - route.start.y) >= CHIP_ROOM) && (
        <EdgeLabelRenderer>
          <button
            type="button"
            className={`edge-chip nodrag nopan ${selected ? 'is-selected' : ''}`}
            style={{ transform: `translate(-50%, -50%) translate(${route.mid.x}px, ${route.mid.y}px)` }}
            title={`Forward results (${mode}) after ${trigger}. Click for options.`}
            aria-label={`Forward (${mode}, after ${trigger}): options`}
            onClick={(e) => env?.edgeMenu?.(id, e.clientX, e.clientY)}
            onContextMenu={(e) => {
              e.preventDefault();
              env?.edgeMenu?.(id, e.clientX, e.clientY);
            }}
          >
            Forward · {mode}
          </button>
        </EdgeLabelRenderer>
      )}
    </>
  );
}

/** Phase 7: console → auto preview (cyan) and web tile → its log card. */
function PreviewEdgeImpl({ id, source, target, selected, data }: EdgeProps<FlowEdge>) {
  const route = useRoute(source, target);
  if (!route) return null;
  return <Link id={id} route={route} kind={data?.edge.kind === 'log' ? 'log' : 'preview'} selected={selected} interactionWidth={14} />;
}

export const WireEdge = memo(WireEdgeImpl);
export const ForwardEdge = memo(ForwardEdgeImpl);
export const PreviewEdge = memo(PreviewEdgeImpl);

export const EDGE_TYPES = { wire: WireEdge, forward: ForwardEdge, preview: PreviewEdge, log: PreviewEdge };

const EDGE_LABEL: Record<string, string> = { wire: 'Wire', forward: 'Forward', preview: 'Preview', log: 'Log' };

/** Board edges → React Flow edges (only kinds this client draws). */
export class EdgeCache {
  private cache = new Map<string, { edge: BoardEdge; selected: boolean; flow: FlowEdge }>();
  build(edges: BoardEdge[], selected: ReadonlySet<string>): FlowEdge[] {
    const next = new Map<string, { edge: BoardEdge; selected: boolean; flow: FlowEdge }>();
    const out: FlowEdge[] = [];
    for (const e of edges) {
      if (!(e.kind in EDGE_LABEL)) continue;
      const sel = selected.has(e.id);
      const prev = this.cache.get(e.id);
      const c =
        prev && prev.edge === e && prev.selected === sel
          ? prev
          : {
              edge: e,
              selected: sel,
              flow: {
                id: e.id,
                source: e.source,
                target: e.target,
                sourceHandle: 'out',
                targetHandle: 'in',
                type: e.kind,
                selected: sel,
                data: { edge: e },
                ariaLabel: EDGE_LABEL[e.kind],
              } as FlowEdge,
            };
      next.set(e.id, c);
      out.push(c.flow);
    }
    this.cache = next;
    return out;
  }
}
