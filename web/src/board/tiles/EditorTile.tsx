// The Code workbench as a board card (node kind "editor", docs/API-studio.md §7): files,
// editor, terminal and debugger live among the other tiles and block nothing. One card per
// board: its tabs and unsaved edits belong to the project, not to the card. Drag a wire from
// the right knob to a console, group or orchestrator card to send questions there.

import { lazy, memo, Suspense } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Button } from '../../components/Button';
import type { FlowNode } from '../flow';
import { useBoardEnv } from '../ui';
import { TileFrame } from './common';

const CodeStudio = lazy(() => import('../../studio/code/CodeStudio'));

function EditorTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, project, viewCenter, frame, ui } = useBoardEnv();
  const first = ctl.nodes.find((n) => n.kind === 'editor');
  const duplicate = !!first && first.id !== id;
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" className="nb-handle nb-handle-in" isConnectable={false} />
      <Handle type="source" position={Position.Right} id="out" className="nb-handle nb-handle-out nb-handle-wire" title="Drag to a console, group or orchestrator: questions from the editor go there" />
      <TileFrame id={id} kind="editor" selected={!!selected} label="Code editor" minW={560} minH={340}>
        {duplicate ? (
          <div className="editor-tile-dup">
            <p>The editor of this project is already open in another card.</p>
            <Button size="sm" className="nodrag" onClick={() => first && frame(first.id)}>
              Show it
            </Button>
          </div>
        ) : (
          <Suspense
            fallback={
              <div className="editor-tile-loading" role="status">
                <span className="spinner" aria-hidden="true" /> Loading the editor…
              </div>
            }
          >
            <CodeStudio project={project} ctl={ctl} ui={ui} nodeId={id} viewCenter={viewCenter} filled={!!data.node.data.fill} />
          </Suspense>
        )}
      </TileFrame>
    </>
  );
}

export const EditorTile = memo(EditorTileImpl);
