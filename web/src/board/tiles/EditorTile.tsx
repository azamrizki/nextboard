// The Code workbench as a board card (node kind "editor", docs/API-studio.md §7): files,
// editor, terminal and debugger live among the other tiles and block nothing. A card is placed
// like a console (v0.9.0-beta.2, data.part): at the Root, one tree of every part, or in one part.
// A board holds one card per placement; its tabs and unsaved edits belong to the project and that
// placement, not to the card. Drag a wire from the right knob to a console, group or orchestrator
// card to send questions there.

import { lazy, memo, Suspense, useEffect, useRef, useState, type RefObject } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Button } from '../../components/Button';
import type { FlowNode } from '../flow';
import { useBoardEnv } from '../ui';
import { cardFor, cardLabel, cardPlacement } from '../editorCards';
import { isRoot, projectParts } from '../../lib/parts';
import { placeLabel } from '../../lib/placement';
import { TileFrame } from './common';

const CodeStudio = lazy(() => import('../../studio/code/CodeStudio'));

/**
 * Whether the card has been on screen since the board opened. The workbench starts then and
 * stays: before, a card scrolled far away still loaded the editor (megabytes of script), read its
 * tabs again, started language servers on the machine and watched its files at each board open.
 */
function useSeen(ref: RefObject<HTMLElement | null>, now: boolean): boolean {
  const [seen, setSeen] = useState(now || typeof IntersectionObserver === 'undefined');
  useEffect(() => {
    if (seen) return;
    const el = ref.current;
    if (!el) return;
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) setSeen(true);
    });
    io.observe(el);
    return () => io.disconnect();
  }, [ref, seen]);
  return seen || now;
}

function EditorTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, project, viewCenter, frame, ui } = useBoardEnv();
  const placement = cardPlacement(data.node, project);
  // a second card of the same placement points to the first one
  const first = cardFor(ctl.nodes, project, placement);
  const duplicate = !!first && first.id !== id;
  const box = useRef<HTMLDivElement>(null);
  const seen = useSeen(box, !!data.node.data.fill || !!selected);
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" className="nb-handle nb-handle-in" isConnectable={false} />
      <Handle type="source" position={Position.Right} id="out" className="nb-handle nb-handle-out nb-handle-wire" title="Drag to a console, group or orchestrator: questions from the editor go there" />
      <TileFrame id={id} kind="editor" selected={!!selected} label={cardLabel(project, placement)} minW={560} minH={340}>
        {duplicate ? (
          <div className="editor-tile-dup">
            <p>{projectParts(project).length > 1 ? `${isRoot(placement) ? 'The editor of all parts' : `The editor of ${placeLabel(project, placement)}`} is already open in another card.` : 'The editor of this project is already open in another card.'}</p>
            <Button size="sm" className="nodrag" onClick={() => first && frame(first.id)}>
              Show it
            </Button>
          </div>
        ) : !seen ? (
          <div className="editor-tile-loading" ref={box} aria-hidden="true" />
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
