// Jump to a console (needs-you chip, `N`, notifications, live chip): frames the node on
// the open board and gives it the keyboard, or navigates to its workspace first.

import { toast } from '../components/Toast';
import { boardPath, navigate } from '../lib/router';
import { needsYouList, store } from '../store';

type FocusFn = (sessionId: string) => boolean;
const boards = new Map<string, FocusFn>();

/** A mounted board registers how to frame + focus one of its terminals. */
export function registerBoardFocus(workspaceId: string, fn: FocusFn): () => void {
  boards.set(workspaceId, fn);
  return () => {
    if (boards.get(workspaceId) === fn) boards.delete(workspaceId);
  };
}

const editors = new Map<string, FocusFn>();
let pendingPane = '';

/** An open editor card registers how to bring one of its agent panes to the front. */
export function registerEditorFocus(workspaceId: string, fn: FocusFn): () => void {
  editors.set(workspaceId, fn);
  return () => {
    if (editors.get(workspaceId) === fn) editors.delete(workspaceId);
  };
}

/** The pane to show once the editor card has opened. */
export function takePendingPane(): string {
  const id = pendingPane;
  pendingPane = '';
  return id;
}

/** Show a session's console wherever it lives. Returns false if it can't be located. */
export function jumpToSession(sessionId: string): boolean {
  const s = store.getState().sessions[sessionId];
  if (!s) return false;
  if (s.panel && s.workspaceId) {
    const editor = editors.get(s.workspaceId);
    if (editor?.(sessionId)) return true;
    pendingPane = sessionId; // the board opens the card; the card shows the pane
  }
  if (s.workspaceId) {
    const here = boards.get(s.workspaceId);
    if (here && here(sessionId)) return true;
  }
  if (!s.projectId) return false;
  navigate(`${boardPath(s.projectId, s.workspaceId)}?focus=${encodeURIComponent(sessionId)}`);
  return true;
}

let lastJump = '';

/** Oldest needsYouSince first, then the next one on each call (wraps around). */
export function jumpToNextNeedsYou(): void {
  const list = needsYouList(Object.values(store.getState().sessions));
  if (list.length === 0) {
    toast.info('No console needs you right now.');
    return;
  }
  const i = list.findIndex((s) => s.id === lastJump);
  const next = list[(i + 1) % list.length];
  lastJump = next.id;
  if (!jumpToSession(next.id)) toast.error(`Could not open @${next.handle ?? next.name}: its board is not available.`);
}
