// The Code view's Databases side view (docs/API-db.md, mockups 1, 2, 4 and 7): the project's
// connections and the schema of the one chosen; what is picked opens as an editor tab.

import { useSyncExternalStore } from 'react';
import { DbSide } from '../../../db/Side';
import { parseDbArg } from '../../../db/tabs';
import { useLayout } from '../../../lib/layout';
import type { Workspace } from '../workspace';
import { openDb } from './dbOpen';

export default function DbSideView({ ws }: { ws: Workspace }) {
  useSyncExternalStore(ws.subscribe, ws.getVersion);
  const tab = ws.activeTab;
  const active = tab?.tool?.kind === 'db' ? parseDbArg(tab.tool.arg) : undefined;
  const phone = useLayout().layout === 'phone';
  return <DbSide projectId={ws.projectId} active={active} phone={phone} onOpen={(t, sql) => openDb(ws, t, sql)} />;
}
