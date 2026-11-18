// The board's panels and studios in a tool tab of the code view (docs/API-studio.md §7): Memory,
// Loops, Mission Map, Diagrams, Documents, Markdown and Skills. Each takes the editor's board
// binding as props; a dialog-shaped one (Mission Map, the studios) shows in place (ModalInline).
// Where no board is drawn (the code view) nothing is offered that only adds a card to one; a
// console opens as a tab of the editor instead of being shown on the board.

import { lazy } from 'react';
import { Icon } from '../../../components/Icon';
import { ModalInline } from '../../../components/Modal';
import { boardPath, Link } from '../../../lib/router';
import { markdownPartOfPlace } from '../../markdown/notes';
import { showConsole, type ToolEnv } from '../toolEnv';
import '../../../styles/tiles.css';
import '../../../styles/orch.css';
import '../../../styles/phase7.css';
import '../../../styles/studio.css';

const MemoryPanel = lazy(() => import('../../../orch/panels/MemoryPanel'));
const LoopsPanel = lazy(() => import('../../../orch/panels/LoopsPanel'));
const MissionMap = lazy(() => import('../../../orch/panels/MissionMap'));
const DiagramsPanel = lazy(() => import('../../diagrams/DiagramsPanel'));
const DocumentsStudio = lazy(() => import('../../documents/DocumentsStudio'));
const MarkdownStudio = lazy(() => import('../../markdown/MarkdownStudio'));
const SkillsStudio = lazy(() => import('../../skills/SkillsStudio'));

/** A panel's close button and Escape: the tab has its own ×, and Escape belongs to the editor. */
const stay = () => undefined;

export function MemoryTool({ env }: { env: ToolEnv }) {
  return <MemoryPanel project={env.project} onClose={stay} />;
}

export function LoopsTool({ env }: { env: ToolEnv }) {
  return <LoopsPanel ctl={env.ctl} project={env.project} onClose={stay} />;
}

export function MissionTool({ env }: { env: ToolEnv }) {
  return (
    <ModalInline.Provider value>
      <MissionMap ctl={env.ctl} onClose={stay} show={{ label: 'Open as tab', run: (sessionId) => showConsole(env.ws, sessionId) }} />
    </ModalInline.Provider>
  );
}

/** The board the editor is bound to, by name. */
const boardName = (env: ToolEnv) => env.project.workspaces?.find((w) => w.id === env.workspaceId)?.name || 'Main';

export function DiagramsTool({ env }: { env: ToolEnv }) {
  return (
    <>
      {env.page && (
        <p className="tool-note" role="note">
          <Icon name="info" />
          <span>
            A diagram lands on the board {boardName(env)} as shapes, this tab open or not. <Link to={boardPath(env.project.id, env.workspaceId)}>Open the board</Link> to see and edit it.
          </span>
        </p>
      )}
      {/* a run's Drawing card and its landing are the page's (DiagramLander) or the board's: never here, twice would land it twice */}
      <DiagramsPanel ctl={env.ctl} project={env.project} viewCenter={env.viewCenter} reveal={env.reveal} onClose={stay} />
    </>
  );
}

export function DocumentsTool({ env }: { env: ToolEnv }) {
  return (
    <ModalInline.Provider value>
      <DocumentsStudio project={env.project} onClose={stay} />
    </ModalInline.Provider>
  );
}

export function MarkdownTool({ env }: { env: ToolEnv }) {
  // the notes of the active file's part (a Root view: the studio picks, as on the board)
  const ws = env.ws;
  const partId = ws.paths.multi ? markdownPartOfPlace(env.project, { partId: ws.activePart.partId }) : undefined;
  return (
    <ModalInline.Provider value>
      <MarkdownStudio project={env.project} partId={partId} onClose={stay} />
    </ModalInline.Provider>
  );
}

export function SkillsTool({ env }: { env: ToolEnv }) {
  return (
    <ModalInline.Provider value>
      <SkillsStudio project={env.project} onClose={stay} />
    </ModalInline.Provider>
  );
}
