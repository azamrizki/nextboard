// A part's git as the editor's source control, history and stash see it (GitHost): the part's own
// status and repository picker, with the card's tabs around it, so a commit or the graph opens
// in this card and names its part (v0.9.0-beta.2, contracts.md §3.6: built with getters, never by
// spreading the PartService, which would drop its getters).

import type { GitHost } from './CommitGraph';
import type { PartService } from './partsvc';
import { cleanAbs, fileKey, type FileKey } from './paths';
import type { Workspace } from './workspace';

const hosts = new WeakMap<Workspace, WeakMap<PartService, GitHost>>();

/** The GitHost of a part in a card: one object per (card, part), so React effects keyed on it do not run on every render. */
export function gitHostOf(ws: Workspace, part: PartService): GitHost {
  let by = hosts.get(ws);
  if (!by) hosts.set(ws, (by = new WeakMap()));
  let h = by.get(part);
  if (!h) {
    h = {
      get gitTarget() {
        return part.gitTarget;
      },
      get gitVersion() {
        return part.gitVersion;
      },
      get git() {
        return part.git;
      },
      refreshGit: () => part.refreshGit(),
      openCommit: (hash, o) => ws.openCommit(part, hash, o),
      get activeTab() {
        const t = ws.activeTab;
        // a commit of another part is not this list's
        return t?.commit && t.commit.part !== part.partId ? null : t;
      },
      openView: (v, o) => ws.openView({ ...v, part: part.partId }, o),
      saveAll: () => ws.saveAll(),
    };
    by.set(part, h);
  }
  return h;
}

/** A part the card mounts, by id; null when it has none such (a tab of a part since removed). */
export const mountedPart = (ws: Workspace, partId: string | undefined): PartService | null => (partId ? (ws.parts.find((p) => p.partId === partId) ?? null) : null);

/** The key of a path git reports (relative to the selected repository of the part, else to its folder). */
export function repoKey(part: PartService, rel: string): FileKey {
  const base = cleanAbs(part.git?.root ?? '') ?? part.root;
  return fileKey(part.machineId, `${base}/${rel}`);
}
