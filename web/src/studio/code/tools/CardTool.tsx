// A board card as a tab of the Code view (DESIGN.md §3.19, Code view = Board view): the very
// card the board shows (its Kanban, its orchestrator with its task boards, a checklist…), drawn
// by the same component with nothing of the canvas (TileHost "tab"), so every change shows in
// both views at once. The tab's argument is the card's node id on the bound board.

import { useEffect, useSyncExternalStore, type ComponentType } from 'react';
import type { NodeProps } from '@xyflow/react';
import { BranchesTile } from '../../../git/BranchesTile';
import type { FlowNode } from '../../../board/flow';
import { CalendarTile } from '../../../board/tiles/CalendarTile';
import { ChecklistTile } from '../../../board/tiles/ChecklistTile';
import { CredentialsTile } from '../../../board/tiles/CredentialsTile';
import { FileTile } from '../../../board/tiles/FileTile';
import { TileHost } from '../../../board/tiles/host';
import { ImageTile } from '../../../board/tiles/ImageTile';
import { KanbanTile } from '../../../board/tiles/KanbanTile';
import { MemoryTile } from '../../../board/tiles/MemoryTile';
import { NoteTile } from '../../../board/tiles/NoteTile';
import { OrchestratorTile } from '../../../board/tiles/OrchestratorTile';
import { UptimeTile } from '../../../board/tiles/UptimeTile';
import { ResourcesTile } from '../../../board/tiles/ResourcesTile';
import { AppTile } from '../../../gui/AppTile';
import { DocumentTile } from '../../documents/DocumentTile';
import { LinkTile } from '../../../web/LinkTile';
import { WebLogTile } from '../../../web/WebLogTile';
import { WebTile } from '../../../web/WebTile';
import { Icon } from '../../../components/Icon';
import { rememberCard } from '../cardTabs';
import type { ToolBodyProps } from '../ToolTab';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const TILES: Record<string, ComponentType<any>> = {
  kanban: KanbanTile,
  checklist: ChecklistTile,
  calendar: CalendarTile,
  orchestrator: OrchestratorTile,
  memory: MemoryTile,
  uptime: UptimeTile,
  resources: ResourcesTile,
  note: NoteTile,
  credentials: CredentialsTile,
  branches: BranchesTile,
  document: DocumentTile,
  file: FileTile,
  image: ImageTile,
  link: LinkTile,
  weblog: WebLogTile,
  web: WebTile,
  app: AppTile,
};

export default function CardTool({ env }: ToolBodyProps) {
  const { ctl, tab } = env;
  const id = tab.tool?.arg ?? '';
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const status = useSyncExternalStore(ctl.sync.subscribe, () => ctl.sync.status);
  const node = doc.nodes.find((n) => n.id === id);
  useEffect(() => {
    if (node) rememberCard(node);
  }, [node]);
  if (status === 'loading' && !node)
    return (
      <div className="tool-tab-msg" role="status">
        <span className="spinner" aria-hidden="true" /> Loading the board…
      </div>
    );
  const Tile = node ? TILES[node.kind] : undefined;
  if (!node || !Tile)
    return (
      <div className="tool-tab-msg" role="status">
        <Icon name="info" />
        {node ? 'This card does not open as a tab: open it on the board.' : 'This card is no longer on the board.'}
      </div>
    );
  // what React Flow gives a node, for a card drawn outside the canvas
  const props: NodeProps<FlowNode> = {
    id: node.id,
    type: node.kind,
    data: { node },
    selected: false,
    dragging: false,
    zIndex: 0,
    isConnectable: false,
    positionAbsoluteX: 0,
    positionAbsoluteY: 0,
    draggable: false,
    selectable: false,
    deletable: false,
    width: node.w,
    height: node.h,
  };
  return (
    <TileHost.Provider value="tab">
      <div className="card-tab" data-card={node.id} data-kind={node.kind}>
        <Tile {...props} />
      </div>
    </TileHost.Provider>
  );
}
