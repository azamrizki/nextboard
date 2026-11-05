// One pen stroke = one node (perfect-freehand outline, lazy). Only the stroke takes clicks.

import { memo, useMemo } from 'react';
import type { NodeProps } from '@xyflow/react';
import type { FlowNode } from '../flow';
import { readInk } from '../kinds';
import { inkPath, useFreehand } from '../lazy';
import { inkColor } from '../palette';

function InkTileImpl({ data, selected }: NodeProps<FlowNode>) {
  const n = data.node;
  const d = readInk(n);
  const getStroke = useFreehand();
  const path = useMemo(() => inkPath(d.points, d.width, getStroke), [n.data.points, d.width, getStroke]);
  const filled = !!getStroke;
  return (
    <div className={`tile tile-ink ${selected ? 'selected' : ''}`} role="group" aria-label="Drawing">
      <svg width={n.w} height={n.h} aria-hidden="true">
        <path
          className="ink-path"
          d={path}
          fill={filled ? inkColor(d.color) : 'none'}
          stroke={filled ? 'transparent' : inkColor(d.color)}
          strokeWidth={filled ? Math.max(8, d.width) : d.width}
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </div>
  );
}

export const InkTile = memo(InkTileImpl);
