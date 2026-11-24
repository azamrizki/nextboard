// Opening a database object as a tab of the Code view (docs/API-db.md): a table, an SQL editor (a
// new one gets the next number and, when given, its first SQL), a Redis connection's keys. Light:
// the side view and the tabs use it, the heavy views load with their tab.

import { dbArg, newQueryId, parseDbArg, putQueryText, type DbTarget } from '../../../db/tabs';
import type { Workspace } from '../workspace';

export function openDb(ws: Pick<Workspace, 'openTool' | 'groups'>, t: DbTarget, sql?: string) {
  if (t.kind === 'home') return;
  if (t.kind === 'query' && !t.id) {
    // the next free number among the editors open
    let n = 1;
    const used = new Set<number>();
    for (const g of ws.groups)
      for (const tab of g.tabs) {
        if (tab.tool?.kind !== 'db') continue;
        const q = parseDbArg(tab.tool.arg);
        const m = q.kind === 'query' ? /^n(\d+)-/.exec(q.id) : null;
        if (m) used.add(Number(m[1]));
      }
    while (used.has(n)) n++;
    const id = newQueryId(n);
    if (sql) putQueryText(id, sql);
    t = { ...t, id };
  }
  ws.openTool('db', dbArg(t));
}
