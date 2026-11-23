// Database card (docs/API-db.md): the project's database browser on the board, every connection
// or the one the card keeps to. A console wired to a card kept to a connection may use it when
// the connection lets only wired consoles in (Agents › Which agents).

import { memo } from 'react';
import type { NodeProps } from '@xyflow/react';
import type { MenuEntry } from '../../components/Menu';
import { DriverMark } from '../../db/Confirm';
import { useConnections } from '../../db/store';
import { DbWorkspace } from '../../db/Workspace';
import type { FlowNode } from '../flow';
import { readDatabase } from '../kinds';
import { useBoardEnv } from '../ui';
import { InlineTitle, stopKeys, TileFrame, TileHead } from './common';

function DatabaseTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, project } = useBoardEnv();
  const d = readDatabase(data.node);
  const { list } = useConnections(project.id);
  const kept = list.find((c) => c.id === d.connectionId);
  const menu: MenuEntry[] = [
    { label: 'Every connection', icon: 'database', onSelect: () => ctl.updateData(id, { connectionId: '' }), disabled: !d.connectionId },
    ...list.map((c): MenuEntry => ({ label: `Keep to ${c.name}`, icon: 'pin', onSelect: () => ctl.updateData(id, { connectionId: c.id, title: d.title === 'Database' ? c.name : d.title }), disabled: c.id === d.connectionId })),
  ];
  return (
    <TileFrame id={id} kind="database" selected={!!selected} label={`Database ${d.title}`} minW={480} minH={320}>
      <TileHead
        id={id}
        icon={kept ? <DriverMark driver={kept.driver} /> : 'database'}
        title={<InlineTitle value={d.title} label="Card title" maxLength={40} onCommit={(title) => ctl.updateData(id, { title })} />}
        meta={kept ? <span className="tile-meta text-3">{kept.env}</span> : <span className="tile-meta text-3">{list.length} connection{list.length === 1 ? '' : 's'}</span>}
        menu={menu}
        menuLabel="Database card actions"
      />
      <div className="tile-db-body nodrag nowheel" onKeyDown={stopKeys}>
        <DbWorkspace projectId={project.id} storeKey={`card:${id}`} only={d.connectionId || undefined} compact />
      </div>
    </TileFrame>
  );
}

export const DatabaseTile = memo(DatabaseTileImpl);
