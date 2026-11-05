// Rectangle / ellipse / diamond drawn with rough.js (lazy) + optional centred label.
// Only the outline (or the fill, when filled) takes clicks, so an empty frame drawn
// around other tiles doesn't block them.

import { memo, useMemo, type CSSProperties } from 'react';
import type { NodeProps } from '@xyflow/react';
import type { FlowNode } from '../flow';
import { readShape, SIZE_PX } from '../kinds';
import { useRough } from '../lazy';
import { dashArray, drawShape, shapeOutline } from '../rough';
import { useBoardEnv, useIsEditing } from '../ui';
import { TileFrame, useTextDraft } from './common';
import { inkColor } from '../palette';

function ShapeTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const n = data.node;
  const d = readShape(n);
  const gen = useRough();
  const editing = useIsEditing(id);
  const { ui } = useBoardEnv();
  const { draft, change } = useTextDraft(id, 'text', d.text, editing);
  const filled = d.fill !== 'transparent';

  const key = JSON.stringify([d.shape, d.stroke, d.strokeWidth, d.strokeStyle, d.roughness, d.fill, d.fillStyle, d.edges, d.seed]);
  const paths = useMemo(() => (gen ? drawShape(gen, d, n.w, n.h) : null), [gen, key, n.w, n.h]);
  const outline = shapeOutline(d.shape, n.w, n.h, d.strokeWidth + 3, d.edges === 'round');
  const dash = dashArray(d.strokeStyle, d.strokeWidth);

  const textStyle = { '--text-size': `${SIZE_PX[d.size]}px`, textAlign: d.align } as CSSProperties;
  const label = ({ rect: 'Rectangle', ellipse: 'Ellipse', diamond: 'Diamond' } as const)[d.shape];

  return (
    <TileFrame id={id} kind="shape" selected={!!selected} label={d.text ? `${label}: ${d.text}` : label} minW={24} minH={24}>
      <svg className="shape-svg" width={n.w} height={n.h} style={{ opacity: d.opacity / 100 }} aria-hidden="true">
        {d.backed && <path d={outline} fill="var(--bg)" stroke="none" />}
        {paths
          ? paths.map((p, i) => (
              <path
                key={i}
                d={p.d}
                stroke={inkColor(p.stroke)}
                strokeWidth={p.strokeWidth}
                fill={inkColor(p.fill)}
                strokeDasharray={p.outline ? dash : undefined}
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            ))
          : (
              <path d={outline} stroke={inkColor(d.stroke)} strokeWidth={d.strokeWidth} fill={filled ? inkColor(d.fill) : 'none'} strokeDasharray={dash} />
            )}
      </svg>
      {/* hit area: stroke band, plus the inside when filled or labelled */}
      <svg className="shape-hit" width={n.w} height={n.h} aria-hidden="true">
        <path
          d={outline}
          fill="transparent"
          stroke="transparent"
          strokeWidth={Math.max(14, d.strokeWidth + 12)}
          style={{ pointerEvents: filled || d.text ? 'all' : 'stroke' }}
        />
      </svg>
      {(d.text || editing) && (
        <div className={`shape-label in-${d.shape} font-${d.font} ${filled ? 'on-fill' : ''}`} style={textStyle}>
          {editing ? (
            <textarea
              className="shape-input nodrag nowheel"
              value={draft}
              autoFocus
              aria-label="Shape label"
              placeholder="Label"
              style={textStyle}
              onChange={(e) => change(e.target.value)}
              onBlur={() => ui.get().editing === id && ui.set({ editing: null })}
              onKeyDown={(e) => {
                e.stopPropagation();
                if (e.key === 'Escape') e.currentTarget.blur();
              }}
            />
          ) : (
            <span style={filled ? undefined : { color: inkColor(d.stroke) }}>{d.text}</span>
          )}
        </div>
      )}
    </TileFrame>
  );
}

export const ShapeTile = memo(ShapeTileImpl);
