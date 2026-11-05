// Desktop app tile (node kind "app", docs/API-gui.md §4): an app of This PC on its own
// virtual display, shown and controlled through the viewer page in an iframe. The node
// keeps the app id, so a tile whose session ended can open the app again.

import { memo, useState } from 'react';
import type { NodeProps } from '@xyflow/react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import type { FlowNode } from '../board/flow';
import { TileFrame, TileHead } from '../board/tiles/common';
import { useBoardEnv } from '../board/ui';
import { AppIcon } from './AppIcon';
import { AppDisplay, AppHeadControls, AppWhere, useAppViewer } from './AppView';
import { appLaunchBody } from './model';
import { openApp, useGui, useGuiSession } from './store';

const str = (v: unknown) => (typeof v === 'string' ? v : '');

function AppTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, project } = useBoardEnv();
  const node = data.node;
  const appId = str(node.data.appId);
  const name = str(node.data.name) || 'Desktop app';
  const sessionId = str(node.data.sessionId);
  const session = useGuiSession(sessionId || undefined);
  const ended = useGui((s) => (sessionId ? s.ended[sessionId] : undefined));
  const loaded = useGui((s) => s.loaded);
  const [opening, setOpening] = useState(false);
  const [failed, setFailed] = useState('');
  const viewer = useAppViewer(appId, session);

  const open = async () => {
    setOpening(true);
    setFailed('');
    try {
      const s = await openApp(appLaunchBody(appId, project, true, node.w, node.h));
      ctl.apply([{ op: 'patch', id, set: { data: { sessionId: s.id } } }], { history: false });
    } catch (err) {
      setFailed(errorMessage(err));
    } finally {
      setOpening(false);
    }
  };

  const title = session?.title || name;

  return (
    <TileFrame id={id} kind="app" selected={!!selected} label={`Desktop app ${name}`} minW={320} minH={240}>
      <TileHead
        id={id}
        icon={<AppIcon appId={appId} className="app-icon" />}
        title={title}
        meta={<AppWhere viewer={viewer} />}
        actions={<AppHeadControls viewer={viewer} session={session} name={name} />}
        menuLabel={`Actions for ${name}`}
      />
      <div className="web-body nodrag nopan nowheel">
        <AppDisplay viewer={viewer} session={session} title={title} name={name} selected={!!selected} icon={<AppIcon appId={appId} className="app-icon-lg" />} />
        {!session && (loaded || !sessionId) && (
          <div className="web-msg">
            <AppIcon appId={appId} className="app-icon-lg" />
            <p>{failed || (ended?.reason === 'failed' && ended.message) || `${name} is not open.`}</p>
            <Button size="sm" variant="primary" icon="play" disabled={opening || !appId} onClick={() => void open()}>
              {opening ? 'Opening…' : `Open ${name}`}
            </Button>
          </div>
        )}
      </div>
    </TileFrame>
  );
}

export const AppTile = memo(AppTileImpl);
