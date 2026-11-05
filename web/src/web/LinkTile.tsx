// Link card (node kind "link", docs/API-studio.md §4.7): an external site that can't be
// framed; Open goes to a new browser tab.

import { memo } from 'react';
import type { NodeProps } from '@xyflow/react';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import type { FlowNode } from '../board/flow';
import { TileFrame } from '../board/tiles/common';

function LinkTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const url = typeof data.node.data.url === 'string' ? data.node.data.url : '';
  const title = typeof data.node.data.title === 'string' ? data.node.data.title : '';
  let host = url;
  try {
    host = new URL(url).host;
  } catch {
    /* keep the raw text */
  }
  const safe = /^https?:\/\//i.test(url);
  return (
    <TileFrame id={id} kind="link" selected={!!selected} label={`Link ${host}`} minW={200} minH={80}>
      <div className="link-card">
        <span className="link-icon" aria-hidden="true">
          <Icon name="globe" />
        </span>
        <div className="link-text">
          <span className="link-title truncate" title={title || host}>
            {title || host}
          </span>
          <span className="link-url mono truncate" title={url}>
            {url}
          </span>
        </div>
        <Button size="sm" icon="external" className="nodrag" disabled={!safe} onClick={() => window.open(url, '_blank', 'noopener,noreferrer')}>
          Open
        </Button>
      </div>
    </TileFrame>
  );
}

export const LinkTile = memo(LinkTileImpl);
