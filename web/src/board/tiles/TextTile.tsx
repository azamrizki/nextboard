// Free text box: font / size / alignment / colour from the inspector. Grows to fit its
// text while typing; a box left empty disappears when editing ends.

import { memo, useEffect, useLayoutEffect, useRef, type CSSProperties } from 'react';
import type { NodeProps } from '@xyflow/react';
import type { FlowNode } from '../flow';
import { readText, SIZE_PX } from '../kinds';
import { useBoardEnv, useIsEditing } from '../ui';
import { TileFrame, useTextDraft } from './common';
import { inkColor } from '../palette';

function TextTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const n = data.node;
  const d = readText(n);
  const editing = useIsEditing(id);
  const { ctl, ui } = useBoardEnv();
  const { draft, change } = useTextDraft(id, 'text', d.text, editing);
  const ref = useRef<HTMLTextAreaElement | HTMLDivElement | null>(null);

  // Grow the node with its content while editing (part of the same undo step).
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !editing) return;
    el.style.height = '0px';
    const need = Math.ceil(el.scrollHeight) + 8;
    el.style.height = '';
    if (Math.abs(need - n.h) > 2 && need > 24) {
      ctl.apply([{ op: 'patch', id, set: { h: need } }], { merge: `text:${id}` });
    }
  }, [draft, editing, n.w, d.size, d.font, ctl, id, n.h]);

  const stopEditing = () => {
    if (ui.get().editing === id) ui.set({ editing: null });
  };

  // Editing ended (blur, Esc, click elsewhere): an empty box is removed, together with
  // its creation in the undo history.
  const wasEditing = useRef(editing);
  useEffect(() => {
    if (wasEditing.current && !editing && !(ctl.node(id)?.data.text as string | undefined)?.trim()) {
      ctl.forgetLast(`text:${id}`);
      ctl.apply([{ op: 'del', id }], { history: false });
    }
    wasEditing.current = editing;
  }, [editing, ctl, id]);

  const style = {
    '--text-size': `${SIZE_PX[d.size]}px`,
    '--text-color': inkColor(d.color),
    textAlign: d.align,
  } as CSSProperties;

  return (
    <TileFrame id={id} kind="text" selected={!!selected} label="Text" minW={60} minH={24}>
      {editing ? (
        <textarea
          ref={(el) => {
            ref.current = el;
          }}
          className={`text-body font-${d.font} nodrag nowheel`}
          style={style}
          value={draft}
          autoFocus
          aria-label="Text"
          placeholder="Type here"
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
            ref.current = el;
          }}
          className={`text-body font-${d.font} ${d.text ? '' : 'is-empty'}`}
          style={style}
        >
          {d.text || 'Empty text'}
        </div>
      )}
    </TileFrame>
  );
}

export const TextTile = memo(TextTileImpl);
