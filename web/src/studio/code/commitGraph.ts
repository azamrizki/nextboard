// The lanes of the history graph: the column of each commit and the lines that pass it, join it
// or leave it. Commits come children first, so a lane waits for the hash it runs to.

import type { GitRef } from '../types';

export const LANE_COLORS = 8;

export interface GraphLine {
  lane: number;
  color: number;
}

export interface GraphRow {
  lane: number;
  color: number;
  /** Lines that run past the commit. */
  through: GraphLine[];
  /** Lines from the top of the row into the commit. */
  above: GraphLine[];
  /** Lines from the commit to the bottom of the row. */
  below: GraphLine[];
}

export function layoutGraph(commits: { hash: string; parents?: string[] }[]): { rows: GraphRow[]; lanes: number } {
  const open: ({ hash: string; color: number } | null)[] = [];
  let nextColor = 0;
  let lanes = 0;
  const newColor = () => nextColor++ % LANE_COLORS;
  const freeLane = (from: number) => {
    for (let i = from; i < open.length; i++) if (!open[i]) return i;
    return open.length;
  };

  const rows = commits.map((c): GraphRow => {
    const above: GraphLine[] = [];
    const through: GraphLine[] = [];
    open.forEach((l, lane) => {
      if (l?.hash === c.hash) above.push({ lane, color: l.color });
      else if (l) through.push({ lane, color: l.color });
    });
    above.forEach((l) => (open[l.lane] = null));
    const lane = above[0]?.lane ?? freeLane(0);
    const color = above[0]?.color ?? newColor();

    const below: GraphLine[] = [];
    (c.parents ?? []).forEach((hash, i) => {
      const waiting = open.findIndex((l) => l?.hash === hash);
      if (waiting >= 0) {
        // a branch that starts here shows in its own colour on the way to its base
        below.push({ lane: waiting, color: i === 0 ? color : open[waiting]!.color });
        return;
      }
      const to = i === 0 ? lane : freeLane(lane + 1);
      const toColor = i === 0 ? color : newColor();
      open[to] = { hash, color: toColor };
      below.push({ lane: to, color: toColor });
    });

    lanes = Math.max(lanes, lane + 1, open.length);
    while (open.length && !open[open.length - 1]) open.pop();
    return { lane, color, through, above, below };
  });
  return { rows, lanes };
}

export interface RefPill {
  name: string;
  kind: GitRef['kind'];
  /** A local branch with a remote branch of the same name on the same commit. */
  synced: boolean;
}

/** What stands on a commit: local branches take in their remote twin, tags come last. */
export function refPills(refs: GitRef[] = []): RefPill[] {
  const local = refs.filter((r) => r.kind === 'head' || r.kind === 'branch');
  const twinOf = (r: GitRef) => r.name.slice(r.name.indexOf('/') + 1);
  const remotes = refs.filter((r) => r.kind === 'remote');
  const rank: Record<GitRef['kind'], number> = { head: 0, branch: 1, remote: 2, tag: 3 };
  return [
    ...local.map((r) => ({ name: r.name, kind: r.kind, synced: remotes.some((x) => twinOf(x) === r.name) })),
    ...remotes.filter((r) => !local.some((l) => l.name === twinOf(r))).map((r) => ({ name: r.name, kind: r.kind, synced: false })),
    ...refs.filter((r) => r.kind === 'tag').map((r) => ({ name: r.name, kind: r.kind, synced: false })),
  ].sort((a, b) => rank[a.kind] - rank[b.kind]);
}
