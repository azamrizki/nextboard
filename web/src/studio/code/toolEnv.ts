// What a tool tab works with (docs/API-studio.md §7): the board the editor is bound to, its
// controller and UI store, and no canvas. In the code view that is the board the page binds (the
// project's last opened one, else its first); in an editor card it is the card's own board. The
// panels and studios were built for the board and take these as props here.

import { useContext, useMemo } from 'react';
import type { BoardController } from '../../board/controller';
import { rectOf } from '../../board/controller';
import { boundsOf } from '../../board/geometry';
import { BoardEnvContext, type UiStore } from '../../board/ui';
import { parseRoute, usePath } from '../../lib/router';
import type { Project } from '../../lib/types';
import { useProject } from '../../store';
import { PaneEnv } from './AgentPanes';
import type { Tab, Workspace } from './workspace';

export interface ToolEnv {
  ws: Workspace;
  tab: Tab;
  project: Project;
  ctl: BoardController;
  ui: UiStore | null;
  workspaceId: string;
  /** The code view, a page of its own: no board is drawn, so nothing is offered that only adds to one. */
  page: boolean;
  /** Where something that lands on the board goes: the board's view centre, else right of what is on it. */
  viewCenter: () => { x: number; y: number };
  /** Bring a board rect into view: only where the board is drawn. */
  reveal: (r: { x: number; y: number; w: number; h: number }) => void;
}

/** The editor is the code view's page (route /p/<project>/code). */
export function useCodeViewPage(): boolean {
  return parseRoute(usePath()).page === 'code';
}

export function useToolEnv(ws: Workspace, tab: Tab): ToolEnv | null {
  const board = useContext(BoardEnvContext);
  const pane = useContext(PaneEnv);
  const live = useProject(ws.projectId);
  const page = useCodeViewPage();
  const project = live ?? board?.project ?? pane?.project;
  const ctl = board?.ctl ?? pane?.ctl;
  return useMemo(() => {
    if (!project || !ctl) return null;
    // A's page binds a board without a viewport: a centre right of what is on the board
    const right = () => {
      const box = boundsOf(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf));
      return box ? { x: box.x + box.w + 400, y: box.y + 240 } : { x: 400, y: 300 };
    };
    return {
      ws,
      tab,
      project,
      ctl,
      ui: board?.ui ?? null,
      workspaceId: ctl.workspaceId,
      page,
      viewCenter: page || !board ? right : board.viewCenter,
      reveal: page || !board ? () => undefined : board.reveal,
    };
  }, [ws, tab, project, ctl, board, page]);
}

/**
 * Show a console in this editor: its tab focused if it has one, else a new tab. A board agent's
 * console is the same console, not a copy; one asleep wakes when its tab opens.
 */
export const showConsole = (ws: Workspace, sessionId: string) => ws.openPane(sessionId);
