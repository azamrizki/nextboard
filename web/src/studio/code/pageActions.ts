// The lead's UI actions in the Code view (DESIGN.md §3.19). A board runs them on its canvas
// (BoardView.tsx); the Code view has no board, so it runs what an editor can (a file, a tool as a
// tab, a console as a tab, Source Control) and, for what only a board shows, offers to open it.

import type { BoardController } from '../../board/controller';
import { boardPath, navigate } from '../../lib/router';
import { projectParts } from '../../lib/parts';
import type { Project } from '../../lib/types';
import type { UiAction } from '../../orch/types';
import { store } from '../../store';
import { usageUi } from '../../usage/prefs';
import { fileKey } from './paths';
import type { ToolKind } from './toolKinds';
import type { SideView, Workspace } from './workspace';

/** What a lead's panel asks for (UIAction.panel) is, in the Code view, a tool tab. */
const PANEL_TOOLS: Record<string, ToolKind> = {
  memory: 'memory',
  loops: 'loops',
  mission: 'mission',
  chats: 'chats',
  documents: 'documents',
  markdown: 'markdown',
  skills: 'skills',
  diagrams: 'diagrams',
  vault: 'vault',
  credentials: 'vault',
  browser: 'browser',
};

export interface PageActionEnv {
  ws: Pick<Workspace, 'open' | 'openTool' | 'openPane' | 'toKey' | 'changed' | 'focusTick'>;
  project: Project;
  ctl: Pick<BoardController, 'node'>;
  /** For what only a board shows: say so, with a way to the board. */
  onBoard: (what: string) => void;
  showView: (v: SideView) => void;
}

/** One of the lead's UI actions, in the Code view. */
export function runPageUiAction(a: UiAction, { ws, project, ctl, onBoard, showView }: PageActionEnv) {
  switch (a.action) {
    case 'openFile': {
      if (!a.path) return;
      // the part the lead meant decides the machine, as on a board
      const part = a.part ? projectParts(project).find((pt) => pt.id === a.part) : undefined;
      const key = part ? fileKey(part.machineId || 'local', a.path) : ws.toKey(a.path);
      if (key) ws.open(key, { pin: true, source: true, line: a.line });
      return;
    }
    case 'openPanel': {
      const panel = a.panel ?? '';
      if (PANEL_TOOLS[panel]) ws.openTool(PANEL_TOOLS[panel]);
      else if (panel === 'git') showView('scm');
      else if (panel === 'code') {
        // the editor is here already: its keys go back to it
        ws.focusTick++;
        ws.changed();
      } else if (panel === 'settings') navigate('/settings');
      else if (panel === 'usage') usageUi.set(true);
      else onBoard(panel ? panel[0].toUpperCase() + panel.slice(1) : 'That panel');
      return;
    }
    case 'focus': {
      // a console: its tab here (one asleep wakes as its terminal connects); a card: the board's
      const t = a.target ?? '';
      const sessions = store.getState().sessions;
      const node = ctl.node(t);
      const sid = sessions[t] ? t : typeof node?.data.sessionId === 'string' ? node.data.sessionId : '';
      if (sid && sessions[sid]) ws.openPane(sid);
      else onBoard('That card');
      return;
    }
    case 'goWorkspace':
      if (a.workspaceId) navigate(boardPath(project.id, a.workspaceId));
      return;
    case 'closePanel':
      // the editor's tabs close by hand
      return;
    case 'fit':
    case 'zoomIn':
    case 'zoomOut':
    case 'zoomReset':
    case 'tool':
    case 'undo':
    case 'redo':
      onBoard('That');
      return;
  }
}
