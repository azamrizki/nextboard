// Jump to a console (needs-you chip, `N`, notifications, live chip): frames the node on
// the open board and gives it the keyboard, or navigates to its workspace first.
//
// An agent inside an editor card (a pane) has no tile: the card shows it. A board may hold several
// editor cards (v0.9.0-beta.2), at the root or in a part, so every card of the board is asked, and
// when none has the pane, the board opens the card of the session's placement, which then takes
// the pane (takePendingPane).

import { toast } from '../components/Toast';
import { isRoot, normalPlacement, placementOfSession, samePlacement, type Placement } from '../lib/parts';
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

/** Per board, per editor card (its node id): how it brings one of its agent panes to the front. */
const editors = new Map<string, Map<string, FocusFn>>();
/** The pane to show once the right editor card has opened, and where its session works (null: no part). */
let pendingPane: { sessionId: string; placement: Placement | null } | null = null;

/** An open editor card registers how to bring one of its agent panes to the front. */
export function registerEditorFocus(workspaceId: string, cardId: string, fn: FocusFn): () => void {
  let cards = editors.get(workspaceId);
  if (!cards) editors.set(workspaceId, (cards = new Map()));
  cards.set(cardId, fn);
  return () => {
    const now = editors.get(workspaceId);
    if (now?.get(cardId) !== fn) return;
    now.delete(cardId);
    if (!now.size) editors.delete(workspaceId);
  };
}

/** The pane waiting for an editor card, and where its session works: the board opens that card. */
export function pendingPaneOf(sessionId: string): { sessionId: string; placement: Placement | null } | null {
  return pendingPane && pendingPane.sessionId === sessionId ? pendingPane : null;
}

/**
 * The pane to show once the editor card has opened: handed only to a card that fits the
 * session's placement (its own, or a Root card, which lists every part's agents). `placement` is
 * the card's (lib/parts.ts placementOf). Returns "" when nothing is waiting for this card.
 */
export function takePendingPane(placement?: Placement): string {
  const p = pendingPane;
  if (!p) return '';
  if (placement && p.placement && !isRoot(placement) && !samePlacement(placement, p.placement)) return '';
  pendingPane = null;
  return p.sessionId;
}

/** Show a session's console wherever it lives. Returns false if it can't be located. */
export function jumpToSession(sessionId: string): boolean {
  const s = store.getState().sessions[sessionId];
  if (!s) return false;
  if (s.panel && s.workspaceId) {
    for (const show of editors.get(s.workspaceId)?.values() ?? []) if (show(sessionId)) return true;
    // the board opens the card of its placement; that card shows the pane
    const project = s.projectId ? store.getState().projects[s.projectId] : undefined;
    const placement = project ? placementOfSession(s, project) : null;
    pendingPane = { sessionId, placement: placement && project ? normalPlacement(project, placement) : null };
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
