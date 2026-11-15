// "Open in editor" from the board's Git panel: a commit in a tab of the editor card, with the
// diff of each file (CommitView). The editor card shows one part at a time, chosen in its title
// and kept in this browser; a file of another part moves it there (CodeStudio follows the board's
// openFile). The editor's code comes only when asked for: it brings Monaco.

import { toast } from '../components/Toast';
import type { UiStore } from '../board/ui';
import { prefs } from '../lib/format';
import { partHolding, projectParts } from '../lib/parts';
import type { Project, ProjectPart } from '../lib/types';

/** The key under which CodeStudio keeps the part its card shows. */
const PART_PREF = (projectId: string) => `wb.part.${projectId}`;
const TAKEN_MS = 10_000;

export interface CommitInEditor {
  project: Project;
  part: ProjectPart;
  hash: string;
  label: string;
  /** A file of the commit that is still there, absolute: it moves the card to the part. */
  file?: string;
  /** Shows the editor card, with this file when given (the board's openCode). */
  openEditor: (path?: string) => void;
  ui?: UiStore;
}

/** Resolves once the editor card took the file the board asked for, or after a while. */
function taken(ui: UiStore): Promise<void> {
  return new Promise((resolve) => {
    let off = () => {};
    const timer = window.setTimeout(() => (off(), resolve()), TAKEN_MS);
    off = ui.subscribe(() => {
      if (ui.get().openFile) return;
      window.clearTimeout(timer);
      off();
      resolve();
    });
    if (!ui.get().openFile) {
      window.clearTimeout(timer);
      off();
      resolve();
    }
  });
}

export async function showCommitInEditor(o: CommitInEditor): Promise<void> {
  const [{ Workspace }, { setupMonaco }] = await Promise.all([import('../studio/code/workspace'), import('../studio/code/monaco')]);
  setupMonaco();
  const ws = Workspace.get(o.project, o.part);
  const show = () => ws.openCommit(o.hash, { label: o.label, pin: true });
  show();
  const parts = projectParts(o.project);
  const shown = parts.find((p) => p.id === prefs.get(PART_PREF(o.project.id))) ?? parts[0];
  if (parts.length < 2 || shown.id === o.part.id) {
    o.openEditor();
    return;
  }
  // a card placed now starts on this part
  prefs.set(PART_PREF(o.project.id), o.part.id);
  const file = o.file && partHolding(o.project, o.file)?.id === o.part.id ? o.file : '';
  if (!file || !o.ui) {
    o.openEditor();
    toast.info(`The commit is open in ${o.part.name}. Pick ${o.part.name} in the editor’s title if it shows another part.`);
    return;
  }
  // a card on another part moves here with the file, which then comes in front: the commit goes back
  o.openEditor(file);
  await taken(o.ui);
  show();
}
