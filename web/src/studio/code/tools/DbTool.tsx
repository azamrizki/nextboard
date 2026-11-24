// A database tab of the code view (docs/API-db.md; mockups 1, 2, 4 and 8): one table, SQL editor or
// Redis connection's keys, at the editor's full size; the Databases side view lists what opens here.

import { useCallback } from 'react';
import { Button } from '../../../components/Button';
import { Icon } from '../../../components/Icon';
import { ObjectView } from '../../../db/ObjectView';
import { parseDbArg, rememberDbTitle } from '../../../db/tabs';
import type { ToolBodyProps } from '../ToolTab';
import { openDb } from './dbOpen';

export default function DbTool({ env, shown }: ToolBodyProps) {
  const { ws, tab } = env;
  const arg = tab.tool?.arg ?? '';
  const target = parseDbArg(arg);
  const onTitle = useCallback(
    (title: string) => {
      if (rememberDbTitle(arg, title)) ws.changed();
    },
    [arg, ws],
  );
  if (target.kind === 'home')
    return (
      <section className="tool-db db-home" aria-label="Databases">
        <div className="db-empty">
          <Icon name="database" />
          <p>Choose a connection in Databases, then a table or a new query.</p>
          <Button
            size="sm"
            icon="database"
            onClick={() => {
              ws.side = 'databases';
              ws.sideOpen = true;
              ws.changed();
            }}
          >
            Show Databases
          </Button>
        </div>
      </section>
    );
  return (
    <section className="tool-db" aria-label={tab.tool ? 'Database' : 'Databases'}>
      <ObjectView target={target} tabKey={tab.id} shown={shown} onTitle={onTitle} onOpen={(t, sql) => openDb(ws, t, sql)} />
    </section>
  );
}
