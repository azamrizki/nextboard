// Placeholder for node kinds this client doesn't know (added by a newer client / hub).

import { memo } from 'react';
import type { NodeProps } from '@xyflow/react';
import { Icon } from '../../components/Icon';
import type { FlowNode } from '../flow';
import { TileFrame } from './common';

function UnsupportedTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  return (
    <TileFrame id={id} kind="unsupported" selected={!!selected} label="Unsupported tile" resizable={false}>
      <div className="tile-placeholder">
        <Icon name="info" />
        <span>Unsupported tile</span>
        <span className="mono">{data.node.kind}</span>
      </div>
    </TileFrame>
  );
}

export const UnsupportedTile = memo(UnsupportedTileImpl);
