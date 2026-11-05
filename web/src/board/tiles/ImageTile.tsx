// Image: a hub asset or a file on a machine (fs/raw). Aspect ratio kept when resizing.
// Selected images show a "Markup" button (the editor saves a new image next to it).

import { memo, useState } from 'react';
import type { NodeProps } from '@xyflow/react';
import { assetUrl, fsRawUrl } from '../../api';
import { Icon } from '../../components/Icon';
import type { FlowNode } from '../flow';
import { baseName, readImage, type ImageSrc } from '../kinds';
import { useBoardEnv } from '../ui';
import { TileFrame } from './common';

export function imageSrcUrl(src: ImageSrc | null): string {
  if (!src) return '';
  return 'asset' in src ? assetUrl(src.asset) : fsRawUrl(src.machineId, src.path);
}

function ImageTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const d = readImage(data.node);
  const { ui } = useBoardEnv();
  const url = imageSrcUrl(d.src);
  const [state, setState] = useState<'loading' | 'ok' | 'error'>('loading');
  const alt = d.alt || (d.src && 'path' in d.src ? baseName(d.src.path) : 'Image');

  return (
    <TileFrame id={id} kind="image" selected={!!selected} label={alt} keepAspect minW={32} minH={32}>
      {url && state !== 'error' && (
        <img
          className={`image-img ${state === 'loading' ? 'is-loading' : ''}`}
          src={url}
          alt={alt}
          draggable={false}
          onLoad={() => setState('ok')}
          onError={() => setState('error')}
        />
      )}
      {(state === 'error' || !url) && (
        <div className="tile-placeholder">
          <Icon name="image" />
          <span>Image unavailable</span>
          {d.src && 'path' in d.src && <span className="mono truncate">{d.src.path}</span>}
        </div>
      )}
      {selected && state === 'ok' && (
        <button
          type="button"
          className="tile-pill nodrag"
          onClick={(e) => {
            e.stopPropagation();
            ui.set({ markup: id });
          }}
        >
          <Icon name="pen" />
          Markup
        </button>
      )}
    </TileFrame>
  );
}

export const ImageTile = memo(ImageTileImpl);
