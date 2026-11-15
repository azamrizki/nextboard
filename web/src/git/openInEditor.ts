// "Open in editor" from the board's Git panel: a commit in a tab of an editor card, with the diff
// of each file (CommitView). A board may hold several editor cards (v0.9.0-beta.2): the commit
// goes to the card of its part, else a Root card (it shows every part's repositories), else a new
// card placed in that part. The editor's code comes only when asked for: it brings Monaco.

import type { UiStore } from '../board/ui';
import type { Placement } from '../lib/parts';
import type { Project, ProjectPart } from '../lib/types';

export interface CommitInEditor {
  project: Project;
  part: ProjectPart;
  hash: string;
  label: string;
  /** A file of the commit that is still there, absolute (kept for older callers; the part decides). */
  file?: string;
  /**
   * Shows an editor card (the board's openCode). Asked with a placement, it picks or makes the
   * card and says which (its node id and placement); asked with a path (older boards), it shows a
   * card with that file.
   */
  openEditor(to?: string | { placement: Placement }): string | { nodeId: string; placement: Placement } | void;
  ui?: UiStore;
}

export async function showCommitInEditor(o: CommitInEditor): Promise<void> {
  const [{ Workspace }, { PartService }, { setupMonaco }] = await Promise.all([import('../studio/code/workspace'), import('../studio/code/partsvc'), import('../studio/code/monaco')]);
  setupMonaco();
  const shown = o.openEditor({ placement: { partId: o.part.id } });
  // the card the board chose: that part's, or a Root card; a board that does not say: the part's view
  const placement: Placement = shown && typeof shown === 'object' ? shown.placement : { partId: o.part.id };
  const view = Workspace.get(o.project, placement);
  // the part service the card's view holds (a stray PartService.get would never be let go)
  const part = view.parts.find((p) => p.partId === o.part.id) ?? PartService.get(o.project, o.part);
  view.openCommit(part, o.hash, { label: o.label, pin: true });
}
