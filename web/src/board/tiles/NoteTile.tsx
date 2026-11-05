// Sticky note: paper colour (fold corner cycles it), text that shrinks to fit (20 → 11 px),
// double-click to edit. New notes open ready to type.

import { memo, useRef } from 'react';
import type { NodeProps } from '@xyflow/react';
import type { FlowNode } from '../flow';
import { readNote } from '../kinds';
import { NOTE_COLORS } from '../palette';
import { useBoardEnv, useIsEditing } from '../ui';
import { TileFrame, useFitText, useTextDraft } from './common';

const MAX_FONT = 20;
const MIN_FONT = 11;

function NoteTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const n = data.node;
  const d = readNote(n);
  const editing = useIsEditing(id);
  const { ctl, ui } = useBoardEnv();
  const { draft, change } = useTextDraft(id, 'text', d.text, editing);
  const textRef = useRef<HTMLElement | null>(null);
  const font = useFitText(textRef, [draft, n.w, n.h, editing], MAX_FONT, MIN_FONT);

  const cycle = () => {
    const i = NOTE_COLORS.indexOf(d.color);
    ctl.updateData(id, { color: NOTE_COLORS[(i + 1) % NOTE_COLORS.length] });
  };

  const stopEditing = () => {
    if (ui.get().editing === id) ui.set({ editing: null });
  };

  return (
    <TileFrame id={id} kind="note" selected={!!selected} label="Sticky note" minW={120} minH={100}>
      <div className={`note note-${d.color}`}>
        {editing ? (
          <textarea
            ref={(el) => {
              textRef.current = el;
            }}
            className="note-text note-input nodrag nowheel"
            style={{ fontSize: font }}
            value={draft}
            autoFocus
            aria-label="Note text"
            placeholder="Write something…"
            onFocus={(e) => e.currentTarget.setSelectionRange(e.currentTarget.value.length, e.currentTarget.value.length)}
            onChange={(e) => change(e.target.value)}
            onBlur={stopEditing}
            onKeyDown={(e) => {
              e.stopPropagation();
              if (e.key === 'Escape') e.currentTarget.blur();
            }}
          />
        ) : (
          <div
            ref={(el) => {
              textRef.current = el;
            }}
            className={`note-text ${d.text ? '' : 'is-empty'}`}
            style={{ fontSize: font }}
          >
            {d.text || 'Double-click to write'}
          </div>
        )}
        <button
          type="button"
          className="note-fold nodrag"
          aria-label={`Note colour: ${d.color}. Change colour`}
          title="Change colour"
          onClick={(e) => {
            e.stopPropagation();
            cycle();
          }}
          onDoubleClick={(e) => e.stopPropagation()}
        />
      </div>
    </TileFrame>
  );
}

export const NoteTile = memo(NoteTileImpl);
