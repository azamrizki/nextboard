// This PC card (docs/API.md, Host resources): the hub's computer, live, on the board, for a big
// screen or a wall. While it is shown the hub reads every 2 s; every This PC card shows the
// same computer, so it has only a title of its own.

import { memo } from 'react';
import type { NodeProps } from '@xyflow/react';
import { HostPanel } from '../../host/HostPanel';
import { levelOf, useHost } from '../../host/store';
import type { FlowNode } from '../flow';
import { readResources } from '../kinds';
import { TileFrame, TileHead } from './common';

function ResourcesTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const d = readResources(data.node);
  const s = useHost();
  const level = levelOf(s);
  return (
    <TileFrame id={id} kind="resources" selected={!!selected} label={d.title} minW={320} minH={300} className={`lvl-${level}`}>
      <TileHead
        id={id}
        icon="monitor"
        title={d.title}
        meta={
          s?.supported ? (
            <span className={`tile-meta host-card-meta lvl-${level}`}>
              {s.hostname} · {level === 'hot' ? 'running hot' : level === 'warn' ? 'running warm' : 'live'}
            </span>
          ) : undefined
        }
      />
      <div className="resources-body nowheel">
        <HostPanel compact consoles={3} head="none" />
      </div>
    </TileFrame>
  );
}

export const ResourcesTile = memo(ResourcesTileImpl);
