// Board node of kind "terminal" (data.sessionId): header (drag handle) + resizable xterm.
// The hub creates and deletes these nodes together with their session.
// No backdrop-filter on nodes (DESIGN.md §3.5) so pan/zoom stays smooth.

import { memo, useRef, type CSSProperties } from 'react';
import { AgentBar } from '../agents/AgentBar';
import { DraftBar } from '../agents/DraftBar';
import { fontPx, overrideOf, useGlobalTextScale } from '../agents/textSize';
import { Handle, NodeResizer, Position, type NodeProps } from '@xyflow/react';
import { Icon } from '../components/Icon';
import type { Session } from '../lib/types';
import { useMachine, useSession, useStoreReady } from '../store';
import type { FlowNode } from './flow';
import { MIN_H, MIN_W } from './layout';
import { NodeHeader, useConsoleActions } from './NodeHeader';
import { useResizeHistory } from './tiles/common';
import { useTerminal } from './useTerminal';
import { useConsoleThemeAttr } from '../lib/theme';

function TerminalNodeImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const sessionId = typeof data.node.data.sessionId === 'string' ? data.node.data.sessionId : '';
  const session = useSession(sessionId);
  const ready = useStoreReady();
  if (!session) {
    return (
      <div className={`nb-node nb-node-missing ${selected ? 'selected' : ''}`}>
        <div className="nb-node-header">
          <span className="tile-mark" aria-hidden="true">
            <span className="dot dot-grey" />
          </span>
          <span className="node-name">Terminal</span>
        </div>
        <div className="tile-placeholder">
          {ready ? (
            <>
              <Icon name="terminal" />
              <span>This terminal’s session has ended.</span>
            </>
          ) : (
            <>
              <span className="spinner" aria-hidden="true" />
              <span>Connecting…</span>
            </>
          )}
        </div>
      </div>
    );
  }
  return <TerminalCard nodeId={id} session={session} selected={!!selected} textOverride={overrideOf(data.node.data.textScale)} />;
}

interface TerminalCardProps {
  nodeId: string;
  session: Session;
  selected: boolean;
  textOverride: number | null;
}

function TerminalCard({ nodeId, session, selected, textOverride }: TerminalCardProps) {
  const machine = useMachine(session.machineId);
  const workplace = useMachine(session.remoteMachineId || session.machineId);
  const resize = useResizeHistory(nodeId);
  const termRef = useRef<HTMLDivElement>(null);
  const globalScale = useGlobalTextScale();
  const textScale = textOverride ?? globalScale;
  const consoleTheme = useConsoleThemeAttr();
  const socket = useTerminal(session.id, termRef, fontPx(textScale));
  const actions = useConsoleActions(nodeId, session, machine);
  const needsYou = session.agentState === 'needsYou';

  // Preset accent colour from the API (top accent line).
  const style = (session.color ? { '--node-accent': session.color } : {}) as CSSProperties;

  return (
    <>
      {/* Outside .nb-node (which clips its rounded corners) so the handles aren't cut off. */}
      <NodeResizer
        minWidth={MIN_W}
        minHeight={MIN_H}
        lineClassName="nb-resize-line"
        handleClassName="nb-resize-handle"
        onResizeStart={resize.onResizeStart}
        onResizeEnd={resize.onResizeEnd}
      />
      {/* Phase 6 edges: wires from cards end here; drag from the right knob to forward results */}
      <Handle type="target" position={Position.Left} id="in" className="nb-handle nb-handle-in" isConnectable={false} />
      <Handle type="source" position={Position.Right} id="out" className="nb-handle nb-handle-out" title="Drag to another console or group to forward this console’s results" />
      <div
        className={`nb-node ${selected ? 'selected' : ''} ${needsYou ? 'needs-you' : ''} ${session.paused ? 'is-paused' : ''}`}
        style={style}
        data-session={session.id}
        data-state={session.agentState ?? ''}
      >
        <NodeHeader nodeId={nodeId} session={session} machine={workplace} socket={socket} actions={actions} textOverride={textOverride} textScale={textScale} />
        {session.error && session.status === 'error' && <div className="node-error">{session.error}</div>}
        <AgentBar session={session} agent={actions.agent} onRestart={actions.restartAgent} onResume={actions.resume} onPick={actions.pick} />
        <DraftBar session={session} />
        <div className="nb-node-body nodrag nowheel nopan" data-theme={consoleTheme}>
          <div ref={termRef} className="nb-term" />
        </div>
      </div>
      {actions.dialogs}
    </>
  );
}

export const TerminalNode = memo(TerminalNodeImpl);
